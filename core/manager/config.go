// 应用级配置（Rust config.rs 移植）：dataDir/config.json。
// 与实例级 gateway 配置（runtime/<name>/opencode2api.json，由 opencodecfg 生成）不同。
package manager

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
)

// DefaultPassword 是未配置密码时的生效默认值。
const DefaultPassword = "123456"

// Config 应用级配置（字段与 Rust Config 一一对应，JSON 名一致）。
type Config struct {
	BaseURL             string `json:"base_url"`
	DefaultPassword     string `json:"default_password"`
	ClashExternalURL    string `json:"clash_external_url"`
	ClashAuthToken      string `json:"clash_auth_token"`
	TimeoutTTFTMinMS    int64  `json:"timeout_ttft_min_ms,omitempty"`
	TimeoutTTFTMaxMS    int64  `json:"timeout_ttft_max_ms,omitempty"`
	TimeoutSilenceMinMS int64  `json:"timeout_silence_min_ms,omitempty"`
	TimeoutSilenceMaxMS int64  `json:"timeout_silence_max_ms,omitempty"`
	FailoverProbeMin    int64  `json:"failover_probe_min,omitempty"`
	FailoverProbeMax    int64  `json:"failover_probe_max,omitempty"`
	CallLogMax          int64  `json:"call_log_max,omitempty"`
	ShowNodePrefix      bool   `json:"show_node_prefix,omitempty"`
	// UiPollIntervalSec 界面轮询间隔（秒，U3）：nil = 未设置（默认 5），
	// 显式 0 = 关闭轮询，1~60 可配。指针区分「未设置」与「显式 0」，
	// 使 0 = 关轮询能落盘持久生效（ConfigGet/ConfigView 不再把 0 归一为默认 5）。
	UiPollIntervalSec *int `json:"ui_poll_interval_sec,omitempty"`

	// UpstreamProxy 上游代理出口（E1）：非空时实例/探针生成的 active_socks5 指向该代理
	// （生成时剥离 socks5:// / http:// 前缀取 host:port），跳过 sing-box 出口，绕过本机
	// 裸连 IP 被上游风控（429/超时）。留空 = 直连（现状）。
	UpstreamProxy string `json:"upstream_proxy,omitempty"`

	// SubscribeURL 订阅地址；SubscribeIntervalMin 自动拉取间隔（分钟，<=0 不自动拉）。
	SubscribeURL         string `json:"subscribe_url,omitempty"`
	SubscribeIntervalMin int    `json:"subscribe_interval_min,omitempty"`

	// 健康巡检：检查间隔（秒，<=0 不巡检）与自动重启连续失败阈值（<=0 不重启）。
	HealthCheckIntervalSec int `json:"health_check_interval_sec,omitempty"`
	HealthRestartThreshold int `json:"health_restart_threshold,omitempty"`

	// 实例池链路探活（性能模式 P1）：间隔（秒）、单次超时（秒）、质量窗口（分钟）、
	// 探测目标（空 = 按 base_url 自动拼接），<=0 时用默认值（45s / 3s / 10min）；
	// PoolProbeEnabled 未设置（nil）默认开启。
	PoolProbeIntervalSec int    `json:"pool_probe_interval_sec,omitempty"`
	PoolProbeTimeoutSec  int    `json:"pool_probe_timeout_sec,omitempty"`
	PoolQualityWindowMin int    `json:"pool_quality_window_min,omitempty"`
	PoolProbeTarget      string `json:"pool_probe_target,omitempty"`
	PoolProbeEnabled     *bool  `json:"pool_probe_enabled,omitempty"`
	// ProbeSoloEnabled 是否对独享实例（未入池）也做链路质量探测（默认 true；false = 只探测池成员）。
	ProbeSoloEnabled *bool `json:"probe_solo_enabled,omitempty"`

	// 性能模式（P2）：熔断阈值（连续失败达该次数 → open）、半开间隔（秒）、开关。
	// PoolBreakerThreshold/PoolHalfOpenIntervalSec <=0 用默认值（3 / 60）；
	// PoolPerformanceMode 未设置（nil）默认开启（关闭时路由行为与基线一致）。
	PoolBreakerThreshold    int   `json:"pool_breaker_threshold,omitempty"`
	PoolHalfOpenIntervalSec int   `json:"pool_halfopen_interval_sec,omitempty"`
	PoolPerformanceMode     *bool `json:"pool_performance_mode,omitempty"`
	// 链路类坏池自动恢复间隔（秒，S3，<=0 用默认 300）：链路类坏池（如 503）到期放行
	// 1 次探测，成功清 / 失败重新坏池；账号类（401/402/429）永久禁用不受此配置影响。
	BadPoolResetSec int `json:"bad_pool_reset_sec,omitempty"`
	// 请求级竞速并行数上限（P2b/S5）：一次请求并行扇出 N 个候选出口，首个成功者胜
	// （<=0 用默认 2；1 = 关闭）。S5 起为上限，实际副本由压力系数动态决定。
	PoolRaceCopies int `json:"pool_race_copies,omitempty"`
	// 竞速整体预算（毫秒，S1）：一次竞速等待首个成功候选的上限，到期走单发续写（<=0 用默认 10000）。
	RaceBudgetMS int `json:"race_budget_ms,omitempty"`
	// 压力系数分段阈值（S5）：pressure = 活跃请求数/健康节点数，
	// < Low（默认 0.5）→ 全速；Low ≤ p < High（默认 1.0）→ 2；≥ High → 单发。
	PoolRacePressureLow  float64 `json:"pool_race_pressure_low,omitempty"`
	PoolRacePressureHigh float64 `json:"pool_race_pressure_high,omitempty"`
	// 429 感知（S2）：冷却内跳过竞速（秒，默认 30）与指数退避 base/cap（毫秒，默认 1000/30000）。
	RateLimitCooldownSec   int `json:"rate_limit_cooldown_sec,omitempty"`
	RateLimitBackoffBaseMS int `json:"rate_limit_backoff_base_ms,omitempty"`
	RateLimitBackoffCapMS  int `json:"rate_limit_backoff_cap_ms,omitempty"`

	// 并发设置（D3）：扫描 / 批量启停与释放 / 一键测试 / 池链路探活 的 worker 上限（<=0 用默认）。
	ScanConcurrency      int `json:"scan_concurrency,omitempty"`
	BatchConcurrency     int `json:"batch_concurrency,omitempty"`
	TestConcurrency      int `json:"test_concurrency,omitempty"`
	PoolProbeConcurrency int `json:"pool_probe_concurrency,omitempty"`
	// StopScanConcurrency 停止扫描并发上限（N2，默认 4，<=0 用默认）。
	StopScanConcurrency int `json:"stop_scan_concurrency,omitempty"`

	// GatewayKey 统一网关鉴权密钥（空 = 回退默认 sk-unified-local；main 功能 M6）。
	GatewayKey string `json:"gateway_key,omitempty"`

	// 端口配置（0 = 未设置，按 env > config > 编译默认 三源读取）：
	// 供 headless/Web 直跑与自定义部署使用；桌面壳经环境变量按槽位表注入（优先于 config）。
	GatewayPort      uint16 `json:"gateway_port,omitempty"`
	InstanceBasePort uint16 `json:"instance_base_port,omitempty"`
	ProbeAPIPort     uint16 `json:"probe_api_port,omitempty"`
	ProbeSocksPort   uint16 `json:"probe_socks_port,omitempty"`

	// Providers 厂商注册表（透传主程序 AppConfig 格式）：实例子进程/网关子进程
	// 生成的 opencode2api.json 需要带上，才能像核心一样注册多厂商（如 windsurf）。
	Providers []map[string]any `json:"providers,omitempty"`
	// Routing 模型→厂商路由（透传，供子进程按模型路由到正确厂商）。
	Routing map[string]any `json:"routing,omitempty"`
	// AutoModel auto 虚拟模型配置（与根进程 AppConfig 双结构共用 auto_model 键，
	// 任一写者重写 config.json 都不丢；详见 automodel.go）。
	AutoModel *AutoModelCfg `json:"auto_model,omitempty"`
}

// configPath 返回配置文件路径。
func (m *Manager) configPath() string {
	return filepath.Join(m.paths.DataDir, "config.json")
}

// loadConfig 读取应用配置；文件缺失/损坏回退默认值。
func (m *Manager) loadConfig() Config {
	cfg := Config{}
	data, err := os.ReadFile(m.configPath())
	if err != nil {
		return cfg
	}
	_ = json.Unmarshal(data, &cfg)
	return cfg
}

// saveConfig 写回应用配置（原子写，防半写损坏）。
// 注意：config.json 由本 Config 与主程序 AppConfig 两个结构共用（同一物理文件）。
// 若整体覆盖写，会抹掉对方结构独有的字段（如 AppConfig 的 model_alias /
// reasoning_effort_map；主程序启动时无条件 saveConfig(AppConfig) 会把
// gateway_key 等本结构字段抹掉）。因此这里用读-合并-写（MergeConfigJSON）保留
// 对方字段，仅覆盖本结构声明的键。
func (m *Manager) saveConfig(cfg Config) error {
	data, err := MergeConfigJSON(m.configPath(), cfg)
	if err != nil {
		return err
	}
	return writeFileAtomic(m.configPath(), data)
}

// MergeConfigJSON 读-合并-写：以现有文件 JSON 为底，把 v 序列化后的字段覆盖
// 合并上去，保留文件中 v 未声明的其它键（解决 Config 与 AppConfig 双结构共用
// config.json 互相覆盖丢字段的问题）。对 v 声明了但序列化为空（omitempty）的键，
// 会从合并结果中删除——保证「清空某字段」语义生效（如 gateway_key 置空重置默认）。
func MergeConfigJSON(path string, v any) ([]byte, error) {
	merged := map[string]any{}
	if raw, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(raw, &merged) // 损坏 JSON 按空底处理，由本次写覆盖
	}
	declared := declaredJSONKeys(v)
	own, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var ownMap map[string]any
	if err := json.Unmarshal(own, &ownMap); err != nil {
		return nil, err
	}
	for k := range declared {
		if val, ok := ownMap[k]; ok {
			merged[k] = val
		} else {
			delete(merged, k)
		}
	}
	return json.MarshalIndent(merged, "", "  ")
}

// declaredJSONKeys 反射提取结构体全部 json tag 键名（含 omitempty 为空时也会
// 出现在本集合中，供 MergeConfigJSON 判断「声明但未写」→ 删除旧值）。
func declaredJSONKeys(v any) map[string]bool {
	keys := map[string]bool{}
	t := reflect.TypeOf(v)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return keys
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name := strings.Split(tag, ",")[0]
		if name == "" {
			name = f.Name
		}
		keys[name] = true
	}
	return keys
}

// SyncCustomProviders 把自定义模型源条目同步进本配置的 providers 透传
// （后续生成的实例/网关子进程配置经 opencodecfg 继承这些源）。
// 仅替换其中 type=custom 的条目，其余原样保留；providers 原本为空时先物化内建条目
// （显式列表语义下防只写 custom 丢掉 opencode/windsurf）。
// coreConfigPath 与本配置文件相同（同一物理文件）时跳过——核心配置已是唯一事实，避免互覆。
func (m *Manager) SyncCustomProviders(coreConfigPath string, customs []map[string]any) error {
	mp, err1 := filepath.Abs(m.configPath())
	cp, err2 := filepath.Abs(coreConfigPath)
	if err1 == nil && err2 == nil && strings.EqualFold(mp, cp) {
		return nil
	}
	cfg := m.loadConfig()
	cfg.Providers = mergeProviderEntries(cfg.Providers, customs)
	return m.saveConfig(cfg)
}

// mergeProviderEntries 把 custom 条目合并进既有 providers 列表：
// 非 custom 原样保留、custom 全量替换；原列表为空且 customs 非空时物化内建条目
// （显式列表语义下防只写 custom 丢掉 opencode/windsurf）。
func mergeProviderEntries(existing, customs []map[string]any) []map[string]any {
	kept := make([]map[string]any, 0, len(existing)+len(customs))
	for _, p := range existing {
		if t, _ := p["type"].(string); t != "custom" {
			kept = append(kept, p)
		}
	}
	if len(existing) == 0 && len(customs) > 0 {
		for _, t := range []string{"opencode", "windsurf"} {
			kept = append(kept, map[string]any{"id": t, "type": t, "enabled": true})
		}
	}
	return append(kept, customs...)
}

// effectiveDefaultPassword 生效默认密码：未设置 → "123456"。
func (m *Manager) effectiveDefaultPassword() string {
	pw := m.loadConfig().DefaultPassword
	if pw == "" {
		return DefaultPassword
	}
	return pw
}

// ConfigGet 返回配置键值（字符串形态，与 Rust get 一致；未知键报错）。
func (m *Manager) ConfigGet(key string) (string, error) {
	cfg := m.loadConfig()
	switch key {
	case "base_url":
		return cfg.BaseURL, nil
	case "default_password":
		return cfg.DefaultPassword, nil
	case "clash_external_url":
		return cfg.ClashExternalURL, nil
	case "clash_auth_token":
		return cfg.ClashAuthToken, nil
	case "timeout_ttft_min_ms":
		return strconv.FormatInt(cfg.TimeoutTTFTMinMS, 10), nil
	case "timeout_ttft_max_ms":
		return strconv.FormatInt(cfg.TimeoutTTFTMaxMS, 10), nil
	case "timeout_silence_min_ms":
		return strconv.FormatInt(cfg.TimeoutSilenceMinMS, 10), nil
	case "timeout_silence_max_ms":
		return strconv.FormatInt(cfg.TimeoutSilenceMaxMS, 10), nil
	case "failover_probe_min":
		return strconv.FormatInt(cfg.FailoverProbeMin, 10), nil
	case "failover_probe_max":
		return strconv.FormatInt(cfg.FailoverProbeMax, 10), nil
	case "call_log_max":
		return strconv.FormatInt(cfg.CallLogMax, 10), nil
	case "show_node_prefix":
		return strconv.FormatBool(cfg.ShowNodePrefix), nil
	case "ui_poll_interval_sec":
		if cfg.UiPollIntervalSec == nil {
			return strconv.Itoa(defaultUiPollIntervalSec), nil
		}
		return strconv.Itoa(*cfg.UiPollIntervalSec), nil
	case "upstream_proxy":
		return cfg.UpstreamProxy, nil
	case "subscribe_url":
		return cfg.SubscribeURL, nil
	case "subscribe_interval_min":
		return strconv.Itoa(cfg.SubscribeIntervalMin), nil
	case "health_check_interval_sec":
		return strconv.Itoa(cfg.HealthCheckIntervalSec), nil
	case "health_restart_threshold":
		return strconv.Itoa(cfg.HealthRestartThreshold), nil
	case "pool_probe_interval_sec":
		return strconv.Itoa(cfg.PoolProbeIntervalSec), nil
	case "pool_probe_timeout_sec":
		return strconv.Itoa(cfg.PoolProbeTimeoutSec), nil
	case "pool_probe_target":
		return cfg.PoolProbeTarget, nil
	case "pool_quality_window_min":
		return strconv.Itoa(cfg.PoolQualityWindowMin), nil
	case "pool_probe_enabled":
		return strconv.FormatBool(poolProbeEnabled(cfg)), nil
	case "probe_solo_enabled":
		return strconv.FormatBool(probeSoloEnabled(cfg)), nil
	case "pool_breaker_threshold":
		return strconv.Itoa(cfg.PoolBreakerThreshold), nil
	case "pool_halfopen_interval_sec":
		return strconv.Itoa(cfg.PoolHalfOpenIntervalSec), nil
	case "bad_pool_reset_sec":
		return strconv.Itoa(cfg.BadPoolResetSec), nil
	case "pool_performance_mode":
		return strconv.FormatBool(poolPerfModeEnabled(cfg)), nil
	case "pool_race_copies":
		return strconv.Itoa(cfg.PoolRaceCopies), nil
	case "race_budget_ms":
		return strconv.Itoa(cfg.RaceBudgetMS), nil
	case "pool_race_pressure_low":
		return strconv.FormatFloat(cfg.PoolRacePressureLow, 'f', -1, 64), nil
	case "pool_race_pressure_high":
		return strconv.FormatFloat(cfg.PoolRacePressureHigh, 'f', -1, 64), nil
	case "rate_limit_cooldown_sec":
		return strconv.Itoa(cfg.RateLimitCooldownSec), nil
	case "rate_limit_backoff_base_ms":
		return strconv.Itoa(cfg.RateLimitBackoffBaseMS), nil
	case "rate_limit_backoff_cap_ms":
		return strconv.Itoa(cfg.RateLimitBackoffCapMS), nil
	case "scan_concurrency":
		return strconv.Itoa(cfg.ScanConcurrency), nil
	case "stop_scan_concurrency":
		return strconv.Itoa(stopScanConcurrencyOf(cfg)), nil
	case "batch_concurrency":
		return strconv.Itoa(cfg.BatchConcurrency), nil
	case "test_concurrency":
		return strconv.Itoa(cfg.TestConcurrency), nil
	case "pool_probe_concurrency":
		return strconv.Itoa(cfg.PoolProbeConcurrency), nil
	case "gateway_key":
		// 密钥不回显：设置过返回掩码，未设置返回空（main 语义一致）。
		if cfg.GatewayKey == "" {
			return "", nil
		}
		return "******", nil
	case "gateway_port":
		return strconv.FormatUint(uint64(cfg.GatewayPort), 10), nil
	case "instance_base_port":
		return strconv.FormatUint(uint64(cfg.InstanceBasePort), 10), nil
	case "probe_api_port":
		return strconv.FormatUint(uint64(cfg.ProbeAPIPort), 10), nil
	case "probe_socks_port":
		return strconv.FormatUint(uint64(cfg.ProbeSocksPort), 10), nil
	default:
		return "", fmt.Errorf("Unknown config key: %s", key)
	}
}

// ConfigSet 设置配置键（int/bool 自动解析；未知键报错）并落盘。
func (m *Manager) ConfigSet(key, value string) error {
	cfg := m.loadConfig()
	parseInt := func() (int64, error) {
		v, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid integer for %s: %s", key, value)
		}
		return v, nil
	}
	// parsePort 解析端口（空/"0"=未设置=0；合法 1-65535）。
	parsePort := func() (uint16, error) {
		if value == "" || value == "0" {
			return 0, nil
		}
		v, err := strconv.ParseUint(value, 10, 16)
		if err != nil || v == 0 || v > 65535 {
			return 0, fmt.Errorf("invalid port for %s: %s", key, value)
		}
		return uint16(v), nil
	}
	switch key {
	case "base_url":
		cfg.BaseURL = value
	case "default_password":
		cfg.DefaultPassword = value
	case "clash_external_url":
		cfg.ClashExternalURL = value
	case "clash_auth_token":
		cfg.ClashAuthToken = value
	case "timeout_ttft_min_ms":
		v, err := parseInt()
		if err != nil {
			return err
		}
		cfg.TimeoutTTFTMinMS = v
	case "timeout_ttft_max_ms":
		v, err := parseInt()
		if err != nil {
			return err
		}
		cfg.TimeoutTTFTMaxMS = v
	case "timeout_silence_min_ms":
		v, err := parseInt()
		if err != nil {
			return err
		}
		cfg.TimeoutSilenceMinMS = v
	case "timeout_silence_max_ms":
		v, err := parseInt()
		if err != nil {
			return err
		}
		cfg.TimeoutSilenceMaxMS = v
	case "failover_probe_min":
		v, err := parseInt()
		if err != nil {
			return err
		}
		cfg.FailoverProbeMin = v
	case "failover_probe_max":
		v, err := parseInt()
		if err != nil {
			return err
		}
		cfg.FailoverProbeMax = v
	case "call_log_max":
		v, err := parseInt()
		if err != nil {
			return err
		}
		cfg.CallLogMax = v
	case "show_node_prefix":
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("invalid boolean for show_node_prefix: %s", value)
		}
		cfg.ShowNodePrefix = b
	case "ui_poll_interval_sec":
		v, err := parseInt()
		if err != nil {
			return err
		}
		// nil = 未设置（回退默认 5）；0 = 显式关闭轮询；负数/超界（>60）非法回退默认。
		if v < 0 || v > 60 {
			cfg.UiPollIntervalSec = nil
		} else {
			n := int(v)
			cfg.UiPollIntervalSec = &n
		}
	case "upstream_proxy":
		// 原样存储（生成子进程配置时才剥离 scheme / 校验端口，非法值回退直连）。
		cfg.UpstreamProxy = strings.TrimSpace(value)
	case "subscribe_url":
		cfg.SubscribeURL = strings.TrimSpace(value)
	case "subscribe_interval_min":
		v, err := parseInt()
		if err != nil {
			return err
		}
		cfg.SubscribeIntervalMin = int(v)
	case "health_check_interval_sec":
		v, err := parseInt()
		if err != nil {
			return err
		}
		cfg.HealthCheckIntervalSec = int(v)
	case "health_restart_threshold":
		v, err := parseInt()
		if err != nil {
			return err
		}
		cfg.HealthRestartThreshold = int(v)
	case "pool_probe_interval_sec":
		v, err := parseInt()
		if err != nil {
			return err
		}
		if v < 0 {
			return errors.New("pool_probe_interval_sec 需 >= 0")
		}
		cfg.PoolProbeIntervalSec = int(v)
	case "pool_probe_timeout_sec":
		v, err := parseInt()
		if err != nil {
			return err
		}
		if v < 0 {
			return errors.New("pool_probe_timeout_sec 需 >= 0")
		}
		cfg.PoolProbeTimeoutSec = int(v)
	case "pool_probe_target":
		cfg.PoolProbeTarget = strings.TrimSpace(value)
	case "pool_quality_window_min":
		v, err := parseInt()
		if err != nil {
			return err
		}
		if v < 0 {
			return errors.New("pool_quality_window_min 需 >= 0")
		}
		cfg.PoolQualityWindowMin = int(v)
	case "pool_probe_enabled":
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("invalid boolean for pool_probe_enabled: %s", value)
		}
		cfg.PoolProbeEnabled = &b
	case "probe_solo_enabled":
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("invalid boolean for probe_solo_enabled: %s", value)
		}
		cfg.ProbeSoloEnabled = &b
	case "pool_breaker_threshold":
		v, err := parseInt()
		if err != nil {
			return err
		}
		if v < 0 {
			return errors.New("pool_breaker_threshold 需 >= 0")
		}
		cfg.PoolBreakerThreshold = int(v)
	case "pool_halfopen_interval_sec":
		v, err := parseInt()
		if err != nil {
			return err
		}
		if v < 0 {
			return errors.New("pool_halfopen_interval_sec 需 >= 0")
		}
		cfg.PoolHalfOpenIntervalSec = int(v)
	case "bad_pool_reset_sec":
		v, err := parseInt()
		if err != nil {
			return err
		}
		if v < 0 {
			return errors.New("bad_pool_reset_sec 需 >= 0")
		}
		cfg.BadPoolResetSec = int(v)
	case "pool_performance_mode":
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("invalid boolean for pool_performance_mode: %s", value)
		}
		cfg.PoolPerformanceMode = &b
	case "pool_race_copies":
		v, err := parseInt()
		if err != nil {
			return err
		}
		if v < 1 || v > 4 {
			return errors.New("pool_race_copies 需在 1~4 之间（1 = 关闭竞速）")
		}
		cfg.PoolRaceCopies = int(v)
	case "race_budget_ms":
		v, err := parseInt()
		if err != nil {
			return err
		}
		if v < 0 {
			return errors.New("race_budget_ms 需 >= 0")
		}
		cfg.RaceBudgetMS = int(v)
	case "pool_race_pressure_low":
		v, err := strconv.ParseFloat(value, 64)
		if err != nil || v < 0 {
			return errors.New("pool_race_pressure_low 需为非负浮点数")
		}
		cfg.PoolRacePressureLow = v
	case "pool_race_pressure_high":
		v, err := strconv.ParseFloat(value, 64)
		if err != nil || v < 0 {
			return errors.New("pool_race_pressure_high 需为非负浮点数")
		}
		cfg.PoolRacePressureHigh = v
	case "rate_limit_cooldown_sec":
		v, err := parseInt()
		if err != nil {
			return err
		}
		if v < 0 {
			return errors.New("rate_limit_cooldown_sec 需 >= 0")
		}
		cfg.RateLimitCooldownSec = int(v)
	case "rate_limit_backoff_base_ms":
		v, err := parseInt()
		if err != nil {
			return err
		}
		if v < 0 {
			return errors.New("rate_limit_backoff_base_ms 需 >= 0")
		}
		cfg.RateLimitBackoffBaseMS = int(v)
	case "rate_limit_backoff_cap_ms":
		v, err := parseInt()
		if err != nil {
			return err
		}
		if v < 0 {
			return errors.New("rate_limit_backoff_cap_ms 需 >= 0")
		}
		cfg.RateLimitBackoffCapMS = int(v)
	case "scan_concurrency":
		v, err := parseInt()
		if err != nil {
			return err
		}
		if v < 1 || v > 16 {
			return errors.New("scan_concurrency 需在 1~16 之间")
		}
		cfg.ScanConcurrency = int(v)
	case "stop_scan_concurrency":
		v, err := parseInt()
		if err != nil {
			return err
		}
		if v < 1 || v > 8 {
			// 非法值回退默认 4（不落盘，stopScanConcurrencyOf 兜底）。
			cfg.StopScanConcurrency = 0
		} else {
			cfg.StopScanConcurrency = int(v)
		}
	case "batch_concurrency":
		v, err := parseInt()
		if err != nil {
			return err
		}
		if v < 1 || v > 16 {
			return errors.New("batch_concurrency 需在 1~16 之间")
		}
		cfg.BatchConcurrency = int(v)
	case "test_concurrency":
		v, err := parseInt()
		if err != nil {
			return err
		}
		if v < 1 || v > 16 {
			return errors.New("test_concurrency 需在 1~16 之间")
		}
		cfg.TestConcurrency = int(v)
	case "pool_probe_concurrency":
		v, err := parseInt()
		if err != nil {
			return err
		}
		if v < 1 || v > 16 {
			return errors.New("pool_probe_concurrency 需在 1~16 之间")
		}
		cfg.PoolProbeConcurrency = int(v)
	case "gateway_key":
		// 空串 = 重置为默认（gatewayKey() 回退 sk-unified-local）；非空需至少 8 字符（main 校验一致）。
		if value == "" {
			cfg.GatewayKey = ""
		} else if len(value) < 8 {
			return errors.New("网关密钥至少 8 个字符")
		} else {
			cfg.GatewayKey = value
		}
	case "gateway_port":
		port, err := parsePort()
		if err != nil {
			return err
		}
		cfg.GatewayPort = port
	case "instance_base_port":
		port, err := parsePort()
		if err != nil {
			return err
		}
		cfg.InstanceBasePort = port
	case "probe_api_port":
		port, err := parsePort()
		if err != nil {
			return err
		}
		cfg.ProbeAPIPort = port
	case "probe_socks_port":
		port, err := parsePort()
		if err != nil {
			return err
		}
		cfg.ProbeSocksPort = port
	default:
		return errors.New("Unknown config key: " + key)
	}
	if err := m.saveConfig(cfg); err != nil {
		return err
	}
	// T4: 网关密钥立即生效——更新内存密码并热重启网关进程（若正在运行）；
	// 不触碰任何实例，也不等待下次网关自然重启。
	if key == "gateway_key" {
		if err := m.Gateway().ApplyKey(effectiveGatewayKey(cfg), m.Run()); err != nil {
			return fmt.Errorf("密钥已保存，但网关热重启失败: %w", err)
		}
	}
	return nil
}

// ConfigView 是前端 /api/admin/config 的响应形态（密码脱敏）。
type ConfigView struct {
	BaseURL                 string  `json:"base_url"`
	DefaultPassword         string  `json:"default_password"`
	HasPassword             bool    `json:"has_password"`
	ClashExternalURL        string  `json:"clash_external_url"`
	HasClashToken           bool    `json:"has_clash_token"`
	TimeoutTTFTMinMS        int64   `json:"timeout_ttft_min_ms"`
	TimeoutTTFTMaxMS        int64   `json:"timeout_ttft_max_ms"`
	TimeoutSilenceMinMS     int64   `json:"timeout_silence_min_ms"`
	TimeoutSilenceMaxMS     int64   `json:"timeout_silence_max_ms"`
	FailoverProbeMin        int64   `json:"failover_probe_min"`
	FailoverProbeMax        int64   `json:"failover_probe_max"`
	CallLogMax              int64   `json:"call_log_max"`
	ShowNodePrefix          bool    `json:"show_node_prefix"`
	UiPollIntervalSec       int     `json:"ui_poll_interval_sec"`
	UpstreamProxy           string  `json:"upstream_proxy"`
	SubscribeURL            string  `json:"subscribe_url"`
	SubscribeIntervalMin    int     `json:"subscribe_interval_min"`
	HealthCheckIntervalSec  int     `json:"health_check_interval_sec"`
	HealthRestartThreshold  int     `json:"health_restart_threshold"`
	PoolProbeIntervalSec    int     `json:"pool_probe_interval_sec"`
	PoolProbeTimeoutSec     int     `json:"pool_probe_timeout_sec"`
	PoolProbeTarget         string  `json:"pool_probe_target"`
	PoolQualityWindowMin    int     `json:"pool_quality_window_min"`
	PoolProbeEnabled        bool    `json:"pool_probe_enabled"`
	ProbeSoloEnabled        bool    `json:"probe_solo_enabled"`
	PoolBreakerThreshold    int     `json:"pool_breaker_threshold"`
	PoolHalfOpenIntervalSec int     `json:"pool_halfopen_interval_sec"`
	BadPoolResetSec         int     `json:"bad_pool_reset_sec"`
	PoolPerformanceMode     bool    `json:"pool_performance_mode"`
	PoolRaceCopies          int     `json:"pool_race_copies"`
	RaceBudgetMS            int     `json:"race_budget_ms"`
	PoolRacePressureLow     float64 `json:"pool_race_pressure_low"`
	PoolRacePressureHigh    float64 `json:"pool_race_pressure_high"`
	RateLimitCooldownSec    int     `json:"rate_limit_cooldown_sec"`
	RateLimitBackoffBaseMS  int     `json:"rate_limit_backoff_base_ms"`
	RateLimitBackoffCapMS   int     `json:"rate_limit_backoff_cap_ms"`
	ScanConcurrency         int     `json:"scan_concurrency"`
	StopScanConcurrency     int     `json:"stop_scan_concurrency"`
	BatchConcurrency        int     `json:"batch_concurrency"`
	TestConcurrency         int     `json:"test_concurrency"`
	PoolProbeConcurrency    int     `json:"pool_probe_concurrency"`
	HasGatewayKey           bool    `json:"has_gateway_key"`
	GatewayKey              string  `json:"gateway_key"`
}

// ConfigViewOf 生成前端视图（密码与 clash token 脱敏为掩码）。
// 默认值与 Rust config_get 一致：未设置时返回默认超时区间/探测数/日志上限，
// 避免前端拿到 0 导致输入框空白、校验拦截（min ≤ 0 非法）而"按钮不可用"。
func (m *Manager) ConfigViewOf() ConfigView {
	cfg := m.loadConfig()
	def := func(v, d int64) int64 {
		if v <= 0 {
			return d
		}
		return v
	}
	return ConfigView{
		BaseURL:                 cfg.BaseURL,
		DefaultPassword:         maskSecret(cfg.DefaultPassword),
		HasPassword:             cfg.DefaultPassword != "",
		ClashExternalURL:        cfg.ClashExternalURL,
		HasClashToken:           cfg.ClashAuthToken != "",
		TimeoutTTFTMinMS:        def(cfg.TimeoutTTFTMinMS, 10000),
		TimeoutTTFTMaxMS:        def(cfg.TimeoutTTFTMaxMS, 10000),
		TimeoutSilenceMinMS:     def(cfg.TimeoutSilenceMinMS, 5000),
		TimeoutSilenceMaxMS:     def(cfg.TimeoutSilenceMaxMS, 5000),
		FailoverProbeMin:        def(cfg.FailoverProbeMin, 2),
		FailoverProbeMax:        def(cfg.FailoverProbeMax, 3),
		CallLogMax:              def(cfg.CallLogMax, 5000),
		ShowNodePrefix:          cfg.ShowNodePrefix,
		UiPollIntervalSec:       uiPollIntervalSecOf(cfg),
		UpstreamProxy:           cfg.UpstreamProxy,
		SubscribeURL:            cfg.SubscribeURL,
		SubscribeIntervalMin:    cfg.SubscribeIntervalMin,
		HealthCheckIntervalSec:  cfg.HealthCheckIntervalSec,
		HealthRestartThreshold:  cfg.HealthRestartThreshold,
		PoolProbeIntervalSec:    poolProbeInterval(cfg),
		PoolProbeTimeoutSec:     int(poolProbeTimeout(cfg).Seconds()),
		PoolProbeTarget:         cfg.PoolProbeTarget,
		PoolQualityWindowMin:    int(poolQualityWindowSec(cfg) / 60),
		PoolProbeEnabled:        poolProbeEnabled(cfg),
		ProbeSoloEnabled:        probeSoloEnabled(cfg),
		PoolBreakerThreshold:    poolBreakerThresholdOf(cfg),
		PoolHalfOpenIntervalSec: poolHalfOpenIntervalOf(cfg),
		BadPoolResetSec:         badPoolResetSecOf(cfg),
		PoolPerformanceMode:     poolPerfModeEnabled(cfg),
		PoolRaceCopies:          poolRaceCopiesOf(cfg),
		RaceBudgetMS:            poolRaceBudgetMSOf(cfg),
		PoolRacePressureLow:     poolRacePressureLowOf(cfg),
		PoolRacePressureHigh:    poolRacePressureHighOf(cfg),
		RateLimitCooldownSec:    rateLimitCooldownSecOf(cfg),
		RateLimitBackoffBaseMS:  rateLimitBackoffBaseMSOf(cfg),
		RateLimitBackoffCapMS:   rateLimitBackoffCapMSOf(cfg),
		ScanConcurrency:         scanConcurrencyOf(cfg),
		StopScanConcurrency:     stopScanConcurrencyOf(cfg),
		BatchConcurrency:        batchConcurrencyOf(cfg),
		TestConcurrency:         testConcurrencyOf(cfg),
		PoolProbeConcurrency:    poolProbeConcurrencyOf(cfg),
		HasGatewayKey:           cfg.GatewayKey != "",
		GatewayKey:              maskSecret(cfg.GatewayKey),
	}
}

// defaultUiPollIntervalSec 界面轮询间隔默认值（秒，U3）。
const defaultUiPollIntervalSec = 5

// uiPollIntervalSecOf 生效的界面轮询间隔（秒）：nil（未设置）与非法值
// （负数或 >60）回退默认 5；显式 0 = 关闭轮询，1~60 直接用。
func uiPollIntervalSecOf(cfg Config) int {
	if cfg.UiPollIntervalSec == nil {
		return defaultUiPollIntervalSec
	}
	if *cfg.UiPollIntervalSec < 0 || *cfg.UiPollIntervalSec > 60 {
		return defaultUiPollIntervalSec
	}
	return *cfg.UiPollIntervalSec
}

// effectiveGatewayKey 生效的统一网关密钥：配置未设置/为空时回退默认 "sk-unified-local"。
func effectiveGatewayKey(cfg Config) string {
	if cfg.GatewayKey != "" {
		return cfg.GatewayKey
	}
	return unifiedGatewayKey
}

// maskSecret 非空秘密展示为 ***。
func maskSecret(s string) string {
	if s == "" {
		return ""
	}
	return strings.Repeat("*", len(s))
}
