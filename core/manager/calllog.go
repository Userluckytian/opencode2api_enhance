// 调用日志读取（Rust call_log.rs 移植）：解析 runtime/_unified-gateway/call_log.jsonl
// + 各 Running 独享实例 cwd 下的 call_log.jsonl（S4 聚合），保持 Go 网关写盘的 snake_case 字段名。
package manager

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// callLogAggregateTail 聚合读取时单个日志文件最多取末尾多少条（限制 IO）。
const callLogAggregateTail = 5000

// CallLogEvent 单条事件。
type CallLogEvent struct {
	Type   string `json:"type"`
	Node   string `json:"node,omitempty"`
	Detail string `json:"detail,omitempty"`
	At     string `json:"at,omitempty"`
}

// CallLogRecord 单条调用记录（字段与 main 包 CallRecord 一致）。
type CallLogRecord struct {
	ReqID            string         `json:"req_id"`
	TS               string         `json:"ts"`
	Path             string         `json:"path,omitempty"`
	Model            string         `json:"model,omitempty"`
	Stream           bool           `json:"stream,omitempty"`
	RouteMode        string         `json:"route_mode,omitempty"`
	Nodes            []string       `json:"nodes,omitempty"`
	Events           []CallLogEvent `json:"events,omitempty"`
	Status           string         `json:"status,omitempty"`
	PromptTokens     int64          `json:"prompt_tokens,omitempty"`
	CompletionTokens int64          `json:"completion_tokens,omitempty"`
	DurationMS       int64          `json:"duration_ms,omitempty"`
	ErrMsg           string         `json:"err_msg,omitempty"`
	// Source 来源标注："" = 统一网关；否则为实例名（独享实例 call_log.jsonl 聚合）。
	Source string `json:"source,omitempty"`
	// Tier request layer: free/paid (same as main package CallRecord).
	Tier string `json:"tier,omitempty"`
	// ViaProxy custom source uses node pool proxy.
	ViaProxy bool `json:"via_proxy,omitempty"`
	// ServingPort actual ingress port.
	ServingPort string `json:"serving_port,omitempty"`
}

// StatusText 状态前缀（前端着色用）。
func (r CallLogRecord) StatusText() string {
	if r.Status == "ok" {
		return "【成功】"
	}
	return "【失败】"
}

// HasIssue 是否有切换/异常事件（前端"只看失败"过滤）。
func (r CallLogRecord) HasIssue() bool {
	if r.Status != "ok" {
		return true
	}
	for _, e := range r.Events {
		switch e.Type {
		case "switch", "ttft_timeout", "silence_timeout", "stream_interrupt",
			"stream_error", "connect_error", "upstream_error", "all_failed":
			return true
		}
	}
	return false
}

// CallLogPath 返回统一网关日志路径。
func (m *Manager) CallLogPath() string {
	return filepath.Join(m.paths.RuntimeDir, "_unified-gateway", "call_log.jsonl")
}

// InstanceCallLogPath 返回某实例运行目录（cwd）下的调用日志路径。
func (m *Manager) InstanceCallLogPath(name string) string {
	return filepath.Join(m.paths.RuntimeDirOf(name), "call_log.jsonl")
}

// ReadCallLog 聚合读取最新 max 条：统一网关日志 + 各 Running 实例（含独享）
// call_log.jsonl，按实例名标注 Source、按时间合并升序（旧→新，与既有 API 语义一致）。
// 每个文件只取末尾 callLogAggregateTail 条限制 IO；缺失/损坏 → 跳过。
// 多文件并发读取（semaphore ≤8），结果按下标收集后按「网关 → 实例顺序」拼接，
// 保证同时间戳记录的稳定排序 tie-break 语义不因并发完成顺序而乱。
func (m *Manager) ReadCallLog(max int) []CallLogRecord {
	if max <= 0 {
		max = 1
	}
	if max > 50000 {
		max = 50000
	}
	// 待读文件列表：下标 0 = 统一网关；其后按 ListInstances 顺序的 Running 实例。
	type logFile struct {
		path   string // 主文件
		source string // 来源标注（"" = 统一网关）
	}
	var files []logFile
	files = append(files, logFile{path: m.CallLogPath()})
	for _, inst := range m.ListInstances() {
		if inst.Status.State != "Running" {
			continue
		}
		files = append(files, logFile{path: m.InstanceCallLogPath(inst.Name), source: inst.Name})
	}
	// 并发读各文件尾部：每 goroutine 只写下标 i 自己的槽位（无共享写竞态）。
	parts := make([][]CallLogRecord, len(files))
	var wg sync.WaitGroup
	sem := make(chan struct{}, callLogReadConcurrency)
	for i := range files {
		f := files[i]
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			parts[i] = readCallLogFileTail(f.path, f.source, callLogAggregateTail)
		}()
	}
	wg.Wait()

	var all []CallLogRecord
	for i := range parts {
		all = append(all, parts[i]...)
	}
	// 按时间合并升序（稳定排序：同时间戳保持 网关 → 实例 读取顺序）。
	sort.SliceStable(all, func(i, j int) bool {
		return callLogTime(all[i].TS).Before(callLogTime(all[j].TS))
	})
	if len(all) > max {
		all = all[len(all)-max:]
	}
	// 无日志时返回空数组而非 nil（避免 JSON 序列化为 null，前端展开报 TypeError）
	if all == nil {
		all = []CallLogRecord{}
	}
	return all
}

// callLogReadConcurrency 日志文件并发读取上限（聚合时控制文件句柄数）。
const callLogReadConcurrency = 8

// callLogTailWindow 大文件尾部读取窗口（字节）：文件超过该值不再整文件载入内存，
// 只读末尾固定窗口。小文件（≤ 窗口）仍整读。测试可注入小值。
var callLogTailWindow atomic.Int64

// callLogWindowBytes 读取当前尾部窗口大小（延迟初始化 8MB）。
func callLogWindowBytes() int64 {
	if v := callLogTailWindow.Load(); v > 0 {
		return v
	}
	callLogTailWindow.Store(8 << 20)
	return 8 << 20
}

// readCallLogFileTail 读取单个日志文件的「有效尾部」：.1 轮转旧段 + 主文件。
// .1 内容更旧在前、主文件在后，拼接后取最后 tail 条；缺失 .1 不报错（现状行为）。
// 返回语义：按文件顺序、最后为最新、最多 tail 条；文件缺失 → nil。
func readCallLogFileTail(path, source string, tail int) []CallLogRecord {
	prev := readCallLogFilePart(path+".1", source, tail)
	main := readCallLogFilePart(path, source, tail)
	merged := append(prev, main...)
	if len(merged) > tail {
		merged = merged[len(merged)-tail:]
	}
	return merged
}

// readCallLogFilePart 读取单个日志文件尾部最多 tail 条记录（保持旧→新文件顺序）。
// 大文件只读末尾 callLogWindowBytes 字节窗口；窗口内记录不足 tail（行超长）时
// 回退整读兜底（防丢数据）。
func readCallLogFilePart(path, source string, tail int) []CallLogRecord {
	st, err := os.Stat(path)
	if err != nil {
		return nil
	}
	if st.Size() <= callLogWindowBytes() {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		return parseCallLogRecords(data, source, tail)
	}
	data, ok := readLogTailWindow(path, callLogWindowBytes())
	if !ok {
		return nil
	}
	records := parseLogLines(data, source)
	if len(records) < tail {
		// 窗口内不足 tail 条：行超长导致窗口未覆盖足够记录，回退整读防丢数据。
		full, err := os.ReadFile(path)
		if err != nil {
			return records
		}
		return parseCallLogRecords(full, source, tail)
	}
	if len(records) > tail {
		records = records[len(records)-tail:]
	}
	return records
}

// readLogTailWindow 读取文件末尾 n 字节，返回其中的完整行（旧→新）：
// 窗口前置读 1 字节判定起点——起点不在行首（前一字节非换行）时首段是残行，
// 直接截掉；起点恰在行首则保留整行。窗口覆盖整文件（off=0）时不做截断。
func readLogTailWindow(path string, n int64) ([][]byte, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, false
	}
	// 窗口提前 1 字节：若该字节是换行说明窗口起点恰在行首，否则首段必为残行。
	off := st.Size() - n - 1
	if off < 0 {
		off = 0
	}
	buf := make([]byte, st.Size()-off)
	if _, err := f.ReadAt(buf, off); err != nil {
		return nil, false
	}
	lines := splitCallLog(buf, '\n')
	if off > 0 && buf[0] != '\n' {
		lines = lines[1:]
	}
	return lines, true
}

// parseCallLogRecords 解析整文件字节为记录，截尾最多 tail 条（保持文件顺序）。
func parseCallLogRecords(data []byte, source string, tail int) []CallLogRecord {
	records := parseLogLines(splitCallLog(data, '\n'), source)
	if len(records) > tail {
		records = records[len(records)-tail:]
	}
	return records
}

// parseLogLines 解析行切片为记录（保持顺序，丢弃空白/损坏行）。
func parseLogLines(lines [][]byte, source string) []CallLogRecord {
	records := make([]CallLogRecord, 0, 64)
	for _, line := range lines {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var rec CallLogRecord
		if json.Unmarshal(line, &rec) == nil {
			rec.Source = source
			records = append(records, rec)
		}
	}
	return records
}

// callLogTime 解析记录时间戳（RFC3339）；解析失败返回零值。
func callLogTime(ts string) time.Time {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return time.Time{}
	}
	return t
}

// ClearCallLog 清空全部调用日志（统一网关 + 各实例，与 ReadCallLog 聚合范围一致）。
// 运行中进程（网关/实例）走 HTTP 清空——其进程持有日志文件 fd，管理器跨进程直删
// 会被 Windows「文件被占用」拦截；未运行进程直删文件（带占用重试兜底）。
// 失败按来源收集合并为单个 error（前端 toast 展示）。
func (m *Manager) ClearCallLog() error {
	var errs []string
	defaultPW := m.effectiveDefaultPassword()

	// 统一网关
	gwPort := m.managerGatewayPort()
	if probePort(gwPort, statsResetProbeTimeout) {
		if err := clearLogHTTP(gwPort, effectiveGatewayKey(m.loadConfig()), "统一网关"); err != nil {
			errs = append(errs, err.Error())
		}
	} else if err := removeLogFileRetry(m.CallLogPath()); err != nil {
		errs = append(errs, "统一网关: "+err.Error())
	}

	// 各实例（含独享实例 cwd 下的 call_log.jsonl）
	for _, inst := range m.ListInstances() {
		path := m.InstanceCallLogPath(inst.Name)
		if _, err := os.Stat(path); err != nil {
			continue // 无日志文件：无需处理
		}
		if probePort(inst.Port, statsResetProbeTimeout) || inst.Status.State == "Running" {
			pw := inst.Password
			if pw == "" {
				pw = defaultPW
			}
			if err := clearLogHTTP(inst.Port, pw, inst.Name); err != nil {
				errs = append(errs, err.Error())
			}
		} else if err := removeLogFileRetry(path); err != nil {
			errs = append(errs, inst.Name+": "+err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("清空日志失败: %s", strings.Join(errs, "；"))
	}
	return nil
}

// clearLogHTTP 对运行中进程发 HTTP DELETE 清空其调用日志。
func clearLogHTTP(port uint16, auth, label string) error {
	status, _, err := httpDeleteJSON(port, "/api/clear-call-log", 6*time.Second, auth)
	if err == nil && status >= 200 && status < 300 {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%s: %s", label, err.Error())
	}
	return fmt.Errorf("%s: HTTP %d", label, status)
}

// removeLogFileRetry 删除日志文件（含轮转旧段 .1）并对瞬时跨进程占用重试
//（Windows：文件刚被关闭/正被短暂读取时 Remove 会撞「文件被占用」）。
func removeLogFileRetry(path string) error {
	for _, p := range []string{path, path + ".1"} {
		var err error
		for i := 0; i < statsWriteRetryAttempts; i++ {
			err = os.Remove(p)
			if err == nil || os.IsNotExist(err) {
				err = nil
				break
			}
			if i+1 < statsWriteRetryAttempts {
				time.Sleep(statsWriteRetryDelay)
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// splitCallLog 按字节切行。
func splitCallLog(data []byte, sep byte) [][]byte {
	var out [][]byte
	start := 0
	for i := 0; i < len(data); i++ {
		if data[i] == sep {
			out = append(out, data[start:i])
			start = i + 1
		}
	}
	if start < len(data) {
		out = append(out, data[start:])
	}
	return out
}
