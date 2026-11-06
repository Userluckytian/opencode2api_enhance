// 插件式供应商 → remote 厂商桥接装配（R2，设计定稿 docs/PLUGIN-PROVIDERS.md §四）：
// pluginprovider.Manager.OnChange（子进程就绪/状态/增删变化）→ syncPlugins 重建插件
// 厂商集合 → 并入 rebuildVendors 热重建（与 providersCfg 厂商同链路：模型目录 / failover /
// 统计 / 日志全部复用统一链路，见设计文档 §4.3「注册厂商（走 rebuildVendors() 热重建）」）。
//
// 装配只发生在主进程（main）：子进程端点 url + 一次性令牌来自 RunningPlugins()，
// 静态配置无法表达（随机端口），因此不走 contract 注册表自动装配路径。
package main

import (
	"log/slog"
	"strings"
	"sync"

	"github.com/6Kmfi6HP/opencode2api/core/contract"
	"github.com/6Kmfi6HP/opencode2api/core/manager/pluginprovider"
	"github.com/6Kmfi6HP/opencode2api/vendors/remote"
)

var (
	// pluginMgrGlobal 装配后的插件管理器（syncPlugins 回调读取）。bindPluginMgr 在
	// Start() 之前调用：就绪行由监督协程异步到达，写读有 happens-before 链，无竞态。
	pluginMgrGlobal *pluginprovider.Manager

	// pluginVendors 当前 running 插件的 remote 厂商集合（syncPlugins 重建，rebuildVendors 合并用）。
	pluginVendorsMu sync.Mutex
	pluginVendors   []contract.Vendor

	// pluginSigMu 保护插件集合签名（防 OnChange 风暴触发无关目录刷新）。
	pluginSigMu   sync.Mutex
	lastPluginSig string
)

// bindPluginMgr 记录插件管理器全局引用并返回之（main 装配点：先 bind 后 Start）。
func bindPluginMgr(pm *pluginprovider.Manager) *pluginprovider.Manager {
	pluginMgrGlobal = pm
	return pm
}

// syncPlugins pluginMgr.OnChange 回调：running 插件集合变化时重建插件厂商并触发
// 聚合器热重建。崩溃退避/启停过渡等不改变 running 集合的状态抖动（签名未变）直接
// 跳过，避免目录刷新风暴（rebuildVendors 内部仍串行化）。
func syncPlugins() {
	pm := pluginMgrGlobal
	if pm == nil || globalAgg == nil {
		return
	}
	rps := pm.RunningPlugins()
	pluginSigMu.Lock()
	sig := pluginSigOf(rps)
	if sig == lastPluginSig {
		pluginSigMu.Unlock()
		return
	}
	lastPluginSig = sig
	pluginSigMu.Unlock()

	vendors := make([]contract.Vendor, 0, len(rps))
	for _, rp := range rps {
		v, err := remote.New(remote.Config{
			ID:        rp.ID,
			Name:      rp.Name,
			BaseURL:   rp.URL,
			AuthToken: rp.Auth,
			// 模型暴露白名单（主进程侧过滤；ExposeAll=false 时只暴露 ExposedModels）。
			AllowedModels: allowedOf(rp),
			// 与 custom 同款：注入网关传输（直连 TierPaid 语义，子进程仅监听 127.0.0.1）。
			Transport: contract.DirectTransport{},
		})
		if err != nil {
			slog.Warn("remote vendor create failed, skipped", "id", rp.ID, "error", err)
			continue
		}
		vendors = append(vendors, v)
	}
	pluginVendorsMu.Lock()
	pluginVendors = vendors
	pluginVendorsMu.Unlock()
	rebuildVendors()
}

// appendPluginVendors 把当前插件厂商并入待替换集合（rebuildVendors 结尾调用）。
// 插件集合由 syncPlugins 独立维护，不经 providersCfg 逻辑——两路合并后一次 ReplaceAll，
// 保证任一来源变化（配置保存 / 插件就绪 / 插件崩溃）都不丢另一路厂商。
func appendPluginVendors(list []contract.Vendor) []contract.Vendor {
	pluginVendorsMu.Lock()
	pvs := append([]contract.Vendor(nil), pluginVendors...)
	pluginVendorsMu.Unlock()
	return append(list, pvs...)
}

// allowedOf 计算 remote vendor 的暴露白名单（ExposeAll=false 时取 ExposedModels，
// 否则 nil = 全部暴露）。
func allowedOf(rp pluginprovider.RunningPlugin) []string {
	if rp.ExposeAll {
		return nil
	}
	return rp.ExposedModels
}

// pluginSigOf running 插件集合签名（RunningPlugins 已按 id 排序，拼接即稳定指纹）。
func pluginSigOf(rps []pluginprovider.RunningPlugin) string {
	var sb strings.Builder
	for _, rp := range rps {
		sb.WriteString(rp.ID)
		sb.WriteByte('|')
		sb.WriteString(rp.URL)
		sb.WriteByte('|')
		sb.WriteString(rp.Auth)
		sb.WriteByte('|')
		// 暴露白名单纳入签名：白名单变化需触发重建（remote vendor 的 AllowedModels）。
		if rp.ExposeAll {
			sb.WriteString("all")
		} else {
			sb.WriteString(strings.Join(rp.ExposedModels, ","))
		}
		sb.WriteByte('\n')
	}
	return sb.String()
}

// onPluginModelRefresh 模型清单就绪/手动刷新回调：无节流强制刷新聚合目录（/v1/models 即时更新）。
//
// 刷目录前先 syncPlugins()：插件的「就绪」与「模型清单到位」是两个异步事件，
// OnChange→syncPlugins→rebuildVendors 可能与窗口末刷新并发。若某个 remote vendor
// 尚未 ReplaceAll 进聚合器，这次刷新就看不到它的模型；而 modelsCache 随后非空
// （自定义源模型已在），冷启动补救路径 refreshModelCatalogIfDue 会被短路，
// 只能等 10 分钟定时器 —— 正是本次要修的症状残留。
//
// syncPlugins 自带签名判断：集合未变时直接 return，不重建厂商、也不触发
// rebuildVendors（refreshModelCatalog 不产生任何插件通知），不会形成自激循环。
func onPluginModelRefresh(id string) {
	if globalAgg == nil {
		return
	}
	syncPlugins()
	refreshModelCatalog()
	slog.Info("plugin models refreshed", "plugin", id)
}
