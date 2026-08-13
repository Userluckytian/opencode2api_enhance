// Part of the P1 (core split) refactor: code moved out of main.go.
// Same package (main) - do not change package clause manually.
package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"sync/atomic"
	"time"
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
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
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

	if cfg.RouteMode == "round_robin" || cfg.RouteMode == "failover" || cfg.RouteMode == "smart" {
		routeMode = cfg.RouteMode
	}
	setTimeoutConfigFromApp(cfg)
	applyBadStatusConfig(cfg)

	// P2 性能模式：质量加权路由 + 熔断/半开（未设置保持当前值；关闭时路由行为与基线一致）。
	if cfg.PoolPerformanceMode != nil {
		poolPerfMode = *cfg.PoolPerformanceMode
	}
	if cfg.PoolBreakerThreshold > 0 {
		poolBreakerThreshold = cfg.PoolBreakerThreshold
	}
	if cfg.PoolHalfOpenIntervalSec > 0 {
		poolHalfOpenIntervalSec = cfg.PoolHalfOpenIntervalSec
	}
	if cfg.PoolRaceCopies > 0 {
		poolRaceCopies = cfg.PoolRaceCopies
	}

	socks5Mu.Lock()
	proxiesChanged := false
	if cfg.Socks5Proxies != nil {
		proxiesChanged = !sameSocks5Proxies(socks5Proxies, cfg.Socks5Proxies)
		socks5Proxies = append([]Socks5Proxy(nil), cfg.Socks5Proxies...)
	}
	if activeSocks5 != cfg.ActiveSocks5 || proxiesChanged {
		activeSocks5 = cfg.ActiveSocks5
		socks5Client = nil
		socks5ClientAddr = ""
		atomic.StoreUint32(&socks5RRIndex, 0)
	}
	socks5Mu.Unlock()
	if proxiesChanged {
		socks5HealthMu.Lock()
		socks5Health = map[string]socks5HealthState{}
		socks5HealthMu.Unlock()
		// 代理列表变化 → 清空按地址缓存的客户端（连接池随旧配置作废）
		proxyClientMu.Lock()
		proxyClients = map[string]*http.Client{}
		proxyClientMu.Unlock()
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
			lastData = append(lastData[:0], data...)
			slog.Info("config hot-reloaded", "path", path)
		}
	}()
}
