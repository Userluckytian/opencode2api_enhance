package main

import (
	"encoding/json"
	"net/http"
	"time"
)

// handleClaimRules 返回领取页要展示的公开规则（不含任何敏感信息）。
func (a *App) handleClaimRules(w http.ResponseWriter, r *http.Request) {
	a.Mu.RLock()
	ttl := a.Cfg.TTLDays
	a.Mu.RUnlock()

	rule := ClaimRule{OneKeyPerIP: true}
	if a.Claims != nil {
		rule = a.Claims.Rule()
	}
	// 额度与有效期统一取全局配置（唯一数据源），不来自 claim_policy.json。
	// 对外只说明「是否限领一个」「重置时刻」「是否长期有效」，
	// **不下发额度数值**（额度属内部配置，页面只说「默认额度」），也不暴露白名单网段。
	writeJSON(w, http.StatusOK, map[string]any{
		"one_key_per_ip": rule.OneKeyPerIP,
		"reset_at":       nextDailyReset(time.Now()).Format("2006-01-02 15:04"),
		"never_expires":  ttl <= 0,
	})
}

// handleGetClaimPolicy 返回后台可见的完整领取规则（含白名单网段）。
func (a *App) handleGetClaimPolicy(w http.ResponseWriter, r *http.Request) {
	if a.Claims == nil {
		writeErr(w, http.StatusServiceUnavailable, "claim policy not initialized")
		return
	}
	now := time.Now()
	rule := a.Claims.Rule()
	writeJSON(w, http.StatusOK, map[string]any{
		"rule":                  rule,
		"claimed":               a.Claims.ClaimedList(now),
		"default_allowed_cidrs": DefaultAllowedCIDR(),
	})
}

// handlePutClaimPolicy 更新领取规则。
func (a *App) handlePutClaimPolicy(w http.ResponseWriter, r *http.Request) {
	if a.Claims == nil {
		writeErr(w, http.StatusServiceUnavailable, "claim policy not initialized")
		return
	}
	// allow_cidrs 用指针区分三种语义（修复 finding 566 / fact 591）：
	//   nil           → 未提交，保持原白名单不变；
	//   []            → 显式提交空数组，等于「谁都不许领」（fail-closed）；
	//   ["1.2.3.0/24"] → 严格解析，任一非法项整体 400 并点名。
	var req struct {
		AllowCIDRs  *[]string `json:"allow_cidrs"`
		OneKeyPerIP *bool     `json:"one_key_per_ip"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	rule := a.Claims.Rule()
	if req.AllowCIDRs != nil {
		if len(*req.AllowCIDRs) == 0 {
			// 显式清空 = 全拒绝。绝不能像旧版那样变成 fail-open。
			rule.AllowCIDRs = nil
		} else {
			cidrs, err := ParseCIDRsStrict(*req.AllowCIDRs)
			if err != nil {
				// 严格失败：不落盘、不改内存，白名单保持原值。
				if a.Audit != nil {
					a.Audit.Write(AuditEntry{
						Time:   time.Now().Format(time.RFC3339),
						Action: "update_claim_policy",
						Detail: "rejected: " + err.Error(),
						IP:     clientIP(r),
						Result: "denied",
					})
				}
				writeErr(w, http.StatusBadRequest, "allow_cidrs 含非法网段："+err.Error())
				return
			}
			rule.AllowCIDRs = cidrs
		}
	}
	if req.OneKeyPerIP != nil {
		rule.OneKeyPerIP = *req.OneKeyPerIP
	}
	a.Claims.SetRule(rule)
	if a.Audit != nil {
		a.Audit.Write(AuditEntry{
			Time:   time.Now().Format(time.RFC3339),
			Action: "update_claim_policy",
			Detail: "allow_cidrs=" + joinStrings(rule.AllowCIDRs, ",") + "; one_key_per_ip=" + boolStr(rule.OneKeyPerIP),
			IP:     clientIP(r),
			Result: "ok",
		})
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func joinStrings(list []string, sep string) string {
	out := ""
	for i, s := range list {
		if i > 0 {
			out += sep
		}
		out += s
	}
	return out
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
