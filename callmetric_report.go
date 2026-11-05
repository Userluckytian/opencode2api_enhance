package main

// 调用指标聚合（v2.0.0 增补）：把逐条 CallMetric 汇总成可用于决策的视图。
//
// 四个视图对应四个问题：
//   summary     —— 近 N 天整体怎么样？（请求数 / 成功率 / 耗时 / token）
//   matrix      —— 哪个「模型 × 通道 × 密钥」组合最好？
//   keys        —— 每个密钥的健康度如何？该关哪个？
//   suggestions —— 基于上面数据，能给我什么**可执行**建议？
//
// 所有视图都只读指标文件、现算，不做缓存：数据量按天滚、单机使用量有限，
// 现算比维护缓存一致性更简单也更不容易错。

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/6Kmfi6HP/opencode2api/core/manager"
)

// ---- 视图结构 ----

// MetricSummary 总体概览。
type MetricSummary struct {
	Days int `json:"days"`
	// Requests 请求总数；OK / Fail / Canceled 为三种终态分布。
	Requests int `json:"requests"`
	OK       int `json:"ok"`
	Fail     int `json:"fail"`
	Canceled int `json:"canceled"`
	// OKRate 成功率（0~1，仅按 ok / (ok+fail+canceled) 计算）。
	OKRate float64 `json:"ok_rate"`
	// AvgMS / P50MS / P95MS 耗时分布（毫秒）。
	AvgMS float64 `json:"avg_ms"`
	P50MS int64   `json:"p50_ms"`
	P95MS int64   `json:"p95_ms"`
	// PromptTokens / CompletionTokens 累计用量。
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	// Rotated 触发 session 轮换的请求数。
	Rotated int `json:"rotated"`
	// Models / Keys 参与统计的模型数与密钥数。
	Models int `json:"models"`
	Keys   int `json:"keys"`
	// Sources 各来源条数（engine / provider）。
	Sources map[string]int `json:"sources,omitempty"`
	// ErrorClasses 失败原因分布。
	ErrorClasses map[string]int `json:"error_classes,omitempty"`
	// FirstTS / LastTS 数据时间跨度（空 = 无数据）。
	FirstTS string `json:"first_ts,omitempty"`
	LastTS  string `json:"last_ts,omitempty"`
}

// MetricMatrixCell 矩阵中的一格（一个「模型 × 通道 × 密钥」组合）。
type MetricMatrixCell struct {
	Model   string `json:"model"`
	Channel string `json:"channel"`
	KeyID   string `json:"key_id"`
	// Requests / OK / Fail / Canceled 该组合的请求分布。
	Requests int     `json:"requests"`
	OK       int     `json:"ok"`
	Fail     int     `json:"fail"`
	Canceled int     `json:"canceled"`
	OKRate   float64 `json:"ok_rate"`
	AvgMS    float64 `json:"avg_ms"`
	P95MS    int64   `json:"p95_ms"`
	// TopReason 该组合最常见的失败类别（ok 时为空）。
	TopReason string `json:"top_reason,omitempty"`
	// Rotated 该组合触发轮换的次数。
	Rotated int `json:"rotated"`
	// LastTS 最近一次调用时间。
	LastTS string `json:"last_ts,omitempty"`
}

// MetricMatrix 矩阵视图。
type MetricMatrix struct {
	Days int `json:"days"`
	// Cells 按请求数降序（最常用的组合排前面）。
	Cells []MetricMatrixCell `json:"cells"`
	// Models / Channels / Keys 涉及的维度取值（供前端做筛选器）。
	Models   []string `json:"models"`
	Channels []string `json:"channels"`
	Keys     []string `json:"keys"`
}

// MetricKeyStat 单密钥健康度。
type MetricKeyStat struct {
	KeyID string `json:"key_id"`
	// Channel 该 key 主要出现在哪个通道（anonymous 也作为一类 key_id）。
	Channel  string  `json:"channel,omitempty"`
	Requests int     `json:"requests"`
	OKRate   float64 `json:"ok_rate"`
	AvgMS    float64 `json:"avg_ms"`
	// FailReasons 失败类别 → 次数（该 key 专属）。
	FailReasons map[string]int `json:"fail_reasons,omitempty"`
	// Rotated 触发轮换次数。
	Rotated int `json:"rotated"`
	// Models 该 key 服务过的模型数。
	Models int `json:"models"`
	// FirstTS / LastTS 首次/最近使用时间。
	FirstTS string `json:"first_ts,omitempty"`
	LastTS  string `json:"last_ts,omitempty"`
}

// MetricKeyStats 密钥健康度视图。
type MetricKeyStats struct {
	Days int `json:"days"`
	// Keys 按请求数降序。
	Keys []MetricKeyStat `json:"keys"`
}

// MetricSuggestion 一条可执行建议。
type MetricSuggestion struct {
	// Level info / warn / action（前端据此着色）。
	Level string `json:"level"`
	// Text 一句话建议（面向用户，不出现内部术语）。
	Text string `json:"text"`
	// Detail 支撑该建议的数据摘要（便于用户核对）。
	Detail string `json:"detail,omitempty"`
}

// MetricSuggestions 建议视图。
type MetricSuggestions struct {
	Days        int                `json:"days"`
	Suggestions []MetricSuggestion `json:"suggestions"`
}

// ---- 聚合实现 ----

// metricWindow 把 days 换算成 [from, to] 区间；days<=0 时用 7 天。
func metricWindow(days int) (time.Time, time.Time) {
	if days <= 0 {
		days = 7
	}
	to := time.Now()
	from := to.AddDate(0, 0, -(days - 1))
	return from, to
}

// loadMetrics 读窗口内指标。
func loadMetrics(m *manager.Manager, days int) ([]manager.CallMetric, int) {
	if days <= 0 {
		days = 7
	}
	from, to := metricWindow(days)
	recs, err := m.ReadCallMetrics(from, to, 0)
	if err != nil {
		return nil, days
	}
	return recs, days
}

// okRateOf 计算成功率；总数为 0 时返回 0。
func okRateOf(ok, total int) float64 {
	if total <= 0 {
		return 0
	}
	return float64(ok) / float64(total)
}

// percentiles 计算 P50 / P95 与均值（输入为毫秒）。
//
// 用最近秩法（nearest-rank）：小样本下比线性插值更贴近真实观测值，
// 且不会出现「P95 比样本最大值还大」这种反直觉结果。
func percentiles(vals []int64) (avg float64, p50, p95 int64) {
	if len(vals) == 0 {
		return 0, 0, 0
	}
	sorted := make([]int64, len(vals))
	copy(sorted, vals)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	var sum int64
	for _, v := range sorted {
		sum += v
	}
	avg = float64(sum) / float64(len(sorted))

	rank := func(p float64) int64 {
		idx := int(float64(len(sorted))*p+0.999999) - 1
		if idx < 0 {
			idx = 0
		}
		if idx >= len(sorted) {
			idx = len(sorted) - 1
		}
		return sorted[idx]
	}
	return avg, rank(0.50), rank(0.95)
}

// buildSummary 生成总体概览。
func buildSummary(recs []manager.CallMetric, days int) MetricSummary {
	out := MetricSummary{Days: days, Sources: map[string]int{}, ErrorClasses: map[string]int{}}
	if len(recs) == 0 {
		return out
	}
	durs := make([]int64, 0, len(recs))
	models := map[string]struct{}{}
	keys := map[string]struct{}{}

	for _, r := range recs {
		out.Requests++
		switch r.Status {
		case "ok":
			out.OK++
		case "canceled":
			out.Canceled++
		default:
			out.Fail++
		}
		if r.DurationMS > 0 {
			durs = append(durs, r.DurationMS)
		}
		out.PromptTokens += r.PromptTokens
		out.CompletionTokens += r.CompletionTokens
		if r.Rotated {
			out.Rotated++
		}
		if r.Model != "" {
			models[r.Model] = struct{}{}
		}
		if r.KeyID != "" {
			keys[r.KeyID] = struct{}{}
		}
		if r.Source != "" {
			out.Sources[r.Source]++
		}
		if r.Status != "ok" && r.ErrorClass != "" {
			out.ErrorClasses[r.ErrorClass]++
		}
		if out.FirstTS == "" || r.TS < out.FirstTS {
			out.FirstTS = r.TS
		}
		if r.TS > out.LastTS {
			out.LastTS = r.TS
		}
	}
	out.OKRate = okRateOf(out.OK, out.Requests)
	out.AvgMS, out.P50MS, out.P95MS = percentiles(durs)
	out.Models = len(models)
	out.Keys = len(keys)
	return out
}

// matrixKey 矩阵分组键。
type matrixKey struct{ model, channel, key string }

// buildMatrix 生成「模型 × 通道 × 密钥」矩阵。
//
// 分组后每格独立算成功率 / 耗时 / 主因，按请求数降序。
func buildMatrix(recs []manager.CallMetric, days int) MetricMatrix {
	out := MetricMatrix{Days: days}
	type agg struct {
		ok, fail, canceled, rotated int
		durs                        []int64
		reasons                     map[string]int
		lastTS                      string
	}
	groups := map[matrixKey]*agg{}
	modelSet := map[string]struct{}{}
	channelSet := map[string]struct{}{}
	keySet := map[string]struct{}{}

	for _, r := range recs {
		k := matrixKey{
			model:   orDefault(r.Model, "(unknown)"),
			channel: orDefault(r.Channel, "(unknown)"),
			key:     orDefault(r.KeyID, "(none)"),
		}
		a := groups[k]
		if a == nil {
			a = &agg{reasons: map[string]int{}}
			groups[k] = a
		}
		switch r.Status {
		case "ok":
			a.ok++
		case "canceled":
			a.canceled++
		default:
			a.fail++
			if r.ErrorClass != "" {
				a.reasons[r.ErrorClass]++
			}
		}
		if r.DurationMS > 0 {
			a.durs = append(a.durs, r.DurationMS)
		}
		if r.Rotated {
			a.rotated++
		}
		if r.TS > a.lastTS {
			a.lastTS = r.TS
		}
		modelSet[k.model] = struct{}{}
		channelSet[k.channel] = struct{}{}
		keySet[k.key] = struct{}{}
	}

	cells := make([]MetricMatrixCell, 0, len(groups))
	for k, a := range groups {
		total := a.ok + a.fail + a.canceled
		avg, _, p95 := percentiles(a.durs)
		c := MetricMatrixCell{
			Model:     k.model,
			Channel:   k.channel,
			KeyID:     k.key,
			Requests:  total,
			OK:        a.ok,
			Fail:      a.fail,
			Canceled:  a.canceled,
			OKRate:    okRateOf(a.ok, total),
			AvgMS:     avg,
			P95MS:     p95,
			TopReason: topReasonOf(a.reasons),
			Rotated:   a.rotated,
			LastTS:    a.lastTS,
		}
		cells = append(cells, c)
	}
	sort.Slice(cells, func(i, j int) bool {
		if cells[i].Requests != cells[j].Requests {
			return cells[i].Requests > cells[j].Requests
		}
		if cells[i].Model != cells[j].Model {
			return cells[i].Model < cells[j].Model
		}
		return cells[i].KeyID < cells[j].KeyID
	})

	out.Cells = cells
	out.Models = sortedKeys(modelSet)
	out.Channels = sortedKeys(channelSet)
	out.Keys = sortedKeys(keySet)
	return out
}

// buildKeyStats 生成密钥健康度。
func buildKeyStats(recs []manager.CallMetric, days int) MetricKeyStats {
	out := MetricKeyStats{Days: days}
	type agg struct {
		ok, fail, canceled, rotated int
		durs                        []int64
		reasons                     map[string]int
		channel                     string
		firstTS, lastTS             string
		models                      map[string]struct{}
	}
	groups := map[string]*agg{}

	for _, r := range recs {
		kid := orDefault(r.KeyID, "(none)")
		a := groups[kid]
		if a == nil {
			a = &agg{reasons: map[string]int{}, models: map[string]struct{}{}}
			groups[kid] = a
		}
		switch r.Status {
		case "ok":
			a.ok++
		case "canceled":
			a.canceled++
		default:
			a.fail++
			if r.ErrorClass != "" {
				a.reasons[r.ErrorClass]++
			}
		}
		if r.DurationMS > 0 {
			a.durs = append(a.durs, r.DurationMS)
		}
		if r.Rotated {
			a.rotated++
		}
		if a.channel == "" && r.Channel != "" {
			a.channel = r.Channel
		}
		if r.Model != "" {
			a.models[r.Model] = struct{}{}
		}
		if a.firstTS == "" || r.TS < a.firstTS {
			a.firstTS = r.TS
		}
		if r.TS > a.lastTS {
			a.lastTS = r.TS
		}
	}

	keys := make([]MetricKeyStat, 0, len(groups))
	for kid, a := range groups {
		total := a.ok + a.fail + a.canceled
		avg, _, _ := percentiles(a.durs)
		keys = append(keys, MetricKeyStat{
			KeyID:       kid,
			Channel:     a.channel,
			Requests:    total,
			OKRate:      okRateOf(a.ok, total),
			AvgMS:       avg,
			FailReasons: a.reasons,
			Rotated:     a.rotated,
			Models:      len(a.models),
			FirstTS:     a.firstTS,
			LastTS:      a.lastTS,
		})
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Requests != keys[j].Requests {
			return keys[i].Requests > keys[j].Requests
		}
		return keys[i].KeyID < keys[j].KeyID
	})
	out.Keys = keys
	return out
}

// buildSuggestions 按计划里的规则表生成建议。
//
// 规则门槛（与计划文档一致，改动需同步文档）：
//   - 密钥 / 模型判定要求请求数 ≥ 10，避免小样本噪声；
//   - 密钥跨模型成功率差要求 ≥ 30 个百分点；
//   - 轮换比例阈值 30%；P95 阈值 60s。
func buildSuggestions(recs []manager.CallMetric, days int) MetricSuggestions {
	out := MetricSuggestions{Days: days, Suggestions: []MetricSuggestion{}}
	if len(recs) == 0 {
		out.Suggestions = append(out.Suggestions, MetricSuggestion{
			Level:  "info",
			Text:   fmt.Sprintf("近 %d 天没有任何调用记录。", days),
			Detail: "指标随调用产生；先用一次模型或等引擎跑起来再看。",
		})
		return out
	}

	const minSamples = 10

	// 规则 1 / 2：密钥 / 模型成功率过低
	keyStats := buildKeyStats(recs, days)
	for _, k := range keyStats.Keys {
		if k.KeyID == "anonymous" || strings.HasPrefix(k.KeyID, "(") {
			continue
		}
		if k.Requests >= minSamples && k.OKRate < 0.60 {
			out.Suggestions = append(out.Suggestions, MetricSuggestion{
				Level: "action",
				Text: fmt.Sprintf("密钥 %s 近 %d 天成功率仅 %.0f%%，建议降低优先级或更换。",
					k.KeyID, days, k.OKRate*100),
				Detail: fmt.Sprintf("请求 %d 次，失败主因 %s。", k.Requests, topReasonOf(k.FailReasons)),
			})
		}
	}

	matrix := buildMatrix(recs, days)
	// 规则 2：模型整体成功率过低
	modelAgg := map[string]struct {
		ok, total int
		reasons   map[string]int
	}{}
	for _, c := range matrix.Cells {
		cur := modelAgg[c.Model]
		if cur.reasons == nil {
			cur.reasons = map[string]int{}
		}
		cur.ok += c.OK
		cur.total += c.Requests
		if c.TopReason != "" {
			cur.reasons[c.TopReason] += c.Fail
		}
		modelAgg[c.Model] = cur
	}
	for model, a := range modelAgg {
		if a.total >= minSamples && okRateOf(a.ok, a.total) < 0.50 {
			out.Suggestions = append(out.Suggestions, MetricSuggestion{
				Level: "action",
				Text: fmt.Sprintf("模型 %s 近 %d 天成功率 %.0f%%，建议暂时排除。",
					model, days, okRateOf(a.ok, a.total)*100),
				Detail: fmt.Sprintf("请求 %d 次，失败主因 %s。", a.total, topReasonOf(a.reasons)),
			})
		}
	}

	// 规则 3：同一模型在不同 key 间成功率差异显著
	type modelKeyAgg struct{ ok, total int }
	mk := map[string]map[string]modelKeyAgg{}
	for _, c := range matrix.Cells {
		if c.KeyID == "" || strings.HasPrefix(c.KeyID, "(") {
			continue
		}
		if mk[c.Model] == nil {
			mk[c.Model] = map[string]modelKeyAgg{}
		}
		cur := mk[c.Model][c.KeyID]
		cur.ok += c.OK
		cur.total += c.Requests
		mk[c.Model][c.KeyID] = cur
	}
	for model, perKey := range mk {
		var bestKey, worstKey string
		bestRate, worstRate := -1.0, 2.0
		for kid, a := range perKey {
			if a.total < minSamples {
				continue
			}
			r := okRateOf(a.ok, a.total)
			if r > bestRate {
				bestRate, bestKey = r, kid
			}
			if r < worstRate {
				worstRate, worstKey = r, kid
			}
		}
		if bestKey != "" && worstKey != "" && bestKey != worstKey && bestRate-worstRate >= 0.30 {
			out.Suggestions = append(out.Suggestions, MetricSuggestion{
				Level: "warn",
				Text: fmt.Sprintf("模型 %s 在密钥 %s 上成功率（%.0f%%）显著高于 %s（%.0f%%），建议优先分配 %s。",
					model, bestKey, bestRate*100, worstKey, worstRate*100, bestKey),
				Detail: "成功率按近 " + fmt.Sprint(days) + " 天统计，样本均 ≥ " + fmt.Sprint(minSamples) + " 次。",
			})
		}
	}

	// 规则 4：轮换比例过高（免费额度消耗快）
	summary := buildSummary(recs, days)
	if summary.Requests > 0 {
		rotRate := float64(summary.Rotated) / float64(summary.Requests)
		if rotRate > 0.30 {
			out.Suggestions = append(out.Suggestions, MetricSuggestion{
				Level: "warn",
				Text: fmt.Sprintf("%.0f%% 的请求触发了 session 轮换，说明免费额度消耗较快；可考虑减少并发或增加密钥。",
					rotRate*100),
				Detail: fmt.Sprintf("%d / %d 次请求发生过轮换。", summary.Rotated, summary.Requests),
			})
		}
	}

	// 规则 5：P95 耗时偏高
	if summary.P95MS > 60000 {
		out.Suggestions = append(out.Suggestions, MetricSuggestion{
			Level: "warn",
			Text: fmt.Sprintf("P95 耗时 %.1fs，偏慢；建议到「引擎实时」看 WARP 出口是否在冷却。",
				float64(summary.P95MS)/1000),
			Detail: fmt.Sprintf("P50 %.1fs / 均值 %.1fs。",
				float64(summary.P50MS)/1000, summary.AvgMS/1000),
		})
	}

	// 全部正常时给一句确认，避免用户以为功能没工作
	if len(out.Suggestions) == 0 {
		out.Suggestions = append(out.Suggestions, MetricSuggestion{
			Level: "info",
			Text:  fmt.Sprintf("近 %d 天运行正常，暂未发现需要调整的地方。", days),
			Detail: fmt.Sprintf("共 %d 次调用，成功率 %.0f%%，P95 %.1fs。",
				summary.Requests, summary.OKRate*100, float64(summary.P95MS)/1000),
		})
	}
	return out
}

// ---- 小工具 ----

// orDefault 空串时返回兜底值（用于 group by 时避免空键）。
func orDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

// sortedKeys 返回排序后的键集合。
func sortedKeys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// topReasonOf 取出现次数最多的失败类别；无失败时返回空串。
func topReasonOf(reasons map[string]int) string {
	best, bestN := "", 0
	for r, n := range reasons {
		if n > bestN || (n == bestN && r < best) {
			best, bestN = r, n
		}
	}
	return best
}
