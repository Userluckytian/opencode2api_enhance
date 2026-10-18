package main

import (
	"net/http"
	"time"

	"github.com/6Kmfi6HP/opencode2api/core/contract"
)

// callPolicyVer 选路/冷却策略版本戳：改参数时同步更新，便于日志里对比策略前后效果。
const callPolicyVer = "b6-rate15x5"

// classifyUpstreamError 委托 contract 的统一分类（错误分析视图/离线挖掘共用）。
func classifyUpstreamError(status int, body []byte) (class, code, message string) {
	return contract.ClassifyUpstreamError(status, body)
}

// retryAfterMS 委托 contract 的 Retry-After 解析。
func retryAfterMS(h http.Header) int64 {
	return contract.RetryAfterMS(h)
}

// attachUpstreamError 写入结构化错误字段 + 事件 + 失败时的池快照。
// 调用方仍负责设置 rec.ErrMsg（保留人类可读的完整错误串）。
func attachUpstreamError(rec *CallRecord, status int, body []byte, headers http.Header, selected string) {
	rec.SchemaVersion = 3
	rec.UpstreamStatus = status
	class, code, msg := classifyUpstreamError(status, body)
	rec.ErrorClass = class
	rec.ErrorCode = code
	rec.ErrorMessage = msg
	rec.RetryAfterMS = retryAfterMS(headers)
	attachPoolSnapshot(rec, selected)
	detail := rec.ErrMsg
	if detail == "" {
		detail = upstreamErrMsg(status, nil, body)
	}
	rec.Events = append(rec.Events, CallEvent{
		Type: "upstream_error", Node: selected, Detail: detail, At: time.Now(),
		Status: status, ErrorClass: class, ErrorCode: code, RetryAfterMS: rec.RetryAfterMS,
	})
}

// attachPoolSnapshot 记录失败时的池状态与所选节点状态。
func attachPoolSnapshot(rec *CallRecord, selected string) {
	snap := snapshotPoolHealth()
	rec.Pool = &contract.PoolSnapshot{
		Total:       snap.Total,
		Available:   snap.Available,
		Cooling:     snap.Cooling,
		BadQuota:    snap.BadQuota,
		BadAuth:     snap.BadAuth,
		BadLink:     snap.BadLink,
		BreakerOpen: snap.BreakerOpen,
		Selected:    selected,
	}
	for _, n := range snap.Nodes {
		if n.Addr != selected {
			continue
		}
		rec.Pool.SelectedState = n.State
		rec.Pool.SelectedUntil = n.Until
		rec.NodeState = &contract.NodeState{
			Addr:          n.Addr,
			Class:         n.State,
			Reason:        n.Reason,
			BadCount:      n.BadCount,
			CooldownUntil: n.Until,
		}
		break
	}
}
