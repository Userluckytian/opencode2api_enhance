// 调用日志过滤查询与按实例聚合（main 分支功能迁移 M4）：
// 前端日志页过滤（节点/关键词/状态/时间窗/分页）与按节点组合聚合统计。
//
// 端点：POST /api/admin/call-log/filtered、GET /api/admin/call-log/aggregate
package manager

import (
	"encoding/json"
	"net/http"
	"strings"
)

// CallLogFilter 过滤参数（前端日志页；字段与 main CallLogFilter 一致）。
type CallLogFilter struct {
	Node    string `json:"node,omitempty"`    // nodes 数组中任一元素包含该串即匹配
	Keyword string `json:"keyword,omitempty"` // 匹配 model / path / err_msg / req_id
	Status  string `json:"status,omitempty"`  // ok=无异常；error=有异常/切换；其他不过滤
	Limit   int    `json:"limit,omitempty"`
	Offset  int    `json:"offset,omitempty"`
	FromTS  string `json:"from_ts,omitempty"` // ISO 字符串，字典序即时间序
	ToTS    string `json:"to_ts,omitempty"`
}

// matches 单条记录是否满足过滤条件（语义与 main CallLogFilter::matches 一致）。
func (f *CallLogFilter) matches(r *CallLogRecord) bool {
	if f.Node != "" {
		found := false
		for _, n := range r.Nodes {
			if strings.Contains(n, f.Node) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if f.Keyword != "" {
		hay := strings.Join([]string{r.Model, r.Path, r.ErrMsg, r.ReqID}, " ")
		if !strings.Contains(hay, f.Keyword) {
			return false
		}
	}
	switch f.Status {
	case "ok":
		if r.HasIssue() {
			return false
		}
	case "error":
		if !r.HasIssue() {
			return false
		}
	}
	if f.FromTS != "" && r.TS < f.FromTS {
		return false
	}
	if f.ToTS != "" && r.TS > f.ToTS {
		return false
	}
	return true
}

// ReadCallLogFiltered 读全量日志 → 过滤 → 最新在前 → 分页。
func (m *Manager) ReadCallLogFiltered(f *CallLogFilter) []CallLogRecord {
	all := m.ReadCallLog(0)
	out := make([]CallLogRecord, 0, len(all))
	for i := range all {
		if f.matches(&all[i]) {
			out = append(out, all[i])
		}
	}
	// 最新在前（ReadCallLog 按文件顺序=旧→新）
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	offset := f.Offset
	if offset < 0 || offset > len(out) {
		offset = len(out)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	if offset+limit > len(out) {
		limit = len(out) - offset
	}
	if limit < 0 {
		limit = 0
	}
	return out[offset : offset+limit]
}

// CallLogAggregate 日志聚合条目（按节点组合统计）。
type CallLogAggregate struct {
	Instance string `json:"instance"` // 节点组合（nodes 用 " → " 连接，无节点为 "-"）
	Total    int    `json:"total"`
	Errors   int    `json:"errors"`
	LastTS   string `json:"last_ts"` // 最近一次时间（ISO 字符串）
}

// AggregateCallLog 按节点组合聚合条数/错误数/最近时间。
func (m *Manager) AggregateCallLog() []CallLogAggregate {
	all := m.ReadCallLog(0)
	type acc struct {
		total  int
		errors int
		lastTS string
	}
	m2 := map[string]*acc{}
	var order []string
	for i := range all {
		key := "-"
		if len(all[i].Nodes) > 0 {
			key = strings.Join(all[i].Nodes, " → ")
		}
		a := m2[key]
		if a == nil {
			a = &acc{}
			m2[key] = a
			order = append(order, key)
		}
		a.total++
		if all[i].HasIssue() {
			a.errors++
		}
		if all[i].TS > a.lastTS {
			a.lastTS = all[i].TS
		}
	}
	out := make([]CallLogAggregate, 0, len(order))
	for _, k := range order {
		a := m2[k]
		out = append(out, CallLogAggregate{Instance: k, Total: a.total, Errors: a.errors, LastTS: a.lastTS})
	}
	return out
}

// CallLogFilteredHandler POST {filter} 过滤查询。
func (m *Manager) CallLogFilteredHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodOK(w, r, http.MethodPost) {
			return
		}
		var f CallLogFilter
		if json.NewDecoder(r.Body).Decode(&f) != nil {
			writeErr(w, http.StatusBadRequest, "bad body")
			return
		}
		writeJSON(w, m.ReadCallLogFiltered(&f))
	}
}

// CallLogAggregateHandler GET 按实例聚合。
func (m *Manager) CallLogAggregateHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodOK(w, r, http.MethodGet) {
			return
		}
		writeJSON(w, m.AggregateCallLog())
	}
}
