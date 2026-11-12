package manager

// Kilo 调用日志持久化（阶段 L / Task 1）。
//
// 为什么单独一个文件、为什么不复用 main 包的 EventLog.Append：
//   - EventLog 在 main 包，core/manager 反向 import 会造成循环依赖，且会
//     破坏阶段 K 的独立性红线（30080 链路不得引用管理面推理代码）。
//   - 这里只依赖 Go 标准库 + 本包的 Paths()，写出与 CallLogRecord 兼容的
//     JSONL 行；ReadCallLog() 把它作为第三个来源合并（见 calllog.go）。
//
// 安全：只记元数据（时间/模型/状态/耗时/端口），**绝不记录请求体、响应体、
// API Key**。上游 Bearer anonymous 是公开匿名档，不需要保密也不需要记录。

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	kiloCallsLogDir  = "kilo"
	kiloCallsLogName = "kilo_calls.jsonl"
	// 与 readCallLogFileTail 的 8MB 尾窗对齐：超过就轮转为 .1（它本来就会读 .1）。
	kiloCallsRotateBytes = 8 << 20
)

// KiloCallsLogPath 返回 Kilo 调用日志文件路径（<runtime>/kilo/kilo_calls.jsonl）。
func (m *Manager) KiloCallsLogPath() string {
	return filepath.Join(m.Paths().RuntimeDir, kiloCallsLogDir, kiloCallsLogName)
}

// kiloCallWriter 串行 append（单进程写、多 goroutine 调用，用一把锁串行化）。
//
// 设计取舍：本地文件 append 是微秒级、O_APPEND 单行原子写，直接同步写最
// 简单可靠 —— 不引入异步队列/flush 生命周期（toggle 停用再启用时 writer
// 必须继续可用，异步方案要处理「关闭后重启」反而更脆）。写失败只告警，
// 绝不把错误传播到响应路径（与「失败不拖垮管理面」同原则）。
type kiloCallWriter struct {
	path string
	mu   sync.Mutex
}

func newKiloCallWriter(path string) *kiloCallWriter {
	return &kiloCallWriter{path: path}
}

// write 同步落盘一行；失败只告警，不阻塞/不中断调用链。
func (w *kiloCallWriter) write(rec CallLogRecord) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.append(rec); err != nil {
		slog.Warn("kilo call log append failed", "error", err)
	}
}

func (w *kiloCallWriter) append(rec CallLogRecord) error {
	dir := filepath.Dir(w.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建日志目录: %w", err)
	}
	if err := w.rotateIfNeeded(); err != nil {
		slog.Warn("kilo call log rotate failed", "error", err)
	}
	f, err := os.OpenFile(w.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = f.Write(b)
	return err
}

// rotateIfNeeded 超过阈值把当前文件改名为 .1（覆盖旧 .1）。
// readCallLogFileTail 会先读 .1 再读主文件，语义与供应商服务日志一致。
func (w *kiloCallWriter) rotateIfNeeded() error {
	fi, err := os.Stat(w.path)
	if err != nil || fi.Size() < kiloCallsRotateBytes {
		return nil
	}
	return os.Rename(w.path, w.path+".1")
}

// kiloCallToLogRecord 把观测记录映射成统一 CallLogRecord。
// 字段与 CallLogRecord 的 JSON tag 完全对齐，readCallLogFileTail 可直接解析。
func kiloCallToLogRecord(rec KiloCallRecord, servingPort uint16) CallLogRecord {
	ts := rec.TS
	if ts == "" {
		ts = time.Now().Format(time.RFC3339)
	}
	status := "ok"
	errMsg := ""
	if rec.Status < 200 || rec.Status >= 300 {
		status = "error"
		errMsg = fmt.Sprintf("kilo upstream HTTP %d", rec.Status)
	}
	return CallLogRecord{
		ReqID:          rec.ReqID,
		TS:             ts,
		Path:           "/v1/chat/completions",
		Model:          rec.Requested,
		Stream:         rec.Stream,
		Status:         status,
		DurationMS:     rec.DurationMs,
		ErrMsg:         errMsg,
		Vendor:         "kilo",
		ServingPort:    fmt.Sprintf("%d", servingPort),
		Tier:           "free",
		RouteVerdict:   "direct_by_design",
		UpstreamStatus: rec.Status,
		ActualModel:    rec.Actual,
		SchemaVersion:  2,
	}
}
