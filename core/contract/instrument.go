package contract

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Attempt 是一次上游尝试的结构化记录（供错误分析视图与离线策略挖掘）。
type Attempt struct {
	Node         string `json:"node"`
	Status       int    `json:"status"`
	ErrorClass   string `json:"error_class,omitempty"`
	ErrorCode    string `json:"error_code,omitempty"`
	DurationMS   int64  `json:"duration_ms"`
	RetryAfterMS int64  `json:"retry_after_ms,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

// Decision 是传输层一次选路的决策快照。
type Decision struct {
	Node           string `json:"node"`
	Reason         string `json:"reason"`
	CandidateCount int    `json:"candidate_count"`
	AvailableCount int    `json:"available_count"`
	Cooling        int    `json:"cooling"`
	BadQuota       int    `json:"bad_quota"`
	BadAuth        int    `json:"bad_auth"`
	BreakerOpen    int    `json:"breaker_open"`
}

// Picker 是可选传输接口：返回客户端 + 选路决策。未实现时厂商退化为 Client()。
type Picker interface {
	Pick(tier Tier, streaming bool) (*http.Client, Decision)
}

// MarkRetry 是可选传输接口：带上游 Retry-After 的结果回填。未实现时走 Mark()。
type MarkRetry interface {
	MarkWithRetry(addr string, status int, reqErr error, retryAfterMS int64)
}

// FingerprintMarker 是可选传输接口：上游免费层客户端指纹拒绝
// （429 FreeUsageLimitError）专用回填。它不是节点故障，不应进入 bad_quota；
// 实现方应只做短冷却，让节点在指纹问题消失后立即恢复。
type FingerprintMarker interface {
	MarkFingerprint(addr string, retryAfterMS int64)
}

// PoolCounter 是可选传输接口：返回当前池健康计数，供竞速等
// 不经 Pick 的选路路径回填决策快照（available/cooling/bad*/breakerOpen）。
type PoolCounter interface {
	PoolCounts() (available, cooling, badQuota, badAuth, breakerOpen int)
}

// PickTransport 选路统一入口：transport 实现 Picker 时取结构化决策，
// 否则回退 Client() 并按出口地址推断 direct/healthy 决策。
func PickTransport(tr Transport, tier Tier, streaming bool) (*http.Client, string, Decision) {
	if p, ok := tr.(Picker); ok {
		c, d := p.Pick(tier, streaming)
		return c, d.Node, d
	}
	c, addr := tr.Client(tier, streaming)
	reason := "direct"
	if addr != "" {
		reason = "healthy"
	}
	return c, addr, Decision{Node: addr, Reason: reason}
}

// MarkResult 结果回填统一入口：transport 实现 MarkRetry 时带上 Retry-After，
// 否则回退 Mark()（忽略 retryAfterMS）。
func MarkResult(tr Transport, addr string, status int, reqErr error, retryAfterMS int64) {
	if mr, ok := tr.(MarkRetry); ok {
		mr.MarkWithRetry(addr, status, reqErr, retryAfterMS)
		return
	}
	tr.Mark(addr, status, reqErr)
}

// ---- 埋点回流通道（vendor → main，经 ctx 传递，不改各层函数签名） ----

type sinkKey struct{}

// InstrumentSink 是一次上游调用埋点的收集器：厂商在 Chat/ChatStream 返回前
// 写入决策快照与尝试链；main 侧 handler 在 recordCall 前读出写入 CallRecord。
type InstrumentSink struct {
	mu       sync.Mutex
	decision *Decision
	attempts []Attempt
}

// NewInstrumentSink 创建空收集器。
func NewInstrumentSink() *InstrumentSink { return &InstrumentSink{} }

// WithInstrumentSink 把收集器挂到 ctx 上。
func WithInstrumentSink(ctx context.Context, s *InstrumentSink) context.Context {
	return context.WithValue(ctx, sinkKey{}, s)
}

// SinkFromContext 取出 ctx 上的收集器（无则 nil）。
func SinkFromContext(ctx context.Context) *InstrumentSink {
	if ctx == nil {
		return nil
	}
	s, _ := ctx.Value(sinkKey{}).(*InstrumentSink)
	return s
}

// Record 写入本次决策与尝试链：决策覆盖（以最终一次选路为准），
// 尝试链累加（auto 降级链 / 流中断续写的多次上游调用拼成完整链）。
func (s *InstrumentSink) Record(d Decision, atts []Attempt) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dd := d
	s.decision = &dd
	s.attempts = append(s.attempts, atts...)
}

// Snapshot 读出当前决策与尝试链（副本；未写入返回 nil, nil）。
func (s *InstrumentSink) Snapshot() (*Decision, []Attempt) {
	if s == nil {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var atts []Attempt
	if s.attempts != nil {
		atts = append([]Attempt(nil), s.attempts...)
	}
	return s.decision, atts
}

// ClassifyUpstreamError 把上游状态码 + 错误体归为稳定错误大类/错误码。
func ClassifyUpstreamError(status int, body []byte) (class, code, message string) {
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
	if len(body) > 0 {
		var root map[string]any
		if json.Unmarshal(body, &root) == nil {
			if errObj, _ := root["error"].(map[string]any); errObj != nil {
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
	}
	if code == "" && status != 0 {
		code = fmt.Sprintf("http_%d", status)
	}
	// 网关本地合成的传输层失败（502 + type=upstream_transport_error）归为 transport，
	// 避免和真实上游 5xx 混在一起统计。
	if code == "upstream_transport_error" {
		class = "transport"
	}
	return class, code, truncateRunes(message, 300)
}

// RetryAfterMS 解析 Retry-After（秒或 HTTP 日期），无效返回 0。
func RetryAfterMS(h http.Header) int64 {
	if h == nil {
		return 0
	}
	v := h.Get("Retry-After")
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

// TruncateRunes 按 rune 截断（导出，供厂商记录 attempt 错误串防刷屏）。
func TruncateRunes(s string, max int) string { return truncateRunes(s, max) }
