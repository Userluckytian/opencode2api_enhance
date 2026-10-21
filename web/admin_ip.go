package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"time"
)

// validIPPath 校验路径参数里的 IP：normalizeIP 解析失败时会原样返回（见 ip.go），
// 所以必须再用 netip.ParseAddr 判一次，否则 "not-an-ip" 会被当成合法 key 写进
// overrides / 用于 reset（修复 finding 566 同源问题）。
func validIPPath(raw string) (string, error) {
	ip := normalizeIP(raw)
	if ip == "" {
		return "", fmt.Errorf("缺少 IP")
	}
	if _, err := netip.ParseAddr(ip); err != nil {
		return "", fmt.Errorf("非法 IP：%q", raw)
	}
	return ip, nil
}

func (a *App) handleGetIPPolicies(w http.ResponseWriter, r *http.Request) {
	if a.IPs == nil {
		writeErr(w, http.StatusServiceUnavailable, "ip policy not initialized")
		return
	}
	now := time.Now()
	writeJSON(w, http.StatusOK, map[string]any{
		"defaults":  a.IPs.Policies().Defaults(),
		"overrides": a.IPs.Policies().ListOverrides(),
		"usage":     a.IPs.Snapshot(now),
		"now":       now.Format(time.RFC3339),
	})
}

func (a *App) handlePutIPDefaults(w http.ResponseWriter, r *http.Request) {
	if a.IPs == nil {
		writeErr(w, http.StatusServiceUnavailable, "ip policy not initialized")
		return
	}
	// 合并语义（修复 finding 564 / fact 591）：旧实现把请求体直接 decode 进
	// IPDefaults（非指针）再整体 SetDefaults，前端/攻击者只提交 {"window_minutes":N}
	// 就会把 window_tokens / max_active_minutes / output_tps 全部归零。
	// max_active_minutes=0 意味着「不限活跃时长」——静默放开限速。
	// 现在只覆盖提交了的字段，其余保持当前生效值。
	var req struct {
		WindowTokens     *int `json:"window_tokens"`
		WindowMinutes    *int `json:"window_minutes"`
		MaxActiveMinutes *int `json:"max_active_minutes"`
		OutputTPS        *int `json:"output_tps"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	merged := a.IPs.Policies().Defaults()
	if req.WindowTokens != nil {
		merged.WindowTokens = *req.WindowTokens
	}
	if req.WindowMinutes != nil {
		merged.WindowMinutes = *req.WindowMinutes
	}
	if req.MaxActiveMinutes != nil {
		merged.MaxActiveMinutes = *req.MaxActiveMinutes
	}
	if req.OutputTPS != nil {
		merged.OutputTPS = *req.OutputTPS
	}
	if err := a.IPs.Policies().SetDefaults(merged); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ipOverrideReq 用指针区分「继承」(null/缺省) 与「显式 0=不限」。
type ipOverrideReq struct {
	WindowTokens     *int    `json:"window_tokens"`
	WindowMinutes    *int    `json:"window_minutes"`
	MaxActiveMinutes *int    `json:"max_active_minutes"`
	OutputTPS        *int    `json:"output_tps"`
	Disabled         *bool   `json:"disabled"`
	Note             *string `json:"note"`
}

func (a *App) handlePutIPOverride(w http.ResponseWriter, r *http.Request) {
	if a.IPs == nil {
		writeErr(w, http.StatusServiceUnavailable, "ip policy not initialized")
		return
	}
	ip, err := validIPPath(r.PathValue("ip"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var req ipOverrideReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	o := IPOverride{
		IP:               ip,
		WindowTokens:     req.WindowTokens,
		WindowMinutes:    req.WindowMinutes,
		MaxActiveMinutes: req.MaxActiveMinutes,
		OutputTPS:        req.OutputTPS,
	}
	if req.Disabled != nil {
		o.Disabled = *req.Disabled
	}
	if req.Note != nil {
		o.Note = *req.Note
	}
	if err := a.IPs.Policies().SetOverride(ip, o); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) handleDeleteIPOverride(w http.ResponseWriter, r *http.Request) {
	if a.IPs == nil {
		writeErr(w, http.StatusServiceUnavailable, "ip policy not initialized")
		return
	}
	ip, err := validIPPath(r.PathValue("ip"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := a.IPs.Policies().DeleteOverride(ip); err != nil {
		if os.IsNotExist(err) {
			writeErr(w, http.StatusNotFound, "override not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, "delete failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) handleResetIPWindow(w http.ResponseWriter, r *http.Request) {
	if a.IPs == nil {
		writeErr(w, http.StatusServiceUnavailable, "ip policy not initialized")
		return
	}
	ip, err := validIPPath(r.PathValue("ip"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := a.IPs.Reset(ip, time.Now()); err != nil {
		writeErr(w, http.StatusInternalServerError, "reset failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleAskUsage 返回 AI 问数的当日用量、额度与最近问答记录。
func (a *App) handleAskUsage(w http.ResponseWriter, r *http.Request) {
	if a.Ask == nil {
		writeErr(w, http.StatusServiceUnavailable, "ask meter not initialized")
		return
	}
	now := time.Now()
	date := strings.TrimSpace(r.URL.Query().Get("date"))
	if date == "" {
		date = now.Format("2006-01-02")
	}
	// 路径穿越修复（finding 543）：date 直接参与日志文件名，必须先做白名单校验。
	date, err := sanitizeDate(date)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid date")
		return
	}
	st := a.Ask.Snapshot(now)
	writeJSON(w, http.StatusOK, map[string]any{
		"quota":  st,
		"recent": a.Ask.AskUsageList(date, 20),
		"date":   date,
	})
}

// handleAuditList 返回管理端敏感操作审计记录。
func (a *App) handleAuditList(w http.ResponseWriter, r *http.Request) {
	if a.Audit == nil {
		writeErr(w, http.StatusServiceUnavailable, "audit not initialized")
		return
	}
	date := r.URL.Query().Get("date")
	if date == "" {
		date = time.Now().Format("2006-01-02")
	}
	// 路径穿越修复（finding 543）：同 handleAskUsage，拒绝含 / \ .. 的值。
	date, err := sanitizeDate(date)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid date")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": a.Audit.Read(date), "date": date})
}
