package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

type App struct {
	Mu       sync.RWMutex // 保护 Cfg 字段读写与 Rl 热替换
	Cfg      *Config
	Store    *Store
	Rl       *RateLimiter
	Sessions *SessionStore
	Logger   *Logger
	Client   *http.Client
	// Guard 是上游地址 SSRF 防护器（白名单 + IP 归类 + 拨号前校验）。
	// handlePutSettings 用它校验 upstream_base_url；DialContext 用它做连接期兜底。
	Guard *upstreamGuard
	// guardMu 只保护 Guard 的惰性初始化（不复用 a.Mu，避免在持读锁时自锁）。
	guardMu    sync.Mutex
	ConfigPath string // 数据目录（config.json 所在目录）
	IPs        *IPTracker
	Login      *LoginLimiter
	Ask        *AskMeter    // AI 问数计量与每日额度（管理员自用，独立于 IP 策略）
	Audit      *AuditLog    // 管理端敏感操作审计
	Claims     *ClaimPolicy // 领取规则：来源网段白名单 + 同一 IP 限领
	// curPwMu 保护 curAdminPlain（内存中暂存的当前管理口令明文）。
	// 单独用一把锁，避免与 a.Mu（配置/限速器）在同一临界区里互相嵌套。
	curPwMu sync.RWMutex
	// curAdminPlain 是登录成功时暂存的当前管理口令明文，**只在内存**：
	// 供改密时判断「新口令是否与当前口令相同/相似」使用。
	// 绝不写入 config.json / keys.json / 日志 / 审计。
	curAdminPlain string
}

// RateLimiter 返回当前限速器（RLock 读）。返回的 *RateLimiter 创建后不再被修改，
// 可在锁外安全使用。
func (a *App) RateLimiter() *RateLimiter {
	a.Mu.RLock()
	defer a.Mu.RUnlock()
	return a.Rl
}

// SetRateLimiter 热替换限速器（Lock 写）。
func (a *App) SetRateLimiter(r *RateLimiter) {
	a.Mu.Lock()
	a.Rl = r
	a.Mu.Unlock()
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func (a *App) HandleModels(w http.ResponseWriter, r *http.Request) {
	a.Mu.RLock()
	models := append([]string(nil), a.Cfg.AllowedModels...)
	a.Mu.RUnlock()
	writeJSON(w, http.StatusOK, map[string]any{"models": models})
}

func (a *App) HandleCreateKey(w http.ResponseWriter, r *http.Request) {
	// 2026-10-28 变更：Key 不再绑定单个模型。
	// 一个 Key 可使用全部已暴露（allowed_models 白名单）的模型，跨上游同样适用——
	// 走哪个上游仍由请求的 model 前缀决定，与 Key 无关。
	// 请求体的 model 字段保留兼容（旧客户端会发），但不再写入 Key。
	var req struct {
		Model string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	now := time.Now()
	ip := clientIP(r)

	// 领取前置校验：来源必须在允许网段内（挡住走代理/外网进来的人）。
	if a.Claims != nil && !a.Claims.IPAllowed(ip) {
		writeErr(w, http.StatusForbidden, "当前网络不允许领取（仅限内网访问）")
		return
	}

	a.Mu.RLock()
	allowed := append([]string(nil), a.Cfg.AllowedModels...)
	ttl, quota := a.Cfg.TTLDays, a.Cfg.TokenQuota
	a.Mu.RUnlock()
	// 白名单为空时不允许领取：否则发出去的 Key 什么都用不了（fail-closed）。
	if len(allowed) == 0 {
		writeErr(w, http.StatusServiceUnavailable, "暂无可领取的模型，请联系管理员在后台勾选")
		return
	}
	// 兼容校验：客户端若仍指定 model，必须在白名单内，避免「选了不存在的模型」
	// 却拿到一个看起来可用的 Key。该值不写入 Key。
	if m := strings.TrimSpace(req.Model); m != "" && !contains(allowed, m) {
		writeErr(w, http.StatusBadRequest, "model not allowed")
		return
	}

	// 每个 IP 固定一个 Key：已存在就返回原来的（幂等），额度每日 08:00 自动重置，
	// 使用者不需要每天重新领取。model 传空串 = 不绑定，可用全部白名单模型。
	k, isNew, err := a.Store.CreateOrGetForIP(ip, "", ttl, quota, now)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to create key")
		return
	}
	if a.Claims != nil {
		a.Claims.RecordClaim(ip, k.ID, k.Model, now)
	}
	// 经 nginx 反代时带路径前缀（如 /token-buffet），由 X-Forwarded-Prefix 头传入；
	// 直连 23081 时无此前缀，行为与旧版一致。
	// 信任边界（修复 finding 548）：X-Forwarded-Prefix 由客户端控制，只做**形状**校验，
	// 命中白名单才使用；含 .. / 重复斜杠 / 非法字符一律丢弃（返回空串）。
	prefix := sanitizeForwardedPrefix(r.Header.Get("X-Forwarded-Prefix"))
	a.Mu.RLock()
	publicBase := a.Cfg.PublicBaseURL
	a.Mu.RUnlock()
	// 只信任部署方配置的 public_base_url；缺失或非法 → fail-closed 拒绝下发，
	// 绝不回退到 X-Forwarded-Host / Host 等客户端可控头。
	baseURL, err := buildBaseURL(publicBase, prefix, lanIPv4())
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "base_url 未正确配置："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"base_url":   baseURL,
		"key":        k.Key,
		// 零值 = 永不过期，返回空串而不是 0001-01-01 这种零值时间。
		"expires_at": func() string {
			if k.ExpiresAt.IsZero() {
				return ""
			}
			return k.ExpiresAt.Format(time.RFC3339)
		}(),
		"model":      "", // 不再绑定单模型；可用全部白名单（见 allowed_models）
		// is_new=false 表示这是该 IP 原有的 Key（额度每日 08:00 自动重置）
		"is_new": isNew,
		// 不下发额度数值：额度是内部配置，页面只说「默认额度」。
		"reset_at":     nextDailyReset(now).Format(time.RFC3339),
		"reset_at_fmt": nextDailyReset(now).Format("2006-01-02 15:04"),
	})
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
