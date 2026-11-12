package manager

// 阶段 O：OpenCode 引擎通道统计（WARP 匿名 / key）。
//
// 数据来自现有 ReadCallLog(0) 全量日志，进程内现算、不落盘、不改写入侧。
// 通道归一化的单一真源是 CallLogRecord.Channel（calllog_engine.go 从 stdout
// 的 channel/anonymous/tier 推导）；旧日志无该字段时按 vendor 兜底推断，
// 并单独计入 inferred，避免把「推断」伪装成「实测」。

import (
	"net/http"
	"time"
)

// ChannelBucket 单个通道的聚合结果。
type ChannelBucket struct {
	Requests int `json:"requests"`
	OK       int `json:"ok"`
	Fail     int `json:"fail"`
}

// ChannelStats 通道统计响应。
type ChannelStats struct {
	Total       int           `json:"total"`        // 全部调用日志条数
	EngineTotal int           `json:"engine_total"` // 引擎记录条数 = warp+key（占比分母）
	Warp        ChannelBucket `json:"warp"`
	Key         ChannelBucket `json:"key"`
	Other       ChannelBucket `json:"other"`
	Inferred    int           `json:"inferred"`     // 旧日志按 vendor 推断的条数
	GeneratedAt string        `json:"generated_at"` // RFC3339
}

// channelOf 把一条调用日志归到 "warp" / "key" / "other"，并标注是否推断。
// 只把 OpenCode 引擎记录纳入 warp/key：Kilo / 自定义源一律 other。
func channelOf(r *CallLogRecord) (string, bool) {
	if r.Channel == "warp" || r.Channel == "key" {
		return r.Channel, false
	}
	// 旧日志无 Channel：只认明确来自 OpenCode 引擎的记录。
	// ReadCallLog 会覆写 Source（引擎="OpenCode 引擎"，JSONL="供应商服务"/
	// "Kilo 独立网关"），故无需放宽到 Source==""——那样反而可能把恰好叫
	// opencode 的自定义源误算进 WARP（独立审查 P2 #2）。
	isEngine := r.Source == "OpenCode 引擎" && (r.Vendor == "opencode" || r.Vendor == "zen" || r.Vendor == "go")
	if !isEngine {
		return "other", false
	}
	switch r.Vendor {
	case "opencode":
		return "warp", true // 引擎 anonymous 通道的 vendor 就是 opencode
	case "zen", "go":
		return "key", true
	default:
		return "other", false
	}
}

// ComputeChannelStats 从调用日志现算通道分布。
func (m *Manager) ComputeChannelStats() ChannelStats {
	st := ChannelStats{GeneratedAt: time.Now().Format(time.RFC3339)}
	for _, r := range m.ReadCallLog(0) {
		st.Total++
		bucket, inferred := channelOf(&r)
		if inferred {
			st.Inferred++
		}
		var b *ChannelBucket
		switch bucket {
		case "warp":
			b = &st.Warp
		case "key":
			b = &st.Key
		default:
			b = &st.Other
		}
		b.Requests++
		if r.Status == "ok" {
			b.OK++
		} else {
			b.Fail++
		}
	}
	st.EngineTotal = st.Warp.Requests + st.Key.Requests
	return st
}

// ChannelStatsHandler GET /api/admin/channel-stats（只读）。
func (m *Manager) ChannelStatsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethod(w, r, http.MethodGet) {
			return
		}
		writeJSON(w, m.ComputeChannelStats())
	}
}
