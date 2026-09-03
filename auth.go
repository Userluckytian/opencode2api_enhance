// Part of the P1 (core split) refactor: code moved out of main.go.
// Same package (main) - do not change package clause manually.
package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// L5：会话 TTL——sessionTTL 有效期，鉴权时惰性删除过期条目并滑动续期。
const sessionTTL = 24 * time.Hour

// sessionEntry 登录会话条目（含过期时间）。
type sessionEntry struct {
	expiresAt time.Time
}

func requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if adminPassword == "" {
			next(w, r)
			return
		}
		cookie, err := r.Cookie("session")
		if err != nil || cookie.Value == "" {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		sessionsMu.Lock()
		entry, ok := sessions[cookie.Value]
		if !ok {
			sessionsMu.Unlock()
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		if time.Now().After(entry.expiresAt) {
			// L5：会话过期——惰性删除后重定向登录。
			delete(sessions, cookie.Value)
			sessionsMu.Unlock()
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		// L5：滑动续期——有效使用即刷新过期时间。
		entry.expiresAt = time.Now().Add(sessionTTL)
		sessions[cookie.Value] = entry
		sessionsMu.Unlock()
		next(w, r)
	}
}

// ======================== API 密钥校验 ========================
// 实例密钥（即 -password 传入的 adminPassword）同时作为 /v1/* 的访问门禁：
// 请求必须携带 Authorization: Bearer <实例密钥> 或 x-api-key: <实例密钥>
// （Anthropic 兼容客户端规范头；均支持 go:/zen: 前缀），否则 401 并补记一条调用日志。
// adminPassword 为空时跳过校验（保持"未启用认证"语义）。

func apiKeyAuthMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if adminPassword == "" {
			next(w, r)
			return
		}
		if reason := apiKeyFailureReason(r); reason != "" {
			// 鉴权失败在中间件直接 401 返回、不进业务 handler，补记一条调用日志（成功路径由 handler 记录）
			recordAuthFailure(r, reason)
			writeAuthError(w)
			return
		}
		next(w, r)
	}
}

// validAPIKey 检查请求密钥是否与 adminPassword 匹配：
// 接受 Authorization: Bearer <key> 与 x-api-key: <key>（Anthropic 兼容客户端规范头），
// 均支持 go:/zen: 前缀剥离。
func validAPIKey(r *http.Request) bool {
	return apiKeyFailureReason(r) == ""
}

// apiKeyFailureReason 返回鉴权失败原因；密钥有效时返回空串。
func apiKeyFailureReason(r *http.Request) string {
	token := apiKeyFromRequest(r)
	if token == "" {
		return "缺少 Authorization/x-api-key 头"
	}
	if subtle.ConstantTimeCompare([]byte(token), []byte(adminPassword)) == 1 {
		return ""
	}
	return "key 不匹配"
}

// apiKeyFromRequest 提取请求密钥：优先 Authorization: Bearer <token>，其次 x-api-key <token>。
func apiKeyFromRequest(r *http.Request) string {
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		if token := strings.TrimSpace(strings.TrimPrefix(auth, "Bearer ")); token != "" {
			return stripKeyPrefix(token)
		}
	}
	if token := strings.TrimSpace(r.Header.Get("x-api-key")); token != "" {
		return stripKeyPrefix(token)
	}
	return ""
}

// stripKeyPrefix 剥离 go:/zen: 上游路由前缀（仅用于密钥比对）。
func stripKeyPrefix(token string) string {
	if rest, ok := strings.CutPrefix(token, "go:"); ok {
		return rest
	}
	if rest, ok := strings.CutPrefix(token, "zen:"); ok {
		return rest
	}
	return token
}

func writeAuthError(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	fmt.Fprintf(w, `{"error":{"message":"invalid api key","type":"invalid_request_error","code":"invalid_api_key"}}`)
}

// recordAuthFailure 鉴权失败（401）时补记一条调用日志：成功路径由各业务 handler 记录，
// 失败在中间件直接返回，需在此记录以保证日志页可见失败原因（含 ReqID）。
// 受 callLogEnabled 控制（网关 / 实例子进程写盘）。
func recordAuthFailure(r *http.Request, reason string) {
	rec := CallRecord{
		ReqID:  getReqID(r.Context()),
		TS:     time.Now().Format(time.RFC3339),
		Path:   r.URL.Path,
		Status: "fail",
		ErrMsg: "鉴权失败：" + reason,
		ServingPort: port,
		Events: []CallEvent{{Type: "auth_failed", Detail: reason, At: time.Now()}},
	}
	if rec.ReqID == "" {
		rec.ReqID = "req_" + randomString(12)
	}
	recordCall(rec)
}

func generateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func loginHandler(w http.ResponseWriter, r *http.Request) {
	if adminPassword == "" {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			renderLoginPage(w, "表单解析失败")
			return
		}
		if r.FormValue("password") != adminPassword {
			renderLoginPage(w, "密码错误")
			return
		}
		token, err := generateToken()
		if err != nil {
			renderLoginPage(w, "创建会话失败")
			return
		}
		sessionsMu.Lock()
		sessions[token] = sessionEntry{expiresAt: time.Now().Add(sessionTTL)}
		sessionsMu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: "session", Value: token, Path: "/", HttpOnly: true})
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	renderLoginPage(w, "")
}

func logoutHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	cookie, err := r.Cookie("session")
	if err == nil && cookie.Value != "" {
		sessionsMu.Lock()
		delete(sessions, cookie.Value)
		sessionsMu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: "session", Value: "", Path: "/", HttpOnly: true, MaxAge: -1})
	http.Redirect(w, r, "/login", http.StatusFound)
}

// ======================== Token 统计 ========================
