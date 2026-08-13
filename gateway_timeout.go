package main

import (
	"bufio"
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
}

func CallStatusText(rec CallRecord) string {
	if rec.Status == "ok" {
		return "【成功】"
	}
	return "【失败】"
}

type EventLog struct {
	mu           sync.Mutex
	maxRecords   int
	records      []CallRecord
	path         string // 非空时同步落盘 JSONL
	bytesWritten int64  // 当前落盘文件累计写入字节（触发轮转用）
	maxBytes     int64  // 落盘文件轮转阈值（默认 callLogMaxBytes）
}

const (
	callLogMaxBytes = 64 << 20 // 落盘文件 64MB 轮转一次（保留一份 .1 历史）
)

func NewEventLog(maxRecords int) *EventLog {
	return &EventLog{maxRecords: maxRecords, maxBytes: callLogMaxBytes}
}

// SetPath 启用 JSONL 落盘（路径的父目录需存在）
func (l *EventLog) SetPath(path string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.path = path
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
		// 轮转：落盘文件超过上限时把当前文件改名为 .1（覆盖旧 .1），重新写新文件。
		if l.bytesWritten > l.maxBytes {
			if err := os.Rename(l.path, l.path+".1"); err == nil {
				l.bytesWritten = 0
			}
		}
		f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return err
		}
		n, werr := f.Write(append(b, '\n'))
		f.Close()
		l.bytesWritten += int64(n)
		if werr != nil {
			return werr
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
	callLogEnabled = true // 仅网关/代理池模式启用（避免直连实例产生无人读取的日志）
)

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
	// callLog 指针可能被热加载替换（setTimeoutConfigFromApp），加锁读取
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
		callLog = NewEventLog(cfg.CallLogMax)
		callLog.SetPath(callLogPath)
		callLogMu.Unlock()
	}
}

// ======================== 流内超时 + 断点续写切换 ========================
// 阶段1实验验证过的核心逻辑落地：SSE 读循环加 TTFT/静默计时，
// 超时或流中断时把已吐内容作为上下文续写，重新请求上游（自动换健康代理）。

// resumeStreamResult 描述一次流式转发的最终结果
type resumeStreamResult struct {
	OK         bool  // 是否成功完成（读到 [DONE] 或 EOF）
	Switched   bool  // 是否发生过节点切换
	PromptTok  int64 // 最终 usage
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
			upResp, _, _, proxyAddr, err = callOpenCodeAPIStream(currentBody, model, auth)
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
					w.Write([]byte(resLine.line))
					if f, ok := w.(http.Flusher); ok {
						f.Flush()
					}
					continue
				}
				dataStr := line[6:]
				// 累积内容（续写用）+ 转发转换共用一次解析（避免每 chunk 双重 JSON 解析）
				var obj map[string]any
				if json.Unmarshal([]byte(dataStr), &obj) != nil {
					obj = nil
				}
				if obj != nil {
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
				// 转发：复用现有转换（清洗 delta/usage/cost 字段），保持协议兼容
				var out string
				if obj != nil {
					conv, chunkUsage := convertStreamChunkFromObj(obj, keepReasoning)
					if conv != "" {
						out = "data: " + conv
					}
					if chunkUsage != nil {
						if tt, _ := chunkUsage["total_tokens"].(float64); tt > 0 {
							lastUsage = chunkUsage
						}
					}
				}
				if out == "" {
					// 解析失败回退原始转换（保持与历史一致的转发行为）
					fallback, chunkUsage := convertStreamChunkWithUsage(line, keepReasoning)
					out = fallback
					if chunkUsage != nil {
						if tt, _ := chunkUsage["total_tokens"].(float64); tt > 0 {
							lastUsage = chunkUsage
						}
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

		// 客户端主动断开：不惩罚节点、不续写重连（避免浪费上游配额 + 误伤健康节点）
		if clientGone {
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
