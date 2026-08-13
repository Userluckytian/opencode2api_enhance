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
		Level: lvl,
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

	logger := slog.New(handler)
	slog.SetDefault(logger)
	return logger
}

// loggingMiddleware 为每个请求注入 request_id 并记录请求信息
func loggingMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		reqID := randomString(12)
		ctx := context.WithValue(r.Context(), reqIDKey, reqID)
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
	configMu             sync.RWMutex
	storedResponses      = map[string]StoredResponseState{}
	storedResponsesMu    sync.RWMutex
	// 厂商注册表与路由（配置驱动；applyConfig 写入）
	providersCfg []ProviderCfg
	routingCfg   RoutingCfg
)

// ======================== 管理面板认证 ========================

var (
	adminPassword string
	sessions      = map[string]struct{}{}
	sessionsMu    sync.Mutex
	// maxSessions 管理面板会话容量上限；超限整体重置，防止无界增长。
	maxSessions = 10000
)
