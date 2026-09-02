// 自定义模型源自注册：providers[] 一条 type:"custom" 条目 = 一个用户自定义源，
// 同类型多条即多个源（id 各异）；未配置 providers 时不会自动注册（无参数无从构造）。
package custom

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/6Kmfi6HP/opencode2api/core/contract"
	"github.com/6Kmfi6HP/opencode2api/vendors/opencode"
)

// Params 键（ProviderSpec.Params；配置 providers[].params 同名透传）。
const (
	ParamBaseURL       = "base_url"
	ParamAPIKey        = "api_key"
	ParamAPIKeys       = "api_keys"       // 多 key（优先于 api_key，两者合并去重）
	ParamKeyStrategy   = "key_strategy"   // round_robin（默认）| failover；仅作用于本源
	ParamAllowedModels = "allowed_models" // 暴露白名单（上游模型 ID；空 = 全部暴露）
	ParamProtocol      = "protocol"
	ParamViaProxy      = "via_proxy"
	// ParamKey403Cooldown 401/403 失效冷却时长（秒）：>0 到期自动回池重试；0 = 永久禁用（旧行为）。
	ParamKey403Cooldown = "key_403_cooldown"
)

func init() {
	contract.Register("custom", func(spec contract.ProviderSpec) (contract.Vendor, error) {
		cfg := Config{
			ID:              spec.ID,
			Name:            spec.Name,
			BaseURL:         strParam(spec.Params, ParamBaseURL),
			APIKey:          strParam(spec.Params, ParamAPIKey),
			APIKeys:         strSliceParam(spec.Params, ParamAPIKeys),
			KeyStrategy:     strParam(spec.Params, ParamKeyStrategy),
			AllowedModels:   strSliceParam(spec.Params, ParamAllowedModels),
			Protocol:        strParam(spec.Params, ParamProtocol),
			ViaProxy:        boolParam(spec.Params, ParamViaProxy),
			Key403Cooldown:  time.Duration(intParam(spec.Params, ParamKey403Cooldown, 0)) * time.Second,
		}
		if tr, ok := spec.Params[opencode.ParamTransport].(contract.Transport); ok && tr != nil {
			cfg.Transport = tr
		}
		return New(cfg)
	})
}

func strParam(p map[string]any, key string) string {
	s, _ := p[key].(string)
	return s
}

func boolParam(p map[string]any, key string) bool {
	b, _ := p[key].(bool)
	return b
}

// intParam 读整数参数（兼容 JSON 反序列化的 float64/json.Number 与内存直传的 int），
// 非法/缺失返回 fallback。
func intParam(p map[string]any, key string, fallback int) int {
	switch n := p[key].(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return int(i)
		}
	case string:
		if i, err := strconv.Atoi(n); err == nil {
			return i
		}
	}
	return fallback
}

// strSliceParam 读字符串数组参数（兼容 JSON 反序列化的 []any 与内存直传的 []string）。
func strSliceParam(p map[string]any, key string) []string {
	switch arr := p[key].(type) {
	case []any:
		out := make([]string, 0, len(arr))
		for _, v := range arr {
			if s, ok := v.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return arr
	}
	return nil
}
