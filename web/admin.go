package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

func (a *App) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	ip := clientIP(r)
	now := time.Now()
	// 登录限流竞态修复（finding 544）：旧实现是「Allow 判定」与「Fail 记账」两段式，
	// 并发请求会在 Fail 生效前一起通过 Allow（实测 30 并发有 24 次真正校验了口令）。
	// TryAttempt 在同一个临界区内完成判定 + 计入本次尝试，并发下不会漏计。
	if a.Login != nil {
		if ok, remain := a.Login.TryAttempt(ip, now); !ok {
			secs := int(remain.Seconds()) + 1
			w.Header().Set("Retry-After", fmt.Sprintf("%d", secs))
			writeErr(w, http.StatusTooManyRequests, fmt.Sprintf("登录尝试过于频繁，请 %d 秒后再试", secs))
			return
		}
	}
	a.Mu.RLock()
	hash := a.Cfg.AdminPasswordHash
	a.Mu.RUnlock()
	if !VerifyPassword(hash, req.Password) {
		// 失败次数已在 TryAttempt 中原子计入，这里不再重复 Fail。
		writeErr(w, http.StatusUnauthorized, "bad password")
		return
	}
	if a.Login != nil {
		// 成功时清空该 IP 的失败记录（同时解除可能刚触发的锁定）。
		a.Login.Success(ip)
	}
	// 登录成功：把当前口令明文暂存在内存里，供后续「改密时判断与当前口令是否
	// 相同/相似」使用。**只在内存**，不写 config.json / 日志 / 审计；
	// 进程重启后自动消失，相似性检查自动降级为「仅相同性」（相同性仍走哈希校验）。
	a.setCurrentAdminPlain(req.Password)
	tok := a.Sessions.Create(12 * time.Hour)
	writeJSON(w, http.StatusOK, map[string]string{"token": tok})
}

func (a *App) adminAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.Sessions.Valid(bearerToken(r), time.Now()) {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) handleOverview(w http.ResponseWriter, r *http.Request) {
	keys := a.Store.List()
	now := time.Now()
	active, expired, disabled, exhausted := 0, 0, 0, 0
	for _, k := range keys {
		if k.Disabled {
			disabled++
		}
		if isExpired(k, now) {
			expired++
		}
		if k.TokenUsed >= k.Quota {
			exhausted++
		}
		if !k.Disabled && !isExpired(k, now) && k.TokenUsed < k.Quota {
			active++
		}
	}
	logs, _ := a.Logger.ReadLogs(now.Format("2006-01-02"), "")
	reqTotal, tokTotal, errTotal, blocked, ips := summarizeLogs(logs)
	writeJSON(w, http.StatusOK, map[string]any{
		"total_keys":       len(keys),
		"active_keys":      active,
		"requests_today":   reqTotal,
		"tokens_today":     tokTotal,
		"error_today":      errTotal,
		"blocked_today":    blocked,
		"active_ips_today": len(ips),
		"keys": map[string]int{
			"active": active, "expired": expired,
			"disabled": disabled, "exhausted": exhausted,
		},
		"trend": a.dailyTrend(7, now),
	})
}

func (a *App) handleAdminModels(w http.ResponseWriter, r *http.Request) {
	a.Mu.RLock()
	allowed := append([]string(nil), a.Cfg.AllowedModels...)
	upstream := a.Cfg.UpstreamBaseURL
	apiKey := a.Cfg.UpstreamAPIKey
	a.Mu.RUnlock()
	out := map[string]any{"models": []string{}, "allowed": allowed}
	if strings.TrimSpace(upstream) == "" {
		out["warning"] = "尚未配置上游实例地址，请先到「参数设置」填写后再拉取模型"
	} else {
		models, err := FetchModels(upstream, apiKey, a.client())
		if err != nil {
			out["warning"] = "无法连接上游拉取模型: " + err.Error()
		} else {
			out["models"] = models
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *App) handleSaveModels(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AllowedModels []string `json:"allowed_models"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	a.Mu.Lock()
	a.Cfg.AllowedModels = req.AllowedModels
	err := a.Cfg.Save(a.ConfigPath)
	a.Mu.Unlock()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "save failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	a.Mu.RLock()
	out := map[string]any{
		"upstream_base_url": a.Cfg.UpstreamBaseURL,
		// 上游密钥不回传明文：管理员会话一旦泄露，明文密钥可直接绕过本服务
		// 直连上游，等于绕过全部 IP 策略。页面只显示末 4 位。
		"upstream_api_key_masked": maskSecret(a.Cfg.UpstreamAPIKey),
		"upstream_api_key_set":    a.Cfg.UpstreamAPIKey != "",
		"public_base_url":         a.Cfg.PublicBaseURL,
		"ttl_days":                a.Cfg.TTLDays,
		"token_quota":             a.Cfg.TokenQuota,
		"rate_limit_per_min":      a.Cfg.RateLimitPerMin,
		"lan_ip":                  lanIPv4(),
		// 只读展示：部署方下发的上游主机白名单（config.json / 环境变量），
		// 管理 API 不可写，避免拿到管理员会话后自行扩白名单绕过 SSRF 防护。
		"upstream_allowed_hosts": a.guard().AllowedHosts(),
	}
	a.Mu.RUnlock()
	writeJSON(w, http.StatusOK, out)
}

// handlePutSettings 保存参数设置。
//
// SSRF 修复（finding 541 / fact 569）：写入 upstream_base_url 之前必须先过
// ValidateUpstreamBaseURL —— 旧实现直接 a.Cfg.UpstreamBaseURL = req.UpstreamBaseURL，
// 管理员（或拿到会话的攻击者）可以把上游指向 http://169.254.169.254/、内网任意
// 主机端口，随后 /v1 请求或「拉取模型」就会由服务端代发，构成管理端 SSRF。
// 校验失败返回 400 且保持原值不变（不落盘、不生效）。
func (a *App) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	// 合并语义（修复 finding 564 / fact 591）：
	// 旧实现 decode 进非指针结构体后整体赋值，前端只要漏提交一个字段（或攻击者
	// 故意只发 {"token_quota":N}），其余字段就会被零值覆盖 —— upstream_base_url
	// 变空（然后 /v1 直接 502）、rate_limit_per_min 变 0（等于关掉限速）、
	// ttl_days 变 0（等于把已发 Key 改成永不过期）。
	// 现在用指针字段：nil = 未提交 → 保持原值。
	var req struct {
		UpstreamBaseURL *string `json:"upstream_base_url"`
		UpstreamAPIKey  *string `json:"upstream_api_key"`
		PublicBaseURL   *string `json:"public_base_url"`
		TTLDays         *int    `json:"ttl_days"`
		TokenQuota      *int    `json:"token_quota"`
		RateLimitPerMin *int    `json:"rate_limit_per_min"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}

	a.Mu.RLock()
	curTTL, curQuota, curRate := a.Cfg.TTLDays, a.Cfg.TokenQuota, a.Cfg.RateLimitPerMin
	curUpstream, curPublic := a.Cfg.UpstreamBaseURL, a.Cfg.PublicBaseURL
	a.Mu.RUnlock()

	// ttl_days=0 表示「Key 永不过期」（用量由每日 08:00 自动重置的额度控制），
	// 因此允许 0；负数非法，同时设一个 10 年上界防止溢出/误配。
	newTTL := curTTL
	if req.TTLDays != nil {
		if *req.TTLDays < 0 || *req.TTLDays > MaxTTLDays {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("ttl_days 必须在 0..%d 之间", MaxTTLDays))
			return
		}
		newTTL = *req.TTLDays
	}
	// token_quota 有上界（旧实现允许写入 9223372036854775807 并存到每个新 Key 上）。
	newQuota := curQuota
	if req.TokenQuota != nil {
		if *req.TokenQuota <= 0 || *req.TokenQuota > MaxTokenQuota {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("token_quota 必须在 1..%d 之间", MaxTokenQuota))
			return
		}
		// 额度变更对存量 Key 采用「次日 08:00 生效」语义 —— 当天不会砍掉任何
		// 在用 Key 的额度，因此这里不再拦截下调（否则管理员永远无法调低额度：
		// 存量 Key 的额度总是高于新默认值）。
		//
		// 原拦截的理由是「一次误操作会砍掉所有在用 Key 的额度」，该前提在改为
		// 次日生效后已不成立。需要立刻收紧存量 Key 时走
		// POST /api/admin/keys/{id}/quota 单独调整。
		newQuota = *req.TokenQuota
	}
	newRate := curRate
	if req.RateLimitPerMin != nil {
		if *req.RateLimitPerMin < 0 || *req.RateLimitPerMin > MaxRateLimitPerMin {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("rate_limit_per_min 必须在 0..%d 之间", MaxRateLimitPerMin))
			return
		}
		newRate = *req.RateLimitPerMin
	}

	// 上游地址 SSRF 校验：scheme 只允许 http/https、禁止 userinfo、解析主机名后
	// 拒绝回环/RFC1918/链路本地/CGNAT/云元数据地址（除非该主机在部署方下发的白名单中；
	// 元数据地址即便在白名单里也拒绝）。非法 → 400，配置保持原值。
	newUpstream := curUpstream
	if req.UpstreamBaseURL != nil {
		newUpstream = strings.TrimSpace(*req.UpstreamBaseURL)
		if err := ValidateUpstreamBaseURL(newUpstream, a.guard()); err != nil {
			if a.Audit != nil {
				a.Audit.Write(AuditEntry{
					Time:   time.Now().Format(time.RFC3339),
					Action: "update_settings",
					Detail: fmt.Sprintf("upstream_base_url: rejected %q: %v", newUpstream, err),
					IP:     clientIP(r),
					Result: "denied",
				})
			}
			writeErr(w, http.StatusBadRequest, "upstream_base_url 被拒绝: "+err.Error())
			return
		}
	}
	newPublic := curPublic
	if req.PublicBaseURL != nil {
		newPublic = strings.TrimSpace(*req.PublicBaseURL)
		// 对外访问地址必须是部署方显式配置的合法 URL：scheme 只允许 http/https、
		// 禁止 userinfo、主机名走白名单正则、路径不得含 . / ..。
		// 非法 → 400 且配置保持原值（fail-closed，绝不把垃圾值写进配置）。
		if newPublic != "" {
			if err := ValidatePublicBaseURL(newPublic); err != nil {
				if a.Audit != nil {
					a.Audit.Write(AuditEntry{
						Time:   time.Now().Format(time.RFC3339),
						Action: "update_settings",
						Detail: fmt.Sprintf("public_base_url: rejected %q: %v", newPublic, err),
						IP:     clientIP(r),
						Result: "denied",
					})
				}
				writeErr(w, http.StatusBadRequest, "public_base_url 被拒绝: "+err.Error())
				return
			}
		}
	}

	a.Mu.Lock()
	oldKey := a.Cfg.UpstreamAPIKey
	oldUpstream := a.Cfg.UpstreamBaseURL
	oldQuota, oldRate, oldTTL := a.Cfg.TokenQuota, a.Cfg.RateLimitPerMin, a.Cfg.TTLDays
	a.Cfg.UpstreamBaseURL = newUpstream
	// 上游密钥：留空/未提交 = 保持原值不变（页面不再回传明文，无法原样回填）。
	keyChanged := false
	if req.UpstreamAPIKey != nil && strings.TrimSpace(*req.UpstreamAPIKey) != "" {
		a.Cfg.UpstreamAPIKey = strings.TrimSpace(*req.UpstreamAPIKey)
		keyChanged = true
	}
	a.Cfg.PublicBaseURL = newPublic
	a.Cfg.TTLDays = newTTL
	a.Cfg.TokenQuota = newQuota
	a.Cfg.RateLimitPerMin = newRate
	newKey := a.Cfg.UpstreamAPIKey
	err := a.Cfg.Save(a.ConfigPath)
	a.Mu.Unlock()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "save failed")
		return
	}
	// 「Key 有效期」是全局策略，立即应用到所有已存在的 Key。
	if newTTL != oldTTL {
		a.Store.ApplyTTL(newTTL, time.Now())
	}
	// 额度变更**次日 08:00 生效**，当天不打断正在使用的 Key。
	//
	// 为什么要延迟：额度是「每个 Key 每日可用量」。当天已经用掉的量不该被
	// 追溯作废 —— 否则把额度从 1000W 调到 200W 时，一个当天已用 500W 的 Key
	// 会当场超额、下一次请求直接被拒，等于用一次配置操作打断别人的工作。
	//
	// 生效方式：存量 Key 在跨额度日（08:00）重置时恢复为新的默认额度
	// （Store.RollQuota 用的是 config.token_quota）。新领的 Key 立即按新额度发放。
	if newQuota != oldQuota {
		// 同步 store 的默认额度：跨日重置走 ConsumeAt 内部路径时用的是它。
		a.Store.SetDefaultQuota(newQuota)
		log.Printf("[额度变更] token_quota %d → %d，将在次日 08:00 对存量 Key 生效（当天不打断在用 Key）",
			oldQuota, newQuota)
	}
	// 敏感配置变更落审计：谁在什么时候把上游指向了哪里、换了什么密钥。
	if a.Audit != nil {
		detail := fmt.Sprintf("upstream_base_url: %s → %s", oldUpstream, newUpstream)
		if keyChanged {
			detail += fmt.Sprintf("; upstream_api_key: %s → %s", maskSecret(oldKey), maskSecret(newKey))
		} else {
			detail += "; upstream_api_key: 未修改"
		}
		if newQuota != oldQuota {
			detail += fmt.Sprintf("; token_quota: %d → %d", oldQuota, newQuota)
		}
		if newRate != oldRate {
			detail += fmt.Sprintf("; rate_limit_per_min: %d → %d", oldRate, newRate)
		}
		if newTTL != oldTTL {
			detail += fmt.Sprintf("; ttl_days: %d → %d", oldTTL, newTTL)
		}
		a.Audit.Write(AuditEntry{
			Time:   time.Now().Format(time.RFC3339),
			Action: "update_settings",
			Detail: detail,
			IP:     clientIP(r),
			Result: "ok",
		})
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// 设置项上界（集中定义，便于审计与前端提示）。
const (
	MaxTTLDays         = 3650          // 10 年
	MaxTokenQuota      = 1000000000000 // 1e12 token
	MaxRateLimitPerMin = 100000        // 每分钟请求数
)

// handlePutPassword 修改管理口令。
//
// 口令强度修复（finding 544 配套项 / finding 714 修复要点第⑤条）：
// 旧实现只有 `len(req.Password) < 4` 一道闸门，等价于允许 4 位口令（现网口令 0099
// 正是 4 位纯数字），而 finding 544 已证登录限流可被并发齐射放大爆破次数。
// 现在统一走 ValidateAdminPassword（web/passwordpolicy.go）：
//   - 长度下限 8 位（建议 12 位）、拒绝纯数字/纯字母/纯重复字符/常见弱口令；
//   - 拒绝与当前口令相同（用哈希直接校验）或高度相似（用登录时内存暂存的明文基准）。
//
// 校验失败 → 400 + 写审计（沿用 Audit.Write 模式）+ **不落盘**（原口令哈希保持不变）。
func (a *App) handlePutPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	a.Mu.RLock()
	curHash := a.Cfg.AdminPasswordHash
	a.Mu.RUnlock()
	cur := CurrentPassword{
		Plain:  a.currentAdminPlain(),
		Verify: func(candidate string) bool { return VerifyPassword(curHash, candidate) },
	}
	if err := ValidateAdminPassword(req.Password, cur); err != nil {
		if a.Audit != nil {
			a.Audit.Write(AuditEntry{
				Time:   time.Now().Format(time.RFC3339),
				Action: "update_password",
				Detail: fmt.Sprintf("password rejected: %v（策略：%s）", err, PasswordPolicyHint),
				IP:     clientIP(r),
				Result: "denied",
			})
		}
		writeErr(w, http.StatusBadRequest, "password rejected: "+err.Error())
		return
	}
	a.Mu.Lock()
	a.Cfg.AdminPasswordHash = HashPassword(req.Password)
	err := a.Cfg.Save(a.ConfigPath)
	a.Mu.Unlock()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "save failed")
		return
	}
	// 口令已变更：内存里暂存的明文基准必须同步更新（否则下一次改密仍按旧口令判相似）。
	a.setCurrentAdminPlain(req.Password)
	// 旧会话全部失效，保留当前操作者，避免改密后自己也被踢出。
	a.Sessions.RevokeAllExcept(bearerToken(r))
	if a.Audit != nil {
		a.Audit.Write(AuditEntry{
			Time:   time.Now().Format(time.RFC3339),
			Action: "update_password",
			Detail: "password updated (policy passed)",
			IP:     clientIP(r),
			Result: "ok",
		})
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) handleListKeys(w http.ResponseWriter, r *http.Request) {
	keys := a.Store.List()
	now := time.Now()
	type outKey struct {
		ID        string `json:"id"`
		Key       string `json:"key"`
		Model     string `json:"model"`
		CreatedAt string `json:"created_at"`
		ExpiresAt string `json:"expires_at"`
		TokenUsed int    `json:"token_used"`
		Quota     int    `json:"quota"`
		Remaining int    `json:"remaining"`
		Disabled  bool   `json:"disabled"`
		Expired   bool   `json:"expired"`
		Exhausted bool   `json:"exhausted"`
		Status    string `json:"status"`
		IP        string `json:"ip,omitempty"`
		QuotaDate string `json:"quota_date,omitempty"`
		ResetAt   string `json:"reset_at"`
	}
	out := make([]outKey, 0, len(keys))
	for _, k := range keys {
		rem := k.Quota - k.TokenUsed
		if rem < 0 {
			rem = 0
		}
		expired := isExpired(k, now)
		exhausted := k.TokenUsed >= k.Quota
		status := "active"
		switch {
		case k.Disabled:
			status = "disabled"
		case expired:
			status = "expired"
		case exhausted:
			status = "exhausted"
		}
		expiresAt := ""
		if !k.ExpiresAt.IsZero() {
			expiresAt = k.ExpiresAt.Format(time.RFC3339)
		}
		out = append(out, outKey{
			ID: k.ID, Key: k.Key, Model: k.Model,
			CreatedAt: k.CreatedAt.Format(time.RFC3339),
			ExpiresAt: expiresAt,
			TokenUsed: k.TokenUsed, Quota: k.Quota, Remaining: rem,
			Disabled: k.Disabled, Expired: expired, Exhausted: exhausted, Status: status,
			IP:        k.IP,
			QuotaDate: k.QuotaDate,
			ResetAt:   nextDailyReset(now).Format(time.RFC3339),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": out})
}

// handleSetKeyQuota 调整单个 Key 的额度。
//
// 存在的意义：下调全局默认额度（token_quota）时，若存量 Key 的额度高于新值，
// 后端会 400 拒绝（避免误砍在用 Key 的额度）。若没有单独调整存量的入口，
// 管理员就永远调不下去 —— 这条接口补上这条出路。
func (a *App) handleSetKeyQuota(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Quota *int `json:"quota"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.Quota == nil {
		writeErr(w, http.StatusBadRequest, "missing quota")
		return
	}
	if *req.Quota <= 0 || *req.Quota > MaxTokenQuota {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("quota 必须在 1..%d 之间", MaxTokenQuota))
		return
	}
	if err := a.Store.SetQuota(id, *req.Quota); err != nil {
		writeErr(w, http.StatusNotFound, "key not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) handleSetKeyDisabled(w http.ResponseWriter, r *http.Request, disabled bool) {
	id := r.PathValue("id")
	if err := a.Store.SetDisabled(id, disabled); err != nil {
		writeErr(w, http.StatusNotFound, "key not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) handleDisableKey(w http.ResponseWriter, r *http.Request) {
	a.handleSetKeyDisabled(w, r, true)
}

func (a *App) handleEnableKey(w http.ResponseWriter, r *http.Request) {
	a.handleSetKeyDisabled(w, r, false)
}

func (a *App) handleDeleteKey(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := a.Store.Delete(id); err != nil {
		writeErr(w, http.StatusNotFound, "key not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) handleLogs(w http.ResponseWriter, r *http.Request) {
	date := r.URL.Query().Get("date")
	if date == "" {
		date = time.Now().Format("2006-01-02")
	}
	// 路径穿越修复（finding 543）：date 直接拼进 "log-"+date+".jsonl"，
	// 必须在入口用白名单拒绝 / \ .. 等路径片段。
	date, err := sanitizeDate(date)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid date")
		return
	}
	keyID := r.URL.Query().Get("key_id")
	ip := r.URL.Query().Get("ip")
	logs, err := a.Logger.ReadLogsFilter(date, keyID, ip)
	if err != nil {
		if errors.Is(err, errInvalidDate) {
			writeErr(w, http.StatusBadRequest, "invalid date")
			return
		}
		writeErr(w, http.StatusInternalServerError, "read logs failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"logs": logs})
}
