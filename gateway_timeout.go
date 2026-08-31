package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ======================== 流内超时配置（区间随机） ========================
// 从 model-gateway 分析结论：SSE 流一旦建立，现有代码无 TTFT/静默超时，
// 上游挂起时 reader.ReadString 无限阻塞。本模块为每个请求在 [min,max]
// 区间内随机取一个超时值，避免固定超时被上游识别为定时扫描/竞速探测，
// 同时 min 下限保护防止过密重试。

// 区间默认值（生产）
const (
	DefaultTTFTMin    = 10 * time.Second
	DefaultTTFTMax    = 10 * time.Second
	DefaultSilenceMin = 5 * time.Second
	DefaultSilenceMax = 5 * time.Second
	DefaultProbeMin   = 2
	DefaultProbeMax   = 3
	// DefaultCallLogMax 日志保留上限（条），前端设置页可改
	DefaultCallLogMax = 5000
)

type TimeoutConfig struct {
	TTFTRange    [2]time.Duration
	SilenceRange [2]time.Duration
	ProbeRange   [2]int
}

func DefaultTimeoutConfig() TimeoutConfig {
	return TimeoutConfig{
		TTFTRange:    [2]time.Duration{DefaultTTFTMin, DefaultTTFTMax},
		SilenceRange: [2]time.Duration{DefaultSilenceMin, DefaultSilenceMax},
		ProbeRange:   [2]int{DefaultProbeMin, DefaultProbeMax},
	}
}

// randDuration 返回 [min,max] 均匀随机值（含端点）
func randDuration(min, max time.Duration) time.Duration {
	if max <= min {
		return min
	}
	return min + time.Duration(rand.Int64N(int64(max-min)+1))
}

func (c TimeoutConfig) RandomTTFT() time.Duration {
	return randDuration(c.TTFTRange[0], c.TTFTRange[1])
}

func (c TimeoutConfig) RandomSilence() time.Duration {
	return randDuration(c.SilenceRange[0], c.SilenceRange[1])
}

func (c TimeoutConfig) RandomProbeN() int {
	if c.ProbeRange[1] <= c.ProbeRange[0] {
		return c.ProbeRange[0]
	}
	return c.ProbeRange[0] + rand.IntN(c.ProbeRange[1]-c.ProbeRange[0]+1)
}

// ======================== 全流程调用日志 ========================
// 记录每个请求的完整决策链：接口/模型/节点/路由模式/连接结果/超时/切换/结果。
// 前端日志页按「成功一行简短、异常整块详细」渲染，每条以【成功】/【失败】开头。
// CONC-3（M4 写侧）：落盘移出请求路径——Append 锁内只更新内存 ring + 入队待写缓冲，
// 由后台单写者周期批量写 JSONL（写者/Flush 串行，请求路径零文件 IO）。

const (
	// maxPendingLines/maxPendingBytes 待写缓冲上限：超限丢最旧待写行并告警一次
	//（内存 ring 完整，读侧不受影响；文件最多滞后一个写周期）。
	maxPendingLines = 1024
	maxPendingBytes = 1 << 20
)

// callLogRotateBytes 单文件轮转阈值（atomic：测试可注入小阈值）：超过后滚动为 <path>.1。
// 读侧对 .1 的尾部读取在 CONC-8 补齐（本阶段只做写侧轮转）。
var callLogRotateBytes atomic.Int64

// callLogWriteInterval 后台写者周期（atomic：测试可缩短注入）。
var callLogWriteInterval atomic.Int64

func init() {
	callLogRotateBytes.Store(64 << 20)
	callLogWriteInterval.Store(int64(500 * time.Millisecond))
}

type CallEvent struct {
	Type   string    `json:"type"`
	Node   string    `json:"node,omitempty"`
	Detail string    `json:"detail,omitempty"`
	At     time.Time `json:"at"`
}

type CallRecord struct {
	ReqID         string      `json:"req_id"`
	TS            string      `json:"ts"`
	Path          string      `json:"path"`
	Model         string      `json:"model"`
	Stream        bool        `json:"stream"`
	RouteMode     string      `json:"route_mode"`
	Nodes         []string    `json:"nodes"`
	Events        []CallEvent `json:"events"`
	Status        string      `json:"status"`
	PromptTok     int64       `json:"prompt_tokens"`
	CompletionTok int64       `json:"completion_tokens"`
	DurationMS    int64       `json:"duration_ms"`
	ErrMsg        string      `json:"err_msg,omitempty"`
	// KeyTail 实际使用的 key 末 4 位（自定义源多 key 场景；定位串对话）。
	KeyTail string `json:"key_tail,omitempty"`
}

func CallStatusText(rec CallRecord) string {
	if rec.Status == "ok" {
		return "【成功】"
	}
	return "【失败】"
}

type EventLog struct {
	mu         sync.Mutex
	maxRecords int
	records    []CallRecord
	path       string // 非空时写者落盘 JSONL（异步单写者）

	// 待写缓冲：Append 在锁内入队，写者/Flush 锁外批量写盘
	pendingWrite []byte
	pendingLines int
	droppedLines int
	dropWarned   bool

	writeMu sync.Mutex // 串行化文件写（写者与 Flush 共享 fd）
	// writeGen 写批代际：Clear 清空时自增；写者持旧代际的批次在拿到写锁后
	// 发现代际不符即丢弃——防止清空前的待写缓冲在清空落盘后仍写入文件（日志复活）。
	writeGen uint64
	f        *os.File // 复用 fd；换路径/出错/轮转时重开

	writerOnce sync.Once
	startCh    chan struct{} // 写者启动时关闭（Stop 判断是否需等待）
	writerDone chan struct{} // 写者退出时关闭（含 fd 关闭）
	stopCh     chan struct{}
	stopOnce   sync.Once
}

func NewEventLog(maxRecords int) *EventLog {
	return &EventLog{
		maxRecords: maxRecords,
		startCh:    make(chan struct{}),
		writerDone: make(chan struct{}),
		stopCh:     make(chan struct{}),
	}
}

// SetPath 启用 JSONL 落盘（路径的父目录需存在）。
// 首次设置即惰性启动后台单写者；换路径关闭旧 fd，已有待写缓冲在下一次 flush 写新路径。
func (l *EventLog) SetPath(path string) {
	l.mu.Lock()
	oldPath := l.path
	l.path = path
	l.mu.Unlock()
	if path != "" && path != oldPath {
		l.writeMu.Lock()
		if l.f != nil {
			l.f.Close()
			l.f = nil
		}
		l.writeMu.Unlock()
		l.startWriter()
	}
}

// SetMaxRecords 就地调整环形上限（热加载迁移用：保留已聚合记录与写者生命周期，
// 不再整体替换对象导致内存记录整批丢失）。
func (l *EventLog) SetMaxRecords(n int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if n > 0 {
		l.maxRecords = n
	}
	if len(l.records) > l.maxRecords {
		l.records = l.records[len(l.records)-l.maxRecords:]
	}
}

// startWriter 惰性启动后台单写者（仅一次）。
func (l *EventLog) startWriter() {
	l.writerOnce.Do(func() { go l.writerLoop() })
}

// writerLoop 后台单写者：周期排空待写缓冲（间隔每次动态读取，便于测试缩短）。
func (l *EventLog) writerLoop() {
	close(l.startCh)
	defer close(l.writerDone)
	defer l.closeFile()
	for {
		select {
		case <-l.stopCh:
			return
		case <-time.After(time.Duration(callLogWriteInterval.Load())):
			l.flushPending()
		}
	}
}

// Stop 幂等停止后台写者并等待其退出（fd 已关闭；测试/替换实例用）。
// 停止后仍可 Append（仅内存 ring）/ReadAll，持久化只剩显式 Flush。
func (l *EventLog) Stop() {
	l.stopOnce.Do(func() { close(l.stopCh) })
	select {
	case <-l.startCh:
		<-l.writerDone // 写者已启动：等其退出，确保 fd 关闭后再返回
	default:
		// 写者未启动（无 path）：无 fd，无需等待
	}
}

// Flush 同步排空待写缓冲落盘（测试与管理端需要即时可见时调用）。
func (l *EventLog) Flush() {
	l.flushPending()
}

// Clear 清空调用日志：内存环形、待写缓冲与磁盘文件（含轮转旧段 .1）一并清空。
// 供管理端「清空调用日志」经 HTTP 调用——本进程持有 fd，关闭后删除自己的文件
// 不会撞 Windows 占用；管理器跨进程直删会被「文件被占用」拦截。
// 锁序 writeMu → mu（写者/Flush 均为取完 mu 再写，不反向持锁，无死锁）。
func (l *EventLog) Clear() error {
	l.writeMu.Lock()
	defer l.writeMu.Unlock()
	l.mu.Lock()
	l.records = nil
	l.pendingWrite = nil
	l.pendingLines = 0
	l.droppedLines = 0
	l.dropWarned = false
	l.writeGen++
	path := l.path
	l.mu.Unlock()
	if path == "" {
		return nil
	}
	if l.f != nil {
		l.f.Close()
		l.f = nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Remove(path + ".1"); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// flushPending 排空待写缓冲到文件：循环直到无新 pending（写者/Flush 共用，
// 写盘全程在 lock 外）。
func (l *EventLog) flushPending() {
	for {
		data, path, gen := l.takePending()
		if len(data) == 0 {
			return
		}
		l.writeToFile(data, path, gen)
	}
}

// takePending 锁内取走全部待写缓冲（返回目标路径与代际，供锁外写盘丢弃陈旧批次）。
func (l *EventLog) takePending() ([]byte, string, uint64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	data := l.pendingWrite
	l.pendingWrite = nil
	l.pendingLines = 0
	return data, l.path, l.writeGen
}

// writeToFile 把一批序列化行追加到 JSONL（fd 复用；被外部删除/写失败时重开）。
// 写前检查文件大小，超过轮转阈值时滚动为 .1（Windows 下 os.Rename 不覆盖
// 已存在目标，先删旧 .1 再改名）。
func (l *EventLog) writeToFile(data []byte, path string, gen uint64) {
	l.writeMu.Lock()
	defer l.writeMu.Unlock()
	if gen != l.writeGen {
		return // Clear 清空前的陈旧批次：丢弃，防日志复活
	}
	if l.f != nil {
		if _, err := os.Stat(path); err != nil {
			// 文件被外部删除（如管理端清空日志）：重开重建
			l.f.Close()
			l.f = nil
		}
	}
	if l.f != nil {
		if st, err := l.f.Stat(); err == nil && st.Size() > callLogRotateBytes.Load() {
			l.f.Close()
			l.f = nil
			_ = os.Remove(path + ".1")
			_ = os.Rename(path, path+".1")
		}
	}
	if l.f == nil {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return // 打开失败：本批丢弃，下次 flush 重试（请求路径无 IO）
		}
		l.f = f
	}
	if _, err := l.f.Write(data); err != nil {
		l.f.Close()
		l.f = nil // 写失败：丢弃本批并重开
	}
}

// closeFile 关闭复用 fd（写者退出时调用）。
func (l *EventLog) closeFile() {
	l.writeMu.Lock()
	defer l.writeMu.Unlock()
	if l.f != nil {
		l.f.Close()
		l.f = nil
	}
}

func (l *EventLog) MaxRecords() int { return l.maxRecords }

func (l *EventLog) Append(rec CallRecord) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.records = append(l.records, rec)
	if len(l.records) > l.maxRecords {
		l.records = l.records[len(l.records)-l.maxRecords:]
	}
	if l.path != "" {
		b, err := json.Marshal(rec)
		if err != nil {
			return err
		}
		l.pendingWrite = append(l.pendingWrite, b...)
		l.pendingWrite = append(l.pendingWrite, '\n')
		l.pendingLines++
		// 待写缓冲超限：丢最旧待写行（内存 ring 完整），告警一次
		for l.pendingLines > maxPendingLines || len(l.pendingWrite) > maxPendingBytes {
			l.droppedLines++
			if !l.dropWarned {
				l.dropWarned = true
				slog.Warn("call log 待写缓冲超限，丢弃最旧待写记录", "dropped", l.droppedLines)
			}
			idx := bytes.IndexByte(l.pendingWrite, '\n')
			if idx < 0 {
				l.pendingWrite = nil
				l.pendingLines = 0
				break
			}
			l.pendingWrite = l.pendingWrite[idx+1:]
			l.pendingLines--
		}
	}
	return nil
}

func (l *EventLog) ReadAll() []CallRecord {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]CallRecord, len(l.records))
	copy(out, l.records)
	return out
}

// LoadCallLogFromFile 从 JSONL 恢复（重启后读取历史）
func LoadCallLogFromFile(path string) (*EventLog, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return NewEventLog(DefaultCallLogMax), nil
		}
		return nil, err
	}
	l := NewEventLog(DefaultCallLogMax)
	for _, line := range splitJSONLines(data) {
		if len(line) == 0 {
			continue
		}
		var rec CallRecord
		if json.Unmarshal(line, &rec) == nil {
			l.records = append(l.records, rec)
		}
	}
	if len(l.records) > l.maxRecords {
		l.records = l.records[len(l.records)-l.maxRecords:]
	}
	return l, nil
}

func splitJSONLines(b []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i := 0; i < len(b); i++ {
		if b[i] == '\n' {
			lines = append(lines, b[start:i])
			start = i + 1
		}
	}
	if start < len(b) {
		lines = append(lines, b[start:])
	}
	return lines
}

// ======================== 全局状态 ========================

var (
	timeoutCfg     = DefaultTimeoutConfig()
	timeoutCfgMu   sync.RWMutex // 保护 timeoutCfg（热加载写 / 流式读 并发）
	callLog        = NewEventLog(DefaultCallLogMax)
	callLogPath    = "call_log.jsonl"
	callLogMu      sync.RWMutex
	callLogEnabled = true // main() 按 -gateway/-call-log 赋值；测试默认开启
)

// ======================== 并发流上限（CONC-10 L6） ========================
// 进程级信号量：SSE 流式请求并发上限，防恶意客户端无限挂连接。
// 非流式请求不受限；超限直接 503（与上游不可用语义区分）。
const maxConcurrentStreams = 512

// streamSlots 流式请求并发名额。
var streamSlots = make(chan struct{}, maxConcurrentStreams)

// tryAcquireStream 尝试占用一个流名额（非阻塞）；满员返回 false。
func tryAcquireStream() bool {
	select {
	case streamSlots <- struct{}{}:
		return true
	default:
		return false
	}
}

// releaseStream 归还一个流名额（与 tryAcquireStream 成对，defer 覆盖所有返回路径）。
func releaseStream() {
	<-streamSlots
}

// ======================== SSE 调试（诊断 JSON 拼接） ========================
// 临时诊断工具：把流式转发收到的原始行与转发行写入 sse_debug.log，
// 便于定位 "Unexpected non-whitespace character after JSON" 的拼接现场。
var (
	sseDebugMu   sync.Mutex
	sseDebugFile *os.File
)

// sseDebugf 追加一行到 sse_debug.log，并在调试模式（-debug 或环境变量 OPCODE2API_SSE_DEBUG=1）
// 下输出到控制台。控制台输出便于在 tauri dev 终端观察实际收发的 SSE 流（排查 IDE 解析问题）。
// 整个函数（含文件写入）受开关控制：生产环境不产生 sse_debug.log，避免日志膨胀与原始流落盘。
func sseDebugf(format string, args ...any) {
	enabled := debugMode || os.Getenv("OPCODE2API_SSE_DEBUG") == "1"
	if !enabled {
		return
	}
	msg := fmt.Sprintf(format, args...)
	// 输出到控制台（带时间戳）
	fmt.Printf("[sse-debug] %s %s\n", time.Now().Format("15:04:05.000"), msg)
	sseDebugMu.Lock()
	defer sseDebugMu.Unlock()
	if sseDebugFile == nil {
		f, err := os.OpenFile("sse_debug.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return
		}
		sseDebugFile = f
	}
	fmt.Fprintf(sseDebugFile, time.Now().Format("15:04:05.000")+" "+msg+"\n")
}

// closeSSEDebug 关闭调试文件（进程退出时调用）
func closeSSEDebug() {
	sseDebugMu.Lock()
	defer sseDebugMu.Unlock()
	if sseDebugFile != nil {
		sseDebugFile.Close()
		sseDebugFile = nil
	}
}

// initCallLog 在进程启动时加载历史并启用落盘
func initCallLog() {
	loaded, err := LoadCallLogFromFile(callLogPath)
	if err == nil {
		callLog = loaded
	}
	callLog.SetPath(callLogPath)
}

// recordCall 便捷包装：追加一条调用日志（失败不阻塞请求）。
// 直连请求（无代理地址）在 Nodes/Events 中显示「直连」，避免空串不可读。
func recordCall(rec CallRecord) {
	if !callLogEnabled {
		return
	}
	direct := "直连"
	for i := range rec.Nodes {
		if strings.TrimSpace(rec.Nodes[i]) == "" {
			rec.Nodes[i] = direct
		}
	}
	for i := range rec.Events {
		if strings.TrimSpace(rec.Events[i].Node) == "" {
			rec.Events[i].Node = direct
		}
	}
	// callLog 指针启动时可能被替换（initCallLog），加锁读取
	callLogMu.RLock()
	l := callLog
	callLogMu.RUnlock()
	if err := l.Append(rec); err != nil {
		slog.Error("call log append failed", "error", err)
	}
}

// setTimeoutConfigFromApp 从 AppConfig 读取区间配置并应用（热加载）
func setTimeoutConfigFromApp(cfg AppConfig) {
	timeoutCfgMu.Lock()
	if cfg.TTFTMinMS > 0 && cfg.TTFTMaxMS >= cfg.TTFTMinMS {
		timeoutCfg.TTFTRange = [2]time.Duration{
			time.Duration(cfg.TTFTMinMS) * time.Millisecond,
			time.Duration(cfg.TTFTMaxMS) * time.Millisecond,
		}
	}
	if cfg.SilenceMinMS > 0 && cfg.SilenceMaxMS >= cfg.SilenceMinMS {
		timeoutCfg.SilenceRange = [2]time.Duration{
			time.Duration(cfg.SilenceMinMS) * time.Millisecond,
			time.Duration(cfg.SilenceMaxMS) * time.Millisecond,
		}
	}
	if cfg.ProbeMin > 0 && cfg.ProbeMax >= cfg.ProbeMin {
		timeoutCfg.ProbeRange = [2]int{cfg.ProbeMin, cfg.ProbeMax}
	}
	timeoutCfgMu.Unlock()
	if cfg.CallLogMax > 0 {
		callLogMu.Lock()
		// CONC-3（M4 写侧）热加载迁移：就地改上限，不整体替换对象——
		// 保留已聚合内存记录与写者生命周期（原整批丢失问题随整体替换消除）
		callLog.SetMaxRecords(cfg.CallLogMax)
		callLogMu.Unlock()
	}
}

// ======================== 流内超时 + 断点续写切换 ========================
// 阶段1实验验证过的核心逻辑落地：SSE 读循环加 TTFT/静默计时，
// 超时或流中断时把已吐内容作为上下文续写，重新请求上游（自动换健康代理）。

// resumeStreamResult 描述一次流式转发的最终结果
type resumeStreamResult struct {
	OK         bool   // 是否成功完成（读到 [DONE] 或 EOF）
	Switched   bool   // 是否发生过节点切换
	PromptTok  int64  // 最终 usage
	Completion int64
	ErrMsg     string
	DoneAt     time.Time
}

// streamWithResume 从初始上游响应开始，带 TTFT/静默超时地读取 SSE 并转发。
// 超时/中断时续写重试（最多 maxResume 次）。返回结果供调用方记录日志。
//
// 参数：
//   - w: 客户端响应写入器
//   - r: 客户端请求（用于取消上下文）
//   - upstreamBody: 原始上游请求体（续写时基于它构造新 body）
//   - model: 模型 ID
//   - auth: 上游鉴权
//   - initial: 初始上游响应（可能为 nil，此时直接尝试重连）
//   - keepReasoning: 是否保留 reasoning 内容
//   - callRec: 调用日志记录（追加事件）
func streamWithResume(w http.ResponseWriter, r *http.Request, upstreamBody []byte, model string, auth UpstreamAuth, initial io.ReadCloser, initialProxyAddr string, keepReasoning bool, callRec *CallRecord) resumeStreamResult {
	reqID := ""
	if callRec != nil {
		reqID = callRec.ReqID
	}
	maxResume := maxRouteRetries() // 复用现有重试上限
	if maxResume > 3 {
		maxResume = 3 // 续写重试上限，避免无限循环
	}
	attempt := 0
	res := resumeStreamResult{DoneAt: time.Now()}
	accumulated := ""
	// 已有部分内容时，通过续写 body 重连
	currentBody := upstreamBody
	sseDebugf("[%s] streamWithResume start, model=%s, keepReasoning=%v", reqID, model, keepReasoning)

	// 已尝试过的代理地址：流中断后标记冷却，重连时强制换节点（failover 默认成功不动游标）
	triedAddrs := map[string]bool{}
	// 当前活动的上游响应；attempt 0 用 initial（其代理地址为 initialProxyAddr），后续用重连结果
	upResp := initial
	proxyAddr := initialProxyAddr
	doneSeen := false

	for attempt <= maxResume {
		// 若需要重连（initial 为 nil 或上次超时）
		if upResp == nil {
			var err error
			upResp, _, _, proxyAddr, err = callOpenCodeAPIStream(r.Context(), currentBody, model, &auth)
			if err != nil {
				res.ErrMsg = err.Error()
				callRec.Events = append(callRec.Events, CallEvent{Type: "connect_error", Node: proxyAddr, Detail: err.Error(), At: time.Now()})
				attempt++
				continue
			}
			if proxyAddr != "" && triedAddrs[proxyAddr] {
				sseDebugf("[%s] 警告: 重连仍命中已尝试节点 %s，可能无健康备选", reqID, proxyAddr)
			}
			triedAddrs[proxyAddr] = true
			callRec.Events = append(callRec.Events, CallEvent{Type: "connect_ok", Node: proxyAddr, Detail: "reconnected", At: time.Now()})
			callRec.Nodes = append(callRec.Nodes, proxyAddr)
		}

		reader := bufio.NewReader(upResp)
		// 快照读超时配置（RLock 防止与热加载写并发）
		timeoutCfgMu.RLock()
		ttft := timeoutCfg.RandomTTFT()
		silence := timeoutCfg.RandomSilence()
		timeoutCfgMu.RUnlock()
		gotFirst := false
		// 当前节点是否已插入过「🤖 节点 · 模型」标识前缀（每节点仅一次）
		prefixDone := false

		// 常驻读 goroutine：阻塞读转 channel，主循环 select timer
		type lineResult struct {
			line string
			err  error
		}
		lineCh := make(chan lineResult, 1)
		readDone := make(chan struct{})
		stopRead := make(chan struct{})
		go func() {
			defer close(readDone)
			for {
				ln, er := reader.ReadString('\n')
				select {
				case lineCh <- lineResult{ln, er}:
				case <-stopRead:
					return
				}
				if er != nil {
					return
				}
			}
		}()

		var lastUsage map[string]any
		interrupted := false
		clientGone := false // 客户端主动断开：不惩罚节点、不续写

	readLoop:
		for {
			dur := silence
			if !gotFirst {
				dur = ttft
			}
			timer := time.NewTimer(dur)
			select {
			case resLine := <-lineCh:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				if resLine.err != nil {
					if resLine.err == io.EOF {
						// 正常 EOF：若见过 [DONE] 视为成功，否则视为中断
						if !doneSeen {
							interrupted = true
							res.ErrMsg = "EOF without [DONE]"
							callRec.Events = append(callRec.Events, CallEvent{Type: "stream_interrupt", Node: proxyAddr, Detail: "EOF without [DONE]", At: time.Now()})
						}
						break readLoop
					}
					interrupted = true
					res.ErrMsg = resLine.err.Error()
					callRec.Events = append(callRec.Events, CallEvent{Type: "stream_error", Node: proxyAddr, Detail: resLine.err.Error(), At: time.Now()})
					break readLoop
				}
				line := strings.TrimSpace(resLine.line)
				sseDebugf("[%s] RAW<< %q", reqID, resLine.line)
				if line == "" {
					continue
				}
				if strings.HasPrefix(line, "data: [DONE]") || line == "[DONE]" {
					doneSeen = true
					sseDebugf("[%s] DONE>> %q", reqID, "data: [DONE]\n\n")
					w.Write([]byte("data: [DONE]\n\n"))
					flushWriter(w)
					interrupted = false
					break readLoop
				}
				if !strings.HasPrefix(line, "data: ") {
					// 非 data 行原样转发（如 event:/id:）
					sseDebugf("[%s] META>> %q", reqID, resLine.line)
					// 诊断：首行非空且不像 SSE 格式（无 event:/id:/retry: 前缀），
					// 可能是上游返回了 HTML/纯文本等非 SSE 内容。
					if !gotFirst && line != "" && !strings.HasPrefix(line, "event:") && !strings.HasPrefix(line, "id:") && !strings.HasPrefix(line, "retry:") {
						snippet := line
						if len(snippet) > 300 {
							snippet = snippet[:300] + "...(truncated)"
						}
						slog.Warn("stream first line is not SSE-like",
							"model", model, "node", proxyAddr, "line_snippet", snippet)
					}
					w.Write([]byte(resLine.line))
					if f, ok := w.(http.Flusher); ok {
						f.Flush()
					}
					continue
				}
				dataStr := line[6:]
				// 累积内容（续写用）——从原始 JSON 提取
				var obj map[string]any
				if json.Unmarshal([]byte(dataStr), &obj) == nil {
					// 问题7：上游在流中途以 SSE error 事件报告故障（如免费额度用尽、
					// 限流 429 等），随后可能跟 [DONE] 或直接 EOF——若不识别，下游会
					// 看到错误内容后收到正常结束，网关却当作成功完成不切节点。
					// 可切换故障（额度用尽/限流/5xx）视为中断走续写+换节点；
					// 请求级错误（400 类）切换无意义，保持原样透传。
					if e, ok := obj["error"].(map[string]any); ok {
						errMsg, _ := e["message"].(string)
						if errMsg == "" {
							errMsg, _ = e["type"].(string)
						}
						status, _ := e["code"].(string)
						switching := strings.HasPrefix(status, "5") || strings.Contains(status, "429") ||
							strings.Contains(strings.ToLower(status), "rate") || strings.Contains(strings.ToLower(status), "quota") ||
							strings.Contains(status, "额度")
						// code 缺失/非标准时按文案弱判断（额度用尽/限流文案）
						lowMsg := strings.ToLower(errMsg)
						switching = switching || strings.Contains(lowMsg, "rate") || strings.Contains(lowMsg, "quota") ||
							strings.Contains(lowMsg, "limit") || strings.Contains(lowMsg, "额度") || strings.Contains(lowMsg, "用尽")
						if switching {
							interrupted = true
							res.ErrMsg = "上游流中途错误: " + errMsg
							callRec.Events = append(callRec.Events, CallEvent{Type: "stream_error", Node: proxyAddr, Detail: "upstream SSE error (switching): " + errMsg, At: time.Now()})
							break readLoop
						}
					}
					if u, ok := obj["usage"].(map[string]any); ok {
						lastUsage = u
					}
					if chs, ok := obj["choices"].([]any); ok && len(chs) > 0 {
						if first, ok := chs[0].(map[string]any); ok {
							if delta, ok := first["delta"].(map[string]any); ok {
								if c, ok := delta["content"].(string); ok {
									accumulated += c
								}
								// reasoning_content 不拼入 accumulated：续写时仅把可见内容
								// 作为 assistant 上下文，避免思维链泄露到续写消息/用户可见内容
							}
						}
					}
				} else {
					sseDebugf("[%s] !! JSON parse fail on data payload: %q", reqID, dataStr)
				}
				if !gotFirst {
					gotFirst = true
				}
				// 转发：复用现有转换（清洗 delta/usage/cost 字段），保持协议兼容。
				// 与上方累积提取共用一次解析（避免每 chunk 双重 JSON 解析）。
				var out string
				var chunkUsage map[string]any
				if obj != nil {
					conv, u := convertStreamChunkFromObj(obj, keepReasoning)
					if conv != "" {
						out = "data: " + conv
					}
					chunkUsage = u
				}
				if out == "" {
					// 解析失败或 marshal 失败：回退按原始行转换（保持历史转发行为）
					out, chunkUsage = convertStreamChunkWithUsage(line, keepReasoning)
				}
				if chunkUsage != nil {
					if tt, _ := chunkUsage["total_tokens"].(float64); tt > 0 {
						lastUsage = chunkUsage
					}
				}
				sseDebugf("[%s] FWD>> %q", reqID, out)
				// 节点/模型标识：每个节点首个内容 chunk 前插入「🤖 节点 · 模型」前缀，
				// 用户可感知当前由哪个节点/模型回答、以及何时切换（切换后新节点重新加前缀）。
				// 前缀独立成行（\n\n 分隔），不影响后续内容阅读。
				// 由配置 show_node_prefix 控制（默认关闭）。
				if getShowNodePrefix() && !prefixDone && strings.HasPrefix(out, "data: ") {
					var outObj map[string]any
					if json.Unmarshal([]byte(out[6:]), &outObj) == nil {
						if chs, ok := outObj["choices"].([]any); ok && len(chs) > 0 {
							if first, ok := chs[0].(map[string]any); ok {
								if delta, ok := first["delta"].(map[string]any); ok {
									if c, ok := delta["content"].(string); ok && c != "" {
										nodeLabel := proxyDisplayName(proxyAddr)
										if nodeLabel == "" {
											nodeLabel = "未知节点"
										}
										delta["content"] = fmt.Sprintf("\n\n🤖 %s · %s\n\n%s", nodeLabel, displayModelName(model), c)
										first["delta"] = delta
										chs[0] = first
										outObj["choices"] = chs
										if nb, err := json.Marshal(outObj); err == nil {
											out = "data: " + string(nb)
										}
										prefixDone = true
									}
								}
							}
						}
					}
				}
				// 标准 SSE：每个事件以 \n\n 结尾（事件间空行分隔）。
				// 之前只写单个 \n，导致严格的 OpenAI 兼容客户端把连续两行当成一个事件，
				// 第二行 JSON 报 "Unexpected non-whitespace character after JSON"。
				w.Write([]byte(out))
				w.Write([]byte("\n\n"))
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
			case <-timer.C:
				if !gotFirst {
					res.ErrMsg = fmt.Sprintf("TTFT timeout (%v)", ttft)
					callRec.Events = append(callRec.Events, CallEvent{Type: "ttft_timeout", Node: proxyAddr, Detail: res.ErrMsg, At: time.Now()})
				} else {
					res.ErrMsg = fmt.Sprintf("silence timeout (%v)", silence)
					callRec.Events = append(callRec.Events, CallEvent{Type: "silence_timeout", Node: proxyAddr, Detail: res.ErrMsg, At: time.Now()})
				}
				interrupted = true
				break readLoop
			case <-r.Context().Done():
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				interrupted = true
				clientGone = true
				res.ErrMsg = "client disconnected"
				break readLoop
			}
		}

		// 关闭当前流：先通知读 goroutine 退出，再关连接，最后等 goroutine 结束
		close(stopRead)
		upResp.Close()
		<-readDone

		if lastUsage != nil {
			// token 统计修正（切换场景）：
			//   - prompt_tokens 取首次（原始输入），后续节点因续写追加了 assistant 上下文，
			//     其 prompt 虚高，不采用
			//   - completion_tokens 累加所有节点的输出（切换后是续写部分，需相加）
			if res.PromptTok == 0 {
				if pt, _ := lastUsage["prompt_tokens"].(float64); pt > 0 {
					res.PromptTok = int64(pt)
				}
			}
			if ct, _ := lastUsage["completion_tokens"].(float64); ct > 0 {
				res.Completion += int64(ct)
			}
		}

		if !interrupted {
			res.OK = true
			res.DoneAt = time.Now()
			return res
		}

		// 客户端主动断开：不惩罚节点、不续写重连（避免浪费上游配额 + 误伤健康节点）。
		// 注：请求 ctx 已取消时，上游 body 读也会被中止并在 lineCh 先报错（竞态），
		// 因此除 clientGone 标志外，ctx 已取消同样按客户端断开处理。
		if clientGone || r.Context().Err() != nil {
			res.OK = false
			res.ErrMsg = "client disconnected"
			res.DoneAt = time.Now()
			return res
		}

		// 中断：记录切换事件，续写重试
		res.Switched = true
		attempt++
		// 标记当前节点冷却（20s），强制下一次重连选择其他健康节点——
		// failover 模式"成功不动游标"，若流中断但连接曾 2xx，健康表仍认为它可用，
		// 不标记冷却会导致重连永远命中同一节点（用户实测的 28110→28110→... 死循环）。
		if proxyAddr != "" {
			markSocks5Result(proxyAddr, http.StatusServiceUnavailable, nil)
			sseDebugf("[%s] 节点 %s 流中断，标记冷却以强制换节点", reqID, proxyAddr)
		}
		if attempt > maxResume {
			res.OK = false
			res.ErrMsg = "所有候选节点均失败，回复中断"
			callRec.Events = append(callRec.Events, CallEvent{Type: "all_failed", Node: proxyAddr, Detail: res.ErrMsg, At: time.Now()})
			return res
		}
		callRec.Events = append(callRec.Events, CallEvent{Type: "switch", Node: proxyAddr, Detail: fmt.Sprintf("switching (resume, accumulated=%d chars)", len(accumulated)), At: time.Now()})
		// 续写 body：原 messages + assistant(已吐内容) + user(请继续)
		if len(accumulated) > 0 {
			var bodyMap map[string]any
			if json.Unmarshal(currentBody, &bodyMap) == nil {
				msgs, _ := bodyMap["messages"].([]any)
				msgs = append(msgs,
					map[string]any{"role": "assistant", "content": accumulated},
					map[string]any{"role": "user", "content": "请继续上面的回复，从中断处接着写。"},
				)
				bodyMap["messages"] = msgs
				if b, err := json.Marshal(bodyMap); err == nil {
					currentBody = b
				}
			}
		}
		// 不额外发送「已切换节点」提示文本——切换感知由新节点首个 chunk 前的
		// 🤖 节点·模型 标识承担，避免插入提示打断用户预览阅读流。
		// 下一轮重连
		upResp = nil
	}

	res.OK = false
	res.ErrMsg = "所有候选节点均失败，回复中断"
	return res
}

func flushWriter(w http.ResponseWriter) {
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// buildResumeBody 供测试使用的续写 body 构造（独立于 streamWithResume 内部逻辑）
func buildResumeBody(body []byte, accumulated string) []byte {
	var bodyMap map[string]any
	if json.Unmarshal(body, &bodyMap) != nil {
		return body
	}
	msgs, _ := bodyMap["messages"].([]any)
	msgs = append(msgs,
		map[string]any{"role": "assistant", "content": accumulated},
		map[string]any{"role": "user", "content": "请继续上面的回复，从中断处接着写。"},
	)
	bodyMap["messages"] = msgs
	b, err := json.Marshal(bodyMap)
	if err != nil {
		return body
	}
	return b
}

// applyBadStatusConfig 从配置读取坏状态码组与坏池阈值并应用（热加载）。
// 配置为 "状态码"→"原因文案" 的 map；未配置的项保留默认值。
func applyBadStatusConfig(cfg AppConfig) {
	socks5HealthMu.Lock()
	defer socks5HealthMu.Unlock()
	if cfg.BadStatusCodes != nil {
		newMap := map[int]string{}
		for codeStr, reason := range cfg.BadStatusCodes {
			code, err := strconv.Atoi(codeStr)
			if err != nil {
				continue
			}
			newMap[code] = reason
		}
		if len(newMap) > 0 {
			badStatusCodes = newMap
		}
	}
	// badThreshold 用全局 const（badThreshold），配置暂不覆盖（保持简单）；
	// 如需可配置可在此读取 cfg.BadThreshold。
	_ = cfg.BadThreshold
}

// proxyDisplayName 按 SOCKS5 地址反查实例名（用于流式前缀「🤖 实例名 · 模型」）。
// Rust 生成网关配置时把 socks5_proxies[].name 填为真实实例名；
// 查不到时回退返回地址本身。纯内存查询，无网络开销。
func proxyDisplayName(addr string) string {
	if addr == "" {
		return ""
	}
	socks5Mu.RLock()
	defer socks5Mu.RUnlock()
	for _, p := range socks5Proxies {
		if p.Addr == addr && p.Name != "" {
			return p.Name
		}
	}
	return addr
}