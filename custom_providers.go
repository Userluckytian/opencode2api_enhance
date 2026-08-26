// 自定义模型源：管理 API（列表/保存/连通测试）+ 厂商热重建。
// 保存流程：写核心配置 → applyConfig（内存生效）→ 重建厂商集合（非 custom 实例复用）
// → 刷新模型目录 → 同步 manager 配置透传（供后续生成的实例/网关子进程继承）。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/6Kmfi6HP/opencode2api/core/contract"
	"github.com/6Kmfi6HP/opencode2api/core/manager"
	"github.com/6Kmfi6HP/opencode2api/vendors/custom"
)

// ---------------------------------------------------------------------------
// 厂商热重建
// ---------------------------------------------------------------------------

var (
	vendorsSigMu   sync.Mutex
	lastVendorsSig string
	// rebuildVendorsMu 串行化厂商热重建：config watcher（maybeRebuildVendors）/ 自定义源
	// 保存（applyCustomProvidersSave）/ 插件 OnChange（syncPlugins）三来源并发触发，
	// 防 ReplaceAll 与目录刷新交错导致聚合目录短暂对应错误厂商集合。
	rebuildVendorsMu sync.Mutex
)

// providersSignature providers 配置签名（变化才重建）。
func providersSignature() string {
	configMu.RLock()
	cfgs := append([]ProviderCfg(nil), providersCfg...)
	configMu.RUnlock()
	b, err := json.Marshal(cfgs)
	if err != nil {
		return ""
	}
	return string(b)
}

// initVendorsSignature 启动装配完成后记录初始签名（避免首次无关配置变更触发重建）。
func initVendorsSignature() {
	vendorsSigMu.Lock()
	lastVendorsSig = providersSignature()
	vendorsSigMu.Unlock()
}

// maybeRebuildVendors providers 配置变化时重建厂商集合。
func maybeRebuildVendors() {
	sig := providersSignature()
	vendorsSigMu.Lock()
	same := sig == lastVendorsSig
	vendorsSigMu.Unlock()
	if same {
		return
	}
	rebuildVendors()
}

// rebuildVendors 按 providersCfg 重建聚合器内的厂商集合（原聚合器实例不动，全局指针稳定）：
// 非 custom 旧实例按 ID 复用（保住 opencode 会话缓存 / windsurf 账号池状态），
// custom 恒重建（无状态，参数可能已变），随后刷新模型目录。
func rebuildVendors() {
	if globalAgg == nil {
		return
	}
	rebuildVendorsMu.Lock()
	defer rebuildVendorsMu.Unlock()
	configMu.RLock()
	cfgs := append([]ProviderCfg(nil), providersCfg...)
	configMu.RUnlock()

	old := map[string]contract.Vendor{}
	for _, v := range globalAgg.Vendors() {
		old[v.ID()] = v
	}
	// 诊断日志：重建前快照
	oldIDs := make([]string, 0, len(old))
	for id := range old {
		oldIDs = append(oldIDs, id)
	}
	cfgIDs := make([]string, 0, len(cfgs))
	for _, pc := range cfgs {
		if pc.ID != "" && (pc.Enabled == nil || *pc.Enabled) {
			cfgIDs = append(cfgIDs, pc.ID+"("+pc.Type+")")
		}
	}
	slog.Info("rebuildVendors: pre-rebuild snapshot",
		"old_vendors", oldIDs,
		"cfg_providers", cfgIDs,
		"config_file", configPath,
		"gateway_mode", gatewayMode,
	)
	create := func(pc ProviderCfg) (contract.Vendor, bool) {
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
			slog.Warn("vendor create failed during rebuild, skipped", "type", pc.Type, "id", pc.ID, "error", err)
			return nil, false
		}
		return v, true
	}

	var list []contract.Vendor
	reused := []string{}
	fresh := []string{}
	skipped := []string{}
	if len(cfgs) > 0 {
		for _, pc := range cfgs {
			if pc.ID == "" || (pc.Enabled != nil && !*pc.Enabled) {
				continue
			}
			// 非 custom：同 ID 旧实例直接复用。
			if pc.Type != "custom" {
				if v, ok := old[pc.ID]; ok {
					list = append(list, v)
					reused = append(reused, pc.ID)
					continue
				}
			}
			if v, ok := create(pc); ok {
				list = append(list, v)
				fresh = append(fresh, pc.ID)
			} else {
				skipped = append(skipped, pc.ID)
			}
		}
	} else {
		// 未配置 providers：自动注册全部内建类型（custom/remote 除外——
		// custom 需条目级参数、remote 需插件子进程端点，无参构造必失败，见 newAggregator 同款语义）。
		for _, t := range contract.RegisteredTypes() {
			if t == "custom" || t == "remote" {
				continue
			}
			if v, ok := old[t]; ok {
				list = append(list, v)
				continue
			}
			if v, ok := create(ProviderCfg{ID: t, Type: t}); ok {
				list = append(list, v)
			}
		}
	}

	// R2：并入 running 插件的 remote 厂商（syncPlugins 独立维护，见 plugin_vendors.go）。
	// 与 providersCfg 厂商一次 ReplaceAll——插件就绪/崩溃与配置保存互不丢厂商。
	list = appendPluginVendors(list)

	globalAgg.ReplaceAll(list)
	vendorsSigMu.Lock()
	lastVendorsSig = providersSignature()
	vendorsSigMu.Unlock()
	resultIDs := make([]string, 0, len(list))
	for _, v := range list {
		resultIDs = append(resultIDs, v.ID())
	}
	slog.Info("vendors rebuilt",
		"count", len(list),
		"reused", reused,
		"fresh", fresh,
		"skipped", skipped,
		"result_ids", resultIDs,
	)
	refreshModelCatalog()
}

// ---------------------------------------------------------------------------
// 视图与请求类型
// ---------------------------------------------------------------------------

// customProviderView 列表项。key 明文回传给已鉴权的面板（单用户/内网定位，
// key 本就明文存于本机 config.json）：编辑表单直接回填，免得每次测试连通重新粘贴。
type customProviderView struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Protocol      string   `json:"protocol"`
	BaseURL       string   `json:"base_url"`
	APIKeys       []string `json:"api_keys"` // 全部 key（明文回填编辑表单）
	APIKey        string   `json:"api_key"`  // 首 key（旧 UI 兼容）
	APIKeySet     bool     `json:"api_key_set"`
	KeyStrategy   string   `json:"key_strategy"` // round_robin | failover
	ViaProxy      bool     `json:"via_proxy"`
	Enabled       bool     `json:"enabled"`
	Models        int      `json:"models"`                 // 聚合目录中该源模型数（实时，经白名单过滤）
	ModelsAll     []string `json:"models_all"`             // 全量模型清单（上游 ID，编辑勾选用）
	AllowedModels []string `json:"allowed_models"`         // 暴露白名单（空 = 全部暴露）
	LastSuccess   string   `json:"last_success,omitempty"` // 最近一次成功（探测/请求）
	// key 健康计数（运行时快照；无活实例时全 0）
	KeysTotal     int    `json:"keys_total"`
	KeysAvailable int    `json:"keys_available"`
	KeysCooling   int    `json:"keys_cooling"`
	KeysDisabled  int    `json:"keys_disabled"`
	LastError     string `json:"last_error,omitempty"`
}

// customProviderInput 保存请求中的单个源定义。
type customProviderInput struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Protocol      string   `json:"protocol"`
	BaseURL       string   `json:"base_url"`
	APIKey        string   `json:"api_key"`        // 单 key 兼容输入
	APIKeys       []string `json:"api_keys"`       // 多 key（优先于 api_key）
	KeyStrategy   string   `json:"key_strategy"`   // round_robin（默认）| failover
	AllowedModels []string `json:"allowed_models"` // 暴露白名单（空 = 全部暴露）
	ViaProxy      bool     `json:"via_proxy"`
	Enabled       *bool    `json:"enabled"`
}

// customProvidersView 聚合目录中该源模型数。
func customModelCounts() map[string]int {
	counts := map[string]int{}
	if globalAgg == nil {
		return counts
	}
	for _, m := range globalAgg.Catalog() {
		counts[m.Provider]++
	}
	return counts
}

// vendorLastErr 查该源 Health().LastError（无实例返回空）。
func vendorLastErr(id string) string {
	if globalAgg == nil {
		return ""
	}
	for _, v := range globalAgg.Vendors() {
		if v.ID() == id {
			return v.Health().LastError
		}
	}
	return ""
}

// customProviderViews 读核心配置中的 custom 条目并装配视图。
func customProviderViews() []customProviderView {
	cfg := loadConfig(configPath)
	counts := customModelCounts()
	views := make([]customProviderView, 0, 4)
	for _, pc := range cfg.Providers {
		if pc.Type != "custom" {
			continue
		}
		enabled := pc.Enabled == nil || *pc.Enabled
		p := pc.Params
		if p == nil {
			p = map[string]any{}
		}
		via, _ := p[custom.ParamViaProxy].(bool)
		proto, _ := p[custom.ParamProtocol].(string)
		if proto == "" {
			proto = custom.ProtoOpenAI
		}
		baseURL, _ := p[custom.ParamBaseURL].(string)
		keys := customKeyListFromParams(p)
		firstKey := ""
		if len(keys) > 0 {
			firstKey = keys[0]
		}
		strategy, _ := p[custom.ParamKeyStrategy].(string)
		if strategy != custom.StrategyFailover {
			strategy = custom.StrategyRoundRobin
		}
		kh := vendorKeyHealth(pc.ID)
		allowed, _ := p[custom.ParamAllowedModels].([]any)
		allowedIDs := make([]string, 0, len(allowed))
		for _, a := range allowed {
			if s, ok := a.(string); ok {
				allowedIDs = append(allowedIDs, s)
			}
		}
		views = append(views, customProviderView{
			ID: pc.ID, Name: pc.Name, Protocol: proto, BaseURL: baseURL,
			APIKeys: keys, APIKey: firstKey, APIKeySet: len(keys) > 0,
			KeyStrategy: strategy,
			ViaProxy:    via, Enabled: enabled,
			Models: counts[pc.ID], LastError: vendorLastErr(pc.ID),
			ModelsAll: vendorFullModels(pc.ID), AllowedModels: allowedIDs,
			LastSuccess: vendorLastSuccess(pc.ID),
			KeysTotal:   kh.Total, KeysAvailable: kh.Available, KeysCooling: kh.Cooling, KeysDisabled: kh.Disabled,
		})
	}
	return views
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// customProvidersHandler GET：当前自定义源列表。
func customProvidersHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeAdminJSON(w, map[string]any{"providers": customProviderViews()})
	}
}

var customIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,31}$`)

// customProvidersSaveHandler POST：整表保存自定义源集合（增/改/删一次到位）。
func customProvidersSaveHandler(m *manager.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Providers []customProviderInput `json:"providers"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeAdminErr(w, http.StatusBadRequest, "请求体需为 {\"providers\":[...]}")
			return
		}
		views, err := applyCustomProvidersSave(m, req.Providers)
		if err != nil {
			writeAdminErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeAdminJSON(w, map[string]any{"status": "ok", "providers": views})
	}
}

// customProvidersClearHandler POST：清空全部自定义源（含目录磁盘缓存）。
// 与设置页「数据清理」相互独立——那里任何级别都不再动自定义模型源。
func customProvidersClearHandler(m *manager.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		views, err := applyCustomProvidersSave(m, nil)
		if err != nil {
			writeAdminErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if err := custom.PurgeAllModelCaches(); err != nil {
			slog.Warn("purge custom model caches failed", "error", err)
		}
		writeAdminJSON(w, map[string]any{"status": "ok", "cleared": true, "providers": views})
	}
}

// applyCustomProvidersSave 校验并整表落盘自定义源集合：保存 → 热重建 →
// manager 透传 → 子进程传播，返回最新视图。inputs 为 nil 即清空全部自定义源
// （非 custom 条目保留）。
func applyCustomProvidersSave(m *manager.Manager, inputs []customProviderInput) ([]customProviderView, error) {
	cfg := loadConfig(configPath)

	// 保留非 custom 条目；providers 原本为空（隐式全注册）→ 物化内建条目，
	// 防显式列表只含 custom 导致内建源全部失效。
	var kept []ProviderCfg
	for _, pc := range cfg.Providers {
		if pc.Type != "custom" {
			kept = append(kept, pc)
		}
	}
	if len(cfg.Providers) == 0 {
		for _, t := range []string{"opencode", "windsurf"} {
			enabled := true
			kept = append(kept, ProviderCfg{ID: t, Type: t, Enabled: &enabled})
		}
	}

	seen := map[string]bool{}
	// 旧 custom 条目的 key 列表（按 id）：编辑时 keys 全留空 → 保留旧 keys（「留空则不修改」）。
	oldKeys := map[string][]string{}
	// 旧 custom 条目的模型白名单（按 id）：整表提交缺 allowed_models 键时保留旧值，
	// 避免「改启停洗掉白名单」（问题 4，对齐 keys 的「留空=保留旧」惯例）。
	oldAllowed := map[string][]string{}
	for _, pc := range cfg.Providers {
		if pc.Type == "custom" {
			if ks := customKeyListFromParams(pc.Params); len(ks) > 0 {
				oldKeys[pc.ID] = ks
			}
			if am := customAllowedListFromParams(pc.Params); len(am) > 0 {
				oldAllowed[pc.ID] = am
			}
		}
	}
	for _, in := range inputs {
		in.ID = strings.TrimSpace(in.ID)
		if !customIDPattern.MatchString(in.ID) {
			return nil, fmt.Errorf("无效 id %q：需字母数字开头，可含 - _，≤32 字符", in.ID)
		}
		if in.ID == "opencode" || in.ID == "windsurf" {
			return nil, fmt.Errorf("id %q 与内建厂商冲突", in.ID)
		}
		if seen[in.ID] {
			return nil, fmt.Errorf("重复 id %q", in.ID)
		}
		seen[in.ID] = true
		in.BaseURL = strings.TrimRight(strings.TrimSpace(in.BaseURL), "/")
		u, err := url.Parse(in.BaseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("源 %s：base_url 需为 http(s) 地址", in.ID)
		}
		switch in.Protocol {
		case "":
			in.Protocol = custom.ProtoOpenAI
		case custom.ProtoOpenAI, custom.ProtoAnthropic, custom.ProtoGemini, custom.ProtoResponses:
		default:
			return nil, fmt.Errorf("源 %s：协议需为 openai|anthropic|gemini|responses", in.ID)
		}
		strategy := in.KeyStrategy
		if strategy == "" {
			strategy = custom.StrategyRoundRobin
		}
		if strategy != custom.StrategyRoundRobin && strategy != custom.StrategyFailover {
			return nil, fmt.Errorf("源 %s：key 策略需为 round_robin|failover", in.ID)
		}
		keys := inputKeyList(in)
		if len(keys) == 0 {
			keys = oldKeys[in.ID] // 编辑时全留空 = 保留旧 keys
		}
		allowed := make([]string, 0, len(in.AllowedModels))
		for _, am := range in.AllowedModels {
			if am = strings.TrimSpace(am); am != "" {
				allowed = append(allowed, am)
			}
		}
		enabled := in.Enabled == nil || *in.Enabled
		params := map[string]any{
			custom.ParamBaseURL:     in.BaseURL,
			custom.ParamProtocol:    in.Protocol,
			custom.ParamKeyStrategy: strategy,
			custom.ParamViaProxy:    in.ViaProxy,
		}
		if len(keys) > 0 {
			params[custom.ParamAPIKeys] = keys
		}
		// allowed_models：区分「键缺失」与「键为空数组」（问题 4 修复核心）——
		// nil（请求缺键，如整表提交漏带）→ 保留旧白名单，避免改启停洗掉白名单；
		// 显式空数组 → 不写键（= 全部暴露）；非空 → 写入白名单。
		if in.AllowedModels != nil {
			if len(allowed) > 0 {
				params[custom.ParamAllowedModels] = allowed
			}
		} else if old, ok := oldAllowed[in.ID]; ok {
			params[custom.ParamAllowedModels] = old
		}
		pc := ProviderCfg{
			ID: in.ID, Type: "custom", Name: strings.TrimSpace(in.Name),
			Enabled: &enabled, Params: params,
		}
		kept = append(kept, pc)
	}

	cfg.Providers = kept
	if err := saveConfig(configPath, cfg); err != nil {
		return nil, fmt.Errorf("配置写入失败: %w", err)
	}
	// 诊断日志：保存前后 providers 对比
	keptIDs := make([]string, 0, len(kept))
	for _, pc := range kept {
		keptIDs = append(keptIDs, pc.ID+"("+pc.Type+")")
	}
	slog.Info("applyCustomProvidersSave: config saved",
		"configPath", configPath,
		"providers", keptIDs,
	)
	applyConfig(cfg)
	rebuildVendors()

	// manager 配置透传：后续生成的实例/网关子进程配置继承自定义源（尽力而为）。
	if m != nil {
		if err := m.SyncCustomProviders(configPath, customEntriesForManager(kept)); err != nil {
			slog.Warn("sync custom providers to manager config failed", "error", err)
		}
		// 传播到已存在实例/网关的 runtime 配置：运行中的子进程经 1s 配置监视
		// 热重建厂商（不重启即出现在其 /v1/models），停着的实例保持磁盘一致。
		if err := m.PropagateCustomProviders(); err != nil {
			slog.Warn("propagate custom providers to runtime configs failed", "error", err)
		}
	}

	return customProviderViews(), nil
}

// customEntriesForManager 把 ProviderCfg 集合转为 manager 透传的 map 形态（custom 部分）。
func customEntriesForManager(cfgs []ProviderCfg) []map[string]any {
	var out []map[string]any
	for _, pc := range cfgs {
		if pc.Type != "custom" {
			continue
		}
		m := map[string]any{"id": pc.ID, "type": "custom"}
		if pc.Name != "" {
			m["name"] = pc.Name
		}
		if pc.Enabled != nil {
			m["enabled"] = *pc.Enabled
		}
		if len(pc.Params) > 0 {
			m["params"] = pc.Params
		}
		out = append(out, m)
	}
	return out
}

// customProvidersTestHandler POST：连通测试（不落盘）——逐 key 构造临时实例拉取模型目录，
// 返回每个 key 的结果（多 key 一键全验）。
func customProvidersTestHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var in customProviderInput
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeAdminErr(w, http.StatusBadRequest, "请求体需为 {id?,name?,protocol,base_url,api_keys|api_key}")
			return
		}
		in.BaseURL = strings.TrimRight(strings.TrimSpace(in.BaseURL), "/")
		if in.Protocol == "" {
			in.Protocol = custom.ProtoOpenAI
		}
		if in.ID == "" {
			in.ID = "_test"
		}
		keys := inputKeyList(in)
		if len(keys) == 0 {
			keys = []string{""} // 无 key（本地网关）也测
		}
		type keyResult struct {
			KeyTail   string `json:"key_tail"` // 末 4 位（明文不整段回显）
			OK        bool   `json:"ok"`
			Count     int    `json:"count,omitempty"`
			LatencyMS int64  `json:"latency_ms"`
			Error     string `json:"error,omitempty"`
		}
		results := make([]keyResult, 0, len(keys))
		allOK := true
		var firstOKModels []string
		for _, key := range keys {
			v, err := custom.New(custom.Config{
				ID: in.ID, Name: in.Name, BaseURL: in.BaseURL,
				APIKey: key, Protocol: in.Protocol, ViaProxy: in.ViaProxy,
				// 连通测试必须真实触达上游：禁用目录缓存（防不可达时拿旧缓存误报成功）。
				NoModelCache: true,
			})
			if err != nil {
				allOK = false
				results = append(results, keyResult{KeyTail: keyTail(key), Error: err.Error()})
				continue
			}
			ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
			start := time.Now()
			models, err := v.ListModels(ctx)
			latency := time.Since(start).Milliseconds()
			cancel()
			if err != nil {
				allOK = false
				results = append(results, keyResult{KeyTail: keyTail(key), LatencyMS: latency, Error: err.Error()})
				continue
			}
			ids := make([]string, 0, len(models))
			for _, m := range models {
				ids = append(ids, strings.TrimPrefix(m.ID, in.ID+"/"))
			}
			if firstOKModels == nil {
				firstOKModels = ids
			}
			results = append(results, keyResult{KeyTail: keyTail(key), OK: true, Count: len(ids), LatencyMS: latency})
		}
		writeAdminJSON(w, map[string]any{
			"ok": allOK, "results": results, "count": len(results), "models": firstOKModels,
		})
	}
}

// keyTail key 末 4 位（结果展示用，不整段回显）。
func keyTail(key string) string {
	if len(key) <= 4 {
		return strings.Repeat("*", len(key))
	}
	return "****" + key[len(key)-4:]
}

// ---------------------------------------------------------------------------
// 输出小工具（与 manager.writeJSON 同语义，main 侧独立实现避免依赖导出）
// ---------------------------------------------------------------------------

func writeAdminJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeAdminErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": msg})
}

// customKeyListFromParams 合并条目 params 里的 api_keys + api_key（去空去重）。
func customKeyListFromParams(p map[string]any) []string {
	var out []string
	seen := map[string]bool{}
	add := func(k string) {
		k = strings.TrimSpace(k)
		if k == "" || seen[k] {
			return
		}
		seen[k] = true
		out = append(out, k)
	}
	if arr, ok := p[custom.ParamAPIKeys].([]any); ok {
		for _, v := range arr {
			if s, ok := v.(string); ok {
				add(s)
			}
		}
	}
	if s, ok := p[custom.ParamAPIKey].(string); ok {
		add(s)
	}
	return out
}

// customAllowedListFromParams 提取条目 params 里的模型白名单（allowed_models，去空去重）。
// 对齐 customKeyListFromParams：config.json 里白名单存为 []any，需逐项断言 string。
func customAllowedListFromParams(p map[string]any) []string {
	var out []string
	seen := map[string]bool{}
	if arr, ok := p[custom.ParamAllowedModels].([]any); ok {
		for _, v := range arr {
			if s, ok := v.(string); ok {
				s = strings.TrimSpace(s)
				if s == "" || seen[s] {
					continue
				}
				seen[s] = true
				out = append(out, s)
			}
		}
	}
	return out
}

// inputKeyList 取输入里的 key 列表（api_keys 优先，兼容单 api_key；去空去重）。
func inputKeyList(in customProviderInput) []string {
	var out []string
	seen := map[string]bool{}
	add := func(k string) {
		k = strings.TrimSpace(k)
		if k == "" || seen[k] {
			return
		}
		seen[k] = true
		out = append(out, k)
	}
	for _, k := range in.APIKeys {
		add(k)
	}
	add(in.APIKey)
	return out
}

// vendorKeyHealth 活实例的 key 健康计数（无实例返回零值）。
func vendorKeyHealth(id string) custom.KeyPoolStatus {
	if globalAgg == nil {
		return custom.KeyPoolStatus{}
	}
	for _, v := range globalAgg.Vendors() {
		if cv, ok := v.(*custom.Vendor); ok && cv.ID() == id {
			return cv.PoolStatus()
		}
	}
	return custom.KeyPoolStatus{}
}

// vendorFullModels 活实例的全量模型清单（上游 ID；无实例返回空）。
func vendorFullModels(id string) []string {
	if globalAgg == nil {
		return nil
	}
	for _, v := range globalAgg.Vendors() {
		if cv, ok := v.(*custom.Vendor); ok && cv.ID() == id {
			return cv.FullModelIDs()
		}
	}
	return nil
}

// vendorLastSuccess 活实例最近一次成功时间（探测或真实请求）。
func vendorLastSuccess(id string) string {
	if globalAgg == nil {
		return ""
	}
	for _, v := range globalAgg.Vendors() {
		if v.ID() == id {
			return v.Health().LastSuccess
		}
	}
	return ""
}

// customProvidersProbeHandler POST：手动活性探测 {"id":"src1"}——真实拉一次上游目录，
// 刷新健康状态并返回结果（不落盘、不走缓存）。
func customProvidersProbeHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			ID string `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
			writeAdminErr(w, http.StatusBadRequest, "请求体需为 {\"id\":\"源ID\"}")
			return
		}
		if globalAgg == nil {
			writeAdminErr(w, http.StatusServiceUnavailable, "聚合器未装配")
			return
		}
		var cv *custom.Vendor
		for _, v := range globalAgg.Vendors() {
			if c, ok := v.(*custom.Vendor); ok && c.ID() == req.ID {
				cv = c
				break
			}
		}
		if cv == nil {
			writeAdminErr(w, http.StatusNotFound, "源 "+req.ID+" 未启用或未装配（停用状态请先启用）")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		ok, latency, errStr := cv.Probe(ctx)
		writeAdminJSON(w, map[string]any{
			"id": req.ID, "ok": ok, "latency_ms": latency,
			"error": errStr, "last_success": cv.Health().LastSuccess,
		})
	}
}

// startCustomProbeLoop 后台活性探测：每 5 分钟对所有已装配自定义源真实拉一次目录，
// 刷新健康（LastSuccess/LastError 供列表页活性徽标；无流量时也保持新鲜）。
func startCustomProbeLoop() {
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			if globalAgg == nil {
				continue
			}
			for _, v := range globalAgg.Vendors() {
				cv, isCustom := v.(*custom.Vendor)
				if !isCustom {
					continue
				}
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				cv.Probe(ctx)
				cancel()
			}
		}
	}()
}
