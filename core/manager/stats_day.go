// 统计按天查看：按调用日志（runtime/_unified-gateway/call_log.jsonl）聚合单日用量。
// stats.json 只存累计值、无时间维度；调用日志带 ts/tokens/model/nodes，是按天聚合的唯一数据源。
// 与日志页一致，日期取 ts 的 YYYY-MM-DD 前缀。
package manager

import (
	"net/http"
	"sort"
	"strings"
)

// DayStats 单日统计（统计页按天查看契约）。
type DayStats struct {
	Day                   string            `json:"day"`
	TotalRequests         int64             `json:"total_requests"`
	OkRequests            int64             `json:"ok_requests"`
	FailRequests          int64             `json:"fail_requests"`
	TotalPromptTokens     int64             `json:"total_prompt_tokens"`
	TotalCompletionTokens int64             `json:"total_completion_tokens"`
	TotalTokens           int64             `json:"total_tokens"`
	ByModel               []ModelStat       `json:"by_model"`
	ByNode                []GatewayNodeStat `json:"by_node"`
}

// newDayStats 空视图。
func newDayStats(day string) DayStats {
	return DayStats{Day: day, ByModel: []ModelStat{}, ByNode: []GatewayNodeStat{}}
}

// StatsByDay 聚合指定日期（YYYY-MM-DD）的调用日志；空日返回全量。
// 只统计最近一次“重置统计”时间点之后的记录：重置后累计 Token 已清零，
// 按天视图也必须从同一时间点起算，避免两套数据源不一致。
func (m *Manager) StatsByDay(day string) DayStats {
	out := newDayStats(day)
	models := map[string]*GoModelStats{}
	nodes := map[string]*GoNodeStat{}
	resetAt := m.statsResetAt()
	for _, rec := range m.ReadCallLog(0) {
		if day != "" && !strings.HasPrefix(rec.TS, day) {
			continue
		}
		if !resetAt.IsZero() {
			if t := callLogTime(rec.TS); !t.IsZero() && t.Before(resetAt) {
				continue
			}
		}
		out.TotalRequests++
		if rec.Status == "ok" {
			out.OkRequests++
		} else {
			out.FailRequests++
		}
		out.TotalPromptTokens += rec.PromptTokens
		out.TotalCompletionTokens += rec.CompletionTokens
		out.TotalTokens += rec.PromptTokens + rec.CompletionTokens

		if rec.Model != "" {
			ms := models[rec.Model]
			if ms == nil {
				ms = &GoModelStats{}
				models[rec.Model] = ms
			}
			ms.RequestCount++
			ms.PromptTokens += rec.PromptTokens
			ms.CompletionTokens += rec.CompletionTokens
			ms.TotalTokens += rec.PromptTokens + rec.CompletionTokens
		}
		if len(rec.Nodes) > 0 {
			node := rec.Nodes[len(rec.Nodes)-1]
			ns := nodes[node]
			if ns == nil {
				ns = &GoNodeStat{}
				nodes[node] = ns
			}
			ns.RequestCount++
			ns.PromptTokens += rec.PromptTokens
			ns.CompletionTokens += rec.CompletionTokens
			ns.TotalTokens += rec.PromptTokens + rec.CompletionTokens
		}
	}
	for name, ms := range models {
		out.ByModel = append(out.ByModel, ModelStat{
			Model: name, Requests: ms.RequestCount,
			PromptTokens: ms.PromptTokens, CompletionTokens: ms.CompletionTokens, TotalTokens: ms.TotalTokens,
		})
	}
	sort.Slice(out.ByModel, func(i, j int) bool { return out.ByModel[i].TotalTokens > out.ByModel[j].TotalTokens })
	for name, ns := range nodes {
		out.ByNode = append(out.ByNode, GatewayNodeStat{
			Name: name, Addr: name, Requests: ns.RequestCount,
			PromptTokens: ns.PromptTokens, CompletionTokens: ns.CompletionTokens, TotalTokens: ns.TotalTokens,
		})
	}
	sort.Slice(out.ByNode, func(i, j int) bool { return out.ByNode[i].TotalTokens > out.ByNode[j].TotalTokens })
	return out
}

// StatsByDayHandler GET /api/admin/stats/by-day?date=YYYY-MM-DD（空=全量）。
func (m *Manager) StatsByDayHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodOK(w, r, http.MethodGet) {
			return
		}
		writeJSON(w, m.StatsByDay(strings.TrimSpace(r.URL.Query().Get("date"))))
	}
}
