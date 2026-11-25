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
	"time"
)

// WatchNodePoolHealth 每 20s 醒一次，按 node_pool_health_sec 节流执行巡检。
func (m *Manager) WatchNodePoolHealth() {
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	var last time.Time
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
