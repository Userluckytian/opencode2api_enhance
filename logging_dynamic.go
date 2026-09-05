// 阶段 2（统一归因日志 / 可观测性）：在既有 slog 封装之上叠加两件事，且与 logging.go 最小耦合
// （后者只改两行：基准级别改为动态 LevelVar，并把 handler 包一层 contextHandler）。
//   1. 归因字段自动注入——每条日志带 role / trace_id（本阶段预留留空，阶段 3 填实值）/
//      node / tier / provider / port（从 context 取，随请求流转）。
//   2. 运行时日志分级热切换——基准级别 + 子系统级 debug 开关，均可不重启动态调整
//      （见 POST /api/admin/debug 与 -debug-subsystem 启动标志）。
// Same package (main) - do not change package clause manually.
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
)

// ============================ 动态日志级别 ============================

// logLevelVar 进程级基准日志级别；可在运行时经 setBaseLogLevel 调整（slog.LevelVar 并发安全）。
var logLevelVar = new(slog.LevelVar)

// setBaseLogLevel 运行时调整基准级别（整体热开关；子系统级见 setSubsystemDebug）。
func setBaseLogLevel(l slog.Level) { logLevelVar.Set(l) }

// ============================ 进程角色 ============================

// 四类角色：管理器 / 网关子进程 / 实例子进程 / 探针子进程。
const (
	RoleManager  = "manager"
	RoleGateway  = "gateway"
	RoleInstance = "instance"
	RoleProbe    = "probe"
)

var (
	processRoleMu sync.RWMutex
	processRole   string
)

// setProcessRole 记录本进程角色（启动早期调用一次）。
func setProcessRole(role string) {
	processRoleMu.Lock()
	processRole = role
	processRoleMu.Unlock()
}

// getProcessRole 读取本进程角色（日志注入用）。
func getProcessRole() string {
	processRoleMu.RLock()
	defer processRoleMu.RUnlock()
	return processRole
}

// resolveProcessRole 决定本进程角色：显式环境变量 OPCODE2API_ROLE 优先，
// 否则按既有启动标志派生（-gateway → gateway；-call-log → instance；默认 manager）。
//
// 为何用环境变量而非新增 CLI flag：管理器用同一份二进制拉起网关/实例/探针子进程；
// 若新增 -role flag，滚动升级期「新管理器 + 旧二进制」会因未知 flag 解析失败、
// 子进程根本起不来。环境变量对不认识它的旧二进制无害（直接忽略），前后兼容更稳。
func resolveProcessRole() string {
	if v := strings.TrimSpace(os.Getenv("OPCODE2API_ROLE")); v != "" {
		return v
	}
	switch {
	case gatewayMode:
		return RoleGateway
	case callLogFlag:
		return RoleInstance
	default:
		return RoleManager
	}
}

// ============================ 归因上下文字段 ============================

const (
	logNodeKey      contextKey = "log_node"
	logTierKey      contextKey = "log_tier"
	logProviderKey  contextKey = "log_provider"
	logPortKey      contextKey = "log_port"
	logSubsystemKey contextKey = "log_subsystem"
	traceIDKey      contextKey = "trace_id"
)

func withLogNode(ctx context.Context, v string) context.Context {
	return context.WithValue(ctx, logNodeKey, v)
}
func withLogTier(ctx context.Context, v string) context.Context {
	return context.WithValue(ctx, logTierKey, v)
}
func withLogProvider(ctx context.Context, v string) context.Context {
	return context.WithValue(ctx, logProviderKey, v)
}
func withLogPort(ctx context.Context, v string) context.Context {
	return context.WithValue(ctx, logPortKey, v)
}
func withSubsystem(ctx context.Context, v string) context.Context {
	return context.WithValue(ctx, logSubsystemKey, v)
}

// withTraceID 阶段 3 才会注入实值；阶段 2 仅预留通路（字段留空）。
func withTraceID(ctx context.Context, v string) context.Context {
	return context.WithValue(ctx, traceIDKey, v)
}

func ctxStr(ctx context.Context, key contextKey) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(key).(string); ok {
		return v
	}
	return ""
}

func getTraceID(ctx context.Context) string   { return ctxStr(ctx, traceIDKey) }

// ===== 阶段 3：分布式 Trace ID 传播 =====

// traceHeader 跨进程传递 trace_id 的 HTTP 头（入站复用、出站注入）。
const traceHeader = "X-Trace-ID"

// traceEnvVar 跨进程传递 trace_id 的环境变量：父进程 spawn 子进程时注入，
// 子进程无入站 X-Trace-ID 头时以此为进程级默认 trace（如启动期日志）。
const traceEnvVar = "OPENCODE2API_TRACE"

// resolveTraceID 计算本请求 trace_id：入站 X-Trace-ID 头优先复用（跨进程延续），
// 否则退回进程级 traceEnvVar，最后复用本请求 req_id。三者皆空时返回空串。
func resolveTraceID(headerVal, reqID string) string {
	if v := strings.TrimSpace(headerVal); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv(traceEnvVar)); v != "" {
		return v
	}
	return strings.TrimSpace(reqID)
}
func getSubsystem(ctx context.Context) string { return ctxStr(ctx, logSubsystemKey) }

// processServingPort 本进程监听端口（全局 port），供日志 port 字段兜底。
func processServingPort() string { return port }

// ============================ 子系统级 debug 热切换 ============================

var (
	subsystemDebugMu sync.RWMutex
	subsystemDebug   = map[string]bool{}
)

// setSubsystemDebug 开/关某子系统的 debug 级日志（运行时热生效，无需重启）。
// subsystem 为空视为整体基准级别开关：on → base=Debug，off → base=Info。
func setSubsystemDebug(subsystem string, on bool) {
	subsystem = strings.TrimSpace(subsystem)
	if subsystem == "" {
		if on {
			setBaseLogLevel(slog.LevelDebug)
		} else {
			setBaseLogLevel(slog.LevelInfo)
		}
		return
	}
	subsystemDebugMu.Lock()
	if on {
		subsystemDebug[subsystem] = true
	} else {
		delete(subsystemDebug, subsystem)
	}
	subsystemDebugMu.Unlock()
}

func subsystemDebugEnabled(subsystem string) bool {
	if subsystem == "" {
		return false
	}
	subsystemDebugMu.RLock()
	defer subsystemDebugMu.RUnlock()
	return subsystemDebug[subsystem]
}

func anySubsystemDebug() bool {
	subsystemDebugMu.RLock()
	defer subsystemDebugMu.RUnlock()
	return len(subsystemDebug) > 0
}

// recordLevelAllowed 纯函数（可单测，无全局状态）：给定基准级别、记录级别、
// 「该记录所属子系统是否开了 debug」，判定是否应输出。
func recordLevelAllowed(base, recordLevel slog.Level, subsystemOn bool) bool {
	if recordLevel >= base {
		return true
	}
	// 低于基准：仅当该记录所属子系统显式开了 debug 且记录达到 debug 级时放行。
	if subsystemOn && recordLevel >= slog.LevelDebug {
		return true
	}
	return false
}

// debugSubsystemFlag -debug-subsystem 启动标志值（逗号分隔子系统列表）。
var debugSubsystemFlag string

// applyDebugSubsystemFlag 把启动标志里的子系统逐个开启 debug（main 在 initLogger 后调用）。
func applyDebugSubsystemFlag() {
	for _, s := range strings.Split(debugSubsystemFlag, ",") {
		if s = strings.TrimSpace(s); s != "" {
			setSubsystemDebug(s, true)
		}
	}
}

// ============================ contextHandler ============================

// contextHandler 包装底层 slog.Handler：
//  1. 每条日志自动注入 role / trace_id / node / tier / provider / port；
//  2. 统一分级——底层 handler 恒放行（Level=Debug），实际分级在此处按
//     基准级别 + 子系统 debug 热切换决策。
type contextHandler struct {
	inner slog.Handler
}

// newContextHandler 包一层，并把基准级别初始化为 initial。
func newContextHandler(inner slog.Handler, initial slog.Level) slog.Handler {
	setBaseLogLevel(initial)
	return &contextHandler{inner: inner}
}

func (h *contextHandler) Enabled(_ context.Context, level slog.Level) bool {
	if level >= logLevelVar.Level() {
		return true
	}
	// 有任意子系统开着 debug 时，放行 debug 记录进入 Handle 再按子系统精确过滤。
	if level >= slog.LevelDebug && anySubsystemDebug() {
		return true
	}
	return false
}

func (h *contextHandler) Handle(ctx context.Context, r slog.Record) error {
	// 二次精确分级：Enabled 为放行 debug 整体开闸，这里按记录所属子系统收窄。
	sub := recordSubsystem(ctx, r)
	if !recordLevelAllowed(logLevelVar.Level(), r.Level, subsystemDebugEnabled(sub)) {
		return nil
	}
	// 注入归因字段。
	if role := getProcessRole(); role != "" {
		r.AddAttrs(slog.String("role", role))
	}
	// trace_id 阶段 2 预留字段（留空）；阶段 3 经 withTraceID 注入实值。
	r.AddAttrs(slog.String("trace_id", getTraceID(ctx)))
	if v := ctxStr(ctx, logNodeKey); v != "" {
		r.AddAttrs(slog.String("node", v))
	}
	if v := ctxStr(ctx, logTierKey); v != "" {
		r.AddAttrs(slog.String("tier", v))
	}
	if v := ctxStr(ctx, logProviderKey); v != "" {
		r.AddAttrs(slog.String("provider", v))
	}
	portVal := ctxStr(ctx, logPortKey)
	if portVal == "" {
		portVal = processServingPort()
	}
	if portVal != "" {
		r.AddAttrs(slog.String("port", portVal))
	}
	return h.inner.Handle(ctx, r)
}

func (h *contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &contextHandler{inner: h.inner.WithAttrs(attrs)}
}

func (h *contextHandler) WithGroup(name string) slog.Handler {
	return &contextHandler{inner: h.inner.WithGroup(name)}
}

// recordSubsystem 从 context 或记录属性（key = "subsystem"）取子系统名。
func recordSubsystem(ctx context.Context, r slog.Record) string {
	if s := getSubsystem(ctx); s != "" {
		return s
	}
	sub := ""
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "subsystem" {
			sub = a.Value.String()
			return false
		}
		return true
	})
	return sub
}

// ============================ 运行时热切换端点 ============================

// debugSwitchHandler POST /api/admin/debug?subsystem=<name>&enabled=<bool>
// 运行时热切换某子系统的 debug 级日志（无需重启）。main 以 requireAuth 挂载（复用管理鉴权）。
// subsystem 省略/为空 → 切换整体基准级别（Debug/Info）；enabled 省略默认 true。
func debugSwitchHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	subsystem := strings.TrimSpace(r.URL.Query().Get("subsystem"))
	enabledStr := strings.TrimSpace(r.URL.Query().Get("enabled"))
	on := enabledStr == "" || enabledStr == "true" || enabledStr == "1"
	setSubsystemDebug(subsystem, on)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":    "ok",
		"subsystem": subsystem,
		"enabled":   on,
	})
}
