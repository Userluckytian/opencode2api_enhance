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

	"github.com/6Kmfi6HP/opencode2api/core/contract"
)

// ReadCallLog 的 max 语义：<=0 = 读取全部历史；>0 = 只取最近 max 条。
// 不再设置“每文件最多 5000 条”的硬窗口——日志页/过滤/聚合都按调用方请求读取。

// CallLogEvent 单条事件。
type CallLogEvent struct {
	Type   string `json:"type"`
	Node   string `json:"node,omitempty"`
	Detail string `json:"detail,omitempty"`
	At     string `json:"at,omitempty"`
	// 结构化错误元数据（数据分析视图/离线挖掘）。
	Status       int    `json:"status,omitempty"`
	ErrorClass   string `json:"error_class,omitempty"`
	ErrorCode    string `json:"error_code,omitempty"`
	RetryAfterMS int64  `json:"retry_after_ms,omitempty"`
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
	// Vendor 实际服务厂商 ID（写入侧 CallRecord.Vendor 镜像；旧记录为空）。
	Vendor string `json:"vendor,omitempty"`
	// Channel 阶段 O：OpenCode 引擎通道 —— "warp"（匿名出口）/ "key"（zen|go 密钥）。
	// 由 calllog_engine.go 从 stdout 的 channel/anonymous/tier 推导；旧记录为空。
	Channel string `json:"channel,omitempty"`
	// ServingPort actual ingress port.
	ServingPort string `json:"serving_port,omitempty"`
	// RouteVerdict 路由结论：proxied / direct_by_design / direct_config_missing / direct_unexpected；空 = 旧记录或无法判定。
	RouteVerdict string `json:"route_verdict,omitempty"`
	// KeyTail 实际使用的 key 末 4 位（自定义源多 key 场景；定位串对话）。
	// 注意：写入侧 CallRecord 已声明该 tag 但至今零赋值点，故当前恒为空；
	// 补齐写入侧接线是独立待办，见 docs/issue-log/2026-09-03.md。
	KeyTail string `json:"key_tail,omitempty"`
	// TraceID 分布式追踪 ID（阶段3），与 main 包 CallRecord.TraceID 镜像；omitempty 保持旧记录兼容。
	TraceID string `json:"trace_id,omitempty"`
	// ActualModel 上游实际返回的模型（Kilo 会静默换模型；阶段 L 新增）。
	// 旧记录无此字段，omitempty 保持兼容。
	ActualModel string `json:"actual_model,omitempty"`
	// SchemaVersion call_log 结构化字段版本（2 = 含错误分类/池快照）。
	SchemaVersion int `json:"schema_version,omitempty"`
	// UpstreamStatus 最后一次上游 HTTP 状态码（网络错误/未拿到响应为 0）。
	UpstreamStatus int `json:"upstream_status,omitempty"`
	// ErrorClass 错误大类：rate_limit / quota / auth / upstream / transport / protocol / other。
	ErrorClass string `json:"error_class,omitempty"`
	// ErrorCode 上游错误体中的 error.type / error.code。
	ErrorCode string `json:"error_code,omitempty"`
	// ErrorMessage 上游错误消息（截断）。
	ErrorMessage string `json:"error_message,omitempty"`
	// RetryAfterMS 上游 Retry-After 头的毫秒值（0 = 未提供）。
	RetryAfterMS int64 `json:"retry_after_ms,omitempty"`
	// Pool 失败时的池状态快照。
	Pool *contract.PoolSnapshot `json:"pool,omitempty"`
	// NodeState 本次失败节点的健康状态快照。
	NodeState *contract.NodeState `json:"node_state,omitempty"`
	// Decision 传输层本次选路决策快照。
	Decision *contract.Decision `json:"decision,omitempty"`
	// Attempts 本次请求的结构化尝试链。
	Attempts []contract.Attempt `json:"attempts,omitempty"`
	// PolicyVer 选路/冷却策略版本戳。
	PolicyVer string `json:"policy_ver,omitempty"`
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
		case "switch", "retry_same_node", "resume_retry", "ttft_timeout", "silence_timeout", "stream_interrupt",
			"stream_error", "connect_error", "upstream_error", "all_failed":
			return true
		}
	}
	return false
}

// ProviderServiceCallLogPath 返回供应商服务运行目录（cwd）下的调用日志路径。
func (m *Manager) ProviderServiceCallLogPath() string {
	return filepath.Join(m.paths.RuntimeDir, providerServiceDirBase, "call_log.jsonl")
}

// ReadCallLog 读取并合并调用日志（供应商服务 JSONL + Kilo 独立网关 JSONL +
// OpenCode 引擎 stdout 事件）。max<=0 = 全部历史；max>0 = 只取最近 max 条。
// 缺失/损坏 → 空数组。三个来源按时间升序合并，前端按 vendor 维度筛选。
//
// 阶段 L 起 Kilo 也写入统一日志（kilo_calls.jsonl），日志页/统计页因此能
// 覆盖 Kilo；引擎仍只通过 stdout 事件解析（不写 JSONL）。
func (m *Manager) ReadCallLog(max int) []CallLogRecord {
	allRequested := max <= 0
	tail := max
	if allRequested {
		tail = 0
	}
	// 阶段 L：三个来源（Kilo 独立网关 30080 走自己的 JSONL，见 kilo_calls_log.go）。
	//   - 供应商服务：写 JSONL（call_log.jsonl），直接按行解析；
	//   - Kilo 独立网关：写 JSONL（kilo_calls.jsonl），格式与 CallLogRecord 兼容；
	//   - OpenCode 引擎：不写 JSONL，只把 request_routed 事件打进 stdout，
	//     由 readEngineCallLog 解析（见 calllog_engine.go）。
	files := []struct{ path, source string }{
		{m.ProviderServiceCallLogPath(), "供应商服务"},
		{m.KiloCallsLogPath(), "Kilo 独立网关"},
	}
	// 并发读各文件尾部：每 goroutine 只写下标 i 自己的槽位（无共享写竞态）。
	parts := make([][]CallLogRecord, len(files)+1)
	var wg sync.WaitGroup
	sem := make(chan struct{}, callLogReadConcurrency)
	for i := range files {
		f := files[i]
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			parts[i] = readCallLogFileTail(f.path, f.source, tail)
		}()
	}
	// 引擎 stdout 单独走一条分支（解析方式不同）。
	wg.Add(1)
	sem <- struct{}{}
	go func() {
		defer wg.Done()
		defer func() { <-sem }()
		parts[len(files)] = readEngineCallLog(m.EngineStdoutLogPath(), tail)
	}()
	wg.Wait()

	var all []CallLogRecord
	for i := range parts {
		all = append(all, parts[i]...)
	}
	// 按时间合并升序（稳定排序：同时间戳保持 网关 → 实例 读取顺序）。
	sort.SliceStable(all, func(i, j int) bool {
		return callLogTime(all[i].TS).Before(callLogTime(all[j].TS))
	})
	if !allRequested && len(all) > max {
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

// readCallLogFileTail 读取单个日志文件的「有效记录」：.1 轮转旧段 + 主文件。
// .1 内容更旧在前、主文件在后；tail<=0 = 全部，tail>0 = 最后 tail 条。
func readCallLogFileTail(path, source string, tail int) []CallLogRecord {
	prev := readCallLogFilePart(path+".1", source, tail)
	main := readCallLogFilePart(path, source, tail)
	merged := append(prev, main...)
	if tail > 0 && len(merged) > tail {
		merged = merged[len(merged)-tail:]
	}
	return merged
}

// readCallLogFilePart 读取单个日志文件记录（保持旧→新文件顺序）。
// tail<=0 = 全部；tail>0 = 末尾最多 tail 条（大文件只读末尾窗口）。
func readCallLogFilePart(path, source string, tail int) []CallLogRecord {
	if tail <= 0 {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		return parseCallLogRecords(data, source, 0)
	}
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

// parseCallLogRecords 解析整文件字节为记录；tail>0 时截尾最多 tail 条，tail<=0 全部。
func parseCallLogRecords(data []byte, source string, tail int) []CallLogRecord {
	records := parseLogLines(splitCallLog(data, '\n'), source)
	if tail > 0 && len(records) > tail {
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

// ClearCallLog 清空全部调用日志（供应商服务 + Kilo 独立网关 + 引擎 stdout，
// 与 ReadCallLog 聚合范围一致 —— 阶段 L 把 Kilo 并入后必须一并清，否则
// 「清空日志」后 Kilo 记录会在下次轮询时复活）。
// 运行中进程（网关/实例）走 HTTP 清空——其进程持有日志文件 fd，管理器跨进程直删
// 会被 Windows「文件被占用」拦截；未运行进程直删文件（带占用重试兜底）。
// 失败按来源收集合并为单个 error（前端 toast 展示）。
func (m *Manager) ClearCallLog() error {
	var errs []string
	// 供应商服务（自定义源 + 插件）
	psPort := m.managerProviderPort()
	if probePort(psPort, statsResetProbeTimeout) {
		if err := clearLogHTTP(psPort, effectiveGatewayKey(m.loadConfig()), "供应商服务"); err != nil {
			errs = append(errs, err.Error())
		}
	} else if err := removeLogFileRetry(m.ProviderServiceCallLogPath()); err != nil {
		errs = append(errs, "供应商服务: "+err.Error())
	}
	// 阶段 L：Kilo 独立网关（由本进程内 kiloCallWriter 写，每次 open/close 不常驻 fd，直删可行）
	if err := removeLogFileRetry(m.KiloCallsLogPath()); err != nil {
		errs = append(errs, "Kilo 独立网关: "+err.Error())
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
// （Windows：文件刚被关闭/正被短暂读取时 Remove 会撞「文件被占用」）。
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
