// remote 厂商注册：类型注册使 "remote" 可被发现/被 contract.Create 构造（面板展示、
// 配置驱动场景），但主装配路径（R2）不走注册表——插件子进程端点/令牌只能来自
// 插件管理器（pluginprovider.Manager.RunningPlugins()），main 侧直接构造 remote.New。
// 因此自动注册（未配置 providers 时）必须排除 "remote"：无参无法构造（缺 base_url），
// 与 custom 同款跳过。
package remote

import (
	"github.com/6Kmfi6HP/opencode2api/core/contract"
	"github.com/6Kmfi6HP/opencode2api/vendors/opencode"
)

// Params 键（ProviderSpec.Params 透传；仅配置驱动场景使用）。
const (
	ParamBaseURL   = "base_url"
	ParamAuthToken = "auth_token"
)

func init() {
	contract.Register("remote", func(spec contract.ProviderSpec) (contract.Vendor, error) {
		cfg := Config{
			ID:        spec.ID,
			Name:      spec.Name,
			BaseURL:   strParam(spec.Params, ParamBaseURL),
			AuthToken: strParam(spec.Params, ParamAuthToken),
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
