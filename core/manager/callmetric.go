package manager

// 调用指标（v2.0.0 增补）：面向分析的**结构化指标行**。
//
// 与 call_log.jsonl 的分工：
//   - call_log.jsonl 是给人看的日志（自由文本 events、失败文案），字段零散；
//   - 这里存的是**可聚合**的规整一行，专门用于回答
//     「哪个模型 × 哪个通道 × 哪个密钥 成功率更高 / 更快」。
//
// 为什么不复用 call_log.jsonl：它的记录由 main 包写盘，缺失 key_id / channel /
// tier 这些分析必需的维度，且历史记录格式漂移大；混进去反而要在聚合时反复兜底。
//
// 存储：<dataDir>/runtime/call-metrics/YYYY-MM-DD.jsonl，一行一条，按天滚动。
// 目录在数据目录内（gitignore），不落仓库。

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// CallMetricLeg 一跳上游尝试（引擎 legs 字段的一项）。
type CallMetricLeg struct {
	Key    string `json:"key"`
	Status int    `json:"status"`
	DurMS  int64  `json:"dur_ms,omitempty"`
}

// CallMetric 一次调用的分析用指标行。
//
// 字段刻意保持扁平：聚合时只做 group by，不需要嵌套结构。
type CallMetric struct {
	TS    string `json:"ts"`
	ReqID string `json:"req_id,omitempty"`
	// Source 指标来源：engine（OpenCode 引擎）/ provider（供应商服务）。
	Source string `json:"source"`
	Model  string `json:"model,omitempty"`
	// Channel 通道：key（带密钥）/ anonymous（匿名免费层）。
	Channel string `json:"channel,omitempty"`
	// Tier 层：zen / go（引擎当前恒为 zen）。
	Tier string `json:"tier,omitempty"`
	// KeyID 引擎给的密钥短标识（**不是**完整密钥，无泄漏风险）。
	KeyID string `json:"key_id,omitempty"`
	// Status ok / fail / canceled。
	Status string `json:"status"`
	// UpstreamStatus 上游最后一跳 HTTP 状态码（0 = 未拿到响应）。
	UpstreamStatus int `json:"upstream_status,omitempty"`
	// ErrorClass 归一化错误类别（rate_limit / quota / auth / upstream / timeout / other）。
	ErrorClass string `json:"error_class,omitempty"`
	// Outcome 引擎原始 outcome（success / client_error / client_canceled）。
	Outcome    string `json:"outcome,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	// PromptTokens / CompletionTokens 该次调用的用量（失败时通常为 0）。
	PromptTokens     int64 `json:"prompt_tokens,omitempty"`
	CompletionTokens int64 `json:"completion_tokens,omitempty"`
	// Attempts 尝试次数；Legs 逐跳明细。
	Attempts int             `json:"attempts,omitempty"`
	Legs     []CallMetricLeg `json:"legs,omitempty"`
	// Rotated 该请求是否触发了 session 身份轮换（免费额度耗尽的信号）。
	Rotated bool `json:"rotated,omitempty"`
	// ProxyClass 轮换事件里拿到的出口类别（如 warp:2a09:bac5:1ef3::/64）。
	// 引擎的 request_routed.egress / exit_id 实测恒为空，所以这里只能靠 join 轮换事件补齐。
	ProxyClass string `json:"proxy_class,omitempty"`
	// Stream 是否流式请求。
	Stream bool `json:"stream,omitempty"`
	// Path 请求路径（/v1/chat/completions 等）。
	Path string `json:"path,omitempty"`
}

// callMetricDirName 指标目录名（位于 runtime 下）。
const callMetricDirName = "call-metrics"

// callMetricKeepDays 默认保留天数。
const callMetricKeepDays = 30

// callMetricMu 串行化写入：多个 goroutine 同时 append 同一文件时必须有序，
// 否则可能写出交错的行导致解析失败。
var callMetricMu sync.Mutex

// CallMetricsDir 返回指标目录。
func (m *Manager) CallMetricsDir() string {
	return filepath.Join(m.paths.RuntimeDir, callMetricDirName)
}

// callMetricFilePath 返回某天的指标文件路径。
func (m *Manager) callMetricFilePath(day time.Time) string {
	return filepath.Join(m.CallMetricsDir(), day.Format("2006-01-02")+".jsonl")
}

// AppendCallMetric 追加一条指标。
//
// 写入失败只返回 error，由调用方决定是否记日志 —— 指标是旁路，
// 绝不能因为写指标失败而影响推理主流程。
func (m *Manager) AppendCallMetric(rec CallMetric) error {
	if strings.TrimSpace(rec.TS) == "" {
		rec.TS = time.Now().Format(time.RFC3339Nano)
	}
	if rec.Source == "" {
		rec.Source = "engine"
	}
	day, err := callMetricDay(rec.TS)
	if err != nil {
		// 时间戳不可解析时归到当天，避免丢记录。
		day = time.Now()
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("marshal call metric: %w", err)
	}

	callMetricMu.Lock()
	defer callMetricMu.Unlock()

	dir := m.CallMetricsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create call-metrics dir: %w", err)
	}
	f, err := os.OpenFile(m.callMetricFilePath(day), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open call metric file: %w", err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("write call metric: %w", err)
	}
	return nil
}

// callMetricDay 把 RFC3339 时间戳解析成当天日期（本地时区）。
func callMetricDay(ts string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		t, err = time.Parse(time.RFC3339, ts)
		if err != nil {
			return time.Time{}, err
		}
	}
	return t.Local(), nil
}

// ReadCallMetrics 读取 [from, to] 区间内的指标（两端按天包含）。
//
// limit>0 时只保留**最新** limit 条。损坏行跳过，不中断读取。
func (m *Manager) ReadCallMetrics(from, to time.Time, limit int) ([]CallMetric, error) {
	dir := m.CallMetricsDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read call-metrics dir: %w", err)
	}

	// 按文件名（=日期）升序处理，保证结果按时间递增。
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	fromDay := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, time.Local)
	toDay := time.Date(to.Year(), to.Month(), to.Day(), 0, 0, 0, 0, time.Local)

	var all []CallMetric
	for _, name := range names {
		dayStr := strings.TrimSuffix(name, ".jsonl")
		day, err := time.ParseInLocation("2006-01-02", dayStr, time.Local)
		if err != nil {
			continue
		}
		if day.Before(fromDay) || day.After(toDay) {
			continue
		}
		all = append(all, readCallMetricFile(filepath.Join(dir, name))...)
	}

	// 文件内已按写入顺序，但跨文件合并后仍按 TS 排一次，避免时钟回拨导致错序。
	sort.SliceStable(all, func(i, j int) bool { return all[i].TS < all[j].TS })

	if limit > 0 && len(all) > limit {
		all = all[len(all)-limit:]
	}
	return all, nil
}

// readCallMetricFile 读单个指标文件，损坏行跳过。
func readCallMetricFile(path string) []CallMetric {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()

	out := make([]CallMetric, 0, 256)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] != '{' {
			continue
		}
		var rec CallMetric
		if json.Unmarshal([]byte(line), &rec) != nil {
			continue
		}
		out = append(out, rec)
	}
	return out
}

// PruneCallMetrics 删除超过 keepDays 天的指标文件。keepDays<=0 时用默认 30 天。
//
// 返回被删除的文件数。
func (m *Manager) PruneCallMetrics(keepDays int) int {
	if keepDays <= 0 {
		keepDays = callMetricKeepDays
	}
	entries, err := os.ReadDir(m.CallMetricsDir())
	if err != nil {
		return 0
	}
	cutoff := time.Now().AddDate(0, 0, -keepDays)
	cutoffDay := time.Date(cutoff.Year(), cutoff.Month(), cutoff.Day(), 0, 0, 0, 0, time.Local)

	removed := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		dayStr := strings.TrimSuffix(e.Name(), ".jsonl")
		day, err := time.ParseInLocation("2006-01-02", dayStr, time.Local)
		if err != nil {
			continue
		}
		if day.Before(cutoffDay) {
			if os.Remove(filepath.Join(m.CallMetricsDir(), e.Name())) == nil {
				removed++
			}
		}
	}
	return removed
}
