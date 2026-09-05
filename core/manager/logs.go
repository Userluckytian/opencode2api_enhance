// 阶段 2（统一归因日志 / 可观测性）：/api/logs 聚合读取各进程 slog 输出。
//
// 读取 runtime/_unified-gateway/logs/ 与各实例 runtime/<name>/logs/ 下的
// opencode2api.{out,err}.log，按 process/role/since 过滤，返回统一结构。
// 对大量小文件节流：单文件只读末尾窗口 + 单文件最大回溯行数 + 总行数上限 + 最大文件数。
package manager

import (
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	logsDefaultLimit = 1000
	logsMaxLimit     = 10000
	logsPerFileTail  = 4000    // 单文件最多回溯行数（节流）
	logsTailWindow   = 4 << 20 // 大文件只读末尾 4MB
	logsMaxFiles     = 400     // 最多聚合文件数（防目录爆炸）

	gatewayLogSource = "_unified-gateway"
)

// LogEntry 单条聚合日志行（统一结构）。
type LogEntry struct {
	Source string `json:"source"`         // 来源：_unified-gateway 或实例名
	Role   string `json:"role"`           // gateway/instance（优先行内 role= 解析，缺失时按来源推断）
	File   string `json:"file"`           // 文件基名（opencode2api.out.log 等）
	Time   string `json:"time,omitempty"` // 行内 time= 原值
	Level  string `json:"level,omitempty"`
	Line   string `json:"line"` // 原始日志行
}

// LogsResult /api/logs 响应。
type LogsResult struct {
	Entries   []LogEntry `json:"entries"`
	Count     int        `json:"count"`
	Truncated bool       `json:"truncated"` // 命中上限被截断
}

// logSource 一个日志来源（目录 + 名称 + 推断角色）。
type logSource struct {
	name string
	role string
	dir  string
}

// LogsHandler GET /api/logs?process=&role=&since=&limit= （main 以 requireAuth 挂载）。
func (m *Manager) LogsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethod(w, r, http.MethodGet) {
			return
		}
		q := r.URL.Query()
		processFilter := strings.TrimSpace(q.Get("process"))
		roleFilter := strings.TrimSpace(q.Get("role"))
		since := parseSince(strings.TrimSpace(q.Get("since")))
		limit := logsDefaultLimit
		if s := q.Get("limit"); s != "" {
			if n := parsePositiveInt(s); n > 0 {
				limit = n
			}
		}
		if limit > logsMaxLimit {
			limit = logsMaxLimit
		}
		writeJSON(w, m.ReadProcessLogs(processFilter, roleFilter, since, limit))
	}
}

// ReadProcessLogs 聚合读取各进程 slog 输出并过滤。
//   - processFilter：限定来源（实例名；"gateway"/"_unified-gateway" 互为同义）；空 = 全部。
//   - roleFilter：按来源角色（gateway/instance）过滤；空 = 全部。
//   - since：仅保留 time= 不早于 since 的行（零值 = 不限）。
//   - limit：总行数上限（保留最新）。
func (m *Manager) ReadProcessLogs(processFilter, roleFilter string, since time.Time, limit int) LogsResult {
	if limit <= 0 {
		limit = logsDefaultLimit
	}
	sources := m.collectLogSources()

	entries := make([]LogEntry, 0, 256)
	truncated := false
	filesRead := 0

	for _, src := range sources {
		if !matchProcess(processFilter, src.name) {
			continue
		}
		if roleFilter != "" && !strings.EqualFold(roleFilter, src.role) {
			continue
		}
		for _, base := range []string{"opencode2api.out.log", "opencode2api.err.log"} {
			if filesRead >= logsMaxFiles {
				truncated = true
				break
			}
			path := filepath.Join(src.dir, base)
			lines, ok := readLogTailWindow(path, int64(logsTailWindow))
			if !ok {
				continue // 文件不存在/不可读——跳过
			}
			filesRead++
			if len(lines) > logsPerFileTail {
				lines = lines[len(lines)-logsPerFileTail:]
				truncated = true
			}
			for _, raw := range lines {
				line := strings.TrimRight(string(raw), "\r")
				if strings.TrimSpace(line) == "" {
					continue
				}
				if !since.IsZero() {
					if t, ok := lineTime(line); ok && t.Before(since) {
						continue
					}
				}
				role := fieldValue(line, "role=")
				if role == "" {
					role = src.role
				}
				if roleFilter != "" && !strings.EqualFold(roleFilter, role) {
					continue
				}
				entries = append(entries, LogEntry{
					Source: src.name,
					Role:   role,
					File:   base,
					Time:   fieldValue(line, "time="),
					Level:  fieldValue(line, "level="),
					Line:   line,
				})
			}
		}
	}

	// 按时间升序（无法解析时间的行排在前，保持相对稳定）。
	sort.SliceStable(entries, func(i, j int) bool {
		ti, oki := lineTime(entries[i].Line)
		tj, okj := lineTime(entries[j].Line)
		if oki && okj {
			return ti.Before(tj)
		}
		if oki != okj {
			return !oki // 不可解析的排前
		}
		return false
	})

	if len(entries) > limit {
		entries = entries[len(entries)-limit:]
		truncated = true
	}

	return LogsResult{Entries: entries, Count: len(entries), Truncated: truncated}
}

// collectLogSources 枚举网关 + 全部实例（含已停止，保留历史日志）的日志来源。
func (m *Manager) collectLogSources() []logSource {
	sources := []logSource{{
		name: gatewayLogSource,
		role: "gateway",
		dir:  filepath.Join(m.paths.RuntimeDir, gatewayLogSource, "logs"),
	}}
	for _, inst := range m.ListInstances() {
		sources = append(sources, logSource{
			name: inst.Name,
			role: "instance",
			dir:  filepath.Join(m.paths.RuntimeDirOf(inst.Name), "logs"),
		})
	}
	return sources
}

// matchProcess 过滤来源名；空过滤器 = 全部命中；"gateway" 与 "_unified-gateway" 互为同义。
func matchProcess(filter, name string) bool {
	if filter == "" {
		return true
	}
	if strings.EqualFold(filter, name) {
		return true
	}
	if name == gatewayLogSource && strings.EqualFold(filter, "gateway") {
		return true
	}
	return false
}

// parseSince 解析 since：先试 time.Duration（如 "10m"，相对当前），再试多种时间布局；均失败返回零值。
func parseSince(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if d, err := time.ParseDuration(s); err == nil {
		return time.Now().Add(-d)
	}
	for _, layout := range []string{
		"2006-01-02T15:04:05.000Z07:00",
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05",
		"2006-01-02",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// lineTime 从 slog 文本行解析 time= 值（initLogger 固定为 "2006-01-02T15:04:05.000Z07:00"）。
func lineTime(line string) (time.Time, bool) {
	v := fieldValue(line, "time=")
	if v == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{"2006-01-02T15:04:05.000Z07:00", time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, v); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// fieldValue 从 slog 文本行提取 key（形如 "role="）对应的值。
// key 必须位于行首或紧跟一个空格之后（避免 "runtime=" 误命中 "time="）；
// 值若以引号开头则取至下一引号，否则取至下一空格。
func fieldValue(line, key string) string {
	i := 0
	for {
		j := strings.Index(line[i:], key)
		if j < 0 {
			return ""
		}
		pos := i + j
		if pos == 0 || line[pos-1] == ' ' {
			return extractFieldVal(line[pos+len(key):])
		}
		i = pos + len(key)
	}
}

func extractFieldVal(rest string) string {
	if rest == "" {
		return ""
	}
	if rest[0] == '"' {
		if end := strings.IndexByte(rest[1:], '"'); end >= 0 {
			return rest[1 : 1+end]
		}
		return rest[1:]
	}
	if end := strings.IndexByte(rest, ' '); end >= 0 {
		return rest[:end]
	}
	return rest
}
