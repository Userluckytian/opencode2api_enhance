// auto 虚拟模型：网关侧选择器（打分 / 三策略 / 候选链 / 上下文护栏 / 实测反馈）。
//
// 请求 model:"auto" 时在免费模型全集里按「用户权重 × 实测成功率」打分选主选并生成
// 降级链（ctx 传递，厂商 failover 之上叠加模型级降级，客户端无感）。实例维度不显式
// 指定——由既有 smart/failover 池逻辑（质量加权 + 熔断 + 竞速）负责（2026-08-17 决策②）。
//
// 上下文护栏（三道防线）：请求侧保守 token 估算；模型侧有效上限 = min(用户配置,
// 失败学习值, 保守默认 128k)；候选资格 = est ≤ 上限×0.9，整条链在构建时完成过滤。
// 未配置上下文且无学习值的模型按保守默认处理——长对话自动避开它们，短对话不受影响。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/6Kmfi6HP/opencode2api/core/manager"
)

// defaultAutoContextTokens 未配置上下文上限的模型的保守默认（200k，2026-08-26 由 128k 调整）。
const defaultAutoContextTokens = 200000

// maxAutoSwitches 单次请求最多沿降级链切换的模型数（有界重试，防长链打爆上游）。
const maxAutoSwitches = 3

// autoFeedbackWindowSec 模型×实例反馈滑窗（与池反馈窗口一致的 10 分钟）。
const autoFeedbackWindowSec = 600

// autoFeedbackPerKeyCap 每个模型×实例键的样本上限（防高频调用下内存无界增长）。
const autoFeedbackPerKeyCap = 64

// ======================== 配置（热重载） ========================

var (
	autoCfgMu sync.RWMutex
	autoCfg   = manager.AutoModelCfg{Strategy: manager.AutoStrategyBalanced}
)

// setAutoConfig 由 applyConfig 调用（配置加载/3s 热重载）：nil 回默认（关闭+balanced）。
func setAutoConfig(cfg *manager.AutoModelCfg) {
	c := manager.AutoModelCfg{Strategy: manager.AutoStrategyBalanced}
	if cfg != nil {
		c = *cfg
		c.Normalize()
	}
	autoCfgMu.Lock()
	autoCfg = c
	autoCfgMu.Unlock()
}

func autoConfig() manager.AutoModelCfg {
	autoCfgMu.RLock()
	defer autoCfgMu.RUnlock()
	return autoCfg
}

func autoEnabled() bool {
	autoCfgMu.RLock()
	defer autoCfgMu.RUnlock()
	return autoCfg.Enabled
}

// autoModelName 当前虚拟模型对外名称（默认 "auto"，可自定义避免与其它模型名冲突）。
func autoModelName() string {
	n := autoConfig().Name
	if n == "" {
		return "auto"
	}
	return n
}

// isAutoModelName 判定请求模型名是否为当前虚拟模型名（大小写不敏感、容忍空白）。
// 仅匹配配置的名称——用户自定义名称后，原 "auto" 不再被本网关认作虚拟模型（避免冲突）。
func isAutoModelName(model string) bool {
	return strings.EqualFold(strings.TrimSpace(model), autoModelName())
}

// lastNode 调用记录里最后使用的节点（流内切换后 Nodes 会追加，取末位即最终实例）。
func lastNode(rec CallRecord) string {
	if n := len(rec.Nodes); n > 0 {
		return rec.Nodes[n-1]
	}
	return ""
}

// ======================== 候选与打分 ========================

// autoCand 一个可参与的候选模型（权重按模型粒度；实例维度交给池路由）。
type autoCand struct {
	Upstream string  // 上游真实模型 id（含 -free）
	Display  string  // /v1/models 展示名（权重/上下文配置的键）
	Weight   int     // 用户权重 0~10
	SR       float64 // 实测成功率（无样本 = 1.0）
	AvgMS    int64   // 实测均延迟（无样本 = 0，未知）
	Limit    int     // 有效上下文上限（token）
	Score    float64 // (Weight/10) × SR
}

// autoDecision 一次 auto 请求的完整决策（挂 ctx 供降级循环与日志使用）。
type autoDecision struct {
	Strategy        string
	EstTokens       int64
	ContextFallback bool     // 全候选超限，按最大上下文兜底
	Chain           []autoCand // Chain[0] = 主选；其余为降级序
	FinalModel      string   // 最终实际尝试的模型（降级循环回写，供失败学习/日志）
}

type autoCtxKey struct{}

// prepareAuto 入口拦截：auto → 选主选模型 + 生成降级链挂 ctx。
// 非 auto / 未启用 / 无候选：原样返回（dec = nil，调用方零开销路径）。
// auto 已启用但无可用候选（未勾选模型 / 全部权重 0 / 目录为空）时返回明确错误，
// 调用方应直接回客户端（不落到默认厂商撞 502/404——2026-08-26 方案 C）。
func prepareAuto(ctx context.Context, model string, upstreamBody []byte) (context.Context, string, *autoDecision, error) {
	if !isAutoModelName(model) || !autoEnabled() {
		return ctx, model, nil, nil
	}
	est := estimateRequestTokens(upstreamBody)
	chain, fallback := buildAutoChain(est)
	if len(chain) == 0 {
		name := autoModelName()
		return ctx, model, nil, fmt.Errorf("%s 模型下没有可用模型，请先配置参与 %s 的模型", name, name)
	}
	dec := &autoDecision{
		Strategy:        autoConfig().Strategy,
		EstTokens:       est,
		ContextFallback: fallback,
		Chain:           chain,
	}
	return context.WithValue(ctx, autoCtxKey{}, dec), chain[0].Upstream, dec, nil
}

// autoNextModel 降级循环取下一个候选；switched 为已切换次数。
func autoNextModel(ctx context.Context, switched int) (string, bool) {
	dec, _ := ctx.Value(autoCtxKey{}).(*autoDecision)
	if dec == nil || switched >= maxAutoSwitches {
		return "", false
	}
	if 1+switched >= len(dec.Chain) {
		return "", false
	}
	return dec.Chain[1+switched].Upstream, true
}

// recordAutoAttempt auto 请求的尝试级反馈（ctx 无决策 = 非 auto 请求，不记录，
// 由 handler 侧记录，避免双计）。
func recordAutoAttempt(ctx context.Context, model, addr string, status int, ms int64) {
	dec, _ := ctx.Value(autoCtxKey{}).(*autoDecision)
	if dec == nil {
		return
	}
	recordModelFeedback(model, addr, status >= 200 && status < 300, ms)
}

// pickEvent 生成调用日志的 auto_pick 事件（决策可解释：为什么选它、链是什么）。
func (d *autoDecision) pickEvent() CallEvent {
	names := make([]string, 0, len(d.Chain))
	for i, c := range d.Chain {
		if i >= 4 { // 链可能较长，日志只留前几个
			names = append(names, "…")
			break
		}
		names = append(names, c.Display)
	}
	var detail string
	if d.ContextFallback {
		detail = fmt.Sprintf("auto→%s（%s，est=%dtok 超限，按最大上下文兜底，chain=%s）",
			d.Chain[0].Display, d.Strategy, d.EstTokens, strings.Join(names, "→"))
	} else {
		detail = fmt.Sprintf("auto→%s（%s，est=%dtok，w=%d，sr=%.2f，chain=%s）",
			d.Chain[0].Display, d.Strategy, d.EstTokens, d.Chain[0].Weight, d.Chain[0].SR, strings.Join(names, "→"))
	}
	return CallEvent{Type: "auto_pick", Detail: detail, At: time.Now()}
}

// buildAutoChain 构建有序候选链（含主选）。返回 fallback = 全候选上下文超限。
func buildAutoChain(est int64) ([]autoCand, bool) {
	cfg := autoConfig()
	all := autoCandidateModels(cfg)
	if len(all) == 0 {
		return nil, false
	}
	// 上下文护栏：整链过滤（主选与全部降级候选都必须装得下这次对话）。
	elig := make([]autoCand, 0, len(all))
	for _, c := range all {
		if est <= int64(float64(c.Limit)*0.9) {
			elig = append(elig, c)
		}
	}
	fallback := false
	if len(elig) == 0 {
		fallback = true
		// 全部超限：按上下文降序兜底（估算偏保守时把最终裁决交给上游）。
		elig = append(elig, all...)
		sort.SliceStable(elig, func(i, j int) bool {
			if elig[i].Limit != elig[j].Limit {
				return elig[i].Limit > elig[j].Limit
			}
			return elig[i].Score > elig[j].Score
		})
	}

	switch cfg.Strategy {
	case manager.AutoStrategySpeed:
		return autoChainSpeed(elig), fallback
	case manager.AutoStrategyQuality:
		return elig, fallback // 已按分降序：锁定权重最高者，失败沿链降
	default: // balanced：SWRR 主选 + 其余按分降序
		return autoChainBalanced(elig), fallback
	}
}

// autoChainSpeed 速度优先：权重≥5 的候选里选实测延迟最低者；无实测数据回退按分。
func autoChainSpeed(elig []autoCand) []autoCand {
	pool := make([]autoCand, 0, len(elig))
	for _, c := range elig {
		if c.Weight >= 5 {
			pool = append(pool, c)
		}
	}
	if len(pool) == 0 {
		pool = elig
	}
	pick := 0
	for i, c := range pool {
		if c.AvgMS > 0 && (pool[pick].AvgMS == 0 || c.AvgMS < pool[pick].AvgMS) {
			pick = i
		}
	}
	chain := make([]autoCand, 0, len(elig))
	chain = append(chain, pool[pick])
	for _, c := range elig {
		if c.Upstream != pool[pick].Upstream {
			chain = append(chain, c)
		}
	}
	return chain
}

// autoChainBalanced 均衡：SWRR（平滑加权轮询）选主选，其余按分降序做降级链。
// 分布 ∝ 分值 → 高权重多承接、低权重保底，额度消耗均匀（速度与能力按用户标定的比例分摊）。
func autoChainBalanced(elig []autoCand) []autoCand {
	if len(elig) == 0 {
		return elig
	}
	i := autoSWRRPick(elig)
	chain := make([]autoCand, 0, len(elig))
	chain = append(chain, elig[i])
	for j, c := range elig {
		if j != i {
			chain = append(chain, c)
		}
	}
	return chain
}

// autoCandidateModels 收集候选全集：与 /v1/models 同语义的可见免费模型中**已勾选
//（Models 白名单）**的条目，按（分, 权重, 名）降序。权重/上下文按可见名（/v1/models 名，
// 含供应商前缀）查；缺省 5；0 = 排除。白名单为空或全未命中 → 无候选（调用方按「未配置」
// 返回明确错误）。候选覆盖全部供应商（opencode 内建 + 自定义/插件，2026-08-26 修复）。
func autoCandidateModels(cfg manager.AutoModelCfg) []autoCand {
	whitelist := make(map[string]bool, len(cfg.Models))
	for _, m := range cfg.Models {
		whitelist[strings.TrimSpace(m)] = true
	}
	visible := visibleFreeModels(globalAgg)
	out := make([]autoCand, 0, len(visible))
	seen := map[string]bool{}
	for _, m := range visible {
		if seen[m.Raw] || !autoWhitelisted(m, whitelist) {
			continue
		}
		seen[m.Raw] = true
		w := 5
		if v, ok := cfg.Weights[m.Visible]; ok {
			w = v
		}
		if w <= 0 {
			continue
		}
		sr, ms := modelFeedbackStats(m.Raw)
		out = append(out, autoCand{
			Upstream: m.Raw,
			Display:  m.Visible,
			Weight:   w,
			SR:       sr,
			AvgMS:    ms,
			Limit:    effContextLimit(cfg, m.Visible),
			Score:    float64(w) / 10 * sr,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if out[i].Weight != out[j].Weight {
			return out[i].Weight > out[j].Weight
		}
		return out[i].Display < out[j].Display
	})
	return out
}

// autoWhitelisted 判定可见模型是否命中白名单：可见名（含前缀）或「厂商前缀+原始名」
// 任一命中即可——供应商前缀是否出现取决于目录是否同名冲突，两态都认，防配置失效
// （2026-08-26 命名空间修复的鲁棒性补充）。
func autoWhitelisted(m visibleFreeModel, whitelist map[string]bool) bool {
	if whitelist[m.Visible] {
		return true
	}
	return whitelist[m.Provider+"/"+m.Raw]
}

// autoHasCandidates 是否存在可参与 auto 的候选（白名单 ∩ 可见免费模型 ∩ 权重>0）。
// 供 /v1/models 决定是否展示虚拟模型：无候选时不展示，避免客户端请求一个必然报错的模型名。
func autoHasCandidates() bool {
	cfg := autoConfig()
	if !cfg.Enabled || len(cfg.Models) == 0 {
		return false
	}
	whitelist := make(map[string]bool, len(cfg.Models))
	for _, m := range cfg.Models {
		whitelist[strings.TrimSpace(m)] = true
	}
	for _, m := range visibleFreeModels(globalAgg) {
		if !autoWhitelisted(m, whitelist) {
			continue
		}
		w := 5
		if v, ok := cfg.Weights[m.Visible]; ok {
			w = v
		}
		if w > 0 {
			return true
		}
	}
	return false
}

// effContextLimit 模型有效上下文上限：用户配置 > 失败学习值 > 保守默认，取最小。
func effContextLimit(cfg manager.AutoModelCfg, display string) int {
	limit := 0
	if n, ok := cfg.ContextWindows[display]; ok && n > 0 {
		limit = n
	}
	if limit == 0 {
		limit = defaultAutoContextTokens
	}
	if learned := autoLearnedLimit(display); learned > 0 && learned < limit {
		limit = learned
	}
	return limit
}

// ======================== 上下文失败学习 ========================

var (
	autoLearnedMu     sync.Mutex
	autoLearnedUpper  = map[string]int{} // 展示名 → 已知会失败时的估算 token（收紧上限）
)

// learnContextFailure 上游报上下文类错误时收紧该模型的学习上限（取历史最小）。
func learnContextFailure(display string, est int64) {
	if display == "" || est <= 0 {
		return
	}
	autoLearnedMu.Lock()
	defer autoLearnedMu.Unlock()
	if cur, ok := autoLearnedUpper[display]; !ok || est < int64(cur) {
		autoLearnedUpper[display] = int(est)
	}
}

func autoLearnedLimit(display string) int {
	autoLearnedMu.Lock()
	defer autoLearnedMu.Unlock()
	return autoLearnedUpper[display]
}

// isContextLimitError 粗判上游错误体是否为上下文超限类（多供应商文案收敛）。
func isContextLimitError(body []byte) bool {
	low := strings.ToLower(string(body))
	for _, marker := range []string{"context length", "context window", "maximum context", "too many tokens", "token limit", "context_length_exceeded"} {
		if strings.Contains(low, marker) {
			return true
		}
	}
	return false
}

// ======================== token 估算（请求侧，保守） ========================

// estimateRequestTokens 估算请求 token（仅 auto 请求执行，一次额外 JSON 解析）。
// est = Σ 每条消息 max(字节/4, 字符/2) + 每条 8 开销——对中文（≈0.7 tok/字）与
// 英文（≈0.25 tok/字）均偏保守：高估只会偏向更大上下文的模型，不会漏判超限。
// 非文本部件（image_url 等二进制）不计入（上游对图像按视觉 token 另计）。
func estimateRequestTokens(body []byte) int64 {
	var req struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &req) != nil {
		return 0
	}
	var est int64
	for _, m := range req.Messages {
		est += countContentTokens(m.Content) + 8
	}
	return est
}

func countContentTokens(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return textTokenEstimate(s)
	}
	var parts []map[string]any
	if json.Unmarshal(raw, &parts) == nil {
		var t int64
		for _, p := range parts {
			if ty, _ := p["type"].(string); ty != "text" {
				continue
			}
			if txt, _ := p["text"].(string); txt != "" {
				t += textTokenEstimate(txt)
			}
		}
		return t
	}
	return 0
}

func textTokenEstimate(s string) int64 {
	byByte := int64(len(s)) / 4
	byRune := int64(utf8.RuneCountInString(s)) / 2
	if byRune > byByte {
		return byRune
	}
	return byByte
}

// ======================== SWRR（平滑加权轮询，nginx 同款） ========================

var (
	autoSWRRMu  sync.Mutex
	autoSWRRCur = map[string]int{} // 展示名 → 当前权重
)

// autoSWRRPick 平滑加权轮询选一个候选（分布 ∝ Score）。候选集变化时天然适应
// （分数随健康/反馈波动即权重波动）；陈旧键在超过 3 倍候选数时修剪。
func autoSWRRPick(cands []autoCand) int {
	autoSWRRMu.Lock()
	defer autoSWRRMu.Unlock()
	total := 0
	ws := make([]int, len(cands))
	for i, c := range cands {
		ws[i] = int(c.Score*100) + 1 // +1：全零分（全部 sr=0）时仍可轮询
		total += ws[i]
	}
	best, bestCur := 0, 0
	for i, c := range cands {
		cur := autoSWRRCur[c.Display] + ws[i]
		autoSWRRCur[c.Display] = cur
		if i == 0 || cur > bestCur {
			best, bestCur = i, cur
		}
	}
	autoSWRRCur[cands[best].Display] -= total
	if len(autoSWRRCur) > 3*len(cands)+8 {
		keep := make(map[string]int, len(cands))
		for _, c := range cands {
			keep[c.Display] = autoSWRRCur[c.Display]
		}
		autoSWRRCur = keep
	}
	return best
}

// ======================== 模型×实例反馈滑窗 ========================

type modelFbSample struct {
	ok bool
	ms int64
	ts int64
}

var (
	modelFbMu sync.Mutex
	modelFb   = map[string][]modelFbSample{} // 键：model + "\x1f" + 实例 socks addr
)

// recordModelFeedback 记一次模型（在某实例上的）请求结果，滑窗修剪 + 每键封顶。
func recordModelFeedback(model, addr string, ok bool, ms int64) {
	if model == "" || addr == "" || isAutoModelName(model) {
		return
	}
	now := time.Now().Unix()
	key := model + "\x1f" + addr
	modelFbMu.Lock()
	defer modelFbMu.Unlock()
	s := append(modelFb[key], modelFbSample{ok: ok, ms: ms, ts: now})
	cutoff := now - autoFeedbackWindowSec
	keep := s[:0]
	for _, x := range s {
		if x.ts >= cutoff {
			keep = append(keep, x)
		}
	}
	if len(keep) > autoFeedbackPerKeyCap {
		keep = keep[len(keep)-autoFeedbackPerKeyCap:]
	}
	modelFb[key] = keep
}

// modelFeedbackStats 模型跨实例聚合：窗口内成功率与成功样本均延迟。
// 无样本 → (1.0, 0)：不惩罚新模型/冷启动（首败即降由降级链兜底）。
func modelFeedbackStats(model string) (sr float64, avgMS int64) {
	if model == "" {
		return 1, 0
	}
	now := time.Now().Unix()
	cutoff := now - autoFeedbackWindowSec
	prefix := model + "\x1f"
	modelFbMu.Lock()
	defer modelFbMu.Unlock()
	total, okN, msSum, msN := 0, 0, int64(0), 0
	for k, samples := range modelFb {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		for _, x := range samples {
			if x.ts < cutoff {
				continue
			}
			total++
			if x.ok {
				okN++
				if x.ms > 0 {
					msSum += x.ms
					msN++
				}
			}
		}
	}
	if total == 0 {
		return 1, 0
	}
	if msN > 0 {
		return float64(okN) / float64(total), msSum / int64(msN)
	}
	return float64(okN) / float64(total), 0
}
