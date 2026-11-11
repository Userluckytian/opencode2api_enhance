// 自定义模型源自注册：providers[] 一条 type:"custom" 条目 = 一个用户自定义源，
// 同类型多条即多个源（id 各异）；未配置 providers 时不会自动注册（无参数无从构造）。
package custom

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/6Kmfi6HP/opencode2api/core/contract"
)

// Params 键（ProviderSpec.Params；配置 providers[].params 同名透传）。
const (
	ParamBaseURL       = "base_url"
	ParamAPIKey        = "api_key"
	ParamAPIKeys       = "api_keys"       // 多 key（优先于 api_key，两者合并去重）
	ParamKeyStrategy   = "key_strategy"   // round_robin（默认）| failover；仅作用于本源
	ParamAllowedModels = "allowed_models" // 暴露白名单（上游模型 ID；空 = 全部暴露）
	ParamProtocol      = "protocol"
	// ParamProxy 本源出站代理（http / https / socks5 / socks5h）；空 = 直连。
	// 取代历史上的 via_proxy 布尔开关：那个开关绑死「节点池」，池化能力已移除，
	// 但「某供应商必须走代理才通」是真实需求，故改为直接填代理地址。
	ParamProxy = "proxy"
	// ParamViaProxy 是 ParamProxy 的向后兼容别名：旧配置里的 via_proxy 布尔值
	// 无法映射到具体代理地址，只用于在迁移时识别「该源曾要求走代理」。
	ParamViaProxy = "via_proxy"
	// ParamUseProxy 是否使用「全局出口代理」（设置页维护的 engine_proxies）。
	// 为 true 时不复制地址：出站装配时解析为全局列表第一个非 direct 项，
	// 因此修改全局代理无需重存各源。与 ParamProxy 同时存在时 ParamProxy 优先。
	ParamUseProxy = "use_proxy"
	// ParamKey403Cooldown 401/403 失效冷却时长（秒）：>0 到期自动回池重试；0 = 永久禁用（旧行为）。
	ParamKey403Cooldown = "key_403_cooldown"
	// ParamDataPolicyNote 数据去向提示（如「提示词可能被上游用于训练」）；空 = 无提示。
	//
	// 为什么要这个字段：有些免费上游在服务条款里明确要求「不得提交敏感数据」，
	// 而它在面板里看起来和其它源一模一样，用户完全可能把私有代码选到这类通道上。
	// 把提示做成配置数据（而不是把某个 id 写死在 UI 里），换上游、加中转站时同样能标。
	// 只影响管理面板展示，不参与路由与出站请求。
	ParamDataPolicyNote = "data_policy_note"
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
			Proxy:           strParam(spec.Params, ParamProxy),
			Key403Cooldown:  time.Duration(intParam(spec.Params, ParamKey403Cooldown, 0)) * time.Second,
		}
		if tr, ok := spec.Params[contract.ParamTransport].(contract.Transport); ok && tr != nil {
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
