package manager

// 节点池定时巡检（2.0.18）。
//
// 只补「源」这一层，不重复引擎的补池逻辑：
//   - 出口够用（active >= target）→ 什么都不做；
//   - 出口不足但槽位节点都健康 → 也不动，引擎自己会串行 refill；
//   - 出口不足且槽位里有死节点 → 重新测延迟、换可达节点、热重载 mihomo
//     （不重启引擎），让引擎下一轮注册能成功。

import (
	"log"
	"strconv"
	"strings"
	"time"
)

// WatchNodePoolHealth 每 20s 醒一次，按 node_pool_health_sec 节流执行巡检。
func (m *Manager) WatchNodePoolHealth() {
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	var last time.Time
	zeroRounds := 0
	for range ticker.C {
		cfg := m.loadConfig()
		interval := nodePoolHealthSec(cfg)
		if !m.nodePoolOn(cfg) || interval <= 0 {
			continue
		}
		if !last.IsZero() && time.Since(last) < time.Duration(interval)*time.Second {
			continue
		}
		info := m.NodeFarmInfo()
		if !info.Running || len(info.Ports) != nodeFarmSlots {
			continue
		}
		st := m.FreeTierEngineStatus()
		if st.State != "ready" || st.Restarting || st.WarpSize == 0 {
			continue
		}
		last = time.Now()
		m.appendWarpSample(WarpSample{
			At: time.Now().Format("15:04:05"), Size: st.WarpSize,
			Active: st.WarpActive, Cooling: st.WarpCooling,
		})
		if st.WarpActive > 0 {
			zeroRounds = 0
		} else {
			zeroRounds++
		}
		// 中继自救：连续两轮一个出口都没有，先怀疑 UDP 中继本身。
		// TCP 注册成功≠握手能出去（2026-11-25 的故障就是这种）。
		if zeroRounds >= 2 {
			m.failoverUDPRelay()
			zeroRounds = 0
		}
		if st.WarpActive >= st.WarpSize {
			m.setNodeHealth(time.Now().Format("15:04:05"), 0)
			continue
		}
		f := m.farm()
		f.mu.Lock()
		ctlPort, secret := f.ctlPort, f.secret
		slots := dedupeNodes(append([]PoolNode(nil), f.nodes...))
		f.mu.Unlock()
		if ctlPort <= 0 || len(slots) == 0 {
			continue
		}
		delays := probeCandidates(ctlPort, secret, slots, 3*time.Second)
		dead := 0
		for _, n := range slots {
			if _, ok := delays[n.Fingerprint]; !ok {
				dead++
			}
		}
		m.setNodeHealth(time.Now().Format("15:04:05"), dead)
		if !shouldReSelect(st.WarpActive, st.WarpSize, dead) {
			continue
		}
		log.Printf("node pool health: %d/%d slot node(s) unreachable while exits %d/%d; re-selecting",
			dead, len(slots), st.WarpActive, st.WarpSize)
		if _, verified, err := m.autoSelectNodeSlots(info); err != nil {
			log.Printf("node pool health: re-select failed: %v", err)
		} else if !verified {
			log.Printf("node pool health: farm has no controller; skip re-select")
		}
	}
}

// relayCandidates 中继候选：手动列表（每行一个）+ 农场槽位 0（兜底）。
func (m *Manager) relayCandidates() []string {
	cfg := m.loadConfig()
	out := splitRelayCandidates(cfg.EngineWarpSocksUDPManual)
	farm := ""
	if info := m.NodeFarmInfo(); len(info.Ports) > 0 {
		farm = "socks5://127.0.0.1:" + strconv.Itoa(info.Ports[0])
	}
	if farm != "" {
		dup := false
		for _, c := range out {
			if c == farm {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, farm)
		}
	}
	return out
}

// pickWorkingRelay 按顺序探测候选，返回第一个能真正转发 UDP 的中继。
func pickWorkingRelay(cands []string, timeout time.Duration) string {
	for _, c := range cands {
		if probeSocks5UDP(c, timeout) == nil {
			return c
		}
	}
	return ""
}

// failoverUDPRelay 当前中继转发不了 UDP 时，切换到备用中继并重启引擎。
// 候选：手动列表的其余行 + 农场槽位 0；选中后写回手动键（手动值优先）。
func (m *Manager) failoverUDPRelay() {
	cfg := m.loadConfig()
	cur := effectiveSocksUDP(cfg, m.nodePoolOn(cfg))
	if cur == "" {
		return // 直连模式，无从探测
	}
	if probeSocks5UDP(cur, 5*time.Second) == nil {
		return // 中继没问题，出口不足是额度/注册问题
	}
	alts := m.relayCandidates()
	for _, alt := range alts {
		if alt == cur || probeSocks5UDP(alt, 5*time.Second) != nil {
			continue
		}
		log.Printf("warp relay failover: %s unusable, switching to %s and restarting engine", cur, alt)
		reordered := []string{alt}
		for _, c := range splitRelayCandidates(cfg.EngineWarpSocksUDPManual) {
			if c != alt {
				reordered = append(reordered, c)
			}
		}
		_ = m.ConfigSet("engine_warp_socks_udp_manual", strings.Join(reordered, "\n"))
		m.restartEngineAsync()
		return
	}
	log.Printf("warp relay failover: no usable relay among %d candidate(s); current %s stays", len(alts), cur)
}

// shouldReSelect 巡检决策：出口不足且槽位有死节点才重新选源。
// 槽位都健康时不动——引擎自己会 refill。
func shouldReSelect(active, target, dead int) bool {
	return target > 0 && active < target && dead > 0
}

// setNodeHealth 记录最近一次巡检结果（状态接口展示）。
func (m *Manager) setNodeHealth(at string, dead int) {
	f := m.farm()
	f.mu.Lock()
	f.healthAt = at
	f.healthDead = dead
	f.mu.Unlock()
}

// dedupeNodes 按指纹去重（槽位里同一节点会出现多次）。
func dedupeNodes(nodes []PoolNode) []PoolNode {
	out := make([]PoolNode, 0, len(nodes))
	seen := map[string]bool{}
	for _, n := range nodes {
		if n.Fingerprint == "" || seen[n.Fingerprint] {
			continue
		}
		seen[n.Fingerprint] = true
		out = append(out, n)
	}
	return out
}
