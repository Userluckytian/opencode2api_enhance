// 自定义模型源 key 池：一源多 key 的调度与健康状态。
//
// 三种调度（providers[].params.key_strategy，仅作用于自定义源，与实例池 route_mode 无关）：
//   - round_robin（默认）：可用 key 间均匀轮询
//   - failover          ：按配置顺序优先（主 key 可用时恒用它，冷却/禁用才降级备用）
//   - health            ：加权轮询——成功率（EWMA）越高的 key 被选中概率越大，
//     但仍覆盖全池；样本不足的 key 有探索加成。冷却/熔断/禁用的 key 不参与。
//     （旧实现按「成功率→样本数」排序取第一名，结果第一个有样本的 key 恒胜，
//     整个池退化成单 key、其余额度闲置。）
//
// 健康语义：429/限流 → 冷却（Retry-After，缺省 60s）到期自动回池；
// 401/403（key 失效）→ 按 key_403_cooldown 冷却（缺省 600s）到期自动回池重试；
// key_403_cooldown=0 时退化为旧行为：永久禁用（运行期内不再使用，
// 编辑保存重建或手动「恢复全部 Key」后解除）。
// 每次请求记录成功/失败与延迟（recordResult），供 health 策略排序；其余策略不依赖计数。
package custom

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 调度策略取值。
const (
	StrategyRoundRobin = "round_robin"
	StrategyFailover   = "failover"
	StrategyHealth     = "health"
)

// defaultKeyCooldown 429 缺省冷却时长（无 Retry-After 时）。
const defaultKeyCooldown = 60 * time.Second

// defaultKeyAuthCooldown 401/403 缺省冷却时长（key_403_cooldown 未配置时）。
// 与旧「永久禁用」不同：到期自动回池重试，订阅/风控恢复后无需人工介入。
const defaultKeyAuthCooldown = 600 * time.Second

// circuitOpenThreshold 连续失败熔断阈值（对齐 model-gateway：连续 3 次失败即熔断）。
const circuitOpenThreshold = 3

// circuitCooldown 熔断冷却时长：到期自动重试，再次连续失败会重新熔断。
const circuitCooldown = 30 * time.Second

// 健康分数平滑系数（EWMA）：样本越多，单次波动影响越小。
const healthAlpha = 0.3

// 加权轮询参数（health 策略）：
//   - healthExploreSamples：样本数低于它的 key 权重放大，保证新 key 有机会被验证；
//   - healthExploreBoost：放大倍数；
//   - healthWeightFloor：成功率极低的 key 仍保留极小概率，避免永久饿死；
//   - healthGoldenRatio：低差异序列的步长（黄金比例小数部分），让连续的 rr
//     均匀铺满权重轴，而不是像 `rr/1000` 那样前几百次全挤在轴的一端。
//
// 早先的实现是「按成功率/样本数排序取第一名」，结果是第一个有样本的 key 恒胜
// （samples 降序），整个池退化成单 key、其余额度全闲置。改成加权轮询后既能
// 优先用表现好的 key，又保证覆盖全池。
const (
	healthExploreSamples = 3
	healthExploreBoost   = 2.0
	healthWeightFloor    = 0.01
	healthGoldenRatio    = 0.6180339887498949
)

// keyState 单个 key 的运行时状态。
type keyState struct {
	value        string
	coolingUntil time.Time
	disabled     bool // key_403_cooldown=0 时 401/403 → 永久禁用（运行期内不再使用）
	authCoolingUntil time.Time // 401/403 冷却到期时间（key_403_cooldown>0 时使用）
	consecFails  int  // 连续失败次数（熔断：达阈值进入 circuitUntil 冷却）
	consec403    int  // 连续 403 次数（软失败：达 authSoftThreshold 才按失效处理）
	circuitUntil time.Time
	samples      int  // 已记录请求次数（health 策略排序用）
	okRate       float64 // 成功率 EWMA，初始乐观 1.0（无数据视为可用）
	latEWMA      float64 // 成功请求平均延迟（毫秒）EWMA，仅成功时更新
}

// available 该 key 当前是否可用（未禁用、不在限流冷却/401-403冷却/熔断期）。
func (k *keyState) available(now time.Time) bool {
	return !k.disabled && !now.Before(k.coolingUntil) && !now.Before(k.authCoolingUntil) && !now.Before(k.circuitUntil)
}

// KeyPoolStatus key 健康计数（供 UI 展示；状态快照，非用量统计）。
type KeyPoolStatus struct {
	Total     int `json:"total"`
	Available int `json:"available"`
	Cooling   int `json:"cooling"`        // 429/限流冷却中
	AuthCooling int `json:"auth_cooling"` // 401/403 冷却中（到期自动恢复）
	CircuitOpen int `json:"circuit_open"` // 连续失败熔断中（阈值后自动重试）
	Disabled  int `json:"disabled"`       // key_403_cooldown=0 时被永久禁用
}

// keyPool 一源一池。
type keyPool struct {
	mu       sync.Mutex
	keys     []*keyState
	strategy string
	rr       uint64 // round_robin 游标
	nowFn    func() time.Time
	// key403Cooldown 401/403 冷却时长；0 = 退化为永久禁用（旧行为）。
	key403Cooldown time.Duration
}

// newKeyPool 构造 key 池。keys 为空 = 无鉴权源（单一空 key，恒可用）。
func newKeyPool(keys []string, strategy string) *keyPool {
	return newKeyPoolCooldown(keys, strategy, 0)
}

// newKeyPoolCooldown 构造 key 池并指定 401/403 冷却时长（>0 冷却自动恢复；0 = 永久禁用）。
func newKeyPoolCooldown(keys []string, strategy string, key403Cooldown time.Duration) *keyPool {
	switch strategy {
	case StrategyFailover:
	case StrategyHealth:
	default:
		strategy = StrategyRoundRobin
	}
	pool := &keyPool{strategy: strategy, nowFn: time.Now, key403Cooldown: key403Cooldown}
	if len(keys) == 0 {
		pool.keys = []*keyState{{value: "", okRate: 1.0}}
		return pool
	}
	seen := map[string]bool{}
	for _, k := range keys {
		k = strings.TrimSpace(k)
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		pool.keys = append(pool.keys, &keyState{value: k, okRate: 1.0})
	}
	if len(pool.keys) == 0 {
		pool.keys = []*keyState{{value: "", okRate: 1.0}}
	}
	return pool
}

// tryAcquire 挑一个可用且本次请求未试过的 key（tried 记录池内下标）。
// 无可挑返回空串与 false。round_robin 从游标处找；failover 恒从头找（保持粘主）。
func (p *keyPool) tryAcquire(tried map[int]bool) (string, int, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.tryAcquireLocked(tried, p.nowFn())
}

// tryAcquireSticky 在 tryAcquire 基础上支持会话粘性：stickyIdx >= 0 且可用、
// 本请求未试过时优先命中该 key（续写同 key / 会话级粘性），否则回退全池普通调度。
// 命中 sticky 不推进 round_robin 游标——同一会话的续写重连稳定落在同一 key，
// 避免重复输出/串对话。
//
// 历史：早期还有「按模型亲和子集优先」的 tryAcquirePreferSticky，但它会让路由
// 只在被采样的少数 key 上轮询（309 key 的源实际只用 8 个）。亲和已移除，见
// docs/issue-log/2026-11-20.md #1。
func (p *keyPool) tryAcquireSticky(tried map[int]bool, stickyIdx int) (string, int, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.nowFn()
	if stickyIdx >= 0 && stickyIdx < len(p.keys) && !tried[stickyIdx] && p.keys[stickyIdx].available(now) {
		return p.keys[stickyIdx].value, stickyIdx, true
	}
	return p.tryAcquireLocked(tried, now)
}

// tryAcquireLocked 全池普通挑选（调用方已持 p.mu；now 为当前时间快照）。
func (p *keyPool) tryAcquireLocked(tried map[int]bool, now time.Time) (string, int, bool) {
	n := len(p.keys)
	if n == 0 {
		return "", -1, false
	}
	if p.strategy == StrategyHealth {
		return p.pickHealthLocked(tried, now)
	}
	start := 0
	if p.strategy == StrategyRoundRobin {
		start = int(p.rr % uint64(n))
	}
	for off := 0; off < n; off++ {
		i := (start + off) % n
		if tried[i] || !p.keys[i].available(now) {
			continue
		}
		if p.strategy == StrategyRoundRobin {
			p.rr++
		}
		return p.keys[i].value, i, true
	}
	return "", -1, false
}

// pickHealthLocked health 策略：按成功率加权的轮询（调用方已持 p.mu）。
// 成功率越高被选中概率越大，但仍覆盖全池；样本不足的 key 有探索加成。
func (p *keyPool) pickHealthLocked(tried map[int]bool, now time.Time) (string, int, bool) {
	idx, ok := p.weightedPickLocked(tried, now)
	if !ok {
		return "", -1, false
	}
	return p.keys[idx].value, idx, true
}

// weightedPickLocked 在可用且未试过的 key 上做加权抽签（权重轴由 rr 游标推进）。
// 不用随机数：同一 rr 序列可复现，测试与排障都更稳定。
func (p *keyPool) weightedPickLocked(tried map[int]bool, now time.Time) (int, bool) {
	type cand struct {
		idx int
		w   float64
	}
	cands := make([]cand, 0, len(p.keys))
	total := 0.0
	for i, k := range p.keys {
		if tried[i] || !k.available(now) {
			continue
		}
		w := k.okRate
		if k.samples < healthExploreSamples {
			w *= healthExploreBoost
		}
		if w < healthWeightFloor {
			w = healthWeightFloor
		}
		cands = append(cands, cand{i, w})
		total += w
	}
	if len(cands) == 0 {
		return -1, false
	}
	p.rr++
	// 低差异序列（黄金比例）取点：连续 rr 在权重轴上均匀分布，且不依赖随机数，
	// 同一 rr 序列可复现（测试与排障稳定）。
	frac := float64(p.rr) * healthGoldenRatio
	frac -= math.Floor(frac)
	pos := frac * total
	acc := 0.0
	for _, c := range cands {
		acc += c.w
		if pos < acc {
			return c.idx, true
		}
	}
	return cands[len(cands)-1].idx, true
}

// recordResult 记录一次 key 请求的结果与耗时（health 策略排序依据）。
// ok = 传输成功且 HTTP 2xx。
//
// 注意：调用方只对「key 级」结果调用它——404（模型不在该 key 上）、429（限流）、
// 403（多为模型级无权限）都不该走到这里，否则健康 key 会被误熔断
// （见 docs/issue-log/2026-11-20.md #1）。
func (p *keyPool) recordResult(idx int, ok bool, latency time.Duration) {
	if ok {
		p.recordSuccess(idx, latency)
		return
	}
	p.recordFail(idx, latency)
}

// recordSuccess 记一次成功：健康分拉高、连续失败/连续 403 清零。
func (p *keyPool) recordSuccess(idx int, latency time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if idx < 0 || idx >= len(p.keys) {
		return
	}
	k := p.keys[idx]
	if k.samples == 0 {
		// 首个样本直接落地（不再保留乐观初始值）
		k.okRate = 1.0
		k.latEWMA = float64(latency.Milliseconds())
	} else {
		k.okRate = k.okRate*(1-healthAlpha) + 1.0*healthAlpha
		k.latEWMA = k.latEWMA*(1-healthAlpha) + float64(latency.Milliseconds())*healthAlpha
	}
	k.samples++
	k.consecFails = 0
	k.consec403 = 0
}

// recordFail 记一次 key 级失败（传输错误 / 5xx）：连续失败达阈值进入熔断冷却。
func (p *keyPool) recordFail(idx int, latency time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if idx < 0 || idx >= len(p.keys) {
		return
	}
	k := p.keys[idx]
	if k.samples == 0 {
		k.okRate = 0
	} else {
		k.okRate = k.okRate * (1 - healthAlpha)
	}
	k.samples++
	k.consecFails++
	if k.consecFails >= circuitOpenThreshold {
		k.circuitUntil = p.nowFn().Add(circuitCooldown)
	}
}

// authSoftThreshold 403 软失败阈值：同一 key 连续这么多次 403 才按失效处理。
// 403 在多数聚合/中转站是「该模型无权限」「地区限制」「风控」，不是 key 坏了，
// 一次就冷却 600s（甚至永久禁用）会把好 key 打入冷宫。
const authSoftThreshold = 3

// softAuthFail 403 软失败：只计数并换 key，连续 3 次才按 401/403 失效处理。
// 成功一次即清零（recordSuccess），所以偶发 403 不会累积。
func (p *keyPool) softAuthFail(idx int) {
	p.mu.Lock()
	if idx < 0 || idx >= len(p.keys) {
		p.mu.Unlock()
		return
	}
	p.keys[idx].consec403++
	n := p.keys[idx].consec403
	p.mu.Unlock()
	if n >= authSoftThreshold {
		p.authFail(idx)
	}
}

// cool key 进入限流冷却（429/限流）。
func (p *keyPool) cool(idx int, d time.Duration) {
	if d <= 0 {
		d = defaultKeyCooldown
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if idx < 0 || idx >= len(p.keys) {
		return
	}
	p.keys[idx].coolingUntil = p.nowFn().Add(d)
}

// authCool key 进入 401/403 冷却（到期自动回池重试）。
func (p *keyPool) authCool(idx int, d time.Duration) {
	if d <= 0 {
		d = defaultKeyAuthCooldown
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if idx < 0 || idx >= len(p.keys) {
		return
	}
	p.keys[idx].authCoolingUntil = p.nowFn().Add(d)
	// 401/403 冷却期间 key 不可用，其它限流/熔断标记一并清掉，避免状态叠加混乱。
	p.keys[idx].coolingUntil = time.Time{}
	p.keys[idx].consecFails = 0
	p.keys[idx].consec403 = 0
	p.keys[idx].circuitUntil = time.Time{}
}

// disable key 永久禁用（key_403_cooldown=0 时 401/403 → 禁用；编辑保存/手动恢复后解除）。
func (p *keyPool) disable(idx int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if idx < 0 || idx >= len(p.keys) {
		return
	}
	p.keys[idx].disabled = true
}

// authFail 401/403 失效处理：按 key403Cooldown 分流——
// >0 进冷却（到期自动回池重试）；=0 永久禁用（旧行为）。
func (p *keyPool) authFail(idx int) {
	if p.key403Cooldown > 0 {
		p.authCool(idx, p.key403Cooldown)
	} else {
		p.disable(idx)
	}
}

// reset 清除全部 key 的运行态（禁用/限流冷却/403 冷却/熔断）——手动「恢复全部 Key」
// 与厂商重建时调用，让 key 池回到全可用。不触碰 key 配置与健康分数（保留实证）。
func (p *keyPool) reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, k := range p.keys {
		k.disabled = false
		k.coolingUntil = time.Time{}
		k.authCoolingUntil = time.Time{}
		k.consecFails = 0
		k.consec403 = 0
		k.circuitUntil = time.Time{}
	}
}

// status 健康计数快照（429 限流冷却 / 401-403 冷却 / 连续失败熔断 分开统计）。
func (p *keyPool) status() KeyPoolStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.nowFn()
	st := KeyPoolStatus{Total: len(p.keys)}
	for _, k := range p.keys {
		switch {
		case k.disabled:
			st.Disabled++
		case k.available(now):
			st.Available++
		case now.Before(k.authCoolingUntil):
			st.AuthCooling++
		case now.Before(k.circuitUntil):
			st.CircuitOpen++
		default:
			st.Cooling++
		}
	}
	return st
}

// countAuthCooling 当前 401/403 冷却中的 key 数（手动恢复按钮是否需要高亮等 UI 用）。
func (p *keyPool) countAuthCooling() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.nowFn()
	n := 0
	for _, k := range p.keys {
		if now.Before(k.authCoolingUntil) {
			n++
		}
	}
	return n
}

// parseRetryAfter 解析 Retry-After 响应头（秒数或 HTTP 日期；日期兜底返回 0=用缺省）。
func parseRetryAfter(h string) time.Duration {
	h = strings.TrimSpace(h)
	if h == "" {
		return 0
	}
	if secs, err := strconv.Atoi(h); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(h); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// keysSnapshot 返回配置内的全部 key 明文（供测试端点逐 key 连通验证）。
func (p *keyPool) keysSnapshot() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, 0, len(p.keys))
	for _, k := range p.keys {
		out = append(out, k.value)
	}
	return out
}

// needProbeIdxs 返回需要补测的 key 下标：可用、有实证、且成功率未满分（失败过）。
// 上限 max。健康探测只补测这些 key——大池（几百个 key）全量探测等于每轮白烧一遍额度。
func (p *keyPool) needProbeIdxs(max int) []int {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.nowFn()
	var out []int
	for i, k := range p.keys {
		if len(out) >= max {
			break
		}
		if !k.available(now) {
			continue
		}
		if k.samples == 0 || k.okRate >= 1.0 {
			continue
		}
		out = append(out, i)
	}
	return out
}

// keyAt 返回下标 idx 的 key 明文（越界返回空串）。
func (p *keyPool) keyAt(idx int) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if idx < 0 || idx >= len(p.keys) {
		return ""
	}
	return p.keys[idx].value
}
