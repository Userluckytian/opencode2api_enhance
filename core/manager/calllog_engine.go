package manager

// OpenCode 引擎（40080）调用事件来源。
//
// 为什么读 stdout 而不是 call_log.jsonl：引擎不写 JSONL 调用日志 ——
// 它把调用事件放在进程内 ring buffer，只经 WebUI 的 /api/logs 暴露
//（WebUI 是独立 server，且需登录）。但引擎**同时**把每条 request_routed
// 事件以结构化 JSON 打进 stdout，我们用 LogOut 重定向到了
// <free-tier>/stdout.log，所以直接解析该文件即可，无需登录会话。
//
// 字段映射（引擎 JSON → CallLogRecord）：
//   time → TS、model → Model、status → UpstreamStatus、tier → Tier、
//   duration_ms → DurationMS、outcome → Status、channel → Vendor 语义补充

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// engineLogLine 引擎 stdout 里的单行 JSON（只声明调用事件用到的字段）。
type engineLogLine struct {
	Time       string `json:"time"`
	Level      string `json:"level"`
	Msg        string `json:"msg"`
	Event      string `json:"event"`
	Method     string `json:"method"`
	Path       string `json:"path"`
	Status     int    `json:"status"`
	DurationMS int64  `json:"duration_ms"`
	RequestID  string `json:"request_id"`
	Model      string `json:"model"`
	Session    string `json:"session"`
	Tier       string `json:"tier"`
	Channel    string `json:"channel"`
	Anonymous  bool   `json:"anonymous"`
	Attempts   int    `json:"attempts"`
	Stream     bool   `json:"stream"`
	Outcome    string `json:"outcome"`
	In         int64  `json:"in"`
	Out        int64  `json:"out"`
	Reasoning  int64  `json:"reasoning_tokens"`
	RouteError string `json:"route_error"`
	Penalty    string `json:"penalty_kind"`
	Stop       string `json:"stop"`
}

// EngineStdoutLogPath 返回引擎 stdout 日志路径（含 request_routed 事件）。
func (m *Manager) EngineStdoutLogPath() string {
	return filepath.Join(m.paths.FreeTierEngineDir(), "stdout.log")
}

// readEngineCallLog 从引擎 stdout 末尾解析调用事件，转成统一记录。
//
// max<=0 = 全部；max>0 = 只取最近 max 条。文件缺失/行损坏一律跳过，不报错
//（引擎未启动时该文件可能不存在）。
func readEngineCallLog(path string, max int) []CallLogRecord {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()

	// 逐行扫描（不整文件读入：引擎长跑后 stdout 可以很大）。
	// 为限制内存，只保留最后 max 条（max<=0 时也设一个内部上限，避免爆内存）。
	keep := max
	if keep <= 0 {
		keep = 5000
	}
	ring := make([]CallLogRecord, 0, keep)
	sc := bufio.NewScanner(f)
	// 引擎单行最长可能几 KB（含 legs 链），放宽缓冲。
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] != '{' {
			continue
		}
		var raw engineLogLine
		if json.Unmarshal([]byte(line), &raw) != nil {
			continue
		}
		if raw.Event != "request_routed" {
			continue
		}
		rec := engineRecordFromLine(raw)
		if len(ring) < keep {
			ring = append(ring, rec)
		} else {
			copy(ring, ring[1:])
			ring[len(ring)-1] = rec
		}
	}
	if err := sc.Err(); err != nil {
		return ring
	}
	return ring
}

// engineRecordFromLine 把一行引擎事件映射为统一调用记录。
func engineRecordFromLine(raw engineLogLine) CallLogRecord {
	// outcome → status 文案（与供应商服务的 ok/fail 语义对齐）。
	status := "fail"
	switch raw.Outcome {
	case "success":
		status = "ok"
	case "client_canceled":
		status = "canceled"
	case "client_error":
		status = "fail"
	}
	// 2xx 且 outcome 缺失时按成功处理（旧版本引擎可能不写 outcome）。
	if status != "ok" && raw.Status >= 200 && raw.Status < 300 {
		status = "ok"
	}

	rec := CallLogRecord{
		ReqID:            raw.RequestID,
		TS:               raw.Time,
		Path:             raw.Path,
		Model:            raw.Model,
		Stream:           raw.Stream,
		Status:           status,
		PromptTokens:     raw.In,
		CompletionTokens: raw.Out,
		DurationMS:       raw.DurationMS,
		Source:           "OpenCode 引擎",
		Tier:             raw.Tier,
		UpstreamStatus:   raw.Status,
		SchemaVersion:    3,
	}
	// 厂商归属：引擎不带 vendor 字段，用 tier + channel 组合标识来源通道。
	// anonymous 通道记为 "opencode"，key 通道记为 "zen"/"go"（与 tiers 名称一致）。
	switch {
	case raw.Anonymous:
		rec.Vendor = "opencode"
	case raw.Tier != "":
		rec.Vendor = raw.Tier
	default:
		rec.Vendor = "opencode"
	}
	// 失败原因：优先用引擎给的 route_error / penalty_kind，再按状态码归类。
	if status != "ok" {
		reason := strings.TrimSpace(raw.RouteError)
		if reason == "" {
			reason = strings.TrimSpace(raw.Penalty)
		}
		switch {
		case raw.Status == 429:
			rec.ErrorClass = "rate_limit"
		case raw.Status == 402:
			rec.ErrorClass = "quota"
		case raw.Status == 401 || raw.Status == 403:
			rec.ErrorClass = "auth"
		case raw.Status >= 500:
			rec.ErrorClass = "upstream"
		case raw.Outcome == "client_canceled":
			rec.ErrorClass = "timeout"
		case reason != "":
			rec.ErrorClass = "other"
		}
		rec.ErrMsg = reason
	}
	return rec
}
