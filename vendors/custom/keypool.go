// 自定义模型源 key 池：一源多 key 的调度与健康状态。
//
// 三种调度（providers[].params.key_strategy，仅作用于自定义源，与实例池 route_mode 无关）：
//   - round_robin（默认）：可用 key 间均匀轮询
//   - failover          ：按配置顺序优先（主 key 可用时恒用它，冷却/禁用才降级备用）
//   - health            ：健康优先——按成功率（EWMA）降序、样本数降序、平均延迟升序挑选，
//     让表现最好的 key 稳定回答，变差才落到下一个（借鉴 model-gateway 的质量分排序）
//
// 健康语义：429/限流 → 冷却（Retry-After，缺省 60s）到期自动回池；
// 401/403（key 失效）→ 禁用（运行期内不再使用，编辑保存后重建实例即恢复）。
// 每次请求记录成功/失败与延迟（recordResult），供 health 策略排序；其余策略不依赖计数。
package custom

import (
	"net/http"
	"sort"
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

// 健康分数平滑系数（EWMA）：样本越多，单次波动影响越小。
const healthAlpha = 0.3

// keyState 单个 key 的运行时状态。
type keyState struct {
	value        string
	coolingUntil time.Time
	disabled     bool // 401/403：key 失效，运行期内禁用
	samples      int  // 已记录请求次数（health 策略排序用）
	okRate       float64 // 成功率 EWMA，初始乐观 1.0（无数据视为可用）
	latEWMA      float64 // 成功请求平均延迟（毫秒）EWMA，仅成功时更新
}

// available 该 key 当前是否可用（未禁用且不在冷却期）。
func (k *keyState) available(now time.Time) bool {
	return !k.disabled && !now.Before(k.coolingUntil)
}

// KeyPoolStatus key 健康计数（供 UI 展示；状态快照，非用量统计）。
type KeyPoolStatus struct {
	Total     int `json:"total"`
	Available int `json:"available"`
	Cooling   int `json:"cooling"`
	Disabled  int `json:"disabled"`
}

// keyPool 一源一池。
type keyPool struct {
	mu       sync.Mutex
	keys     []*keyState
	strategy string
	rr       uint64 // round_robin 游标
	nowFn    func() time.Time
}

// newKeyPool 构造 key 池。keys 为空 = 无鉴权源（单一空 key，恒可用）。
func newKeyPool(keys []string, strategy string) *keyPool {
	switch strategy {
	case StrategyFailover:
	case StrategyHealth:
	default:
		strategy = StrategyRoundRobin
	}
	pool := &keyPool{strategy: strategy, nowFn: time.Now}
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

// tryAcquirePrefer 优先从 preferred（key 下标子集，如「并集目录中能提供目标模型
// 的 key」）里挑可用且未试过的 key，子集无可挑时退回全池普通调度。
// round_robin 在子集内同样轮询；failover 在子集内保持粘主。
func (p *keyPool) tryAcquirePrefer(tried map[int]bool, preferred []int) (string, int, bool) {
	return p.tryAcquirePreferSticky(tried, preferred, -1)
}

// tryAcquirePreferSticky 在 tryAcquirePrefer 基础上支持会话粘性：stickyIdx >= 0
// 时优先命中该 key（续写同 key / 会话级粘性），不可用或已试过再回退 preferred
// 子集与全池普通调度。命中 sticky 不推进 round_robin 游标——同一会话的续写
// 重连稳定落在同一 key，避免重复输出/串对话。
func (p *keyPool) tryAcquirePreferSticky(tried map[int]bool, preferred []int, stickyIdx int) (string, int, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.nowFn()
	if stickyIdx >= 0 && stickyIdx < len(p.keys) && !tried[stickyIdx] && p.keys[stickyIdx].available(now) {
		return p.keys[stickyIdx].value, stickyIdx, true
	}
	if len(preferred) > 0 {
		if p.strategy == StrategyHealth {
			// 健康优先：在模型亲和子集内按健康分挑选
			if key, idx, ok := p.pickHealthLocked(tried, preferred, now); ok {
				return key, idx, true
			}
		} else {
			start := 0
			if p.strategy == StrategyRoundRobin {
				start = int(p.rr % uint64(len(preferred)))
			}
			for off := 0; off < len(preferred); off++ {
				i := preferred[(start+off)%len(preferred)]
				if i < 0 || i >= len(p.keys) {
					continue
				}
				if tried[i] || !p.keys[i].available(now) {
					continue
				}
				if p.strategy == StrategyRoundRobin {
					p.rr++
				}
				return p.keys[i].value, i, true
			}
		}
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
		return p.pickHealthLocked(tried, nil, now)
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

// pickHealthLocked 按健康分挑选可用且未试过的 key（调用方已持 p.mu）：
// 成功率（EWMA）降序 → 样本数降序（有实证者优先于未测者）→ 平均延迟升序 → 配置序。
// subset 非空时仅在子集内挑（模型亲和）；nil 表示全池。冷/禁用的 key 不参与。
func (p *keyPool) pickHealthLocked(tried map[int]bool, subset []int, now time.Time) (string, int, bool) {
	var cand []int
	if len(subset) > 0 {
		for _, i := range subset {
			if i >= 0 && i < len(p.keys) && !tried[i] && p.keys[i].available(now) {
				cand = append(cand, i)
			}
		}
	} else {
		for i, k := range p.keys {
			if !tried[i] && k.available(now) {
				cand = append(cand, i)
			}
		}
	}
	if len(cand) == 0 {
		return "", -1, false
	}
	sort.SliceStable(cand, func(a, b int) bool {
		ka, kb := p.keys[cand[a]], p.keys[cand[b]]
		if ka.okRate != kb.okRate {
			return ka.okRate > kb.okRate
		}
		if ka.samples != kb.samples {
			return ka.samples > kb.samples
		}
		if ka.latEWMA != kb.latEWMA {
			return ka.latEWMA < kb.latEWMA
		}
		return cand[a] < cand[b]
	})
	idx := cand[0]
	return p.keys[idx].value, idx, true
}

// recordResult 记录一次 key 请求的结果与耗时（health 策略排序依据）。
// ok = 传输成功且 HTTP 2xx；失败不更新延迟（仅更新成功率）。
func (p *keyPool) recordResult(idx int, ok bool, latency time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if idx < 0 || idx >= len(p.keys) {
		return
	}
	k := p.keys[idx]
	v := 0.0
	if ok {
		v = 1.0
	}
	if k.samples == 0 {
		// 首个样本直接落地（不再保留乐观初始值）
		k.okRate = v
		if ok {
			k.latEWMA = float64(latency.Milliseconds())
		}
	} else {
		k.okRate = k.okRate*(1-healthAlpha) + v*healthAlpha
		if ok {
			k.latEWMA = k.latEWMA*(1-healthAlpha) + float64(latency.Milliseconds())*healthAlpha
		}
	}
	k.samples++
}

// cool key 进入冷却（429/限流）。
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

// disable key 禁用（401/403 失效）。
func (p *keyPool) disable(idx int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if idx < 0 || idx >= len(p.keys) {
		return
	}
	p.keys[idx].disabled = true
}

// status 健康计数快照。
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
		default:
			st.Cooling++
		}
	}
	return st
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

// availableIdxs 返回当前可用（未禁用、不在冷却期）的 key 下标（后台健康探测用）。
func (p *keyPool) availableIdxs() []int {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.nowFn()
	var out []int
	for i, k := range p.keys {
		if k.available(now) {
			out = append(out, i)
		}
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
