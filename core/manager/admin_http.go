// 管理域 HTTP 处理器（/api/admin/*）。由 main 以现有鉴权中间件挂载：
//
//	GET/POST /api/admin/config        → requireAuth
//	GET /api/admin/stats              → requireAuth
//	POST /api/admin/stats/reset       → apiKeyAuth
//	GET /api/admin/call-log           → requireAuth
//	POST /api/admin/call-log/clear    → requireAuth
//	GET /api/admin/binaries           → apiKeyAuth
//	GET /api/admin/instances          → requireAuth
package manager

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// writeJSON 统一 JSON 输出。
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// writeErr 输出错误 JSON。
func writeErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": msg})
}

// requireMethod 校验 HTTP 方法。
func requireMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method != method {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	return true
}

// ConfigViewHandler GET（也可承担 config_set 的 POST 分支，见 ConfigGetHandler）。
func (m *Manager) ConfigGetHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethod(w, r, http.MethodGet) {
			return
		}
		writeJSON(w, m.ConfigViewOf())
	}
}

// ConfigSettingHandler POST：支持两种形态——
//
//	{"key":"show_node_prefix","value":"true"}（Rust config_set）
//	或整表写回（兼容旧面板 /api/config 风格，忽略）
func (m *Manager) ConfigSetHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethod(w, r, http.MethodPost) {
			return
		}
		var req struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.Key == "" {
			writeErr(w, http.StatusBadRequest, "请求体需含 key/value")
			return
		}
		if err := m.ConfigSet(req.Key, req.Value); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, map[string]any{"status": "ok", "key": req.Key})
	}
}

// ChainCheckHandler POST {node}：检测目标节点经前置组的整条链。
func (m *Manager) ChainCheckHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethod(w, r, http.MethodPost) {
			return
		}
		var req struct {
			Node string `json:"node"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil || strings.TrimSpace(req.Node) == "" {
			writeErr(w, http.StatusBadRequest, "请求体需含 node")
			return
		}
		writeJSON(w, m.CheckChain(strings.TrimSpace(req.Node)))
	}
}

// StatsHandler GET 统计聚合。
func (m *Manager) StatsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethod(w, r, http.MethodGet) {
			return
		}
		writeJSON(w, m.AggregateStats())
	}
}

// ResetStatsHandler POST 重置统计（?clearDeleted=bool）。
func (m *Manager) ResetStatsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethod(w, r, http.MethodPost) {
			return
		}
		clearDeleted := true
		if q := r.URL.Query().Get("clearDeleted"); q != "" {
			clearDeleted = q == "true" || q == "1"
		}
		writeJSON(w, m.ResetStats(clearDeleted))
	}
}

// CallLogHandler GET 调用日志（?limit=；缺省 0 = 全部历史）。
func (m *Manager) CallLogHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethod(w, r, http.MethodGet) {
			return
		}
		limit := 0 // 0 = 全部
		if s := r.URL.Query().Get("limit"); s != "" {
			if n := parsePositiveInt(s); n > 0 {
				limit = n
			}
		}
		writeJSON(w, m.ReadCallLog(limit))
	}
}

// ClearCallLogHandler POST 清空日志。
func (m *Manager) ClearCallLogHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethod(w, r, http.MethodPost) {
			return
		}
		if err := m.ClearCallLog(); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, map[string]any{"status": "ok"})
	}
}

// BinariesInfo 二进制信息（前端契约）。
type BinariesInfo struct {
	BinDir        string `json:"bin_dir"`
	OCExists      bool   `json:"oc_exists"`
	SBExists      bool   `json:"sb_exists"`
	SidecarExists bool   `json:"sidecar_exists"`
}

// BinariesHandler GET 二进制信息。
func (m *Manager) BinariesHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethod(w, r, http.MethodGet) {
			return
		}
		info := BinariesInfo{BinDir: m.paths.BinDir}
		info.OCExists = existsInBin(m.paths.BinDir, "opencode2api")
		info.SBExists = existsInBin(m.paths.BinDir, "sing-box")
		info.SidecarExists = existsInBin(m.paths.BinDir, "free-sidecar")
		writeJSON(w, info)
	}
}

// InstancesHandler GET 实例列表。
func (m *Manager) InstancesHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethod(w, r, http.MethodGet) {
			return
		}
		writeJSON(w, m.ListInstances())
	}
}

// existsInBin 判断 bin 目录中存在 <name>.exe 或 <name>。
func existsInBin(binDir, name string) bool {
	for _, p := range []string{name + ".exe", name} {
		if _, err := os.Stat(filepath.Join(binDir, p)); err == nil {
			return true
		}
	}
	return false
}

// parsePositiveInt 解析正整数（0 → 返回 0）。
func parsePositiveInt(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}
