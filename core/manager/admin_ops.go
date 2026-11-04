package manager

import (
	"net/http"
	"strconv"
	"strings"
)

// requireMethodOK 方法校验（不通过时写 405 并返回 false）。
func requireMethodOK(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method != method {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return false
	}
	return true
}

// itoa16 十六进制端口字符串（实例自动命名等旧用途保留）。
func itoa16(v int) string { return strconv.FormatInt(int64(v), 16) }

// parsePortQuery 解析 ?port= 查询参数到 out（缺省不改）。
func parsePortQuery(r *http.Request, out *uint16) (bool, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("port"))
	if raw == "" {
		return false, nil
	}
	n, err := strconv.ParseUint(raw, 10, 16)
	if err != nil {
		return false, err
	}
	*out = uint16(n)
	return true, nil
}

// PortSuggestHandler GET ?start=&count=：建议一段可用端口。
func (m *Manager) PortSuggestHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodOK(w, r, http.MethodGet) {
			return
		}
		start := 40000
		if _, err := parsePortQuery(r, (*uint16)(nil)); err != nil {
			writeErr(w, http.StatusBadRequest, "port 参数无效")
			return
		}
		if s := r.URL.Query().Get("start"); s != "" {
			if n, err := strconv.Atoi(s); err == nil && n > 0 {
				start = n
			}
		}
		count := 1
		if s := r.URL.Query().Get("count"); s != "" {
			if n, err := strconv.Atoi(s); err == nil && n > 0 && n <= 64 {
				count = n
			}
		}
		writeJSON(w, m.suggestFreePorts(uint16(start), count))
	}
}

// PortCheckHandler GET ?port=：检查单个端口是否空闲。
func (m *Manager) PortCheckHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodOK(w, r, http.MethodGet) {
			return
		}
		var port uint16
		ok, err := parsePortQuery(r, &port)
		if err != nil || !ok {
			writeErr(w, http.StatusBadRequest, "需要 port 参数")
			return
		}
		writeJSON(w, map[string]any{"port": port, "free": isPortFree(port)})
	}
}

// DataCleanHandler POST ?level=1|2|3：分级清理数据。
func (m *Manager) DataCleanHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodOK(w, r, http.MethodPost) {
			return
		}
		level := 1
		if s := r.URL.Query().Get("level"); s != "" {
			if n, err := strconv.Atoi(s); err == nil {
				level = n
			}
		}
		if err := m.DataClean(level); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, map[string]any{"status": "ok", "level": level})
	}
}