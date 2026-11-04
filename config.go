// Part of the P1 (core split) refactor: code moved out of main.go.
// Same package (main) - do not change package clause manually.
//
// 根进程配置加载 / 热重载。
//
// v2.0.0 精简：节点池（SOCKS5）、实例、订阅、竞速、429 退避、sidecar 等
// 免费层相关全局状态全部移除——它们随 40080 自研网关一起消失。
// 本文件只剩：别名 / 推理等级 / providers / routing / auto 模型。
package main

import (
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/6Kmfi6HP/opencode2api/core/manager"
)

// 全局热变量（applyConfig 原子写、请求路径原子读）。
var (
	configMu             sync.RWMutex
	configGen            atomic.Int64
	modelAlias           map[string]string
	reasoningEffortMap   map[string]string
	forceDisableThinking bool
	providersCfg         []ProviderCfg
	routingCfg           RoutingCfg
	adminPassword        string
)

func init() {
	modelAlias = map[string]string{}
	reasoningEffortMap = map[string]string{}
}

// loadConfig 读取配置文件（缺失/损坏 → 零值配置）。
func loadConfig(path string) AppConfig {
	data, err := os.ReadFile(path)
	if err != nil {
		return AppConfig{}
	}
	var cfg AppConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		slog.Warn("config parse failed", "path", path, "error", err)
		return AppConfig{}
	}
	return cfg
}

// configStartupRewriteNeeded 判断启动时是否需要回写配置：
// 文件缺失或损坏（无法解析）才回写，健康文件跳过（避免用启动瞬间的旧快照
// 覆盖并发落盘的补丁）。
func configStartupRewriteNeeded(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return true
	}
	var cfg AppConfig
	return json.Unmarshal(data, &cfg) != nil
}

// saveConfig 落盘配置（合并写，保留本结构未声明的键）。
func saveConfig(path string, cfg AppConfig) error {
	return saveConfigWithWriter(path, cfg, "root")
}

// saveConfigWithWriter 落盘配置并标注写者（仅用于排障日志）。
//
// 合并写而非整文件覆盖：config.json 是「根 AppConfig + manager.Config」双结构共用的
// 同一文件（gateway_key / engine_* / 端口等只属于 manager.Config）。整文件覆盖会把
// 管理面板里配好的引擎参数（WARP、代理、密钥池、端口）全部抹掉。
// MergeConfigJSON 只替换本结构声明的键，其余原样保留。
func saveConfigWithWriter(path string, cfg AppConfig, writer string) error {
	_ = writer
	merged, err := manager.MergeConfigJSON(path, cfg)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, merged, 0o644)
}

// applyConfig 把配置应用到进程内全局状态（热重载与启动共用）。
//
// 注意：providers 变化必须重建厂商聚合器，否则新加的 custom 源在供应商服务
// （50080）热重载后仍不可见（它只在进程启动时装配过一次）。
// rebuildVendors 需要读 configMu（RLock），因此必须在**释放 configMu 之后**调用。
func applyConfig(cfg AppConfig) {
	configMu.Lock()
	configGen.Add(1)
	if cfg.ModelAlias != nil {
		modelAlias = cfg.ModelAlias
	}
	if cfg.ReasoningEffortMap != nil {
		reasoningEffortMap = cfg.ReasoningEffortMap
	}
	forceDisableThinking = cfg.ForceDisableThinking
	if cfg.Providers != nil {
		providersCfg = append([]ProviderCfg(nil), cfg.Providers...)
	}
	routingCfg = cfg.Routing
	setAutoConfig(cfg.AutoModel)
	configMu.Unlock()

	if globalAgg != nil {
		rebuildVendors()
	}
}

// startConfigWatcher 监控配置文件变化并热应用（不重启进程，避免切断 SSE 流）。
func startConfigWatcher(path string) {
	go func() {
		// 3s：配置热加载属低频运维动作，降低每进程每秒一次的文件读。
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()
		lastData, _ := os.ReadFile(path)
		for range ticker.C {
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			if string(data) == string(lastData) {
				continue
			}
			lastData = data
			var cfg AppConfig
			if err := json.Unmarshal(data, &cfg); err != nil {
				slog.Warn("config reload parse failed", "path", path, "error", err)
				continue
			}
			applyConfig(cfg)
			slog.Info("config reloaded", "path", path)
		}
	}()
}

// maxRouteRetries 返回同模型路由重试上限（固定值，不随池规模放大）。
func maxRouteRetries() int {
	return maxUpstreamRetries
}

// splitTrimmed 按换行/逗号切分并去空（配置多行文本字段通用）。
func splitTrimmed(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == '\n' || r == '\r' || r == ',' || r == ';' || r == '，' || r == '；'
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if k := strings.TrimSpace(f); k != "" {
			out = append(out, k)
		}
	}
	return out
}
