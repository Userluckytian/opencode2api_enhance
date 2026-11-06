// 厂商聚合与模型目录（唯一数据源：core/aggregator）。
//
// v2.0.0 起本包只聚合自定义源（custom）与插件供应商（remote）：
// OpenCode 免费层已移出本进程，改由 40080 的 opencode2api-win 引擎承担。
// 因此这里不再有 modelsCache/goModelsCache 的 zen/go 分流、不再是「opencode 基础目录
// + 其它厂商并入」的两段式结构——所有厂商一视同仁，目录直接来自聚合器。
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/6Kmfi6HP/opencode2api/core/aggregator"
	"github.com/6Kmfi6HP/opencode2api/core/contract"
	"github.com/6Kmfi6HP/opencode2api/core/manager"
	chatRouter "github.com/6Kmfi6HP/opencode2api/core/router"
	"github.com/6Kmfi6HP/opencode2api/vendors/custom"
	// 聚合厂商注册：blank import 触发各厂商 init() 自注册（扩增即生效）。
	// 新厂商加一行 blank import 即可，无需改装配逻辑。
	_ "github.com/6Kmfi6HP/opencode2api/vendors/custom"
	_ "github.com/6Kmfi6HP/opencode2api/vendors/remote"
)

// globalAgg 是全局厂商聚合器（main 启动时装配；单元测试中保持 nil）。
var globalAgg *aggregator.Aggregator

// chatRouterVar 是全局「模型→厂商」路由器（main 启动时装配；测试默认 nil → 全部厂商）。
var chatRouterVar *chatRouter.Router

// newAggregator 按配置装配厂商聚合器。
//
// 单一职责：把 providers[] 里的每条配置变成一个已注册的 Vendor。
// 历史上这里有「供应商服务模式 / 显式配置 / 自动注册内建厂商」三条分支，
// 前两条是 OpenCode 免费层与 50080 分离的产物；现在两条路径语义相同，
// 合并为一条：按配置注册，未配置即空聚合器。
func newAggregator() *aggregator.Aggregator {
	agg := aggregator.New()

	configMu.RLock()
	cfgs := append([]ProviderCfg(nil), providersCfg...)
	configMu.RUnlock()

	for _, pc := range cfgs {
		if pc.Enabled != nil && !*pc.Enabled {
			continue
		}
		name := pc.Name
		if name == "" {
			name = pc.ID
		}
		v, err := contract.Create(pc.Type, contract.ProviderSpec{
			Type:   pc.Type,
			ID:     pc.ID,
			Name:   name,
			Params: mergeVendorParams(pc.Type, pc.Params),
		})
		if err != nil {
			slog.Warn("vendor create failed, skipped", "type", pc.Type, "id", pc.ID, "error", err)
			continue
		}
		agg.Register(v)
	}
	return agg
}

// vendorParams 按厂商类型注入运行时必需的装配参数（Transport 等）。
//
// G15：本函数只在 newAggregator 装配时读一次生效值并快照进 Vendor.cfg——
// 运行中修改供应商配置需重建聚合器才生效（applyConfig 热重载不重建已装配的 Vendor）。
func vendorParams(t string) map[string]any {
	switch t {
	case "custom":
		// 默认直连；条目带 proxy 时由 mergeVendorParams 换成 StaticProxyTransport。
		return map[string]any{
			contract.ParamTransport: contract.DirectTransport{},
		}
	case "remote":
		// 插件子进程仅监听 127.0.0.1，恒直连。
		return map[string]any{
			contract.ParamTransport: contract.DirectTransport{},
		}
	}
	return map[string]any{}
}

// globalProxyFile 只声明设置页维护的全局出口代理键：与 manager.Config 共用同一
// config.json，这里只读不写，避免把两套配置结构耦合在一起。
type globalProxyFile struct {
	EngineProxies []string `json:"engine_proxies"`
}

// globalEgressProxy 读取全局出口代理列表并返回第一个可用项（空 = 直连）。
//
// 自定义源勾选「使用代理」时只在配置里记 use_proxy 布尔值，具体地址每次装配
// 现读全局列表 —— 因此在设置页改了代理，无需逐个重存自定义源。
func globalEgressProxy() string {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return ""
	}
	var pc globalProxyFile
	if json.Unmarshal(data, &pc) != nil {
		return ""
	}
	return manager.FirstEgressProxy(pc.EngineProxies)
}

// mergeVendorParams 合并「类型级运行时注入」与「条目级配置参数」：
// 运行时键（"_" 前缀，如 _transport）不可被配置覆盖；其余以条目 params 为准。
//
// 例外：custom 源声明了 proxy（或勾选 use_proxy）时，这里把注入的传输换成
// StaticProxyTransport——代理地址是条目级配置，但传输实现必须由 core 构造
// （厂商不自建 client）。use_proxy 解析为全局代理列表首个可用项。
func mergeVendorParams(t string, entry map[string]any) map[string]any {
	merged := vendorParams(t)
	if t == "custom" {
		proxyURL, _ := entry[custom.ParamProxy].(string)
		proxyURL = strings.TrimSpace(proxyURL)
		if useProxy, _ := entry[custom.ParamUseProxy].(bool); proxyURL == "" && useProxy {
			proxyURL = globalEgressProxy()
		}
		if proxyURL != "" {
			merged[contract.ParamTransport] = contract.StaticProxyTransport{ProxyURL: proxyURL}
		}
	}
	if len(entry) == 0 {
		return merged
	}
	if merged == nil {
		merged = map[string]any{}
	}
	for k, v := range entry {
		if strings.HasPrefix(k, "_") {
			continue
		}
		merged[k] = v
	}
	return merged
}

// newChatRouter 按配置构造「模型→厂商」路由器（缺省取聚合器第一个厂商）。
func newChatRouter(agg *aggregator.Aggregator) *chatRouter.Router {
	configMu.RLock()
	mm := make(map[string]string, len(routingCfg.ModelProvider))
	for k, v := range routingCfg.ModelProvider {
		mm[k] = v
	}
	defaultID := routingCfg.DefaultProvider
	configMu.RUnlock()
	return chatRouter.New(agg, mm, defaultID)
}

// visibleFreeModel 一条按 /v1/models 展示语义的可见模型。
type visibleFreeModel struct {
	Visible  string // /v1/models 可见名（含供应商前缀，如 src1/gpt-4o）
	Raw      string // 聚合目录原始 ID（路由/上游调用用）
	Provider string // 提供厂商
}

// visibleFreeModels 按 /v1/models 同款语义汇总全部可见模型：
// 直接来自聚合器目录，同名冲突加厂商前缀。auto 候选匹配与 /v1/models 展示
// 共用本函数，保证「勾选所见 = auto 所路由」。
func visibleFreeModels(agg *aggregator.Aggregator) []visibleFreeModel {
	if agg == nil {
		return nil
	}
	out := make([]visibleFreeModel, 0, 16)
	have := make(map[string]bool, 16)
	for _, m := range agg.FreeModels() {
		id := m.ID
		if have[id] {
			id = m.Provider + "/" + m.ID
		}
		if have[id] {
			continue // 前缀后仍冲突 → 跳过重复
		}
		have[id] = true
		out = append(out, visibleFreeModel{Visible: id, Raw: m.ID, Provider: m.Provider})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Visible < out[j].Visible })
	return out
}

// appendOtherFreeModels 把聚合器目录并入 /v1/models 展示列表。
// 同名冲突时给后出现的厂商加前缀（如 "src1/gpt-4o"），
// 保证上层 /v1/models 可区分同名模型来自哪个厂商。
func appendOtherFreeModels(base []ModelInfo, agg *aggregator.Aggregator) []ModelInfo {
	if agg == nil {
		return base
	}
	have := make(map[string]bool, len(base))
	for _, m := range base {
		have[m.ID] = true
	}
	now := time.Now().Unix()
	out := append([]ModelInfo(nil), base...)
	for _, m := range agg.FreeModels() {
		id := m.ID
		if have[id] {
			id = m.Provider + "/" + m.ID
		}
		if have[id] {
			continue // 前缀后仍冲突（两个厂商同名）→ 跳过重复
		}
		have[id] = true
		// 厂商目录携带的能力元数据（上下文窗口/最大输出）随目录输出，
		// 供支持的客户端展示；未提供的厂商字段为零值不输出。
		out = append(out, ModelInfo{ID: id, Object: "model", Created: now, OwnedBy: m.Provider,
			ContextLength:   m.Caps.ContextWindow,
			MaxOutputTokens: m.Caps.MaxTokens})
	}
	return out
}

// 目录刷新防惊群：上游故障时 /v1/models（目录未就绪路径）每个请求都会触发一轮
// 外部拉取，放大成请求风暴。refreshModelCatalogIfDue 以 10s 最小间隔收敛；
// 刷新本身经 catalogRefreshMu 串行化，并发调用排队而非并发打上游。
var (
	catalogRefreshMu     sync.Mutex
	catalogLastRefresh   time.Time
	catalogRefreshMinGap = 10 * time.Second
)

// startInitialCatalogRefresh 启动期目录刷新异步化（先监听后刷新）。
// 同步刷新会对自定义供应商上游直连发起真实 HTTP 拉取（60s ctx 上限），
// 上游不可达时进程迟迟不监听。目录未就绪期间 /v1/models 走
// refreshModelCatalogIfDue 冷启动防惊群路径（10s 最小间隔），不引入语义缺口。
func startInitialCatalogRefresh() {
	go func() {
		refreshModelCatalog()
		modelMu.RLock()
		nLoaded := len(modelsCache)
		modelMu.RUnlock()
		if nLoaded > 0 {
			slog.Info("models loaded", "count", nLoaded)
		}
	}()
}

// refreshModelCatalog 拉取各厂商目录并写入既有缓存（同步，启动/厂商重建/定时共用）。
// 无条件刷新：调用方语义是「现在就要最新目录」。
func refreshModelCatalog() {
	if globalAgg == nil {
		return
	}
	catalogRefreshMu.Lock()
	defer catalogRefreshMu.Unlock()
	refreshModelCatalogLocked()
}

// refreshModelCatalogIfDue 带最小间隔的刷新：距上次完成不足 10s 时直接跳过。
// 供 /v1/models 冷启动路径防惊群（失败上游至多每 10s 被打一轮，而不是每请求）。
func refreshModelCatalogIfDue() {
	if globalAgg == nil {
		return
	}
	catalogRefreshMu.Lock()
	defer catalogRefreshMu.Unlock()
	if time.Since(catalogLastRefresh) < catalogRefreshMinGap {
		return
	}
	refreshModelCatalogLocked()
}

// catalogStaleAfter 目录「陈旧」判定阈值：超过它，/v1/models 会顺手在后台触发一轮刷新。
//
// 为什么需要：定时器稳态是 10 分钟，而运行期的模型变更（新增自定义源、插件换源、
// 上游上新模型）没有事件通知路径，只能等下一轮。用一个远小于 10 分钟、又远大于
// 单次刷新耗时的阈值，让「有人在看模型列表」这件事本身成为刷新触发条件 ——
// 客户端每次拉列表都可能带出一轮后台刷新，从而把可见延迟压到 ~30s，而不是 10 分钟。
//
// 不会打爆上游：真正的刷新仍受 refreshModelCatalogIfDue 的 catalogRefreshMinGap
// （10s）与 catalogRefreshMu（整轮串行）双重收敛，与请求量无关。
const catalogStaleAfter = 30 * time.Second

// refreshModelCatalogIfStale 目录非空但已陈旧时，后台触发一轮刷新（不阻塞调用方）。
// 供 /v1/models 在「目录已就绪」的分支调用：此时不再同步等待，只顺手推一把。
func refreshModelCatalogIfStale() {
	if globalAgg == nil {
		return
	}
	catalogRefreshMu.Lock()
	stale := time.Since(catalogLastRefresh) >= catalogStaleAfter
	catalogRefreshMu.Unlock()
	if !stale {
		return
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("async stale catalog refresh panic", "panic", r)
			}
		}()
		refreshModelCatalogIfDue() // 仍受 10s 最小间隔收敛
	}()
}

// refreshModelCatalogLocked 执行刷新（调用方已持 catalogRefreshMu）。
func refreshModelCatalogLocked() {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	vendors := globalAgg.Vendors()
	vendorIDs := make([]string, 0, len(vendors))
	for _, v := range vendors {
		vendorIDs = append(vendorIDs, v.ID())
	}
	if err := globalAgg.Refresh(ctx); err != nil {
		slog.Warn("model catalog refresh failed", "error", err, "vendors", vendorIDs)
	}
	catalog := globalAgg.Catalog()
	providerCounts := map[string]int{}
	for _, m := range catalog {
		providerCounts[m.Provider]++
	}
	slog.Info("model catalog refreshed",
		"vendors", vendorIDs,
		"total_models", len(catalog),
		"by_provider", providerCounts,
	)
	syncModelsFromAggregator(globalAgg)
	// 刷新失败也计时：配合 IfDue 的最小间隔，故障上游不会被连续重打。
	catalogLastRefresh = time.Now()
}

// syncModelsFromAggregator 把聚合目录写入 modelsCache（/v1/models 与路由的共同来源）。
//
// v2.0.0 起不再按 surface 分流 zen/go：OpenCode 的双目录已不存在，
// 所有厂商的模型平铺进同一份缓存。
func syncModelsFromAggregator(agg *aggregator.Aggregator) {
	catalog := agg.Catalog()
	if len(catalog) == 0 {
		return
	}
	now := time.Now().Unix()
	out := make([]ModelInfo, 0, len(catalog))
	for _, m := range catalog {
		out = append(out, ModelInfo{ID: m.ID, Object: "model", Created: now, OwnedBy: m.Provider})
	}
	modelMu.Lock()
	modelsCache = out
	modelsLoaded = true
	modelMu.Unlock()
}
