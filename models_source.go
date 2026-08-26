package main

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/6Kmfi6HP/opencode2api/core/aggregator"
	"github.com/6Kmfi6HP/opencode2api/core/contract"
	chatRouter "github.com/6Kmfi6HP/opencode2api/core/router"
	"github.com/6Kmfi6HP/opencode2api/vendors/opencode"
	// 聚合厂商注册：blank import 触发各厂商 init() 自注册（扩增即生效）。
	// 新厂商加一行 blank import 即可，无需改装配逻辑。
	_ "github.com/6Kmfi6HP/opencode2api/vendors/custom"
	_ "github.com/6Kmfi6HP/opencode2api/vendors/windsurf"
)

// surfaceGoKey 对应 contract.Model.Meta["surface"] 的 go 目录取值（见 vendors/opencode）。
const surfaceGoKey = "go"

// globalAgg 是全局厂商聚合器（main 启动时装配；单元测试中保持 nil）。
var globalAgg *aggregator.Aggregator

// chatRouterVar 是全局"模型→厂商"路由器（main 启动时装配；测试默认 nil → 单 opencode 兜底）。
var chatRouterVar *chatRouter.Router

// rootTransport 把 core/contract.Transport 桥接到本包既有的代理池/健康实现：
// 厂商（opencode）复用现有 SOCKS5 池、冷却与坏池逻辑；
// 单元测试替换 httpClient 时，经此桥自动生效。
type rootTransport struct{}

func (rootTransport) Client(tier contract.Tier, streaming bool) (*http.Client, string) {
	tt := TierFree
	if tier == contract.TierPaid {
		tt = TierPaid
	}
	if streaming {
		return getStreamingHTTPClientForTierWithProxy(tt)
	}
	return getHTTPClientForTierWithProxy(tt)
}

func (rootTransport) Mark(proxyAddr string, status int, reqErr error) {
	markSocks5Result(proxyAddr, status, reqErr)
}

// ---- S5 contract.RaceTracker：每节点 in-flight 上报 + 健康节点规模 ----

// HealthyNodeCount 实现 contract.RaceTracker：可竞速健康节点数（压力系数分母）。
func (rootTransport) HealthyNodeCount() int {
	return raceHealthyNodeCount()
}

// RaceStarted 实现 contract.RaceTracker：候选确定后每节点 in-flight +1。
// 与 RaceFinished 由 raceDo 的 defer 严格成对（见 chat.go raceDo）。
func (rootTransport) RaceStarted(addrs []string) {
	for _, a := range addrs {
		proxyInflightAdd(a, 1)
	}
}

// RaceFinished 实现 contract.RaceTracker：竞速收尾每节点 in-flight -1。
func (rootTransport) RaceFinished(addrs []string) {
	for _, a := range addrs {
		proxyInflightAdd(a, -1)
	}
}

// CandidateClients 实现 contract.Racer：质量优先返回至多 n 个竞速候选。
// 付费层直连无候选；竞速候选空时厂商回退普通 Client。
func (rootTransport) CandidateClients(tier contract.Tier, streaming bool, n int) ([]*http.Client, []string) {
	tt := TierFree
	if tier == contract.TierPaid {
		tt = TierPaid
	}
	if tt == TierPaid {
		return nil, nil // 付费层直连，不参与代理竞速
	}
	proxies := raceCandidates(n)
	if len(proxies) == 0 {
		return nil, nil
	}
	clients := make([]*http.Client, 0, len(proxies))
	addrs := make([]string, 0, len(proxies))
	for _, p := range proxies {
		c := clientForProxy(p)
		if streaming {
			// 流式保持无总超时：Client.Timeout 是请求全生命周期上限，会截断赢家
			// 长推理流；首字节等待预算（race_budget_ms）由 raceDo 在锁流阶段强制，
			// 锁流后继续沿用本 client 读流，不受预算影响。
			sc := *c
			sc.Timeout = 0
			c = &sc
		}
		clients = append(clients, c)
		addrs = append(addrs, p.Addr)
	}
	return clients, addrs
}

// newAggregator 装配厂商注册表：默认自动注册全部已编译厂商（扩增即生效），
// 配置 providers 可选覆盖（enabled=false 禁用 / id、name、params 覆盖）。
//
// 兼容基线：历史上单 opencode 且无 providers 配置时，行为与自动注册 opencode 一致。
// 调用方需保证 config 已 applyConfig（routingCfg 就绪，providersCfg 可选）。
func newAggregator() *aggregator.Aggregator {
	agg := aggregator.New()

	configMu.RLock()
	cfgs := append([]ProviderCfg(nil), providersCfg...)
	configMu.RUnlock()

	// 有显式配置 → 按配置注册（允许 enabled:false 关闭某厂商）
	if len(cfgs) > 0 {
		for _, pc := range cfgs {
			if pc.Enabled != nil && !*pc.Enabled {
				continue
			}
			name := pc.Name
			if name == "" {
				name = pc.ID
			}
			if v, err := contract.Create(pc.Type, contract.ProviderSpec{
				Type:   pc.Type,
				ID:     pc.ID,
				Name:   name,
				Params: mergeVendorParams(pc.Type, pc.Params),
			}); err == nil {
				agg.Register(v)
			} else {
				slog.Warn("vendor create failed, skipped", "type", pc.Type, "error", err)
			}
		}
		return agg
	}

	// 未配置 → 自动注册所有已编译厂商（扩增供应商零配置）。
	// custom 例外：必须带 base_url 等条目级参数，无参自动注册必失败 → 跳过。
	// remote 例外：必须由插件管理器注入子进程端点/令牌（R2 桥接），无参无法构造 → 跳过。
	for _, t := range contract.RegisteredTypes() {
		if t == "custom" || t == "remote" {
			continue
		}
		if v, err := contract.Create(t, contract.ProviderSpec{
			Type:   t,
			Params: vendorParams(t),
		}); err == nil {
			agg.Register(v)
		} else {
			slog.Warn("vendor auto-register failed", "type", t, "error", err)
		}
	}
	return agg
}

// vendorParams 按厂商类型注入运行时必需的装配参数（Transport/AdminPassword 等）。
// G15：本函数只在 newAggregator 装配时读一次生效值并快照进 Vendor.cfg——
// 运行中修改 pool_race_copies / race_budget_ms / 压力阈值 / 429 参数
// 需重启实例/网关才生效（applyConfig 热重载不重建已装配的 Vendor）。
func vendorParams(t string) map[string]any {
	switch t {
	case "opencode":
		return map[string]any{
			opencode.ParamTransport:        rootTransport{},
			opencode.ParamAdminPassword:    adminPassword,
			opencode.ParamRaceCopies:       int(poolRaceCopies.Load()),
			opencode.ParamRaceBudgetMS:     int(raceBudgetMS.Load()),
			opencode.ParamRacePressureLow:  poolRacePressureLow.Load().(float64),
			opencode.ParamRacePressureHigh: poolRacePressureHigh.Load().(float64),
			opencode.ParamRateLimitCooldownSec:  int(rateLimitCooldownSec.Load()),
			opencode.ParamRateLimitBackoffBaseMS: int(rateLimitBackoffBaseMS.Load()),
			opencode.ParamRateLimitBackoffCapMS:  int(rateLimitBackoffCapMS.Load()),
		}
	case "windsurf":
		return map[string]any{}
	case "custom":
		// 自定义模型源：只需传输注入（出站经统一网关，直连或代理池由 via_proxy 决定）。
		return map[string]any{
			opencode.ParamTransport: rootTransport{},
		}
	}
	return map[string]any{}
}

// mergeVendorParams 合并「类型级运行时注入」与「条目级配置参数」：
// 运行时键（"_" 前缀，如 _transport）不可被配置覆盖；其余以条目 params 为准。
func mergeVendorParams(t string, entry map[string]any) map[string]any {
	merged := vendorParams(t)
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

// newChatRouter 按配置构造"模型→厂商"路由器（缺省默认 opencode）。
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

// appendOtherFreeModels 把 opencode 之外其它厂商的免费模型并入展示列表。
// 同名冲突时给后出现的厂商加前缀（如 "windsurf/swe-1-6-slow"），
// 保证上层 /v1/models 可区分同名模型来自哪个厂商。
func appendOtherFreeModels(base []ModelInfo, agg *aggregator.Aggregator) []ModelInfo {
	if agg == nil {
		return base
	}
	openOnly := true
	for _, v := range agg.Vendors() {
		if v.ID() != "opencode" {
			openOnly = false
			break
		}
	}
	if openOnly {
		return base
	}

	have := make(map[string]bool, len(base))
	for _, m := range base {
		have[m.ID] = true
	}
	now := time.Now().Unix()
	out := append([]ModelInfo(nil), base...)
	for _, m := range agg.FreeModels() {
		if m.Provider == "opencode" {
			continue // zen 免费模型已由主路径输出
		}
		id := m.ID
		if have[id] {
			id = m.Provider + "/" + m.ID
		}
		if have[id] {
			continue // 前缀后仍冲突（两个非 opencode 厂商同名）→ 跳过重复
		}
		have[id] = true
		out = append(out, ModelInfo{ID: id, Object: "model", Created: now, OwnedBy: m.Provider})
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

// refreshModelCatalog 拉取各厂商目录并写入既有缓存（同步，启动/厂商重建/定时共用）。
// 无条件刷新：调用方语义是"现在就要最新目录"（如自定义源增删改后的重建）。
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
	modelCount := len(catalog)
	providerCounts := map[string]int{}
	for _, m := range catalog {
		providerCounts[m.Provider]++
	}
	slog.Info("model catalog refreshed",
		"gateway_mode", gatewayMode,
		"vendors", vendorIDs,
		"total_models", modelCount,
		"by_provider", providerCounts,
	)
	syncModelsFromAggregator(globalAgg)
	// 目录代际+1：请求侧 syncVendorState/seedVendorCatalog 检测到变化才重新 SetCatalog。
	catalogGen.Add(1)
	// 刷新失败也计时：配合 IfDue 的最小间隔，故障上游不会被连续重打。
	catalogLastRefresh = time.Now()
}

// syncModelsFromAggregator 把聚合目录中 opencode 厂商的模型写入 modelsCache / goModelsCache
// （按 surface 分流），保证单厂商配置下 /v1/models 与路由行为与基线一致。
// 非 opencode 厂商（如 windsurf）的模型**不写入**缓存——它们由 appendOtherFreeModels
// 在 /v1/models 响应层单独并入，避免混入基础列表导致同名冲突加错前缀/重复展示。
func syncModelsFromAggregator(agg *aggregator.Aggregator) {
	catalog := agg.Catalog()
	if len(catalog) == 0 {
		return
	}
	now := time.Now().Unix()
	var zen, goM []ModelInfo
	for _, m := range catalog {
		if m.Provider != "opencode" {
			continue // 仅 opencode 进入基础缓存；其它厂商模型由响应层并入
		}
		info := ModelInfo{ID: m.ID, Object: "model", Created: now, OwnedBy: m.Provider}
		if m.Meta != nil && m.Meta["surface"] == surfaceGoKey {
			goM = append(goM, info)
		} else {
			zen = append(zen, info)
		}
	}
	modelMu.Lock()
	if len(zen) > 0 {
		modelsCache = zen
		modelsLoaded = true
	}
	if len(goM) > 0 {
		goModelsCache = goM
	}
	modelMu.Unlock()
}
