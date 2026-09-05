package main

// 阶段 5：失败现场打包（postmortem）。
//
// 关键失败（Status != ok）时，自动在进程工作目录下的 postmortem/ 落一份脱敏现场包，
// 含 trace_id、路由决策（tier/route_mode/route_verdict/nodes/events）、生效配置快照，
// 无需复现即可事后排查。写入异步、失败不阻塞主请求、绝不落密钥明文。
// 纯标准库实现，不引入任何新依赖。

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	postmortemDir     = "postmortem"  // 相对进程 cwd（= 实例 runtime 目录）
	postmortemKeepMax = 50            // 保留最近 N 份，超出删最旧
	postmortemCfgFile = "config.json" // 生效配置文件（与 call_log.jsonl 同级）
)

var (
	postmortemMu sync.Mutex // 串行化落盘与清理，避免并发写竞态
	// postmortemBase 非空时作为落盘/读配置的根目录（测试注入隔离用）；生产留空 = 相对进程 cwd。
	postmortemBase string
)

func pmDir() string {
	if postmortemBase == "" {
		return postmortemDir
	}
	return filepath.Join(postmortemBase, postmortemDir)
}

func pmCfgPath() string {
	if postmortemBase == "" {
		return postmortemCfgFile
	}
	return filepath.Join(postmortemBase, postmortemCfgFile)
}

// PostmortemBundle 一次关键失败的现场快照（脱敏）。
type PostmortemBundle struct {
	TraceID      string      `json:"trace_id"`
	ReqID        string      `json:"req_id"`
	TS           string      `json:"ts"`
	WrittenAt    string      `json:"written_at"`
	Path         string      `json:"path"`
	Model        string      `json:"model"`
	Stream       bool        `json:"stream"`
	Status       string      `json:"status"`
	ErrMsg       string      `json:"err_msg,omitempty"`
	Tier         string      `json:"tier,omitempty"`
	ServingPort  string      `json:"serving_port,omitempty"`
	RouteMode    string      `json:"route_mode,omitempty"`
	RouteVerdict string      `json:"route_verdict,omitempty"`
	Nodes        []string    `json:"nodes,omitempty"`
	Events       []CallEvent `json:"events,omitempty"`

	SocksConfigured bool           `json:"socks_configured"`
	ConfigSnapshot  map[string]any `json:"config_snapshot,omitempty"`
	ConfigNote      string         `json:"config_note,omitempty"`
}

// maybeWritePostmortem 在失败调用收口处异步落盘：panic 与写错都不影响主流程。
func maybeWritePostmortem(rec CallRecord) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("postmortem panic recovered", "err", r)
			}
		}()
		if err := writePostmortem(rec); err != nil {
			slog.Error("postmortem write failed", "error", err, "trace_id", rec.TraceID)
		}
	}()
}

// buildPostmortemBundle 从 CallRecord + 运行时状态构造脱敏现场包。
func buildPostmortemBundle(rec CallRecord) PostmortemBundle {
	b := PostmortemBundle{
		TraceID:         rec.TraceID,
		ReqID:           rec.ReqID,
		TS:              rec.TS,
		WrittenAt:       time.Now().Format(time.RFC3339Nano),
		Path:            rec.Path,
		Model:           rec.Model,
		Stream:          rec.Stream,
		Status:          rec.Status,
		ErrMsg:          rec.ErrMsg,
		Tier:            rec.Tier,
		ServingPort:     rec.ServingPort,
		RouteMode:       rec.RouteMode,
		RouteVerdict:    rec.RouteVerdict,
		Nodes:           append([]string(nil), rec.Nodes...),
		Events:          append([]CallEvent(nil), rec.Events...),
		SocksConfigured: socksProxyConfigured(),
	}
	if b.TraceID == "" {
		b.TraceID = rec.ReqID // 向后兼容：无 trace 时回退 req_id
	}
	snap, note := loadConfigSnapshot(pmCfgPath())
	b.ConfigSnapshot = snap
	b.ConfigNote = note
	return b
}

// writePostmortem 同步落一份 bundle（原子写 + 保留上限清理）。供生产异步调用与测试直接调用。
func writePostmortem(rec CallRecord) error {
	b := buildPostmortemBundle(rec)
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal bundle: %w", err)
	}
	postmortemMu.Lock()
	defer postmortemMu.Unlock()
	dir := pmDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir postmortem: %w", err)
	}
	fname := fmt.Sprintf("%s-%d.json", sanitizeFileToken(b.TraceID), time.Now().UnixNano())
	tmp := filepath.Join(dir, fname+".tmp")
	final := filepath.Join(dir, fname)
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename bundle: %w", err)
	}
	prunePostmortem(postmortemKeepMax)
	return nil
}

// loadConfigSnapshot 读取生效配置文件并递归脱敏；读不到/解析失败时返回说明而非中断。
func loadConfigSnapshot(path string) (map[string]any, string) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, "config unavailable: " + err.Error()
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, "config parse error: " + err.Error()
	}
	redactConfigMap(m)
	return m, ""
}

// sensitiveKeySubstrings 命中即掩码的键名子串（小写匹配）。含 proxy/url 等，
// 堵住院代理 URL 内 user:pass 凭据经 socks5_proxies 结构外泄。
var sensitiveKeySubstrings = []string{
	"key", "token", "secret", "password", "passwd", "authorization",
	"credential", "cookie", "apikey", "api_key", "bearer",
	"proxy", "proxies", "url", "uri", "server",
}

func isSensitiveKey(k string) bool {
	lk := strings.ToLower(k)
	for _, s := range sensitiveKeySubstrings {
		if strings.Contains(lk, s) {
			return true
		}
	}
	return false
}

// redactConfigMap 递归掩码敏感键的值：字符串→"***"，数组/对象→"[redacted]"，
// 并对嵌套 map / 数组元素继续下钻。
func redactConfigMap(m map[string]any) {
	for k, v := range m {
		if isSensitiveKey(k) {
			switch v.(type) {
			case string:
				if s, _ := v.(string); s != "" {
					m[k] = "***"
				}
			default:
				m[k] = "[redacted]"
			}
			continue
		}
		switch vv := v.(type) {
		case map[string]any:
			redactConfigMap(vv)
		case []any:
			for _, e := range vv {
				if em, ok := e.(map[string]any); ok {
					redactConfigMap(em)
				}
			}
		}
	}
}

// prunePostmortem 仅保留最近 keep 份 bundle（按文件名字典序，含 UnixNano 故≈时间序）。
func prunePostmortem(keep int) {
	if keep < 1 {
		keep = 1
	}
	dir := pmDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			files = append(files, e.Name())
		}
	}
	if len(files) <= keep {
		return
	}
	sort.Strings(files)
	for _, name := range files[:len(files)-keep] {
		_ = os.Remove(filepath.Join(dir, name))
	}
}

// sanitizeFileToken 只保留文件名安全字符，限长，空则回退占位。
func sanitizeFileToken(s string) string {
	if s == "" {
		return "notrace"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		}
	}
	out := b.String()
	if out == "" {
		return "notrace"
	}
	if len(out) > 32 {
		out = out[:32]
	}
	return out
}
