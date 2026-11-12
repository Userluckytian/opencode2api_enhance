package manager

// 供应商分类单一真源（阶段 L / Task 2）。
//
// 三类口径（与前端 src/lib/vendor.ts 同款规则，两处注释互相指向）：
//   - kilo     : r.Vendor == "kilo"（独立 30080 网关）
//   - opencode : r.Vendor == "opencode"，或来源标注为引擎，或池路径判定
//   - custom   : 其余（自定义源 / 插件供应商 / 未知）
//
// 旧记录（无 vendor 字段）靠 model 前缀与 route_verdict 兜底，保证历史日志
// 在新增筛选后仍能被正确归类，而不是全部掉进 custom。

import "strings"

// VendorKind 供应商下拉的三个值（空串 = 不过滤）。
const (
	VendorKilo     = "kilo"
	VendorOpencode = "opencode"
	VendorCustom   = "custom"
)

// ClassifyVendor 把一条调用日志归到 kilo / opencode / custom。
func ClassifyVendor(r *CallLogRecord) string {
	if r == nil {
		return VendorCustom
	}
	v := strings.ToLower(strings.TrimSpace(r.Vendor))
	if v == VendorKilo {
		return VendorKilo
	}
	if v == VendorOpencode {
		return VendorOpencode
	}
	// 引擎 stdout 记录带固定来源名（见 calllog_engine.go）
	if strings.Contains(r.Source, "引擎") || strings.Contains(strings.ToLower(r.Source), "engine") {
		return VendorOpencode
	}
	// Kilo 来源标注兜底（旧版落盘记录可能没有 vendor 字段）
	if strings.Contains(r.Source, "Kilo") {
		return VendorKilo
	}
	// 池路径（免费层）判定：无自定义前缀 + 明确的池路由结论
	modelHasSlash := strings.Contains(r.Model, "/")
	switch r.RouteVerdict {
	case "proxied", "no_healthy_node":
		return VendorOpencode
	case "direct_by_design", "direct_config_missing", "direct_unexpected":
		// 直连且带前缀 → 自定义源；Kilo 已由 vendor/source 命中
		if modelHasSlash {
			return VendorCustom
		}
		return VendorOpencode
	}
	if v != "" {
		// 具体厂商 ID（xhx / nv / buddyhub …）一律归入「自定义」
		return VendorCustom
	}
	if modelHasSlash {
		return VendorCustom
	}
	return VendorOpencode
}
