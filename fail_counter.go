package main

// 阶段 7：失败原因计数器（节点 × 原因）。
//
// 在失败调用收口处，按「节点 × 失败原因（429/401/503/connect/timeout/other）」二维累加，
// 让「最近哪个节点出什么问题」变成一次查询。数据源复用阶段1的 CallRecord 错误分类。
// 进程内计数（与 token 统计同生命周期），提供查询端点与重置；UI 展示可后续对齐统计页。
// 纯标准库实现。

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
)

// 失败原因类别。
const (
	FailReason429     = "429"
	FailReason401     = "401"
	FailReason503     = "503"
	FailReasonConnect = "connect"
	FailReasonTimeout = "timeout"
	FailReasonOther   = "other"
)

var (
	failCountMu sync.RWMutex
	// failCounts[node][reason] = 累计次数
	failCounts = map[string]map[string]int64{}
)

// classifyFailReason 从失败 CallRecord 的 status/err_msg/events 判定原因类别。
func classifyFailReason(rec CallRecord) string {
	var sb strings.Builder
	sb.WriteString(strings.ToLower(rec.Status))
	sb.WriteByte(' ')
	sb.WriteString(strings.ToLower(rec.ErrMsg))
	for _, ev := range rec.Events {
		sb.WriteByte(' ')
		sb.WriteString(strings.ToLower(ev.Type))
		sb.WriteByte(' ')
		sb.WriteString(strings.ToLower(ev.Detail))
	}
	blob := sb.String()
	switch {
	case strings.Contains(blob, "429"):
		return FailReason429
	case strings.Contains(blob, "401"), strings.Contains(blob, "403"):
		return FailReason401
	case strings.Contains(blob, "503"):
		return FailReason503
	case strings.Contains(blob, "timeout"), strings.Contains(blob, "deadline"), strings.Contains(blob, "超时"):
		return FailReasonTimeout
	case strings.Contains(blob, "connect"), strings.Contains(blob, "dial"), strings.Contains(blob, "refused"):
		return FailReasonConnect
	default:
		return FailReasonOther
	}
}

// recordFailure 对本次失败涉及的每个真实节点累加对应原因计数（跳过空/「直连」占位）。
func recordFailure(rec CallRecord) {
	reason := classifyFailReason(rec)
	failCountMu.Lock()
	defer failCountMu.Unlock()
	for _, n := range rec.Nodes {
		node := strings.TrimSpace(n)
		if node == "" || node == "直连" {
			continue
		}
		if failCounts[node] == nil {
			failCounts[node] = map[string]int64{}
		}
		failCounts[node][reason]++
	}
}

// snapshotFailCounts 返回二维计数的深拷贝（供查询/测试，避免外部持有内部 map）。
func snapshotFailCounts() map[string]map[string]int64 {
	failCountMu.RLock()
	defer failCountMu.RUnlock()
	out := make(map[string]map[string]int64, len(failCounts))
	for n, m := range failCounts {
		cm := make(map[string]int64, len(m))
		for r, c := range m {
			cm[r] = c
		}
		out[n] = cm
	}
	return out
}

// resetFailCounts 清空计数（随统计重置调用）。
func resetFailCounts() {
	failCountMu.Lock()
	failCounts = map[string]map[string]int64{}
	failCountMu.Unlock()
}

// failStatsHandler GET /api/admin/fail-stats → 节点 × 失败原因二维计数。
func failStatsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"fail_counts": snapshotFailCounts()})
}
