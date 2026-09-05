// 实例/网关 opencode2api.json 配置生成（Rust opencode_cfg.rs 移植）。
package manager

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"sort"
	"strconv"
	"strings"
)

// defaultFreeAliases 免费模型别名（-free 后缀隐藏）。
var defaultFreeAliases = []struct{ from, to string }{
	{"deepseek-v4-flash", "deepseek-v4-flash-free"},
	{"mimo-v2.5", "mimo-v2.5-free"},
	{"ling-3.0-flash", "ling-3.0-flash-free"},
	{"nemotron-3-ultra", "nemotron-3-ultra-free"},
	{"north-mini-code", "north-mini-code-free"},
	{"laguna-s-2.1", "laguna-s-2.1-free"},
}

func aliasMap() map[string]string {
	m := map[string]string{}
	for _, a := range defaultFreeAliases {
		m[a.from] = a.to
	}
	return m
}

func reasoningMap() map[string]string {
	return map[string]string{"minimal": "low", "medium": "medium", "high": "high"}
}

// upstreamProxyAddr 把配置的 upstream_proxy 归一化为裸 host:port。
// socks.go 的 socks5Dial 直接 net.Dial("tcp", addr)，地址不接受带 scheme 的前缀——
// 因此 socks5:// 与 http:// 前缀在此剥离（Clash mixed-port 同时支持两种协议，统一
// 存 host:port）。空串（未配置）返回 "" 走本地直连；已配置但非法（无端口 / // 前缀 /
// 端口 0 等）也返回 "" 并告警（G24：静默回退会让用户以为走了代理、实际直连，无从排查）。
func upstreamProxyAddr(raw string) string {
	addr := strings.TrimSpace(raw)
	if addr == "" {
		return ""
	}
	if i := strings.Index(addr, "://"); i >= 0 {
		addr = addr[i+len("://"):]
	}
	// G24：// 前缀（如 "socks5:////127.0.0.1:5" 归一化后剩 "//127.0.0.1:5"）判非法——
	// SplitHostPort 能拆出它，但 socks5Dial 的 net.Dial 会必失败。
	if strings.HasPrefix(addr, "//") {
		upstreamProxyWarn(raw)
		return ""
	}
	if _, port, err := net.SplitHostPort(addr); err != nil {
		upstreamProxyWarn(raw)
		return ""
	} else if n, err := strconv.ParseUint(port, 10, 16); err != nil || n == 0 {
		// SplitHostPort 不校验端口数字（如 "7897/path" 也能拆出），
		// socks5Dial 直接 net.Dial 需要数字端口；端口 0 也是病态输入（G24）。
		upstreamProxyWarn(raw)
		return ""
	}
	return addr
}

// upstreamProxyWarn 告警一次非法 upstream_proxy（G24：失败回退不再静默）。
func upstreamProxyWarn(raw string) {
	slog.Warn("upstream_proxy invalid, falling back to local direct", "value", raw)
}

// buildOpenCodeCfg 单实例配置：默认本地 sing-box 作为唯一 SOCKS5 出口；
// 配置了上游代理（E1）时出口直接指向该代理（跳过 sing-box）。
// probe=true 时为探测模式：不注入厂商 providers/routing/auto_model，
// 避免探测子进程加载 NVIDIA 等付费厂商导致 /v1/models 返回付费模型、
// pickFreeModel 找不到免费模型而全部判定失败（对齐 Rust build_opencode_config 精简行为）。
func (m *Manager) buildOpenCodeCfg(singboxPort uint16, probe bool) ([]byte, error) {
	appCfg := m.loadConfig()
	proxyAddr := upstreamProxyAddr(appCfg.UpstreamProxy)
	proxyName := "singbox"
	if proxyAddr == "" {
		// 未配置 / 非法值：保持现状——本地 sing-box 出口。
		proxyAddr = fmt.Sprintf("127.0.0.1:%d", singboxPort)
	} else {
		proxyName = "upstream"
	}
	cfg := map[string]any{
		"model_alias":            aliasMap(),
		"reasoning_effort_map":   reasoningMap(),
		"force_disable_thinking": false,
		"socks5_proxies": []any{map[string]any{
			"name": proxyName, "addr": proxyAddr,
		}},
		"active_socks5":    proxyAddr,
		"show_node_prefix": appCfg.ShowNodePrefix,
	}
	if !probe {
		// 透传厂商注册表 + 路由：实例子进程与核心一致，能注册多厂商（如 windsurf）
		cfg = injectVendorConfig(cfg, appCfg)
		// auto 虚拟模型配置透传：实例子进程同样支持 model:"auto"（候选=本实例模型）。
		if appCfg.AutoModel != nil {
			cfg["auto_model"] = appCfg.AutoModel
		}
	}
	return json.MarshalIndent(cfg, "", "  ")
}

// BuildOpenCodeCfgFor 导出包装（main 装配实例接缝用）。
func (m *Manager) BuildOpenCodeCfgFor(port uint16) ([]byte, error) { return m.buildOpenCodeCfg(port, false) }

// buildRouterCfg 统一网关路由配置：多 sing-box 出口 + route_mode + 超时区间。
func (m *Manager) buildRouterCfg(singboxPorts []uint16, portNames map[uint16]string, routeMode string) ([]byte, error) {
	appCfg := m.loadConfig()
	proxies := []any{}
	sorted := append([]uint16(nil), singboxPorts...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	for _, p := range sorted {
		name := portNames[p]
		if name == "" {
			name = fmt.Sprintf("instance-%d", p)
		}
		proxies = append(proxies, map[string]any{
			"name": name, "addr": fmt.Sprintf("127.0.0.1:%d", p),
		})
	}
	if routeMode == "" {
		routeMode = "round_robin"
	}
	cfg := map[string]any{
		"model_alias":            aliasMap(),
		"reasoning_effort_map":   reasoningMap(),
		"force_disable_thinking": false,
		"socks5_proxies":         proxies,
		"active_socks5":          "__round_robin__",
		"route_mode":             routeMode,
		"show_node_prefix":       appCfg.ShowNodePrefix,
	}
	// 超时/日志区间（0 = 用 core 默认）
	applyIf := func(key string, v int64) {
		if v > 0 {
			cfg[key] = v
		}
	}
	applyIf("timeout_ttft_min_ms", appCfg.TimeoutTTFTMinMS)
	applyIf("timeout_ttft_max_ms", appCfg.TimeoutTTFTMaxMS)
	applyIf("timeout_silence_min_ms", appCfg.TimeoutSilenceMinMS)
	applyIf("timeout_silence_max_ms", appCfg.TimeoutSilenceMaxMS)
	applyIf("timeout_precontent_silence_min_ms", appCfg.PreContentSilenceMinMS)
	applyIf("timeout_precontent_silence_max_ms", appCfg.PreContentSilenceMaxMS)
	applyIf("failover_probe_min", appCfg.FailoverProbeMin)
	applyIf("failover_probe_max", appCfg.FailoverProbeMax)
	applyIf("call_log_max", appCfg.CallLogMax)
	// P2 性能模式：熔断阈值 / 半开间隔 / 开关透传（>0 才写，未配置子进程用默认）。
	applyIf("pool_breaker_threshold", int64(appCfg.PoolBreakerThreshold))
	applyIf("pool_halfopen_interval_sec", int64(appCfg.PoolHalfOpenIntervalSec))
	// S3 链路类坏池自动恢复间隔（秒；>0 才写，未配置子进程用默认 300）。
	applyIf("bad_pool_reset_sec", int64(appCfg.BadPoolResetSec))
	if appCfg.PoolPerformanceMode != nil {
		cfg["pool_performance_mode"] = *appCfg.PoolPerformanceMode
	}
	// P2b 请求级竞速：并行扇出数上限（>0 才写，未配置子进程用默认 2）。
	applyIf("pool_race_copies", int64(appCfg.PoolRaceCopies))
	// S1 竞速整体预算（毫秒；>0 才写，未配置子进程用默认 10000）。
	applyIf("race_budget_ms", int64(appCfg.RaceBudgetMS))
	// S5 压力系数分段阈值（float；>0 才写，未配置子进程用默认 0.5/1.0）。
	applyFloat := func(key string, v float64) {
		if v > 0 {
			cfg[key] = v
		}
	}
	applyFloat("pool_race_pressure_low", appCfg.PoolRacePressureLow)
	applyFloat("pool_race_pressure_high", appCfg.PoolRacePressureHigh)
	// S2 429 感知：冷却秒 / 退避 base/cap 毫秒（>0 才写，未配置子进程用默认 30/1000/30000）。
	applyIf("rate_limit_cooldown_sec", int64(appCfg.RateLimitCooldownSec))
	applyIf("rate_limit_backoff_base_ms", int64(appCfg.RateLimitBackoffBaseMS))
	applyIf("rate_limit_backoff_cap_ms", int64(appCfg.RateLimitBackoffCapMS))
	// 透传厂商注册表 + 路由：网关子进程与核心一致，能注册多厂商（如 windsurf）
	cfg = injectVendorConfig(cfg, appCfg)
	// auto 虚拟模型配置透传：网关子进程在「实例×模型」矩阵上做加权选择。
	if appCfg.AutoModel != nil {
		cfg["auto_model"] = appCfg.AutoModel
	}
	return json.MarshalIndent(cfg, "", "  ")
}

// injectVendorConfig 把管理器配置里的厂商注册表与路由写入子进程配置。
// 子进程（实例子进程 / 网关子进程）与核心同二进制，读同一格式的 opencode2api.json，
// 不带 providers 则只认 opencode —— 多厂商（windsurf 等）在 exe 实例/网关形态下不可用。
func injectVendorConfig(cfg map[string]any, appCfg Config) map[string]any {
	if len(appCfg.Providers) > 0 {
		cfg["providers"] = appCfg.Providers
	}
	if len(appCfg.Routing) > 0 {
		cfg["routing"] = appCfg.Routing
	}
	return cfg
}
