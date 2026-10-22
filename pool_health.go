// 池健康快照与重置（P1 阶段 3）。
// Same package (main) - do not change package clause manually.
package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"time"
)

// PoolHealthNode 单节点健康快照。
type PoolHealthNode struct {
	Addr       string `json:"addr"`
	State      string `json:"state"`
	Reason     string `json:"reason,omitempty"`
	BadCount   int    `json:"bad_count"`
	Extensions int    `json:"extensions,omitempty"`
	Until      string `json:"until,omitempty"`
	Breaker    string `json:"breaker"`
	Failures   int    `json:"failures"`
}

// PoolHealthSnapshot 池健康聚合快照。
type PoolHealthSnapshot struct {
	Configured        bool             `json:"configured"`
	ActiveSocks5      string           `json:"active_socks5"`
	RouteMode         string           `json:"route_mode"`
	Total             int              `json:"total"`
	Available         int              `json:"available"`
	Cooling           int              `json:"cooling"`
	BadQuota          int              `json:"bad_quota"`
	BadAuth           int              `json:"bad_auth"`
	BadLink           int              `json:"bad_link"`
	BreakerOpen       int              `json:"breaker_open"`
	NoCandidateTotal  int64            `json:"no_candidate_total"`
	LastNoCandidateTS string           `json:"last_no_candidate_ts,omitempty"`
	Nodes             []PoolHealthNode `json:"nodes"`
}

// PoolResetResult 重置结果。
type PoolResetResult struct {
	ResetHealth   int      `json:"reset_health"`
	ResetBreakers int      `json:"reset_breakers"`
	Scope         string   `json:"scope"`
	Nodes         []string `json:"nodes"`
}

func snapshotPoolHealth() PoolHealthSnapshot {
	now := time.Now()

	// 1) 先短暂持 socks5Mu.RLock 复制代理列表与 active_socks5 / route_mode；释放后再持其它锁。
	socks5Mu.RLock()
	proxies := append([]Socks5Proxy(nil), socks5Proxies...)
	active := activeSocks5
	rm, _ := routeMode.Load().(string)
	socks5Mu.RUnlock()

	// 2) 持 socks5HealthMu 复制健康表；释放。
	socks5HealthMu.Lock()
	healthCopy := make(map[string]socks5HealthState, len(socks5Health))
	for k, v := range socks5Health {
		healthCopy[k] = v
	}
	socks5HealthMu.Unlock()

	// 3) 持 poolBreakerMu 复制熔断表；释放（不得嵌套持锁）。
	poolBreakerMu.Lock()
	breakerCopy := make(map[string]poolBreaker, len(poolBreakers))
	for k, v := range poolBreakers {
		if v != nil {
			breakerCopy[k] = *v
		}
	}
	poolBreakerMu.Unlock()

	snap := PoolHealthSnapshot{
		Configured:       socksProxyConfigured(),
		ActiveSocks5:     active,
		RouteMode:        rm,
		Total:            len(proxies),
		NoCandidateTotal: noCandidateTotal.Load(),
		Nodes:            make([]PoolHealthNode, 0, len(proxies)),
	}
	if ts := noCandidateLastTS.Load(); ts > 0 {
		snap.LastNoCandidateTS = time.Unix(0, ts).Format(time.RFC3339)
	}

	for _, p := range proxies {
		st := healthCopy[p.Addr]
		breaker := "closed"
		if b, ok := breakerCopy[p.Addr]; ok {
			if b.openUntil.IsZero() {
				breaker = "closed"
			} else if now.Before(b.openUntil) {
				breaker = "open"
			} else if !b.probeUsed {
				breaker = "halfopen"
			} else {
				breaker = "open"
			}
		}

		state := "healthy"
		if st.badReason != "" {
			switch st.badClass {
			case "auth":
				state = "bad_auth"
			case "quota":
				state = "bad_quota"
			case "link":
				state = "bad_link"
			default:
				if st.badUntil.IsZero() {
					state = "bad_auth"
				} else {
					state = "bad_link"
				}
			}
		} else if breaker == "open" {
			state = "breaker_open"
		} else if !st.until.IsZero() && now.Before(st.until) {
			state = "cooling"
		}

		untilStr := ""
		relevantUntil := st.until
		if st.badReason != "" {
			relevantUntil = st.badUntil
		}
		if !relevantUntil.IsZero() {
			untilStr = relevantUntil.Format(time.RFC3339)
		}

		node := PoolHealthNode{
			Addr:       p.Addr,
			State:      state,
			Reason:     st.badReason,
			BadCount:   st.badCount,
			Extensions: st.badExtendCount,
			Until:      untilStr,
			Breaker:    breaker,
			Failures:   st.failures,
		}
		snap.Nodes = append(snap.Nodes, node)

		switch state {
		case "available", "healthy":
			snap.Available++
		case "cooling":
			snap.Cooling++
		case "bad_quota":
			snap.BadQuota++
		case "bad_auth":
			snap.BadAuth++
		case "bad_link":
			snap.BadLink++
		case "breaker_open":
			snap.BreakerOpen++
		}
	}

	// available 也应包含 half-open 熔断节点（熔断非 open）：上面 healthy 已计；
	// breaker_open 单独计数，不重复。无额外处理。

	// 排序节点地址，保持稳定输出。
	sort.Slice(snap.Nodes, func(i, j int) bool { return snap.Nodes[i].Addr < snap.Nodes[j].Addr })

	// 统计 breaker_open：上面已按 state 计；但 breaker open 且无 badReason 才 state breaker_open。
	// available 口径：未进坏池、未冷却、熔断非 open。healthy 已满足；half-open 节点 state 为 healthy
	//（until 过期），breaker 字段 halfopen，计为 available，符合 plan 口径。

	return snap
}

// resetPoolHealth 清理网关内存健康/熔断状态（不重启进程）。
func resetPoolHealth(scope, node string) (int, int, []string) {
	if scope == "" {
		scope = "quota"
	}

	socks5HealthMu.Lock()
	healthBefore := make(map[string]socks5HealthState, len(socks5Health))
	for k, v := range socks5Health {
		healthBefore[k] = v
	}
	socks5HealthMu.Unlock()

	poolBreakerMu.Lock()
	breakerBefore := make(map[string]*poolBreaker, len(poolBreakers))
	for k, v := range poolBreakers {
		breakerBefore[k] = v
	}
	poolBreakerMu.Unlock()

	targets := []string{}
	switch scope {
	case "all":
		for addr := range healthBefore {
			targets = append(targets, addr)
		}
		// 若健康表为空，仍允许清熔断：把熔断表地址也纳入。
		for addr := range breakerBefore {
			if !containsString(targets, addr) {
				targets = append(targets, addr)
			}
		}
	case "node":
		targets = append(targets, node)
	case "quota", "auth":
		for addr, st := range healthBefore {
			if scope == "quota" && st.badClass == "quota" {
				targets = append(targets, addr)
			}
			if scope == "auth" && st.badClass == "auth" {
				targets = append(targets, addr)
			}
		}
		// 兼容：badClass 为空但 badReason 非空且 scope=quota/auth 时按 badUntil 推断。
		for addr, st := range healthBefore {
			if st.badClass != "" {
				continue
			}
			if st.badReason == "" {
				continue
			}
			if scope == "quota" && !st.badUntil.IsZero() && !containsString(targets, addr) {
				targets = append(targets, addr)
			}
			if scope == "auth" && st.badUntil.IsZero() && !containsString(targets, addr) {
				targets = append(targets, addr)
			}
		}
	default:
		// 未知 scope：按 quota 处理
		scope = "quota"
		for addr, st := range healthBefore {
			if st.badClass == "quota" || (st.badClass == "" && st.badReason != "" && !st.badUntil.IsZero()) {
				targets = append(targets, addr)
			}
		}
	}

	// 去重
	uniq := []string{}
	seen := map[string]bool{}
	for _, a := range targets {
		if a == "" {
			continue
		}
		if seen[a] {
			continue
		}
		seen[a] = true
		uniq = append(uniq, a)
	}
	targets = uniq

	resetHealth := 0
	resetBreakers := 0

	if scope == "all" {
		socks5HealthMu.Lock()
		resetHealth = len(socks5Health)
		socks5Health = map[string]socks5HealthState{}
		socks5HealthMu.Unlock()

		poolBreakerMu.Lock()
		resetBreakers = len(poolBreakers)
		poolBreakers = map[string]*poolBreaker{}
		poolBreakerMu.Unlock()

		if len(targets) == 0 {
			for addr := range healthBefore {
				targets = append(targets, addr)
			}
		}
		// 去重 targets
		uniq = []string{}
		seen = map[string]bool{}
		for _, a := range targets {
			if a == "" || seen[a] {
				continue
			}
			seen[a] = true
			uniq = append(uniq, a)
		}
		targets = uniq
	} else {
		socks5HealthMu.Lock()
		for _, addr := range targets {
			if _, ok := socks5Health[addr]; ok {
				delete(socks5Health, addr)
				resetHealth++
			}
		}
		socks5HealthMu.Unlock()

		poolBreakerMu.Lock()
		for _, addr := range targets {
			if _, ok := poolBreakers[addr]; ok {
				delete(poolBreakers, addr)
				resetBreakers++
			}
		}
		poolBreakerMu.Unlock()
	}

	if targets == nil {
		targets = []string{}
	}
	return resetHealth, resetBreakers, targets
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func poolHealthHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	snap := snapshotPoolHealth()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(snap)
}

func poolResetHealthHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	scope := r.URL.Query().Get("scope")
	if scope == "" {
		scope = "quota"
	}
	node := r.URL.Query().Get("node")
	switch scope {
	case "quota", "auth", "all", "node":
	default:
		http.Error(w, "invalid scope", http.StatusBadRequest)
		return
	}
	if scope == "node" && node == "" {
		http.Error(w, "node required", http.StatusBadRequest)
		return
	}
	health, breakers, nodes := resetPoolHealth(scope, node)
	if nodes == nil {
		nodes = []string{}
	}
	res := PoolResetResult{
		ResetHealth:   health,
		ResetBreakers: breakers,
		Scope:         scope,
		Nodes:         nodes,
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(res)
}
