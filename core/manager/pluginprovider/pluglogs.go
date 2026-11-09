// pluglogs.go —— 插件子进程日志的有界环形缓冲（stdout + stderr 统一收集）。
//
// 为什么需要（2026-11-09 真机事故复盘）：
//
//	宿主原来写 `cmd.Stderr = nil`（manager.go:917「丢弃 stderr，错误经就绪行上报」）。
//	这个假设在真实插件里不成立——插件把日志全打在 stderr（Go log 包默认输出），
//	而 stdout 按契约只能打 ready/need_config/fatal 状态行。于是子进程的
//	「启动浏览器失败: CDP 端口未就绪」「上游 400」这类关键错误**一条都到不了宿主**，
//	stderr 被操作系统直接丢弃（不是读到缓冲区后忽略，是连读都不读）。
//
//	后果：BIOS 插件连续多日凌晨补号全部失败，宿主侧只能看到「子进程在报告就绪前退出」
//	或干脆没有任何线索，排查只能靠手工起探针实例复现——本次定位三个 bug
//	（profile 相对路径 / 进程退出判断 / Content-Type 缺失）全靠这个笨办法。
//
// 设计取舍：
//   - 有界：每个插件固定容量（默认 500 行），满了丢最旧，绝不因为插件狂打日志
//     （某些插件会每请求打日志）把宿主内存吃穿；
//   - 跨重启保留：崩溃前的日志正是要看的，所以环形缓冲挂在 plugin 上而非单次 spawn；
//     每次拉起插一行分隔符标记新周期，便于区分多次尝试；
//   - 单行截断：超长行（插件把 JSON body 整段打出来）截到 maxLogLineBytes，避免
//     一条日志吃掉整个缓冲；
//   - 与 stdout 状态行通道分离：ring 只做记录，不干扰就绪行解析与 supervise 的
//     select 逻辑（那条通道的语义（含「缓冲满丢弃」）保持不变）。
package pluginprovider

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	// defaultLogRingCap 每插件保留的日志行数上限。
	defaultLogRingCap = 500
	// maxLogLineBytes 单行最多保留的字节数（超出截断并标注）。
	maxLogLineBytes = 4096
	// stderrTailLines 启动失败时附进错误信息的 stderr 行数。
	stderrTailLines = 12
)

// LogLine 一条插件子进程日志。
type LogLine struct {
	Time   string `json:"time"`             // 本机接收时刻 RFC3339
	Stream string `json:"stream"`           // out | err | mark
	Line   string `json:"line"`             // 原始行（超长已截断）
	Cycle  int    `json:"cycle,omitempty"`  // 所属 spawn 周期（从 1 计）
}

// logRing 是固定容量的行环形缓冲（并发安全）。
type logRing struct {
	mu    sync.Mutex
	buf   []LogLine
	next  int  // 下一个写入下标
	full  bool // 是否已回绕（决定读取顺序）
	cap   int
	cycle int
}

func newLogRing(cap int) *logRing {
	if cap <= 0 {
		cap = defaultLogRingCap
	}
	return &logRing{cap: cap, buf: make([]LogLine, 0, cap)}
}

// Append 追加一行（自动截断超长行、自动打时间戳）。
func (r *logRing) Append(stream, line string) {
	if r == nil {
		return
	}
	line = strings.TrimRight(line, "\r\n")
	if len(line) > maxLogLineBytes {
		line = line[:maxLogLineBytes] + "…(截断)"
	}
	// 全空白行不占额度（插件输出里的空行没有诊断价值）。
	if strings.TrimSpace(line) == "" && stream != "mark" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	entry := LogLine{
		Time:   time.Now().Format(time.RFC3339),
		Stream: stream,
		Line:   line,
		Cycle:  r.cycle,
	}
	if len(r.buf) < r.cap {
		r.buf = append(r.buf, entry)
		return
	}
	r.buf[r.next] = entry
	r.next = (r.next + 1) % r.cap
	r.full = true
}

// BeginCycle 标记新的一次子进程拉起，返回周期号。
//
// 周期号同时写进 LogLine.Cycle，方便面板按「第几次尝试」分组看。
func (r *logRing) BeginCycle(reason string) int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	r.cycle++
	c := r.cycle
	r.mu.Unlock()
	msg := fmt.Sprintf("===== 第 %d 次拉起子进程（%s） =====", c, reason)
	r.Append("mark", msg)
	return c
}

// CurrentCycle 返回当前周期号。
func (r *logRing) CurrentCycle() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cycle
}

// Snapshot 按时间顺序返回最多 limit 条（limit<=0 表示全部）。
//
// streamFilter 非空时只返回该流（out/err/mark）。
func (r *logRing) Snapshot(limit int, streamFilter string) []LogLine {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	ordered := make([]LogLine, 0, len(r.buf))
	if r.full {
		ordered = append(ordered, r.buf[r.next+len(r.buf)-r.cap:]...)
		if r.next > 0 {
			ordered = append(ordered, r.buf[:r.next]...)
		}
	} else {
		ordered = append(ordered, r.buf...)
	}
	r.mu.Unlock()

	if streamFilter != "" {
		filtered := ordered[:0]
		for _, l := range ordered {
			if strings.EqualFold(l.Stream, streamFilter) {
				filtered = append(filtered, l)
			}
		}
		ordered = filtered
	}
	if limit > 0 && len(ordered) > limit {
		ordered = ordered[len(ordered)-limit:]
	}
	return ordered
}

// Tail 返回指定流最后 n 行纯文本（拼进错误信息用）。
func (r *logRing) Tail(stream string, n int) string {
	lines := r.Snapshot(n, stream)
	if len(lines) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, l := range lines {
		sb.WriteString(l.Line)
		sb.WriteByte('\n')
	}
	return strings.TrimRight(sb.String(), "\n")
}

// Len 返回当前保留行数（诊断/测试用）。
func (r *logRing) Len() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.buf)
}

// withStderrTail 把 stderr 尾部拼进错误信息。
//
// 只在 stderr 确实有内容时追加，避免「无 stderr」时给用户一串空行；
// 明确标注来源，防止与就绪行的 fatal 错误混为一谈。
func withStderrTail(msg string, ring *logRing) string {
	tail := ring.Tail("err", stderrTailLines)
	if tail == "" {
		return msg
	}
	return msg + "\n—— 子进程 stderr 末尾 ——\n" + tail
}
