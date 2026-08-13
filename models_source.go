package main

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/6Kmfi6HP/opencode2api/core/aggregator"
	"github.com/6Kmfi6HP/opencode2api/core/contract"
	chatRouter "github.com/6Kmfi6HP/opencode2api/core/router"
	"github.com/6Kmfi6HP/opencode2api/vendors/opencode"
	// 聚合厂商注册：blank import 触发各厂商 init() 自注册（扩增即生效）。
	// 新厂商加一行 blank import 即可，无需改装配逻辑。
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
			sc := *c // 流式去掉总超时（避免长推理流被切断）
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
				Params: vendorParams(pc.Type),
			}); err == nil {
				agg.Register(v)
			} else {
				slog.Warn("vendor create failed, skipped", "type", pc.Type, "error", err)
			}
		}
		return agg
	}

	// 未配置 → 自动注册所有已编译厂商（扩增供应商零配置）
	for _, t := range contract.RegisteredTypes() {
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
func vendorParams(t string) map[string]any {
	switch t {
	case "opencode":
		return map[string]any{
			opencode.ParamTransport:     rootTransport{},
			opencode.ParamAdminPassword: adminPassword,
		}
	case "windsurf":
		return map[string]any{}
	}
	return map[string]any{}
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

// 目录刷新防惊群：上游故障时 /v1/models、admin reload、定时 ticker 会同时触发拉取，
// 每个调用都打外部请求会放大成请求风暴。用一个 in-flight 互斥 + 最小间隔收敛：
// 同一时刻只允许一个刷新在途，刷新完成后 10s 内的并发调用直接跳过（读取已有缓存）。
var (
	catalogRefreshMu   sync.Mutex
	catalogRefreshing  bool
	catalogLastRefresh time.Time
)

// refreshModelCatalog 拉取各厂商目录并写入既有缓存（同步，启动与定时共用）。
func refreshModelCatalog() {
	if globalAgg == nil {
		return
	}
	catalogRefreshMu.Lock()
	if catalogRefreshing || time.Since(catalogLastRefresh) < 10*time.Second {
		catalogRefreshMu.Unlock()
		return
	}
	catalogRefreshing = true
	catalogRefreshMu.Unlock()

	defer func() {
		catalogRefreshMu.Lock()
		catalogRefreshing = false
		catalogLastRefresh = time.Now()
		catalogRefreshMu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := globalAgg.Refresh(ctx); err != nil {
		slog.Warn("model catalog refresh failed", "error", err)
	}
	syncModelsFromAggregator(globalAgg)
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
