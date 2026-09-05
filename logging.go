// Part of the P1 (core split) refactor: code moved out of main.go.
// Same package (main) - do not change package clause manually.
package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type contextKey string

const reqIDKey contextKey = "request_id"

var (
	logLevel string
	logFile  string
)

func initLogger() *slog.Logger {
	var w io.Writer = os.Stdout
	if logFile != "" {
		f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			slog.Warn("cannot open log file, falling back to stdout", "path", logFile, "error", err)
		} else {
			w = f
		}
	}

	var lvl slog.Level
	switch strings.ToLower(logLevel) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}

	handler := slog.NewTextHandler(w, &slog.HandlerOptions{
		Level: slog.LevelDebug, // 分级下沉到 contextHandler（支持子系统 debug 热切换）
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				return slog.String("time", a.Value.Time().Format("2006-01-02T15:04:05.000Z07:00"))
			}
			if a.Key == slog.SourceKey {
				return slog.Attr{}
			}
			return a
		},
	})

	logger := slog.New(newContextHandler(handler, lvl))
	slog.SetDefault(logger)
	return logger
}

// loggingMiddleware 为每个请求注入 request_id 并记录请求信息
func loggingMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		reqID := randomString(12)
		ctx := context.WithValue(r.Context(), reqIDKey, reqID)
		// 阶段 3：种入 trace_id——入站 X-Trace-ID 复用、否则 env、否则复用 req_id；
		// 回写响应头，便于客户端与下游进程串联同一条链路。
		traceID := resolveTraceID(r.Header.Get(traceHeader), reqID)
		ctx = withTraceID(ctx, traceID)
		w.Header().Set(traceHeader, traceID)
		r = r.WithContext(ctx)

		slog.DebugContext(ctx, "request started",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.String("remote", r.RemoteAddr),
		)

		next(w, r)
	}
}

func getReqID(ctx context.Context) string {
	if id, ok := ctx.Value(reqIDKey).(string); ok {
		return id
	}
	return ""
}

// ======================== 配置 ========================

var (
	port                 string
	configPath           = "config.json"
	modelAlias           = map[string]string{}
	reasoningEffortMap   = map[string]string{}
	forceDisableThinking bool
	showNodePrefix       bool
	debugMode            bool
	gatewayMode          bool
	callLogFlag          bool // -call-log：实例子进程显式启用调用日志写盘
	configMu             sync.RWMutex
	storedResponses      = map[string]storedResponseEntry{}
	storedResponsesMu    sync.RWMutex
	storedSeq            uint64    // storedResponses 插入序号（L5 淘汰最旧用）
	storedLastPurge      time.Time // 上次批量清理时间（L5 节流）
	// 厂商注册表与路由（配置驱动；applyConfig 写入）
	providersCfg []ProviderCfg
	routingCfg   RoutingCfg
)

// ======================== 管理面板认证 ========================

var (
	adminPassword string
	sessions      = map[string]sessionEntry{}
	sessionsMu    sync.Mutex
)
