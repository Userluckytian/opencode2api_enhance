package manager

// WARP 池「起不来」时的自动降级。
//
// ## 为什么需要（引擎侧缺陷）
//
// 引擎判定一条请求要不要走 WARP，只看「WARP 池对象是否存在」，不看池里有没有可用出口
// （internal/gateway/warp.go:38-39）：
//
//	func (g *Gateway) usesWARP(model string, tier config.Tier, anonymous bool) bool {
//	    return g.warp != nil && g.catalog.IsFreeModel(model) && (anonymous || tier == config.TierZen)
//	}
//
// 于是 WARP 启用但池空（active=0）时，**连 zen key 的请求也被判为必须走 WARP**；
// 拿不到出口就得到 errWARPUnavailable，然后直接 break 放弃整个 key tier
// （internal/gateway/upstream.go:1041-1045）：
//
//	if useWARP && errors.Is(err, errWARPUnavailable) {
//	    // No exit at all: another key would only wait on the same pool.
//	    lastErr = err
//	    break
//	}
//
// 结果：**配了 10 个 key 也一个都用不上，全部 503 "WARP pool has no ready egress"**。
//
// ## 典型触发场景
//
// 本机没有可用 IPv6（或 warp.source_ipv6 为空）时，每次注册 WARP 出口都从同一个本机
// 地址出去，Cloudflare 反复发回同一个 /64，被判 duplicate egress：
//
//	Discarding duplicate WARP exit ...: WARP egress is already active or cooling down: 2a09:...::/64
//	OpenCode WARP preload paused for 5m0s after 9 duplicate egresses
//
// 池于是永远 0 出口，引擎 /healthz 报 degraded + issues=["warp_unavailable"]，
// 面板显示「引擎未运行 / 模型 0/0」。
//
// ## 降级方式
//
// 把引擎 config 的 warp.enabled 写成 false → 引擎启动时 g.warp == nil →
// usesWARP() 恒 false → key 直接走 proxies（直连或面板配置的出口代理）。
// 这是**不修改引擎**（GPL 独立程序）前提下唯一能恢复 key 可用的手段。
//
// 降级只做一次、不自动切回：池空通常是环境性的（无 IPv6 / 出口被封），反复重试只会
// 反复烧出口并把冷却账本撑大。用户可在面板点「重试 WARP」重新尝试。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// warpFallbackStrikes 连续多少次观察到「WARP 池 0 出口」才降级。
//
// 不取 1 次：引擎启动瞬间 WARP 池还在注册（preload 期间 active 可能就是 0），
// 立刻降级会把「本来能起来」的情况误杀。30s 看门狗 × 3 = 约 90s，足够跨过
// 首次注册窗口（引擎注册窗口最长 90s），又不会让用户等太久。
const warpFallbackStrikes = 3

// warpFallbackState 降级判定的进程内状态（不落盘：重启后重新观察）。
type warpFallbackState struct {
	strikes int       // 连续「池空」观测次数
	applied bool      // 已执行过降级
	at      time.Time // 降级发生时刻
	reason  string    // 降级原因（面板展示）
}

// warpHealth WARP 健康观测结果。
type warpHealth struct {
	// Present 引擎是否运行在 WARP 模式（/healthz 有 warp 字段）。
	Present bool
	// Status 引擎自报状态（ready / unavailable / starting ...）。
	Status string
	// Active 当前可用出口数。
	Active int
	// Cooling 冷却中的出口数。
	Cooling int
}

// usable 报告 WARP 是否真的能承载请求。
//
// 注意用 Active 而不是 Status：引擎在池空时可能仍报 status=ready（注册成功过、
// 后来烧光），只有 Active==0 才是「现在没出口可用」。
func (w warpHealth) usable() bool {
	return w.Present && w.Active > 0
}

// observeWarpHealth 从 /healthz 响应体解析 WARP 健康（不额外发请求）。
func observeWarpHealth(body []byte) warpHealth {
	var hz struct {
		Warp *struct {
			Status  string `json:"status"`
			Active  int    `json:"active"`
			Cooling int    `json:"cooling"`
		} `json:"warp"`
	}
	if json.Unmarshal(body, &hz) != nil || hz.Warp == nil {
		return warpHealth{}
	}
	return warpHealth{
		Present: true,
		Status:  hz.Warp.Status,
		Active:  hz.Warp.Active,
		Cooling: hz.Warp.Cooling,
	}
}

// WarpFallbackStatus 自动降级状态（面板展示用）。
type WarpFallbackStatus struct {
	// Enabled 用户是否开着 WARP（配置值）。
	Enabled bool `json:"enabled"`
	// AutoFallback 是否启用自动降级（配置值）。
	AutoFallback bool `json:"auto_fallback"`
	// Applied 是否已因池空而降级（本次进程内）。
	Applied bool `json:"applied"`
	// At 降级发生时刻（RFC3339，未降级为空）。
	At string `json:"at,omitempty"`
	// Reason 降级原因（未降级为空）。
	Reason string `json:"reason,omitempty"`
	// Strikes 当前连续「池空」观测次数（未降级时用于面板提示「正在观察」）。
	Strikes int `json:"strikes"`
	// Threshold 触发降级所需的连续观测次数。
	Threshold int `json:"threshold"`
}

// WarpFallbackStatusOf 返回当前自动降级状态。
func (m *Manager) WarpFallbackStatusOf() WarpFallbackStatus {
	cfg := m.loadConfig()
	m.warpFallbackMu.Lock()
	st := m.warpFallback
	m.warpFallbackMu.Unlock()
	out := WarpFallbackStatus{
		Enabled:      engineWarpEnabled(cfg),
		AutoFallback: engineWarpAutoFallback(cfg),
		Applied:      st.applied,
		Strikes:      st.strikes,
		Threshold:    warpFallbackStrikes,
		Reason:       st.reason,
	}
	if !st.at.IsZero() {
		out.At = st.at.Format(time.RFC3339)
	}
	return out
}

// ResetWarpFallback 清除降级状态（面板「重试 WARP」调用）。
//
// 只清判定状态，不改配置：用户重新开启 WARP 开关并保存后，引擎会按新配置重启，
// 由看门狗重新观察。
func (m *Manager) ResetWarpFallback() WarpFallbackStatus {
	m.warpFallbackMu.Lock()
	m.warpFallback = warpFallbackState{}
	m.warpFallbackMu.Unlock()
	return m.WarpFallbackStatusOf()
}

// maybeAutoFallbackWarp 看门狗每轮调用：观测到 WARP 池连续多次 0 出口时，
// 自动把引擎的 warp.enabled 关掉并重启引擎，让 key 恢复直连可用。
//
// 返回 true 表示本轮执行了降级（调用方据此决定是否跳过后续健康检查）。
//
// 为什么在这里做而不是在 Status() 里：Status() 是面板高频轮询路径（前端 5s 一次），
// 在里面改配置 + 重启引擎会把面板变成「点一下就重启」。看门狗是 30s 一次的后台
// 循环，语义上就该由它负责这类自愈动作。
func (m *Manager) maybeAutoFallbackWarp(runner Runner, health warpHealth) bool {
	cfg := m.loadConfig()
	if !engineWarpEnabled(cfg) || !engineWarpAutoFallback(cfg) {
		return false
	}
	m.warpFallbackMu.Lock()
	already := m.warpFallback.applied
	m.warpFallbackMu.Unlock()
	if already {
		return false
	}

	// 池可用 → 清空计数（避免「间歇性池空」累积成误降级）。
	if health.usable() {
		m.warpFallbackMu.Lock()
		m.warpFallback.strikes = 0
		m.warpFallbackMu.Unlock()
		return false
	}
	// 引擎压根不在 WARP 模式（/healthz 无 warp 字段）→ 说明已经降级/未启用，
	// 不是「池空」，不计入。
	if !health.Present {
		return false
	}

	m.warpFallbackMu.Lock()
	m.warpFallback.strikes++
	strikes := m.warpFallback.strikes
	m.warpFallbackMu.Unlock()
	if strikes < warpFallbackStrikes {
		return false
	}

	// 达到阈值：关掉 WARP 并重启引擎。
	reason := fmt.Sprintf(
		"WARP 池连续 %d 次观测到 0 可用出口（冷却 %d 个），已自动降级为直连以恢复 key 可用",
		strikes, health.Cooling)
	if err := m.ConfigSet("engine_warp_enabled", "false"); err != nil {
		// 写配置失败：不标记 applied，下一轮继续尝试。
		return false
	}
	if err := m.RestartFreeTierEngine(runner); err != nil {
		// 重启失败：配置已改，下一轮引擎起来后就是直连模式；仍标记 applied
		// 以免反复重启。
		m.warpFallbackMu.Lock()
		m.warpFallback.applied = true
		m.warpFallback.at = time.Now()
		m.warpFallback.reason = reason + "（重启引擎失败：" + err.Error() + "）"
		m.warpFallbackMu.Unlock()
		return true
	}
	m.warpFallbackMu.Lock()
	m.warpFallback.applied = true
	m.warpFallback.at = time.Now()
	m.warpFallback.reason = reason
	m.warpFallbackMu.Unlock()
	return true
}

// fetchEngineHealth 拉一次引擎 /healthz 并解析 WARP 健康。
// 供看门狗在端口探测成功后进一步判断「引擎活着但 WARP 空转」这种功能性故障。
func fetchEngineHealth(port uint16) (warpHealth, bool) {
	status, body, err := httpGetJSON(port, "/healthz", 3*time.Second, "")
	if err != nil || status != http.StatusOK {
		// 503 degraded 也带完整 body，需要读出来判断 WARP 状态。
		if len(body) == 0 {
			return warpHealth{}, false
		}
	}
	return observeWarpHealth(body), len(body) > 0
}
