package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// 上游管理 API（多上游，2026-10-28）。
//
// 安全约定与单上游时代保持一致：
//   - base_url 写入前必须过 ValidateUpstreamBaseURL（SSRF 四道闸门不变）；
//   - api_key 只回 maskSecret 后的值，页面无法回填明文；
//   - 白名单（upstream_allowed_hosts）仍只由部署方下发，管理 API 不可写。

// upstreamView 是管理 API 返回的单条上游（密钥脱敏）。
type upstreamView struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	BaseURL      string `json:"base_url"`
	APIKeyMasked string `json:"api_key_masked"`
	APIKeySet    bool   `json:"api_key_set"`
	Enabled      bool   `json:"enabled"`
	IsDefault    bool   `json:"is_default"`
}

func (a *App) upstreamViews() ([]upstreamView, string) {
	a.Mu.RLock()
	list := a.Cfg.EffectiveUpstreams()
	defaultID := a.Cfg.DefaultUpstreamID
	a.Mu.RUnlock()
	if defaultID == "" {
		for _, u := range list {
			if u.Enabled {
				defaultID = u.ID
				break
			}
		}
	}
	out := make([]upstreamView, 0, len(list))
	for _, u := range list {
		out = append(out, upstreamView{
			ID:           u.ID,
			Name:         u.Name,
			BaseURL:      u.BaseURL,
			APIKeyMasked: maskSecret(u.APIKey),
			APIKeySet:    u.APIKey != "",
			Enabled:      u.Enabled,
			IsDefault:    u.ID == defaultID,
		})
	}
	return out, defaultID
}

// handleListUpstreams 返回上游列表（密钥脱敏）。
func (a *App) handleListUpstreams(w http.ResponseWriter, r *http.Request) {
	views, defaultID := a.upstreamViews()
	writeJSON(w, http.StatusOK, map[string]any{
		"upstreams":           views,
		"default_upstream_id": defaultID,
	})
}

// upstreamMutation 是新增/修改上游的请求体（指针 = 未提交则保持原值）。
type upstreamMutation struct {
	ID      *string `json:"id"`
	Name    *string `json:"name"`
	BaseURL *string `json:"base_url"`
	APIKey  *string `json:"api_key"`
	Enabled *bool   `json:"enabled"`
}

// auditUpstream 记录一次上游配置变更（密钥只记末 4 位）。
func (a *App) auditUpstream(r *http.Request, action, detail, result string) {
	if a.Audit == nil {
		return
	}
	a.Audit.Write(AuditEntry{
		Time:   time.Now().Format(time.RFC3339),
		Action: action,
		Detail: detail,
		IP:     clientIP(r),
		Result: result,
	})
}

// handleCreateUpstream 新增上游。
func (a *App) handleCreateUpstream(w http.ResponseWriter, r *http.Request) {
	var req upstreamMutation
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.ID == nil || req.BaseURL == nil {
		writeErr(w, http.StatusBadRequest, "id 与 base_url 必填")
		return
	}
	id := strings.TrimSpace(*req.ID)
	if !validUpstreamID(id) {
		writeErr(w, http.StatusBadRequest, "id 需字母数字开头，仅含 - _，长度 1~32")
		return
	}
	baseURL := strings.TrimRight(strings.TrimSpace(*req.BaseURL), "/")
	if baseURL == "" {
		writeErr(w, http.StatusBadRequest, "base_url 不能为空")
		return
	}
	// SSRF 校验：与单上游时代同一套闸门，失败不落盘。
	if err := ValidateUpstreamBaseURL(baseURL, a.guard()); err != nil {
		a.auditUpstream(r, "create_upstream", fmt.Sprintf("id=%s base_url=%q rejected: %v", id, baseURL, err), "denied")
		writeErr(w, http.StatusBadRequest, "base_url 被拒绝: "+err.Error())
		return
	}
	name := id
	if req.Name != nil && strings.TrimSpace(*req.Name) != "" {
		name = strings.TrimSpace(*req.Name)
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	var apiKey string
	if req.APIKey != nil {
		apiKey = strings.TrimSpace(*req.APIKey)
	}

	a.Mu.Lock()
	// 首次写列表前必须把旧单上游字段实体化，否则原上游会凭空消失。
	a.Cfg.EnsureUpstreamsMaterialized()
	if _, exists := FindUpstream(a.Cfg.Upstreams, id); exists {
		a.Mu.Unlock()
		writeErr(w, http.StatusConflict, "上游 ID 已存在: "+id)
		return
	}
	a.Cfg.Upstreams = append(a.Cfg.Upstreams, Upstream{
		ID: id, Name: name, BaseURL: baseURL, APIKey: apiKey, Enabled: enabled,
	})
	if a.Cfg.DefaultUpstreamID == "" && enabled {
		a.Cfg.DefaultUpstreamID = id
	}
	err := a.Cfg.Save(a.ConfigPath)
	a.Mu.Unlock()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "save failed")
		return
	}
	a.auditUpstream(r, "create_upstream",
		fmt.Sprintf("id=%s name=%q base_url=%s key=%s enabled=%v", id, name, baseURL, maskSecret(apiKey), enabled), "ok")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id})
}

// handleUpdateUpstream 修改上游；api_key 留空 = 保持原值。
func (a *App) handleUpdateUpstream(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req upstreamMutation
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}

	// SSRF 校验放在锁外：ValidateUpstreamBaseURL 会做 DNS 解析（最长 5s），
	// 持写锁期间会阻塞所有 /v1 请求读取配置。
	if req.BaseURL != nil {
		next := strings.TrimRight(strings.TrimSpace(*req.BaseURL), "/")
		if next == "" {
			writeErr(w, http.StatusBadRequest, "base_url 不能为空")
			return
		}
		if err := ValidateUpstreamBaseURL(next, a.guard()); err != nil {
			a.auditUpstream(r, "update_upstream",
				fmt.Sprintf("id=%s base_url=%q rejected: %v", id, next, err), "denied")
			writeErr(w, http.StatusBadRequest, "base_url 被拒绝: "+err.Error())
			return
		}
	}

	a.Mu.Lock()
	a.Cfg.EnsureUpstreamsMaterialized()
	idx := -1
	for i, u := range a.Cfg.Upstreams {
		if u.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		a.Mu.Unlock()
		writeErr(w, http.StatusNotFound, "上游不存在: "+id)
		return
	}
	cur := a.Cfg.Upstreams[idx]

	if req.BaseURL != nil {
		cur.BaseURL = strings.TrimRight(strings.TrimSpace(*req.BaseURL), "/")
	}
	if req.Name != nil && strings.TrimSpace(*req.Name) != "" {
		cur.Name = strings.TrimSpace(*req.Name)
	}
	if req.APIKey != nil && strings.TrimSpace(*req.APIKey) != "" {
		cur.APIKey = strings.TrimSpace(*req.APIKey)
	}
	if req.Enabled != nil {
		// 不允许停用最后一个启用中的上游：否则整个网关立刻 503。
		if !*req.Enabled && cur.Enabled {
			others := 0
			for i, u := range a.Cfg.Upstreams {
				if i != idx && u.Enabled {
					others++
				}
			}
			if others == 0 {
				a.Mu.Unlock()
				writeErr(w, http.StatusBadRequest, "不能停用最后一个启用中的上游")
				return
			}
		}
		cur.Enabled = *req.Enabled
	}
	a.Cfg.Upstreams[idx] = cur
	// 默认上游被停用时改指第一个启用项，避免默认 ID 指向停用条目。
	if !cur.Enabled && a.Cfg.DefaultUpstreamID == cur.ID {
		a.Cfg.DefaultUpstreamID = ""
		for _, u := range a.Cfg.Upstreams {
			if u.Enabled {
				a.Cfg.DefaultUpstreamID = u.ID
				break
			}
		}
	}
	err := a.Cfg.Save(a.ConfigPath)
	a.Mu.Unlock()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "save failed")
		return
	}
	detail := fmt.Sprintf("id=%s base_url=%s enabled=%v", cur.ID, cur.BaseURL, cur.Enabled)
	if req.APIKey != nil && strings.TrimSpace(*req.APIKey) != "" {
		detail += "; api_key=" + maskSecret(cur.APIKey)
	} else {
		detail += "; api_key=未修改"
	}
	a.auditUpstream(r, "update_upstream", detail, "ok")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleDeleteUpstream 删除上游；禁止删默认上游与最后一个启用上游。
func (a *App) handleDeleteUpstream(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	a.Mu.Lock()
	a.Cfg.EnsureUpstreamsMaterialized()
	idx := -1
	for i, u := range a.Cfg.Upstreams {
		if u.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		a.Mu.Unlock()
		writeErr(w, http.StatusNotFound, "上游不存在: "+id)
		return
	}
	// 默认上游不允许直接删：先改默认指向，避免「删完才发现没有默认上游」。
	if a.Cfg.DefaultUpstreamID == id {
		a.Mu.Unlock()
		writeErr(w, http.StatusBadRequest, "不能删除默认上游，请先把默认上游改为其它条目")
		return
	}
	// 启用中的上游不能删到「一个不剩」。
	remainingEnabled := 0
	for i, u := range a.Cfg.Upstreams {
		if i != idx && u.Enabled {
			remainingEnabled++
		}
	}
	if a.Cfg.Upstreams[idx].Enabled && remainingEnabled == 0 {
		a.Mu.Unlock()
		writeErr(w, http.StatusBadRequest, "不能删除最后一个启用中的上游")
		return
	}
	a.Cfg.Upstreams = append(a.Cfg.Upstreams[:idx], a.Cfg.Upstreams[idx+1:]...)
	err := a.Cfg.Save(a.ConfigPath)
	a.Mu.Unlock()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "save failed")
		return
	}
	a.auditUpstream(r, "delete_upstream", "id="+id, "ok")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleTestUpstream 拉一次该上游的 /v1/models 验证连通性。
func (a *App) handleTestUpstream(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	a.Mu.RLock()
	list := a.Cfg.EffectiveUpstreams()
	a.Mu.RUnlock()
	u, ok := FindUpstream(list, id)
	if !ok {
		writeErr(w, http.StatusNotFound, "上游不存在: "+id)
		return
	}
	if strings.TrimSpace(u.BaseURL) == "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "warning": "未配置 base_url"})
		return
	}
	models, err := FetchModels(u.BaseURL, u.APIKey, a.client())
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":      false,
			"warning": "无法连接上游: " + err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"model_count": len(models),
		"models":      models,
	})
}

// handleAdminModels 按上游分组返回可用模型（多上游版本）。
//
// 单个上游失败只影响自己那一组（填 warning），不影响其它上游；
// allowed 条目带上游前缀，与用户实际要填的 model 字符串一致。
func (a *App) handleAdminModels(w http.ResponseWriter, r *http.Request) {
	a.Mu.RLock()
	allowed := append([]string(nil), a.Cfg.AllowedModels...)
	list := append([]Upstream(nil), a.Cfg.EffectiveUpstreams()...)
	defaultID := a.Cfg.DefaultUpstreamID
	a.Mu.RUnlock()
	if defaultID == "" {
		for _, u := range list {
			if u.Enabled {
				defaultID = u.ID
				break
			}
		}
	}

	type group struct {
		ID      string   `json:"id"`
		Name    string   `json:"name"`
		Models  []string `json:"models"`
		Warning string   `json:"warning,omitempty"`
		Enabled bool     `json:"enabled"`
		Prefix  string   `json:"prefix"`
	}
	groups := make([]group, len(list))
	var wg sync.WaitGroup
	for i := range list {
		u := list[i]
		groups[i] = group{
			ID: u.ID, Name: u.Name, Enabled: u.Enabled,
			Models: []string{}, Prefix: upstreamPrefix(u.ID, defaultID),
		}
		if !u.Enabled {
			groups[i].Warning = "已停用，不参与路由"
			continue
		}
		if strings.TrimSpace(u.BaseURL) == "" {
			groups[i].Warning = "未配置 base_url"
			continue
		}
		wg.Add(1)
		go func(i int, u Upstream) {
			defer wg.Done()
			models, err := FetchModels(u.BaseURL, u.APIKey, a.client())
			if err != nil {
				groups[i].Warning = "无法连接上游: " + err.Error()
				return
			}
			sort.Strings(models)
			groups[i].Models = models
		}(i, u)
	}
	wg.Wait()

	if len(groups) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{
			"upstreams": []group{},
			"allowed":   allowed,
			"warning":   "尚未配置上游实例地址，请先到「上游管理」新增",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"upstreams": groups,
		"allowed":   allowed,
	})
}
