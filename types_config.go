// Part of the P1 (core split) refactor: code moved out of main.go.
// Same package (main) - do not change package clause manually.
package main

import "github.com/6Kmfi6HP/opencode2api/core/manager"

type AppConfig struct {
	ModelAlias           map[string]string `json:"model_alias"`
	ReasoningEffortMap   map[string]string `json:"reasoning_effort_map"`
	ForceDisableThinking bool              `json:"force_disable_thinking"`
	Socks5Proxies        []Socks5Proxy     `json:"socks5_proxies,omitempty"`
	ActiveSocks5         string            `json:"active_socks5,omitempty"`
	// UpstreamProxy 上游代理出口（E1）：非空时实例/探针的 active_socks5 直接指向该代理
	// （剥离 socks5:// / http:// 前缀取 host:port），跳过 sing-box 节点出口，绕过本机
	// 裸连 IP 被上游风控（429/超时）。留空 = 直连（现状）。
	UpstreamProxy string `json:"upstream_proxy,omitempty"`
	// RouteMode 网关/代理池路由模式：failover（默认，成功不动游标，失败才切换）| round_robin
	RouteMode string `json:"route_mode,omitempty"`

	// 流内超时切换配置（毫秒；区间随机，防上游识别为定时扫描）
	TTFTMinMS    int `json:"timeout_ttft_min_ms,omitempty"`
	TTFTMaxMS    int `json:"timeout_ttft_max_ms,omitempty"`
	SilenceMinMS int `json:"timeout_silence_min_ms,omitempty"`
	SilenceMaxMS int `json:"timeout_silence_max_ms,omitempty"`
	// PreContentSilenceMinMS/MaxMS 预内容静默宽容区间（毫秒；默认见 DefaultPreContentSilence）。
	// 已连接但未吐可见内容（子代理/工具调用长思考）时的静默超时，默认 30s。
	PreContentSilenceMinMS int `json:"timeout_precontent_silence_min_ms,omitempty"`
	PreContentSilenceMaxMS int `json:"timeout_precontent_silence_max_ms,omitempty"`
	ProbeMin     int `json:"failover_probe_min,omitempty"`
	ProbeMax     int `json:"failover_probe_max,omitempty"`
	// 调用日志保留上限（条）
	CallLogMax int `json:"call_log_max,omitempty"`
	// 界面轮询间隔（秒，U3）：nil = 未设置（默认 5），显式 0 = 关闭轮询，1~60 可配。
	UiPollIntervalSec *int `json:"ui_poll_interval_sec,omitempty"`
	// 停止扫描并发数（N2，默认 4）：与 scan_concurrency 一起构成节点扫描并发配置，
	// 经管理面 /api/admin/config 透传（本端不直接使用，子进程透传兼容）。
	StopScanConcurrency int `json:"stop_scan_concurrency,omitempty"`

	// 坏状态码组：状态码 → 原因文案，遇到即切节点并计数（可配置，默认见 badStatusCodes）
	BadStatusCodes map[string]string `json:"bad_status_codes,omitempty"`
	// 坏池阈值：连续坏状态码次数达到后节点进坏池（默认 3）
	BadThreshold int `json:"bad_threshold,omitempty"`

	// 实例池性能模式（P2）：质量加权路由 + 熔断/半开。
	// PoolPerformanceMode 未设置（nil）默认开启；关闭时路由行为与基线一致。
	PoolPerformanceMode *bool `json:"pool_performance_mode,omitempty"`
	// 熔断阈值：连续失败达该次数后节点进入熔断（open）态（默认 3）。
	PoolBreakerThreshold int `json:"pool_breaker_threshold,omitempty"`
	// 半开间隔：熔断节点按该周期（秒）放行 1 个探测请求，成功即恢复（默认 60）。
	PoolHalfOpenIntervalSec int `json:"pool_halfopen_interval_sec,omitempty"`
	// 链路类坏池自动恢复间隔（秒，S3，默认 300）：链路类坏池（如 503）到期后放行 1 次
	// 探测，成功清状态 / 失败重新坏池；账号类（401/402/429）永久禁用不受此配置影响。
	BadPoolResetSec int `json:"bad_pool_reset_sec,omitempty"`
	// 请求级竞速并行数上限（P2b/S5）：一次请求并行扇出 N 个候选出口，首个成功者胜
	// （默认 2；1 = 关闭竞速）。S5 起为上限，实际副本由压力系数动态决定。
	PoolRaceCopies int `json:"pool_race_copies,omitempty"`
	// 竞速整体预算（毫秒，S1）：一次竞速等待首个成功候选的上限，到期走单发续写（默认 10000）。
	RaceBudgetMS int `json:"race_budget_ms,omitempty"`
	// 压力系数分段阈值（S5）：pressure = 活跃请求数/健康节点数，
	// < Low（默认 0.5）→ 全速竞速；Low ≤ p < High（默认 1.0）→ 温和竞速（2）；≥ High → 单发。
	PoolRacePressureLow  float64 `json:"pool_race_pressure_low,omitempty"`
	PoolRacePressureHigh float64 `json:"pool_race_pressure_high,omitempty"`
	// 429 感知（S2）：冷却内跳过竞速（秒，默认 30）与指数退避 base/cap（毫秒，默认 1000/30000）。
	RateLimitCooldownSec   int `json:"rate_limit_cooldown_sec,omitempty"`
	RateLimitBackoffBaseMS int `json:"rate_limit_backoff_base_ms,omitempty"`
	RateLimitBackoffCapMS  int `json:"rate_limit_backoff_cap_ms,omitempty"`
	// ShowNodePrefix 是否在对话流首段展示「🤖 节点 · 模型」前缀（默认关闭）
	ShowNodePrefix *bool `json:"show_node_prefix,omitempty"`

	// Providers 厂商注册表（配置驱动；缺省 = 单 opencode）
	Providers []ProviderCfg `json:"providers,omitempty"`
	// Routing 模型→厂商路由
	Routing RoutingCfg `json:"routing,omitempty"`
	// AutoModel auto 虚拟模型配置（与 core/manager Config 双结构共用 auto_model 键，
	// 任一写者重写 config.json 都不丢；子进程经 3s 配置热重载消费）。
	AutoModel *manager.AutoModelCfg `json:"auto_model,omitempty"`
}

// ProviderCfg 描述一个模型厂商（vendors/ 下的实现）。
type ProviderCfg struct {
	ID   string `json:"id"`   // 厂商标识，与厂商实现 ID() 一致（如 "opencode"）
	Type string `json:"type"` // 厂商类型（"opencode" | "windsurf" | "custom" | ...），用于选择实现
	Name string `json:"name,omitempty"`
	// Enabled 开关；nil 视为 true。
	Enabled *bool `json:"enabled,omitempty"`
	// Params 厂商自定义装配参数（透传给工厂，厂商自行解释）。
	// 以 "_" 开头的键保留给 core 运行时注入（Transport 等），配置中的同名键会被忽略。
	Params map[string]any `json:"params,omitempty"`
}

// RoutingCfg 是模型→厂商分发配置。
type RoutingCfg struct {
	// ModelProvider 模型名 → 厂商 ID 的强制映射（优先于厂商目录匹配）。
	ModelProvider map[string]string `json:"model_provider_map,omitempty"`
	// DefaultProvider 兜底厂商（缺省 "opencode"）。
	DefaultProvider string `json:"default_provider,omitempty"`
}

// ======================== Claude Messages API 类型 ========================
