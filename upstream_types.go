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

// tierOfAuth maps auth route mode to a call-log tier string.
func tierOfAuth(a UpstreamAuth) string {
	if a.Mode == AuthRoutePublic {
		return "free"
	}
	return "paid"
}

// extractUpstreamAuth 解析入站请求带的凭据。
//
// v2.0.0 起本进程不再有「OpenCode 免费层/付费层」之分：所有模型都来自
// 自定义源或插件供应商，凭据只用于本进程的入站鉴权（apiKeyAuthMiddleware 已
// 在更外层校验），这里仅保留「是否带 key」这一事实供调用日志区分。
func extractUpstreamAuth(r *http.Request) UpstreamAuth {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return UpstreamAuth{Mode: AuthRoutePublic}
	}
	token := strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
	if token == "" || token == "public" {
		return UpstreamAuth{Mode: AuthRoutePublic}
	}
	if adminPassword != "" && subtle.ConstantTimeCompare([]byte(token), []byte(adminPassword)) == 1 {
		return UpstreamAuth{Mode: AuthRoutePublic}
	}
	return UpstreamAuth{Token: token, Mode: AuthRouteAuto}
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

// isFreeModel 判定某模型是否「无需额外付费即可用」。
//
// 历史上这是 OpenCode 的价值模型（-free 后缀 / 价格为零），现在所有模型都由
// 用户自己的自定义源提供，因此一律可用——保留函数是为了让调用日志/统计的
// 「free/paid」列继续有意义（本包恒返回 true）。
func isFreeModel(string) bool { return true }
