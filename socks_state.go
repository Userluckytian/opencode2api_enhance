// 路由结论判定（调用日志 CallRecord.RouteVerdict / call_log.jsonl 的 route_verdict）。
//
// v2.0.0：节点池移除后只剩「直连」与「经自定义代理」两种出站形态，
// 因此结论集合收敛为 proxied / direct_by_design，其余常量保留以兼容历史日志。
// 为什么用字符串枚举而不是 bool：bool + omitempty 会让 false 整键从 JSON 消失，
// 前端永远拿到 undefined，无法区分「假」与「未写入」。
package main

import "strings"

const (
	// RouteVerdictProxied 本次调用确实经由代理出站。
	RouteVerdictProxied = "proxied"
	// RouteVerdictDirectByDesign 直连：设计如此（厂商未配置 proxy）。
	RouteVerdictDirectByDesign = "direct_by_design"
	// RouteVerdictDirectConfigMissing 历史常量：免费层本应走代理池但无 SOCKS 配置。
	RouteVerdictDirectConfigMissing = "direct_config_missing"
	// RouteVerdictDirectUnexpected 历史常量：免费层已配代理却直连。
	RouteVerdictDirectUnexpected = "direct_unexpected"
	// RouteVerdictNoHealthyNode 历史常量：免费层已配代理但池空（本地 503）。
	RouteVerdictNoHealthyNode = "no_healthy_node"
)

// socksProxyConfigured 恒为 false：v2.0.0 起本进程不再有全局 SOCKS 池，
// 代理是「每个自定义源自己的 proxy 字段」，由 StaticProxyTransport 承担。
func socksProxyConfigured() bool { return false }

// routeVerdict 判定路由结论（纯函数：无锁、无全局状态，可单测）。
// nodes 必须是未被兜底改写的原始节点列表；tier 取值同 tierOfAuth。
func routeVerdict(nodes []string, tier string, socksConfigured bool, errMsg string) string {
	if hasNonEmptyNode(nodes) {
		return RouteVerdictProxied
	}
	if tier == "paid" || tier == "free" {
		return RouteVerdictDirectByDesign
	}
	return ""
}

// hasNonEmptyNode 报告节点列表里是否存在真实节点（空/纯空白视为无节点）。
func hasNonEmptyNode(nodes []string) bool {
	for _, n := range nodes {
		if strings.TrimSpace(n) != "" {
			return true
		}
	}
	return false
}
