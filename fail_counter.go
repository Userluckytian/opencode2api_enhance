package main

// 失败原因计数器（v2.1：厂商 × 错误码）。
//
// v1.x 的维度是「节点 × 原因」（SOCKS5 节点池时代）。v2.0 删掉节点池后该维度
// 恒为空，统计页的「失败原因分布」卡片因此拿不到任何数据。
// 现在改为按 **厂商 × 错误类别** 聚合：厂商来自 CallRecord.Vendor，错误类别取自
// CallRecord.ErrorClass（rate_limit / quota / auth / upstream / transport /
// protocol / other），回退到状态码与文本归类。
//
// 进程内计数（与 token 统计同生命周期），提供查询端点与重置；纯标准库实现。

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
)

// 失败原因类别（与 CallRecord.ErrorClass 对齐，并补充状态码兜底类）。
const (
	FailReasonRateLimit = "rate_limit"
	FailReasonQuota     = "quota"
	FailReasonAuth      = "auth"
	FailReasonUpstream  = "upstream"
	FailReasonTransport = "transport"
	FailReasonProtocol  = "protocol"
	FailReasonTimeout   = "timeout"
	FailReasonOther     = "other"
)

var (
	failCountMu sync.RWMutex
	// failCounts[厂商][原因] = 累计次数
	failCounts = map[string]map[string]int64{}
)

// failReasonOf 从失败 CallRecord 判定原因类别：优先用结构化字段，再回退到文本匹配。
func failReasonOf(rec CallRecord) string {
	if cls := strings.TrimSpace(rec.ErrorClass); cls != "" {
		switch cls {
		case FailReasonRateLimit, FailReasonQuota, FailReasonAuth,
			FailReasonUpstream, FailReasonTransport, FailReasonProtocol:
			return cls
		}
	}
	// 状态码兜底（旧记录或上游未给结构化错误时）。
	switch rec.UpstreamStatus {
	case 429:
		return FailReasonRateLimit
	case 401, 403:
		return FailReasonAuth
	case 408, 504:
		return FailReasonTimeout
	}
	var sb strings.Builder
	sb.WriteString(strings.ToLower(rec.ErrMsg))
	for _, ev := range rec.Events {
		sb.WriteByte(' ')
		sb.WriteString(strings.ToLower(ev.Type))
		sb.WriteByte(' ')
		sb.WriteString(strings.ToLower(ev.Detail))
	}
	blob := sb.String()
	switch {
	case strings.Contains(blob, "429"), strings.Contains(blob, "rate limit"):
		return FailReasonRateLimit
	case strings.Contains(blob, "quota"), strings.Contains(blob, "额度"):
		return FailReasonQuota
	case strings.Contains(blob, "401"), strings.Contains(blob, "403"), strings.Contains(blob, "unauthorized"):
		return FailReasonAuth
	case strings.Contains(blob, "timeout"), strings.Contains(blob, "deadline"), strings.Contains(blob, "超时"):
		return FailReasonTimeout
	case strings.Contains(blob, "connect"), strings.Contains(blob, "dial"), strings.Contains(blob, "refused"):
		return FailReasonTransport
	case strings.Contains(blob, "503"), strings.Contains(blob, "502"), strings.Contains(blob, "upstream"):
		return FailReasonUpstream
	default:
		return FailReasonOther
	}
}

// failVendorOf 取失败归属的厂商：优先 Vendor 字段，回退到模型前缀。
func failVendorOf(rec CallRecord) string {
	if v := strings.TrimSpace(rec.Vendor); v != "" {
		return v
	}
	if prefix, _, ok := strings.Cut(rec.Model, "/"); ok && prefix != "" {
		return prefix
	}
	if strings.TrimSpace(rec.Model) != "" {
		return "(unknown)"
	}
	return ""
}

// recordFailure 对本次失败累加「厂商 × 原因」计数。
func recordFailure(rec CallRecord) {
	vendor := failVendorOf(rec)
	if vendor == "" {
		return
	}
	reason := failReasonOf(rec)
	failCountMu.Lock()
	defer failCountMu.Unlock()
	if failCounts[vendor] == nil {
		failCounts[vendor] = map[string]int64{}
	}
	failCounts[vendor][reason]++
}

// snapshotFailCounts 返回二维计数的深拷贝（供查询/测试，避免外部持有内部 map）。
func snapshotFailCounts() map[string]map[string]int64 {
	failCountMu.RLock()
	defer failCountMu.RUnlock()
	out := make(map[string]map[string]int64, len(failCounts))
	for v, m := range failCounts {
		cm := make(map[string]int64, len(m))
		for r, c := range m {
			cm[r] = c
		}
		out[v] = cm
	}
	return out
}

// resetFailCounts 清空计数（随统计重置调用）。
func resetFailCounts() {
	failCountMu.Lock()
	failCounts = map[string]map[string]int64{}
	failCountMu.Unlock()
}

// failStatsHandler GET /api/admin/fail-stats → 厂商 × 失败原因二维计数。
func failStatsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"fail_counts": snapshotFailCounts()})
}