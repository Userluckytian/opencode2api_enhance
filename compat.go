// 兼容垫片：v2.0.0 删除免费层后，仍被少量调用点引用的全局符号集中在此。
//
// 为什么需要垫片而不是改所有调用点：
//   - RouteMode / gatewayMode 只用于调用日志的「归属标注」字段，删掉字段会破坏
//     既有日志文件的可读性（历史记录仍有这两个键），但新记录用固定值即可；
//   - maxUpstreamRetries 是厂商级重试预算，与免费层无关，保留常量语义；
//   - writeFileAtomic 是通用文件工具。
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
)

// 厂商级重试预算（同模型上游尝试上限）。
const (
	maxUpstreamRetries = 3
	max401Retries      = 3
)

// routeMode 仅用于调用日志的 route_mode 字段。
// 免费层自研路由器已移除，恒为 "direct"（所有请求直接交给选定厂商）。
var routeMode atomic.Value

// gatewayMode 仅用于调用日志/统计的「是否为网关进程」标注。
// v2.0.0 起本进程只有供应商服务一种形态，恒为 true。
var gatewayMode = true

func init() {
	routeMode.Store("direct")
}

// traceIDCtxKey 追踪 ID 的 context 键。
type traceIDCtxKey struct{}

// getTraceID 从 ctx 取追踪 ID（缺失返回空串，由上层用 req_id 兜底）。
func getTraceID(ctx context.Context) string {
	if v := ctx.Value(traceIDCtxKey{}); v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// withTraceID 把追踪 ID 放进 ctx。
func withTraceID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, traceIDCtxKey{}, id)
}

// traceHeader 分布式追踪的入站/出站头名。
const traceHeaderHeader = "X-Trace-ID"

// resolveTraceID 决定本次请求的追踪 ID：入站头 > 环境变量 > 复用 req_id。
func resolveTraceID(inbound, reqID string) string {
	if v := strings.TrimSpace(inbound); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("OPCODE2API_TRACE_ID")); v != "" {
		return v
	}
	return reqID
}

// withLevelFilter 给 slog.Handler 加最小级别过滤（原 contextHandler 的简化替代：
// 免费层子系统热切换已移除，只剩全局级别）。
func withLevelFilter(h slog.Handler, min slog.Level) slog.Handler {
	return &levelFilterHandler{inner: h, min: min}
}

type levelFilterHandler struct {
	inner slog.Handler
	min   slog.Level
}

func (h *levelFilterHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return l >= h.min && h.inner.Enabled(ctx, l)
}
func (h *levelFilterHandler) Handle(ctx context.Context, r slog.Record) error {
	return h.inner.Handle(ctx, r)
}
func (h *levelFilterHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &levelFilterHandler{inner: h.inner.WithAttrs(attrs), min: h.min}
}
func (h *levelFilterHandler) WithGroup(name string) slog.Handler {
	return &levelFilterHandler{inner: h.inner.WithGroup(name), min: h.min}
}

// writeFileAtomic 临时文件 + Rename 原子落盘（目录自动创建）。
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), perm); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
