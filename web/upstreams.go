package main

import (
	"errors"
	"regexp"
	"strings"
)

var (
	// errNoUpstream 表示没有任何启用的上游可用（配置缺失或全部停用）。
	errNoUpstream = errors.New("no enabled upstream configured")
	// errEmptyModelAfterPrefix 表示剥离前缀后模型名为空（如 "backup/"）。
	errEmptyModelAfterPrefix = errors.New("model name is empty after upstream prefix")
)

// upstreamIDPattern 与主项目 custom_providers.go 的 ID 规则保持一致：
// 字母数字开头，可含 - _，≤32 字符。不允许 "/"，因此前缀分隔无歧义。
var upstreamIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,31}$`)

// validUpstreamID 校验上游 ID 格式。
func validUpstreamID(id string) bool {
	return upstreamIDPattern.MatchString(id)
}

// FindUpstream 在列表中按 ID 精确查找（大小写敏感）。
func FindUpstream(list []Upstream, id string) (Upstream, bool) {
	for _, u := range list {
		if u.ID == id {
			return u, true
		}
	}
	return Upstream{}, false
}

// Resolve 按 model 第一段匹配上游 ID，返回目标上游与剥离前缀后的模型名。
//
// 规则（关键：不能盲切第一个 "/"）：
//  1. 取 model 第一段（首个 "/" 之前）。真实模型名本身常含斜杠
//     （如 "or/minimax/minimax-m3.1-flash:free"），盲切会把 "or" 当成上游 ID；
//  2. 只有当第一段**精确等于**某个 enabled 上游的 ID 时才剥离并路由；
//  3. 否则整体视为普通模型名，走默认上游，model 原样不动（存量客户端零改动）；
//  4. 禁用（Enabled=false）的上游不参与匹配，也不剥离前缀——避免
//     "停用备用上游" 变成 "请求全部 404"。
//
// 默认上游：DefaultUpstreamID 指向的条目；若该 ID 不存在或未启用，
// 退化为第一个 enabled 的条目。没有任何 enabled 条目时返回 errNoUpstream。
func (a *App) Resolve(model string) (Upstream, string, error) {
	a.Mu.RLock()
	list := a.Cfg.EffectiveUpstreams()
	defaultID := a.Cfg.DefaultUpstreamID
	a.Mu.RUnlock()
	return resolveUpstream(list, defaultID, model)
}

// resolveUpstream 是 Resolve 的纯函数版本，便于表驱动测试。
func resolveUpstream(list []Upstream, defaultID, model string) (Upstream, string, error) {
	if len(list) == 0 {
		return Upstream{}, "", errNoUpstream
	}

	// 前缀匹配：只有命中已启用的上游才剥离。
	if idx := strings.IndexByte(model, '/'); idx > 0 {
		prefix, rest := model[:idx], model[idx+1:]
		if u, ok := FindUpstream(list, prefix); ok && u.Enabled {
			if strings.TrimSpace(rest) == "" {
				return Upstream{}, "", errEmptyModelAfterPrefix
			}
			return u, rest, nil
		}
	}

	// 未命中前缀：走默认上游，model 原样。
	if defaultID != "" {
		if u, ok := FindUpstream(list, defaultID); ok && u.Enabled {
			return u, model, nil
		}
	}
	for _, u := range list {
		if u.Enabled {
			return u, model, nil
		}
	}
	return Upstream{}, "", errNoUpstream
}

// DefaultUpstream 返回当前默认上游（无可用上游时返回 false）。
func (a *App) DefaultUpstream() (Upstream, bool) {
	a.Mu.RLock()
	list := a.Cfg.EffectiveUpstreams()
	defaultID := a.Cfg.DefaultUpstreamID
	a.Mu.RUnlock()
	if u, ok := FindUpstream(list, defaultID); ok && u.Enabled {
		return u, true
	}
	for _, u := range list {
		if u.Enabled {
			return u, true
		}
	}
	return Upstream{}, false
}

// enabledUpstreams 返回启用中的上游副本（供并发拉取模型等使用）。
func (a *App) enabledUpstreams() []Upstream {
	a.Mu.RLock()
	list := a.Cfg.EffectiveUpstreams()
	a.Mu.RUnlock()
	out := make([]Upstream, 0, len(list))
	for _, u := range list {
		if u.Enabled {
			out = append(out, u)
		}
	}
	return out
}

// upstreamPrefix 返回某上游在 model 上的前缀写法（默认上游不带前缀）。
// 供管理后台展示「用户该填的模型名」。
func upstreamPrefix(id, defaultID string) string {
	if id == "" || id == defaultID {
		return ""
	}
	return id + "/"
}
