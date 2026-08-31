// Part of the P1 (core split) refactor: code moved out of main.go.
// Same package (main) - do not change package clause manually.
package main

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

type TierType int

const (
	TierFree TierType = iota
	TierPaid
)

type AuthRouteMode int

const (
	AuthRoutePublic AuthRouteMode = iota
	AuthRouteAuto
	AuthRouteZen
	AuthRouteGo
)

type UpstreamAuth struct {
	Token string
	Mode  AuthRouteMode
	// PreferredKeyIdx 会话/续写粘性透传的 custom key 池下标（nil = 无偏好）。
	// 流式中断续写重连时保留首次选中的 key 下标，custom 源 withKeysStream
	// 优先命中同一 key（同请求续写不换 key，避免重复输出/串对话）。
	// 指针字段：零值 nil 天然表示"未指定"，避免与合法下标 0 冲突。
	PreferredKeyIdx *int
}

func extractUpstreamAuth(r *http.Request) UpstreamAuth {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return UpstreamAuth{Mode: AuthRoutePublic}
	}
	token := strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
	if token == "" || token == "public" {
		return UpstreamAuth{Mode: AuthRoutePublic}
	}
	// 本地门禁密钥（adminPassword）：仅用于本层认证，不当作上游付费 key，
	// 底层请求 opencode 时一律走 public 免费。
	if adminPassword != "" && subtle.ConstantTimeCompare([]byte(token), []byte(adminPassword)) == 1 {
		return UpstreamAuth{Mode: AuthRoutePublic}
	}
	// go:/zen: 前缀路由：去掉前缀后剩余部分仍需是有效 key（sk- 开头）；
	// 前缀后的剩余部分若是本地门禁密钥，同样视为 public（底层免费）。
	if rest, ok := strings.CutPrefix(token, "go:"); ok && isValidOpenCodeKey(rest) {
		if adminPassword != "" && subtle.ConstantTimeCompare([]byte(rest), []byte(adminPassword)) == 1 {
			return UpstreamAuth{Mode: AuthRoutePublic}
		}
		return UpstreamAuth{Token: rest, Mode: AuthRouteGo}
	}
	if rest, ok := strings.CutPrefix(token, "zen:"); ok && isValidOpenCodeKey(rest) {
		if adminPassword != "" && subtle.ConstantTimeCompare([]byte(rest), []byte(adminPassword)) == 1 {
			return UpstreamAuth{Mode: AuthRoutePublic}
		}
		return UpstreamAuth{Token: rest, Mode: AuthRouteZen}
	}
	// 只有 sk- 开头的才是有效 key，其余（no-key-required 等占位符）一律走 public
	if isValidOpenCodeKey(token) {
		return UpstreamAuth{Token: token, Mode: AuthRouteAuto}
	}
	return UpstreamAuth{Mode: AuthRoutePublic}
}

// 只认 sk- 开头的 key，避免客户端占位 key（如 no-key-required）被透传给上游导致 401
func isValidOpenCodeKey(token string) bool {
	return strings.HasPrefix(token, "sk-") && len(token) > 15
}

func (auth UpstreamAuth) tier() TierType {
	if auth.Mode == AuthRoutePublic {
		return TierFree
	}
	return TierPaid
}

func (auth UpstreamAuth) authorizationHeader() string {
	if auth.Mode == AuthRoutePublic {
		return "Bearer public"
	}
	return "Bearer " + auth.Token
}

func (auth UpstreamAuth) shouldUseGoCatalog() bool {
	return auth.Mode == AuthRouteGo
}

func (auth UpstreamAuth) shouldUseGoEndpoint(modelID string) bool {
	switch auth.Mode {
	case AuthRouteGo:
		return isModelInGoCatalog(modelID)
	case AuthRouteAuto:
		return isGoCatalogOnlyModel(modelID)
	default:
		return false
	}
}

// isFreeModel 判定免费模型：名称任意位置包含 "-free"，或名称等于官方动态返回的真实免费模型 big-pickle。
func isFreeModel(modelID string) bool {
	return strings.Contains(strings.ToLower(modelID), "-free") || strings.EqualFold(modelID, "big-pickle")
}
