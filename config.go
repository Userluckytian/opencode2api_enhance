// Part of the P1 (core split) refactor: code moved out of main.go.
// Same package (main) - do not change package clause manually.
package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/6Kmfi6HP/opencode2api/core/manager"
)

func loadConfig(path string) AppConfig {
	var cfg AppConfig
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		slog.Warn("config parse failed", "error", err)
	}
	return cfg
}

func saveConfig(path string, cfg AppConfig) error {
	// config.json 由本 AppConfig 与 core/manager.Config 双结构共用（同一物理文件）。
	// 整体覆盖会抹掉 manager 结构独有字段（如 gateway_key——用户设的自定义网关
	// 密码重启后失效的根因）。用读-合并-写保留对方字段。
	data, err := manager.MergeConfigJSON(path, cfg)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, data, 0o644)
}

// writeFileAtomic 临时文件+Rename 原子落盘：读者要么看到旧文件、要么看到完整新文件，
// 崩溃/断电不会留半截 JSON（loadConfig 对损坏 JSON 静默回退默认值，半写会悄悄丢配置）。
// 与 core/manager 的同名助手同款语义（跨包各自持有，暂不为此引公共包）。
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // 失败路径清理；成功 Rename 后目标已不存在，无副作用
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), perm); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// rateLimitCooldownSec / rateLimitBackoffBaseMS / rateLimitBackoffCapMS 429 感知（S2）：
// 冷却秒 / 指数退避起点与上限毫秒（默认 30 / 1000 / 30000；<=0 回退默认）。
// 声明于 config.go：socks_perf.go 由 S3 并行维护，不做 S2 改动。
// G5：热重载（applyConfig）原子写、请求路径原子读，默认值写于 init。
var (
	rateLimitCooldownSec   atomic.Int64
	rateLimitBackoffBaseMS atomic.Int64
	rateLimitBackoffCapMS  atomic.Int64
)

func init() {
	rateLimitCooldownSec.Store(30)
	rateLimitBackoffBaseMS.Store(1000)
	rateLimitBackoffCapMS.Store(30000)
}

func applyConfig(cfg AppConfig) {
	configMu.Lock()
	defer configMu.Unlock()
	if cfg.ModelAlias != nil {
		modelAlias = cfg.ModelAlias
	}
	if cfg.ReasoningEffortMap != nil {
		reasoningEffortMap = cfg.ReasoningEffortMap
	}
	forceDisableThinking = cfg.ForceDisableThinking
	if cfg.ShowNodePrefix != nil {
		showNodePrefix = *cfg.ShowNodePrefix
	}
	if cfg.Providers != nil {
		providersCfg = append([]ProviderCfg(nil), cfg.Providers...)
	}
	routingCfg = cfg.Routing
	// auto 虚拟模型配置（nil 回默认：关闭 + balanced；键被清除时热重载即时生效）。
	setAutoConfig(cfg.AutoModel)

	if cfg.RouteMode == "round_robin" || cfg.RouteMode == "failover" || cfg.RouteMode == "smart" {
		routeMode.Store(cfg.RouteMode)
	}
	setTimeoutConfigFromApp(cfg)
	applyBadStatusConfig(cfg)

	// P2 性能模式：质量加权路由 + 熔断/半开（未设置保持当前值；关闭时路由行为与基线一致）。
	if cfg.PoolPerformanceMode != nil {
		poolPerfMode.Store(*cfg.PoolPerformanceMode)
	}
	if cfg.PoolBreakerThreshold > 0 {
		poolBreakerThreshold.Store(int64(cfg.PoolBreakerThreshold))
	}
	if cfg.PoolHalfOpenIntervalSec > 0 {
		poolHalfOpenIntervalSec.Store(int64(cfg.PoolHalfOpenIntervalSec))
	}
	// S3 链路类坏池自动恢复间隔（>0 才覆盖，未配置保持当前值/默认 300）。
	if cfg.BadPoolResetSec > 0 {
		badPoolResetSec.Store(int64(cfg.BadPoolResetSec))
	}
	if cfg.PoolRaceCopies > 0 {
		poolRaceCopies.Store(int64(cfg.PoolRaceCopies))
	}
	if cfg.RaceBudgetMS > 0 {
		raceBudgetMS.Store(int64(cfg.RaceBudgetMS))
	}
	// S5 压力系数分段阈值（>0 才覆盖，未配置保持当前值/默认）。
	// G16：分段钳制——只接受 0 < Low < High 的单调分段（.Store 原子写，G5 不受影响）；
	// 任一侧与另一侧（含默认值）倒挂时忽略该侧并告警，避免 raceCopies 分段判定反转
	// （压力越大副本越多，与「高压退单发」的设计意图相反）。
	if cfg.PoolRacePressureLow > 0 && cfg.PoolRacePressureHigh > 0 {
		if cfg.PoolRacePressureHigh > cfg.PoolRacePressureLow {
			poolRacePressureLow.Store(cfg.PoolRacePressureLow)
			poolRacePressureHigh.Store(cfg.PoolRacePressureHigh)
		} else {
			slog.Warn("pool race pressure thresholds ignored (low >= high)", "low", cfg.PoolRacePressureLow, "high", cfg.PoolRacePressureHigh)
		}
	} else if cfg.PoolRacePressureLow > 0 {
		if curr := poolRacePressureHigh.Load().(float64); cfg.PoolRacePressureLow < curr {
			poolRacePressureLow.Store(cfg.PoolRacePressureLow)
		} else {
			slog.Warn("pool race pressure low ignored (low >= high)", "low", cfg.PoolRacePressureLow, "high", curr)
		}
	} else if cfg.PoolRacePressureHigh > 0 {
		if curr := poolRacePressureLow.Load().(float64); cfg.PoolRacePressureHigh > curr {
			poolRacePressureHigh.Store(cfg.PoolRacePressureHigh)
		} else {
			slog.Warn("pool race pressure high ignored (low >= high)", "low", curr, "high", cfg.PoolRacePressureHigh)
		}
	}
	// S2 429 感知（>0 才覆盖，未配置保持当前值/默认）。
	if cfg.RateLimitCooldownSec > 0 {
		rateLimitCooldownSec.Store(int64(cfg.RateLimitCooldownSec))
	}
	if cfg.RateLimitBackoffBaseMS > 0 {
		rateLimitBackoffBaseMS.Store(int64(cfg.RateLimitBackoffBaseMS))
	}
	if cfg.RateLimitBackoffCapMS > 0 {
		rateLimitBackoffCapMS.Store(int64(cfg.RateLimitBackoffCapMS))
	}

	socks5Mu.Lock()
	proxiesChanged := false
	if cfg.Socks5Proxies != nil {
		proxiesChanged = !sameSocks5Proxies(socks5Proxies, cfg.Socks5Proxies)
		socks5Proxies = append([]Socks5Proxy(nil), cfg.Socks5Proxies...)
	}
	if activeSocks5 != cfg.ActiveSocks5 || proxiesChanged {
		activeSocks5 = cfg.ActiveSocks5
		// 代理配置变化：清空整个客户端缓存（下一请求按新配置重建）。
		socks5CacheMu.Lock()
		socks5ClientCache = map[proxyCacheKey]*http.Client{}
		socks5CacheMu.Unlock()
		atomic.StoreUint32(&socks5RRIndex, 0)
	}
	socks5Mu.Unlock()
	if proxiesChanged {
		socks5HealthMu.Lock()
		socks5Health = map[string]socks5HealthState{}
		socks5HealthMu.Unlock()
		// G17：代理列表重建时整体重置反馈/in-flight 计数——已移除节点的
		// 死条目常驻 map 会随节点历史缓慢增长，重建即顺带驱逐全部旧 key。
		poolFeedbackMu.Lock()
		poolFeedback = map[string][]poolFbSample{}
		poolFeedbackMu.Unlock()
		proxyInFlightMu.Lock()
		proxyInFlight = map[string]*atomic.Int64{}
		proxyInFlightMu.Unlock()
	}

}

func sameSocks5Proxies(a, b []Socks5Proxy) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func getSocks5ProxyCount() int {
	socks5Mu.RLock()
	defer socks5Mu.RUnlock()
	return len(socks5Proxies)
}

// maxRouteRetries 返回同模型路由重试上限。
// 历史实现会随代理池规模线性放大（proxyCount>3 时返回 proxyCount），
// 上游故障时单请求可串行打上游数十次，形成重试风暴；现收敛为固定上限。
func maxRouteRetries() int {
	return maxUpstreamRetries
}

// startConfigWatcher applies config file changes without restarting the
// process, because restarting a live HTTP server drops active SSE streams.
func startConfigWatcher(path string) {
	go func() {
		// 1s→3s：配置热加载属低频运维动作，3s 内生效足够；降低每进程每秒一次的文件读。
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()
		lastData, _ := os.ReadFile(path)
		for range ticker.C {
			data, err := os.ReadFile(path)
			if err != nil || bytes.Equal(data, lastData) {
				continue
			}
			var cfg AppConfig
			if err := json.Unmarshal(data, &cfg); err != nil {
				slog.Warn("config reload skipped", "path", path, "error", err)
				continue
			}
			applyConfig(cfg)
			// providers 变化（如自定义模型源增删改）→ 原地重建厂商集合并刷新目录。
			maybeRebuildVendors()
			lastData = append(lastData[:0], data...)
			slog.Info("config hot-reloaded", "path", path)
		}
	}()
}
