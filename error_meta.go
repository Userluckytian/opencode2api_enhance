package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/6Kmfi6HP/opencode2api/core/contract"
)

// classifyUpstreamError 把上游状态码 + 错误体归为稳定的错误大类/错误码。
// 目的：日志分析视图和离线挖掘不依赖 err_msg 的中文字面量。
func classifyUpstreamError(status int, body []byte) (class, code, message string) {
	switch {
	case status == http.StatusTooManyRequests:
		class = "rate_limit"
	case status == http.StatusPaymentRequired:
		class = "quota"
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		class = "auth"
	case status == 0:
		class = "transport"
	case status >= 500:
		class = "upstream"
	default:
		class = "other"
	}
	if len(body) == 0 {
		if status != 0 {
			code = fmt.Sprintf("http_%d", status)
		}
		return class, code, ""
	}
	var root map[string]any
	if json.Unmarshal(body, &root) == nil {
		errObj, _ := root["error"].(map[string]any)
		if errObj != nil {
			if c, _ := errObj["code"].(string); c != "" {
				code = c
			}
			if code == "" {
				if t, _ := errObj["type"].(string); t != "" {
					code = t
				}
			}
			if m, _ := errObj["message"].(string); m != "" {
				message = m
			}
		}
		if code == "" {
			if t, _ := root["type"].(string); t != "" {
				code = t
			}
		}
		if message == "" {
			if m, _ := root["message"].(string); m != "" {
				message = m
			}
		}
	}
	if code == "" && status != 0 {
		code = fmt.Sprintf("http_%d", status)
	}
	return class, code, truncateRunes(message, 300)
}

// retryAfterMS 解析 Retry-After（秒数或 HTTP 日期）。
func retryAfterMS(h http.Header) int64 {
	if h == nil {
		return 0
	}
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0
	}
	if secs, err := strconv.ParseInt(v, 10, 64); err == nil && secs >= 0 {
		return secs * 1000
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d.Milliseconds()
		}
	}
	return 0
}

// attachUpstreamError 写入结构化错误字段 + 事件 + 失败时的池快照。
// 调用方仍负责设置 rec.ErrMsg（保留人类可读的完整错误串）。
func attachUpstreamError(rec *CallRecord, status int, body []byte, headers http.Header, selected string) {
	rec.SchemaVersion = 2
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

func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
