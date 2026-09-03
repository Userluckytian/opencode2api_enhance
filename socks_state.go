// SOCKS 配置态只读快照 + 路由结论判定（调试与可观测性计划阶段 1 遗留项）。
// 独立文件：不改动 socks.go（避免与并行工作冲突）。
// Same package (main) - do not change package clause manually.
package main

import "strings"

// 路由结论枚举（CallRecord.RouteVerdict / call_log.jsonl 的 route_verdict）。
//
// 为什么用字符串枚举而不是 bool：已删除的 ViaProxy 就是教训——bool + omitempty
// 会让 false 整键从 JSON 中消失，前端永远拿到 undefined，无法区分「假」与「未写入」。
// 字符串的空值天然表示「未知 / 旧记录」，前端可安全降级为 '-'。
const (
	// RouteVerdictProxied 本次调用确实经由代理节点出站。
	RouteVerdictProxied = "proxied"
	// RouteVerdictDirectByDesign 付费层直连：设计如此（getHTTPClientForTierWithProxy 对 TierPaid 恒返回空代理地址），不是故障。
	RouteVerdictDirectByDesign = "direct_by_design"
	// RouteVerdictDirectConfigMissing 免费层本应走代理池，但本进程根本没有 SOCKS 配置 → 配置丢失、已回退直连。
	RouteVerdictDirectConfigMissing = "direct_config_missing"
	// RouteVerdictDirectUnexpected 免费层且 SOCKS 已配置，却仍然直连 → 异常，需排查。
	RouteVerdictDirectUnexpected = "direct_unexpected"
)

// socksProxyConfigured 报告本进程当前是否配置了 SOCKS 出站代理。
//
// 只看 active_socks5（实例级出站）与 socks5_proxies（网关级聚合）两键：
// 「SOCKS 三键」中的 route_mode 默认值为 "smart"（socks.go 的 init 写入）恒非空，
// 用它判定会永远判成「已配置」——计划 §6 术语表明确警告过的坑。
//
// 并发安全：socks5Mu 是 socks5Proxies / activeSocks5 的专属 RWMutex，本函数只取读锁，
// 且锁内不再获取任何其它锁。applyConfig 的锁序为 configMu → socks5Mu → socks5CacheMu，
// 本函数不持 configMu、不嵌套下游锁，与既有锁序同向无环，不构成死锁。
func socksProxyConfigured() bool {
	socks5Mu.RLock()
	defer socks5Mu.RUnlock()
	return strings.TrimSpace(activeSocks5) != "" || len(socks5Proxies) > 0
}

// routeVerdict 判定路由结论（纯函数：无锁、无全局状态，可单测）。
//
// nodes 必须是未被兜底改写的原始节点列表（recordCall 把空节点换成「直连」之前的值）；
// tier 取值同 tierOfAuth："free" / "paid"，其它值（含空串）视为无法判定。
//
//	存在非空节点                    → proxied
//	节点全空 + paid                 → direct_by_design
//	节点全空 + free + SOCKS 未配置  → direct_config_missing
//	节点全空 + free + SOCKS 已配置  → direct_unexpected
//	其余（tier 为空等）            → ""（前端降级为 '-'）
func routeVerdict(nodes []string, tier string, socksConfigured bool) string {
	for _, n := range nodes {
		if strings.TrimSpace(n) != "" {
			return RouteVerdictProxied
		}
	}
	switch tier {
	case "paid":
		return RouteVerdictDirectByDesign
	case "free":
		if socksConfigured {
			return RouteVerdictDirectUnexpected
		}
		return RouteVerdictDirectConfigMissing
	default:
		return ""
	}
}
