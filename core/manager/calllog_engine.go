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
//
// 关键：顶层 status 是**引擎自己的响应码**，不一定是上游真实状态。最典型的
// 是 outcome=client_canceled 时顶层恒为 499，而真正的原因藏在 legs 里
//（如 "Pwrg3:403:1129ms viYd3:403:1052ms" = 两次尝试都被上游 403）。
// 因此「上游状态 / 错误类别 / 错误文案」一律以 legs 的最后一跳为准，
// 顶层 status 只在 legs 为空时兜底。

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
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
	KeyID      string `json:"key_id"`
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
	// Legs 逐跳尝试链，格式 "<key>:<status>:<ms>"，空格分隔。
	// 空串 = 引擎未记录（早期版本或请求在选路前就失败）。
	Legs string `json:"legs"`

	// Proxy 轮换事件（session_identity_rotated）里的出口标识，形如
	// warp:2a09:bac5:1ef3:3046::/64 或 host:port。其它事件该字段为空。
	Proxy string `json:"proxy"`
}

// engineLeg 一跳尝试（一次上游请求）。
type engineLeg struct {
	Key    string
	Status int
	DurMS  int64
}

// EngineStdoutLogPath 返回引擎 stdout 日志路径（含 request_routed 事件）。
func (m *Manager) EngineStdoutLogPath() string {
	return filepath.Join(m.paths.FreeTierEngineDir(), "stdout.log")
}

// readEngineCallLog 从引擎 stdout 末尾解析调用事件，转成统一记录。
//
// max<=0 = 全部；max>0 = 只取最近 max 条。文件缺失/行损坏一律跳过，不报错
// （引擎未启动时该文件可能不存在）。
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

// parseEngineLegs 解析引擎的 legs 字段：`<key>:<status>:<ms>`，空格分隔。
// 无法识别的片段直接跳过（引擎未来改格式时退化为「无 leg 明细」而非报错）。
func parseEngineLegs(s string) []engineLeg {
	fields := strings.Fields(s)
	out := make([]engineLeg, 0, len(fields))
	for _, f := range fields {
		parts := strings.Split(f, ":")
		if len(parts) < 2 {
			continue
		}
		st, err := strconv.Atoi(parts[1])
		if err != nil {
			continue
		}
		leg := engineLeg{Key: parts[0], Status: st}
		if len(parts) >= 3 {
			ms := strings.TrimSuffix(parts[2], "ms")
			if v, err := strconv.ParseInt(ms, 10, 64); err == nil {
				leg.DurMS = v
			}
		}
		out = append(out, leg)
	}
	return out
}

// engineClassOf 把 HTTP 状态码归到统一错误类别。
func engineClassOf(status int) string {
	switch {
	case status == 429:
		return "rate_limit"
	case status == 402:
		return "quota"
	case status == 401 || status == 403:
		return "auth"
	case status == 408 || status == 504:
		return "timeout"
	case status >= 500:
		return "upstream"
	case status >= 400:
		return "other"
	default:
		return ""
	}
}

// engineRecordFromLine 把一行引擎事件映射为统一调用记录。
func engineRecordFromLine(raw engineLogLine) CallLogRecord {
	// outcome → status 文案（与供应商服务的 ok/fail/canceled 语义对齐）。
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

	legs := parseEngineLegs(raw.Legs)

	// 上游真实状态：取最后一跳；legs 为空才用顶层 status。
	//（顶层在 client_canceled 时恒为 499，会把「两次 403」误报成超时。）
	upstream := raw.Status
	for i := len(legs) - 1; i >= 0; i-- {
		if legs[i].Status > 0 {
			upstream = legs[i].Status
			break
		}
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
		UpstreamStatus:   upstream,
		SchemaVersion:    3,
	}
	// 厂商归属：引擎不带 vendor 字段，用 tier + channel 组合标识来源通道。
	// anonymous 通道记为 "opencode"，key 通道记为 "zen"/"go"（与 tiers 名称一致）。
	// 阶段 O：另存归一化的 Channel（warp/key），供日志页徽标与诊断页统计。
	switch {
	case raw.Anonymous || strings.EqualFold(raw.Channel, "anonymous"):
		rec.Vendor = "opencode"
		rec.Channel = "warp"
	case raw.Channel != "" || raw.Tier != "":
		if raw.Tier != "" {
			rec.Vendor = raw.Tier
		} else {
			rec.Vendor = "opencode"
		}
		rec.Channel = "key"
	default:
		rec.Vendor = "opencode"
		// 旧日志无 channel/tier/anonymous：留空，由前端按 vendor 兜底推断。
	}

	// 逐跳明细 → Events，让日志页时间线能看到每一跳发生了什么。
	rec.Events = engineEvents(raw, legs, status)

	// 失败原因：状态码优先（现在用的是上游真实状态），再退到引擎给的文字。
	if status != "ok" {
		rec.ErrorClass = engineClassOf(upstream)
		if rec.ErrorClass == "" {
			switch {
			case strings.TrimSpace(raw.RouteError) != "":
				rec.ErrorClass = "other"
			case raw.Outcome == "client_canceled":
				rec.ErrorClass = "timeout"
			}
		}
		rec.ErrMsg = engineErrText(raw, legs, upstream, status)
	}
	return rec
}

// engineEvents 把 legs 展开成前端可读的事件时间线。
//
// 前端 issueLabel / issueTagClass 按 event.type 出标签与配色，因此这里只用
// 它已认识的类型（complete / upstream_error），不新造枚举。
// 成功 = 单条 complete（与供应商服务写盘口径一致，前端 hasIssue 不视为异常）；
// 失败 = 每跳一条 upstream_error，让时间线能看出是「第几跳被谁挡了」。
func engineEvents(raw engineLogLine, legs []engineLeg, status string) []CallLogEvent {
	if status == "ok" {
		ev := CallLogEvent{Type: "complete", At: raw.Time, Detail: fmt.Sprintf("完成（%dms）", raw.DurationMS)}
		if len(legs) > 0 {
			ev.Node = legs[len(legs)-1].Key
		}
		return []CallLogEvent{ev}
	}

	// 引擎时间只精确到请求结束时刻；逐跳没有各自时间戳，用同一条 time 标出
	// 顺序即可（前端按数组序渲染，时间列相同不影响阅读）。
	at := raw.Time
	events := make([]CallLogEvent, 0, len(legs))
	for i, leg := range legs {
		ev := CallLogEvent{
			Type:   "upstream_error",
			Node:   leg.Key,
			At:     at,
			Status: leg.Status,
			Detail: fmt.Sprintf("第 %d 跳 HTTP %d（%dms）", i+1, leg.Status, leg.DurMS),
		}
		ev.ErrorClass = engineClassOf(leg.Status)
		events = append(events, ev)
	}
	// 客户端提前断开：不新造事件类型（前端 issueLabel 按类型出标签，多一类会
	// 把「上游 403」误标成「静默超时」），而是补在最后一跳的 detail 里。
	if raw.Outcome == "client_canceled" && len(events) > 0 {
		last := legs[len(legs)-1]
		if last.Status < 200 || last.Status >= 300 {
			events[len(events)-1].Detail += fmt.Sprintf("；客户端在 %dms 后提前断开（引擎收尾 %d）", raw.DurationMS, raw.Status)
		}
	}
	return events
}

// engineErrText 生成一句能直接读懂的错误文案。
//
// 引擎自己的 route_error / penalty_kind 常常是空串（例如上游 403 后客户端放弃），
// 直接透传会让日志页「错误:」后面空着、只能显示兜底「异常」。这里按 legs 合成。
func engineErrText(raw engineLogLine, legs []engineLeg, upstream int, status string) string {
	if v := strings.TrimSpace(raw.RouteError); v != "" {
		return v
	}
	if v := strings.TrimSpace(raw.Penalty); v != "" {
		return v
	}
	if len(legs) > 0 {
		parts := make([]string, 0, len(legs))
		for _, leg := range legs {
			parts = append(parts, fmt.Sprintf("%s→%d(%dms)", leg.Key, leg.Status, leg.DurMS))
		}
		head := fmt.Sprintf("上游 %d", upstream)
		switch len(legs) {
		case 1:
			head = fmt.Sprintf("上游 %d（1 次尝试）", upstream)
		default:
			head = fmt.Sprintf("上游 %d（%d 次尝试全部失败）", upstream, len(legs))
		}
		txt := head + "：" + strings.Join(parts, "、")
		if raw.Outcome == "client_canceled" {
			txt += fmt.Sprintf("；客户端在 %dms 后提前断开", raw.DurationMS)
		}
		return txt
	}
	if raw.Outcome == "client_canceled" {
		return fmt.Sprintf("客户端提前断开（%dms，引擎收尾 %d，上游未返回可用响应）", raw.DurationMS, raw.Status)
	}
	if upstream > 0 {
		return fmt.Sprintf("上游返回 %d", upstream)
	}
	return "引擎未给出原因"
}
