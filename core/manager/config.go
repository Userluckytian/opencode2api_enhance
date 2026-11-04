// 应用级配置（Rust config.rs 移植）：dataDir/config.json。
// 与实例级 gateway 配置（runtime/<name>/opencode2api.json，由 opencodecfg 生成）不同。
package manager

import (
	"encoding/json"
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
//
// v2.0.0 精简：免费层（实例池/节点池/订阅/扫描/竞速/sidecar/链路）全部配置项已移除，
// 只剩管理域与服务端口、以及供应商/路由透传（50080 子进程需要）。
type Config struct {
	BaseURL         string `json:"base_url"`
	DefaultPassword string `json:"default_password"`
	// CallLogMax 调用日志读取上限（0 = 全部历史）。
	CallLogMax int64 `json:"call_log_max,omitempty"`
	// UiPollIntervalSec 界面轮询间隔（秒）：nil = 未设置（默认 5），
	// 显式 0 = 关闭轮询，1~60 可配。
	UiPollIntervalSec *int `json:"ui_poll_interval_sec,omitempty"`

	// GatewayKey 本进程管理的 API 密钥（空 = 回退默认 sk-unified-local）。
	GatewayKey string `json:"gateway_key,omitempty"`

	// 服务端口（0 = 未设置，按 env > config > 编译默认 三源读取）：
	// 桌面壳经环境变量按槽位表注入（优先于 config）。
	GatewayPort  uint16 `json:"gateway_port,omitempty"`
	ProviderPort uint16 `json:"provider_port,omitempty"`

	// Providers 厂商注册表（透传主程序 AppConfig 格式）：供应商服务子进程
	// 生成的配置需要带上，才能像核心一样注册多厂商。
	Providers []map[string]any `json:"providers,omitempty"`
	// Routing 模型→厂商路由（透传，供子进程按模型路由到正确厂商）。
	Routing map[string]any `json:"routing,omitempty"`
	// AutoModel auto 虚拟模型配置（与根 AppConfig 双结构共用 auto_model 键）。
	AutoModel *AutoModelCfg `json:"auto_model,omitempty"`

	// ---- 免费层引擎（40080，opencode2api-win）----
	// EngineEnabled 未设置（nil）默认开启：管理进程自动拉起引擎并看护。
	EngineEnabled *bool `json:"engine_enabled,omitempty"`
	// EngineWarpEnabled 引擎的 WARP 出口池开关。
	EngineWarpEnabled *bool `json:"engine_warp_enabled,omitempty"`
	// EngineWarpPoolSize WARP 出口数上限（1~32；0 = 引擎默认 2）。
	EngineWarpPoolSize int `json:"engine_warp_pool_size,omitempty"`
	// EngineWarpSourceIPv6 外层源 IPv6 前缀（通常一个路由到本机的 /64）。
	EngineWarpSourceIPv6 string `json:"engine_warp_source_ipv6,omitempty"`
	// EngineProxies 引擎出口代理列表（每项 http/https/socks5/socks5h 或 direct）。
	EngineProxies []string `json:"engine_proxies,omitempty"`
	// EngineZenKeys / EngineGoKeys 引擎的可选付费密钥池（空 = 纯匿名免费层）。
	EngineZenKeys []string `json:"engine_zen_keys,omitempty"`
	EngineGoKeys  []string `json:"engine_go_keys,omitempty"`
	// EngineRetryMaxAttempts 引擎单请求重试上限（0 = 默认 3）。
	EngineRetryMaxAttempts int `json:"engine_retry_max_attempts,omitempty"`
	// EngineModelsRefreshSec 引擎模型目录刷新间隔（秒，0 = 默认 300）。
	EngineModelsRefreshSec int `json:"engine_models_refresh_sec,omitempty"`
}
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
// （显式列表语义下防只写 custom 丢掉 opencode）。
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
// （显式列表语义下防只写 custom 丢掉 opencode）。
func mergeProviderEntries(existing, customs []map[string]any) []map[string]any {
	kept := make([]map[string]any, 0, len(existing)+len(customs))
	for _, p := range existing {
		if t, _ := p["type"].(string); t != "custom" {
			kept = append(kept, p)
		}
	}
	if len(existing) == 0 && len(customs) > 0 {
		// 内建厂商仅剩 opencode（windsurf 已于 2026-10-13 移除）。
		kept = append(kept, map[string]any{"id": "opencode", "type": "opencode", "enabled": true})
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
// ConfigGet 读取单个配置键的当前值（字符串形态；布尔为 true/false，端口为十进制）。
// v2.0.0：免费层配置项全部移除，只剩管理域与服务端口、引擎参数。
func (m *Manager) ConfigGet(key string) (string, error) {
	cfg := m.loadConfig()
	switch key {
	case "base_url":
		return cfg.BaseURL, nil
	case "default_password":
		return cfg.DefaultPassword, nil
	case "call_log_max":
		return strconv.FormatInt(cfg.CallLogMax, 10), nil
	case "ui_poll_interval_sec":
		return strconv.Itoa(uiPollIntervalSecOf(cfg)), nil
	case "gateway_key":
		return effectiveGatewayKey(cfg), nil
	case "gateway_port":
		return strconv.Itoa(int(cfg.GatewayPort)), nil
	case "provider_port":
		return strconv.Itoa(int(cfg.ProviderPort)), nil
	case "engine_enabled":
		return strconv.FormatBool(engineEnabled(cfg)), nil
	case "engine_warp_enabled":
		return strconv.FormatBool(engineWarpEnabled(cfg)), nil
	case "engine_warp_pool_size":
		return strconv.Itoa(cfg.EngineWarpPoolSize), nil
	case "engine_warp_source_ipv6":
		return cfg.EngineWarpSourceIPv6, nil
	case "engine_proxies":
		return strings.Join(cfg.EngineProxies, "\n"), nil
	case "engine_zen_keys":
		return strings.Join(cfg.EngineZenKeys, "\n"), nil
	case "engine_go_keys":
		return strings.Join(cfg.EngineGoKeys, "\n"), nil
	case "engine_retry_max_attempts":
		return strconv.Itoa(cfg.EngineRetryMaxAttempts), nil
	case "engine_models_refresh_sec":
		return strconv.Itoa(cfg.EngineModelsRefreshSec), nil
	}
	return "", fmt.Errorf("unknown config key: %s", key)
}

// ConfigSet 设置配置键（int/bool 自动解析；未知键报错）并落盘。
func (m *Manager) ConfigSet(key, value string) error {
	cfg := m.loadConfig()
	parseInt := func() (int64, error) {
		v, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid integer for %s: %s", key, value)
		}
		return v, nil
	}
	parsePort := func() (uint16, error) {
		s := strings.TrimSpace(value)
		if s == "" || s == "0" {
			return 0, nil
		}
		n, err := strconv.ParseUint(s, 10, 16)
		if err != nil {
			return 0, fmt.Errorf("invalid port for %s: %s", key, value)
		}
		return uint16(n), nil
	}
	parseBool := func() (bool, error) {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "1", "true", "yes", "on":
			return true, nil
		case "0", "false", "no", "off", "":
			return false, nil
		}
		return false, fmt.Errorf("invalid bool for %s: %s", key, value)
	}

	switch key {
	case "base_url":
		cfg.BaseURL = strings.TrimSpace(value)
	case "default_password":
		cfg.DefaultPassword = strings.TrimSpace(value)
	case "call_log_max":
		v, err := parseInt()
		if err != nil {
			return err
		}
		cfg.CallLogMax = v
	case "ui_poll_interval_sec":
		v, err := parseInt()
		if err != nil {
			return err
		}
		if v < 0 || v > 60 {
			return fmt.Errorf("ui_poll_interval_sec 需在 0~60 之间")
		}
		n := int(v)
		cfg.UiPollIntervalSec = &n
	case "gateway_key":
		cfg.GatewayKey = strings.TrimSpace(value)
	case "gateway_port":
		p, err := parsePort()
		if err != nil {
			return err
		}
		cfg.GatewayPort = p
	case "provider_port":
		p, err := parsePort()
		if err != nil {
			return err
		}
		cfg.ProviderPort = p
	case "engine_enabled":
		b, err := parseBool()
		if err != nil {
			return err
		}
		cfg.EngineEnabled = &b
	case "engine_warp_enabled":
		b, err := parseBool()
		if err != nil {
			return err
		}
		cfg.EngineWarpEnabled = &b
	case "engine_warp_pool_size":
		v, err := parseInt()
		if err != nil {
			return err
		}
		if v < 0 || v > 32 {
			return fmt.Errorf("engine_warp_pool_size 需在 0~32 之间")
		}
		cfg.EngineWarpPoolSize = int(v)
	case "engine_warp_source_ipv6":
		cfg.EngineWarpSourceIPv6 = strings.TrimSpace(value)
	case "engine_proxies":
		cfg.EngineProxies = splitLinesOrCommas(value)
	case "engine_zen_keys":
		cfg.EngineZenKeys = splitLinesOrCommas(value)
	case "engine_go_keys":
		cfg.EngineGoKeys = splitLinesOrCommas(value)
	case "engine_retry_max_attempts":
		v, err := parseInt()
		if err != nil {
			return err
		}
		if v < 0 || v > 20 {
			return fmt.Errorf("engine_retry_max_attempts 需在 0~20 之间")
		}
		cfg.EngineRetryMaxAttempts = int(v)
	case "engine_models_refresh_sec":
		v, err := parseInt()
		if err != nil {
			return err
		}
		if v < 0 {
			return fmt.Errorf("engine_models_refresh_sec 不能为负")
		}
		cfg.EngineModelsRefreshSec = int(v)
	default:
		return fmt.Errorf("unknown config key: %s", key)
	}

	if err := m.saveConfig(cfg); err != nil {
		return err
	}
	// 端口变更由主进程看门狗在下一轮（≤30s）应用；此处不主动重启，
	// 避免与看门狗并发启停同一进程。
	return nil
}

// splitLinesOrCommas 把多行/逗号（中英文）混排文本切成有序去重列表。
func splitLinesOrCommas(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == '\n' || r == '\r' || r == ',' || r == ';' || r == '，' || r == '；'
	})
	seen := make(map[string]bool, len(fields))
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		k := strings.TrimSpace(f)
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, k)
	}
	return out
}

// engineEnabled 引擎总开关（nil = 默认开启）。
func engineEnabled(cfg Config) bool {
	if cfg.EngineEnabled == nil {
		return true
	}
	return *cfg.EngineEnabled
}

// engineWarpEnabled 引擎 WARP 开关（nil = 默认开启）。
func engineWarpEnabled(cfg Config) bool {
	if cfg.EngineWarpEnabled == nil {
		return true
	}
	return *cfg.EngineWarpEnabled
}

// ConfigView 是前端 /api/admin/config 的响应形态（密码脱敏）。
// ConfigView 前端视图（v2.0.0 精简：免费层配置项已移除）。
type ConfigView struct {
	BaseURL         string `json:"base_url"`
	DefaultPassword string `json:"default_password"`
	HasPassword     bool   `json:"has_password"`
	CallLogMax      int64  `json:"call_log_max"`
	UiPollIntervalSec int  `json:"ui_poll_interval_sec"`
	HasGatewayKey   bool   `json:"has_gateway_key"`
	GatewayKey      string `json:"gateway_key"`

	// 服务端口
	GatewayPort  uint16 `json:"gateway_port"`
	ProviderPort uint16 `json:"provider_port"`
	// 生效值（含环境槽位/默认值），供设置页「端口占用」展示
	GatewayPortEffective  uint16 `json:"gateway_port_effective"`
	ProviderPortEffective uint16 `json:"provider_port_effective"`

	// 免费层引擎（40080）
	EngineEnabled          bool     `json:"engine_enabled"`
	EngineWarpEnabled      bool     `json:"engine_warp_enabled"`
	EngineWarpPoolSize     int      `json:"engine_warp_pool_size"`
	EngineWarpSourceIPv6   string   `json:"engine_warp_source_ipv6"`
	EngineProxies          []string `json:"engine_proxies"`
	EngineZenKeys          []string `json:"engine_zen_keys"`
	EngineGoKeys           []string `json:"engine_go_keys"`
	EngineZenKeyCount      int      `json:"engine_zen_key_count"`
	EngineGoKeyCount       int      `json:"engine_go_key_count"`
	EngineRetryMaxAttempts int      `json:"engine_retry_max_attempts"`
	EngineModelsRefreshSec int      `json:"engine_models_refresh_sec"`
}

// ConfigViewOf 生成前端视图（密码脱敏为掩码）。
func (m *Manager) ConfigViewOf() ConfigView {
	cfg := m.loadConfig()
	callLogMax := cfg.CallLogMax
	if callLogMax <= 0 {
		callLogMax = 5000
	}
	return ConfigView{
		BaseURL:           cfg.BaseURL,
		DefaultPassword:   maskSecret(cfg.DefaultPassword),
		HasPassword:       cfg.DefaultPassword != "",
		CallLogMax:        callLogMax,
		UiPollIntervalSec: uiPollIntervalSecOf(cfg),
		HasGatewayKey:     cfg.GatewayKey != "",
		GatewayKey:        maskSecret(cfg.GatewayKey),

		GatewayPort:           cfg.GatewayPort,
		ProviderPort:          cfg.ProviderPort,
		GatewayPortEffective:  m.managerGatewayPort(),
		ProviderPortEffective: m.managerProviderPort(),

		EngineEnabled:          engineEnabled(cfg),
		EngineWarpEnabled:      engineWarpEnabled(cfg),
		EngineWarpPoolSize:     cfg.EngineWarpPoolSize,
		EngineWarpSourceIPv6:   cfg.EngineWarpSourceIPv6,
		EngineProxies:          cfg.EngineProxies,
		EngineZenKeys:          cfg.EngineZenKeys,
		EngineGoKeys:           cfg.EngineGoKeys,
		EngineZenKeyCount:      len(cfg.EngineZenKeys),
		EngineGoKeyCount:       len(cfg.EngineGoKeys),
		EngineRetryMaxAttempts: cfg.EngineRetryMaxAttempts,
		EngineModelsRefreshSec: cfg.EngineModelsRefreshSec,
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

// effectiveGatewayKey 生效的统一网关密钥：优先环境变量 OPCODE2API_GATEWAY_KEY
// （systemd EnvironmentFile / 自定义部署注入，与 OPCODE2API_GATEWAY_PORT 同级），
// 其次 config.gateway_key，否则回退默认 unifiedGatewayKey（"sk-unified-local"）。
func effectiveGatewayKey(cfg Config) string {
	if s := strings.TrimSpace(os.Getenv("OPCODE2API_GATEWAY_KEY")); s != "" {
		return s
	}
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
