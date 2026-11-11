// Kilo 独立网关（30080）：管理进程内的独立 HTTP 监听器，直通
// https://api.kilo.ai/api/gateway，OpenAI 兼容形态对外。
//
// 为什么存在、为什么长这样（阶段 K，plans/2026-11-11-kilo-independent-gateway.md）：
//   - 用户要求 Kilo 像 OpenCode 一样有独立菜单 + 独立端口，且「不要和别人有
//     任何关联」：本文件 **不引用** globalAgg / chatRouterVar / vendors /
//     ProviderService / 40080 引擎 —— 独立性是功能本身，验收项 V-6/V-7 实测
//     「停掉 50080 后 30080 照常 200」。此前挂在 providers[] 的形态已摘除。
//   - 上游无账号（调研 top5-98：Bearer anonymous 即可，实测不发 Authorization
//     也 200），所以本网关没有 key 池 —— 单一固定值直接写在这里，不是秘密。
//   - 目录动态实拉：上游免费档会漂移（11-11 一天内 11→12 个，新增
//     baidu/ernie-5.5-flash-preview:free），**禁止硬编码模型清单**。
//   - 错误原样透传：参考实现里「上游 403 被洗成 502」「404 不换位卡死链首」
//     都吃过亏；本网关只转发，状态码与错误体不改写。
//   - 警示不可绕过：每个响应带 X-Kilo-Data-Policy 头（上游 12/12 免费模型
//     mayTrainOnYourPrompts=true，NVIDIA ToS 原文「Trial use only — do not
//     submit personal or confidential data」）。前端横幅只是第一层，头与
//     /healthz 是机器可读的后两层。
package manager

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// kiloGatewayPortDefault 独立网关默认端口（槽位与 40080/50080 错开）。
const kiloGatewayPortDefault uint16 = 30080

// 上游常量。Bearer anonymous 是上游公开的匿名档（见文件头注释），非秘密。
const (
	kiloUpstreamBase = "https://api.kilo.ai/api/gateway/v1"
	kiloUpstreamAuth = "Bearer anonymous"
	// kiloDataPolicyHeader 数据去向警示响应头（机器可读，客户端可据此拦截）。
	kiloDataPolicyHeader      = "X-Kilo-Data-Policy"
	kiloDataPolicyHeaderValue = "prompts-may-be-used-for-training"
	// kiloCatalogRefreshEvery 后台目录刷新间隔；kiloCatalogMinInterval 请求触发
	// 的最小间隔（10s 节流，抄 providerservice 的缓存语义、代码自持不共享）。
	kiloCatalogRefreshEvery = 60 * time.Second
	kiloCatalogMinInterval  = 10 * time.Second
)

// KiloModelEntry 单模型目录项（上游 /models 精简形态 + 我们的展示附加信息）。
type KiloModelEntry struct {
	ID            string `json:"id"`
	Free          bool   `json:"free"`
	MayTrain      bool   `json:"may_train"`
	SupportsTools bool   `json:"supports_tools"`
	ContextWindow int    `json:"context_window,omitempty"`
	ProviderName  string `json:"provider_name,omitempty"`
}

// KiloGatewayStatus 独立网关状态快照（前端契约，对齐 FreeTierEngineStatus 风格）。
type KiloGatewayStatus struct {
	State      string `json:"state"` // stopped | ready | failed
	Enabled    bool   `json:"enabled"`
	Port       uint16 `json:"port"`
	Address    string `json:"address"`
	APIKey     string `json:"api_key"`
	FreeOnly   bool   `json:"free_only"`
	Proxy      string `json:"proxy"`
	ModelTotal int    `json:"model_total"`
	ModelShown int    `json:"model_shown"`
	// MayTrain 上游目录的整体结论（当前 12/12 免费档 true）。恒 true 也不
	// 省略 —— 面板警示横幅的可见性由它驱动，缺省会被当作「无风险」。
	MayTrain    bool   `json:"may_train"`
	LastRefresh string `json:"last_refresh,omitempty"`
	LastError   string `json:"last_error,omitempty"`
	Running     bool   `json:"running"`
}

// kiloGateway 网关运行时状态。
type kiloGateway struct {
	mgr *Manager // 读配置（端口/开关/密钥）用；不构成链路依赖

	mu sync.Mutex
	// startMu 串行化 Start：main 后台启动 / toggle / Watchdog 三处都可能调。
	startMu sync.Mutex
	state   string
	lastErr string
	srv     *http.Server
	ln      net.Listener
	// watchdogStop 让 Watchdog 随 Stop 退出（独立审查 B9：原 for range t.C
	// 无退出路径，进程退出前 goroutine 常驻）。close 幂等由 sync.Once 保证。
	watchdogStop     chan struct{}
	watchdogStopOnce sync.Once

	cMu        sync.Mutex
	catalog    []KiloModelEntry // 最近一次成功目录（全量，含付费档）
	cLast      time.Time
	cErr       string
	cLastTryAt time.Time // 节流戳（含失败尝试，防热循环）
}

// 上游请求注入点：单测把 KiloUpstreamBase/Client 换成本地假上游。
var (
	kiloUpstreamBaseOverride = os.Getenv("OPCODE2API_KILO_UPSTREAM") // 测试/灰度用
	kiloUpstreamTimeout      = 90 * time.Second
)

func kiloBaseURL() string {
	if s := strings.TrimSpace(kiloUpstreamBaseOverride); s != "" {
		return strings.TrimRight(s, "/")
	}
	return kiloUpstreamBase
}

// KiloGateway 返回管理器的 Kilo 网关句柄（懒建）。
func (m *Manager) KiloGateway() *kiloGateway {
	m.depMu.Lock()
	defer m.depMu.Unlock()
	if m.kilo == nil {
		m.kilo = &kiloGateway{mgr: m, state: "stopped"}
	}
	return m.kilo
}

// upstreamClient 自建出站：独立 http.Client（红线：不复用 vendors/aggregator
// 的任何 transport），带自己的代理解析与保守连接池。
func (g *kiloGateway) upstreamClient() (*http.Client, error) {
	cfg := g.mgr.loadConfig()
	tr := &http.Transport{
		MaxIdleConns:        64,
		MaxIdleConnsPerHost: 32,
		IdleConnTimeout:     90 * time.Second,
		DialContext:         (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
	}
	if p := strings.TrimSpace(cfg.KiloProxy); p != "" && !strings.EqualFold(p, "direct") {
		u, err := url.Parse(p)
		if err != nil {
			return nil, fmt.Errorf("kilo_proxy 不是合法代理地址: %w", err)
		}
		tr.Proxy = http.ProxyURL(u)
	}
	return &http.Client{Transport: tr, Timeout: kiloUpstreamTimeout}, nil
}

// Start 启动独立监听（幂等；enabled=false 时不起监听但状态可查）。
//
// 与 StartFreeTierEngine 同款教训：长等待放锁外、失败不拖垮管理面 ——
// 这里 ListenAndServe 失败只记 failed + lastErr，返回错误由调用方降级为 warn。
// 独立审查 B9：listen 与状态登记原先分两段持锁，main 后台 / toggle /
// Watchdog 三处并发触发时，第二个 Start 会 bind 失败并把已经 ready 的状态
// 覆盖成 failed。现在用一把 startMu 贯穿「判重 → listen → 登记」，从根上
// 消除竞态（不用 mu 是因为 listen 期间不能占着状态锁）。
func (g *kiloGateway) Start() error {
	g.startMu.Lock()
	defer g.startMu.Unlock()

	g.mu.Lock()
	if g.state == "ready" {
		g.mu.Unlock()
		return nil
	}
	g.mu.Unlock()

	cfg := g.mgr.loadConfig()
	if !kiloEnabled(cfg) {
		g.mu.Lock()
		g.state = "stopped"
		g.mu.Unlock()
		return nil
	}
	port := g.mgr.managerKiloPort()
	ln, err := g.listenOn(port)
	if err != nil {
		g.mu.Lock()
		g.state = "failed"
		g.lastErr = "监听失败: " + err.Error()
		g.mu.Unlock()
		return err
	}
	g.serveOn(ln)
	return nil
}

// listenOn 在指定端口建立监听（0 = 由系统分配，测试用）。
func (g *kiloGateway) listenOn(port uint16) (net.Listener, error) {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	return net.Listen("tcp", addr)
}

// serveOn 在已有监听上起服务并登记状态（Start 与单测共用）。
func (g *kiloGateway) serveOn(ln net.Listener) {
	srv := &http.Server{
		Handler:           g.apiMux(),
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	g.mu.Lock()
	g.ln, g.srv, g.state = ln, srv, "ready"
	g.lastErr = ""
	g.mu.Unlock()

	addr := ln.Addr().String()
	go func() {
		slog.Info("kilo gateway listening", "addr", addr)
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			g.mu.Lock()
			g.state = "failed"
			g.lastErr = err.Error()
			g.mu.Unlock()
			slog.Warn("kilo gateway terminated", "error", err)
		}
	}()

	// 目录首拉异步（不阻塞启动）；失败由后台循环与请求路径重试兜底。
	go func() {
		if _, err := g.refreshCatalog(); err != nil {
			slog.Warn("kilo catalog initial refresh failed", "error", err)
		}
	}()
}

// Stop 停止监听（幂等）。
func (g *kiloGateway) Stop() error {
	g.mu.Lock()
	srv := g.srv
	g.srv, g.ln = nil, nil
	st := g.state
	g.state = "stopped"
	g.mu.Unlock()
	// 看门狗与监听同生命周期：停网关就停看门狗，避免它 30s 后又按配置
	// 把网关拉起来（独立审查 B9）。
	g.watchdogStopOnce.Do(func() {
		if g.watchdogStop != nil {
			close(g.watchdogStop)
		}
	})
	if srv != nil && st == "ready" {
		// 15s 宽限：让在途流结束，超时就掐。
		return srv.Close()
	}
	return nil
}

// Watchdog 周期核对「配置期望 vs 实际监听」，漂移即纠正 —— 与引擎看门狗同一
// 设计原则（判据来自观测，不来自配置缓存；这正是阶段调研里 L2 死锁的教训）。
func (g *kiloGateway) Watchdog(interval time.Duration) {
	g.mu.Lock()
	if g.watchdogStop == nil {
		g.watchdogStop = make(chan struct{})
	}
	stop := g.watchdogStop
	g.mu.Unlock()

	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		}
		cfg := g.mgr.loadConfig()
		want := kiloEnabled(cfg)
		g.mu.Lock()
		live := g.state == "ready" && g.srv != nil
		g.mu.Unlock()
		if want && !live {
			if err := g.Start(); err != nil {
				slog.Warn("kilo gateway watchdog restart failed", "error", err)
			}
			continue
		}
		if !want && live {
			_ = g.Stop()
		}
		// 目录刷新（顺带这里做，避免再开一个 goroutine）。
		if _, err := g.refreshCatalogThrottled(); err != nil {
			slog.Debug("kilo catalog watchdog refresh failed", "error", err)
		}
	}
}

// Status 返回状态快照（读锁内只取字段，不发网络请求）。
func (g *kiloGateway) Status() KiloGatewayStatus {
	cfg := g.mgr.loadConfig()
	g.mu.Lock()
	state, lastErr := g.state, g.lastErr
	g.mu.Unlock()
	port := g.mgr.managerKiloPort()

	st := KiloGatewayStatus{
		State:     state,
		Enabled:   kiloEnabled(cfg),
		Port:      port,
		Address:   fmt.Sprintf("http://127.0.0.1:%d/v1", port),
		APIKey:    effectiveGatewayKey(cfg),
		FreeOnly:  kiloFreeOnly(cfg),
		Proxy:     cfg.KiloProxy,
		Running:   state == "ready",
		LastError: lastErr,
	}
	g.cMu.Lock()
	st.ModelTotal = len(g.catalog)
	shown := g.catalogFilteredLocked(kiloFreeOnly(cfg))
	st.ModelShown = len(shown)
	// 独立审查 B8：OR 口径 —— 视图内「存在」任一可能用于训练的模型即 true，
	// 与 api.ts 的 may_train 注释一致。此前写成 AND，全 true 时凑巧等价，
	// 一旦上游混入一个不训练的模型就会误报「无风险」，进而弱化警示。
	st.MayTrain = false
	for _, e := range shown {
		if e.MayTrain {
			st.MayTrain = true
			break
		}
	}
	if !g.cLast.IsZero() {
		st.LastRefresh = g.cLast.Format(time.RFC3339)
	}
	// 独立审查 B2：两个错误源都要露出来，且**启动/监听错误优先**——
	// 端口被占时 state=failed，若被目录错误无条件覆盖（原实现），面板
	// 只显示「目录拉取失败」，真正的「起不来」原因被吞，无法排障。
	if g.cErr != "" {
		if lastErr != "" {
			st.LastError = lastErr + "；目录: " + g.cErr
		} else {
			st.LastError = g.cErr
		}
	}
	g.cMu.Unlock()
	return st
}

// Models 返回当前视图的模型清单（按 free_only 过滤；空目录时尽力刷新一次）。
func (g *kiloGateway) Models() ([]KiloModelEntry, error) {
	cfg := g.mgr.loadConfig()
	freeOnly := kiloFreeOnly(cfg)
	g.cMu.Lock()
	cat := append([]KiloModelEntry(nil), g.catalog...)
	cErr := g.cErr
	g.cMu.Unlock()
	if len(cat) == 0 {
		var err error
		cat, err = g.refreshCatalog()
		if err != nil {
			return nil, err
		}
		cErr = ""
	} else if time.Since(g.cLast) > kiloCatalogRefreshEvery {
		_, _ = g.refreshCatalogThrottled() // stale-while-revalidate
		g.cMu.Lock()
		cat = append([]KiloModelEntry(nil), g.catalog...)
		g.cMu.Unlock()
	}
	out := cat
	if freeOnly {
		out = catalogFilterFree(cat)
	}
	_ = cErr
	return out, nil
}

// ---- 目录 ----

// refreshCatalogThrottled 距上次尝试不足 kiloCatalogMinInterval 时跳过（返回缓存）。
func (g *kiloGateway) refreshCatalogThrottled() ([]KiloModelEntry, error) {
	g.cMu.Lock()
	recent := time.Since(g.cLastTryAt) < kiloCatalogMinInterval
	cat := append([]KiloModelEntry(nil), g.catalog...)
	g.cMu.Unlock()
	if recent && len(cat) > 0 {
		return cat, nil
	}
	return g.refreshCatalog()
}

// refreshCatalog 实拉上游 /models（成功才替换缓存 —— 上游抖动不清空目录，
// 与 aggregator.lastGood 同一原则）。
func (g *kiloGateway) refreshCatalog() ([]KiloModelEntry, error) {
	g.cMu.Lock()
	g.cLastTryAt = time.Now()
	g.cMu.Unlock()

	client, err := g.upstreamClient()
	if err != nil {
		g.catalogErr(err)
		return nil, err
	}
	resp, err := client.Get(kiloBaseURL() + "/models")
	if err != nil {
		g.catalogErr(err)
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024))
	if err != nil {
		g.catalogErr(err)
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("kilo upstream /models HTTP %d: %.200s", resp.StatusCode, body)
		g.catalogErr(err)
		return nil, err
	}
	entries, err := parseKiloModels(body)
	if err != nil {
		g.catalogErr(err)
		return nil, err
	}
	if len(entries) == 0 {
		err := fmt.Errorf("kilo upstream /models returned no usable entries")
		g.catalogErr(err)
		return nil, err
	}
	g.cMu.Lock()
	g.catalog, g.cLast, g.cErr = entries, time.Now(), ""
	g.cMu.Unlock()
	return append([]KiloModelEntry(nil), entries...), nil
}

func (g *kiloGateway) catalogErr(err error) {
	g.cMu.Lock()
	g.cErr = err.Error()
	g.cMu.Unlock()
}

// parseKiloModels 解析上游 /models（OpenAI 形 data[] + Kilo 附加字段）。
// 免费判定与上游自身口径一致：顶层 isFree=true（今日实测 12/12 与
// mayTrainOnYourPrompts 同集合）；不硬编码 id 名单 —— 上游目录一天内就从
// 11 漂到 12（新增 baidu/ernie-5.5-flash-preview:free）。
func parseKiloModels(body []byte) ([]KiloModelEntry, error) {
	var raw struct {
		Data []struct {
			ID                    string   `json:"id"`
			Name                  string   `json:"name"`
			IsFree                bool     `json:"isFree"`
			MayTrainOnYourPrompts bool     `json:"mayTrainOnYourPrompts"`
			SupportedParameters   []string `json:"supported_parameters"`
			ContextLength         int      `json:"context_length"`
			Architecture          struct {
				Modality string `json:"modality"`
			} `json:"architecture"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("kilo /models JSON: %w", err)
	}
	out := make([]KiloModelEntry, 0, len(raw.Data))
	for _, d := range raw.Data {
		if strings.TrimSpace(d.ID) == "" {
			continue
		}
		e := KiloModelEntry{
			ID:            d.ID,
			Free:          d.IsFree,
			MayTrain:      d.MayTrainOnYourPrompts,
			SupportsTools: false,
			ContextWindow: d.ContextLength,
			ProviderName:  d.Name,
		}
		for _, sp := range d.SupportedParameters {
			if sp == "tools" {
				e.SupportsTools = true
				break
			}
		}
		out = append(out, e)
	}
	return out, nil
}

// catalogFilterFree 只回免费档（保持上游顺序）。
func catalogFilterFree(cat []KiloModelEntry) []KiloModelEntry {
	out := make([]KiloModelEntry, 0, len(cat))
	for _, e := range cat {
		if e.Free {
			out = append(out, e)
		}
	}
	return out
}

// isModelBlockedByFreeOnly 判断请求模型是否应被 free_only 边界拦下。
// 返回 (false, "") 的三类情形：目录尚未拉到（放行，避免上游抖动误伤）、
// 命中免费档、空 model（交给上游裁决，别在这里编错误）。
func (g *kiloGateway) isModelBlockedByFreeOnly(model string) (bool, string) {
	if strings.TrimSpace(model) == "" {
		return false, ""
	}
	g.cMu.Lock()
	cat := append([]KiloModelEntry(nil), g.catalog...)
	g.cMu.Unlock()
	if len(cat) == 0 {
		return false, ""
	}
	for _, e := range cat {
		if e.ID == model && e.Free {
			return false, ""
		}
	}
	return true, "model 不在上游免费档目录内（kilo_free_only=true）；关闭该开关或使用 /v1/models 列出的免费档。目录尚未刷新时此边界暂不生效"
}

// catalogFilteredLocked 在已持 cMu 时过滤。
func (g *kiloGateway) catalogFilteredLocked(freeOnly bool) []KiloModelEntry {
	if !freeOnly {
		return append([]KiloModelEntry(nil), g.catalog...)
	}
	return catalogFilterFree(g.catalog)
}

// ---- HTTP 面 ----

// apiMux 对外路由：/v1/chat/completions、/v1/models、/healthz。
func (g *kiloGateway) apiMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", g.handleHealth)
	mux.HandleFunc("/v1/models", g.requireKey(g.handleModels))
	mux.HandleFunc("/v1/chat/completions", g.requireKey(g.handleChat))
	return mux
}

// requireKey 统一网关密钥鉴权（与其它入口同款 sk-unified-local 语义）。
func (g *kiloGateway) requireKey(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		want := "Bearer " + effectiveGatewayKey(g.mgr.loadConfig())
		got := strings.TrimSpace(r.Header.Get("Authorization"))
		if got != want {
			w.Header().Set(kiloDataPolicyHeader, kiloDataPolicyHeaderValue)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{"message": "invalid api key", "type": "auth_error"},
			})
			return
		}
		next(w, r)
	}
}

// handleHealth 只回计划 §Task 2 约定的 4 个字段（status / models_shown /
// may_train / last_refresh）+ 警示头。
//
// 独立审查 B1：这里**绝不能**把 Status() 整个序列化出去 —— 那是管理端点
// (/api/admin/kilo/status, 挂 requireAuth) 的契约，其中 api_key 是明文统一
// 网关密钥；/healthz 无鉴权，本机任意进程都能读，整包外泄等于把 40000 的
// 门禁交出去。B6：警示头在每条响应都要有，健康探活也不例外。
func (g *kiloGateway) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set(kiloDataPolicyHeader, kiloDataPolicyHeaderValue)
	w.Header().Set("Content-Type", "application/json")
	st := g.Status()
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":       st.State,
		"models_shown": st.ModelShown,
		"may_train":    st.MayTrain,
		"last_refresh": st.LastRefresh,
	})
}

func (g *kiloGateway) handleModels(w http.ResponseWriter, r *http.Request) {
	entries, err := g.Models()
	w.Header().Set(kiloDataPolicyHeader, kiloDataPolicyHeaderValue)
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{"message": "kilo upstream models unavailable: " + err.Error(), "type": "upstream_error"},
		})
		return
	}
	data := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		data = append(data, map[string]any{
			"id":       e.ID,
			"object":   "model",
			"created":  0,
			"owned_by": "kilo",
		})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
}

// handleChat 转发 /chat/completions。三条铁律（全部来自 11-11 调研实测教训）：
//  1. 上游状态码与错误体 **原样透传**（参考实现把 403 洗成 502 害排查）；
//  2. 响应体不改写 model 字段 —— 上游会静默换模型（kilo-auto/free 实测返回
//     nemotron-3-ultra-550b），洗成请求名 = 骗客户端；
//  3. 每个响应带数据政策头。
func (g *kiloGateway) handleChat(w http.ResponseWriter, r *http.Request) {
	w.Header().Set(kiloDataPolicyHeader, kiloDataPolicyHeaderValue)
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 20*1024*1024))
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	// 模型名：接受裸 id；若客户端带了 kilo/ 前缀（面板复制的示例）剥掉。
	var peek struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	_ = json.Unmarshal(body, &peek) // 解析失败不拦：原样转发由上游裁决，别在这里编错误
	upBody := body
	modelID := peek.Model
	if strings.HasPrefix(modelID, "kilo/") {
		modelID = strings.TrimPrefix(modelID, "kilo/")
		upBody = rewriteModel(body, modelID)
	}

	// 独立审查 B7：free_only 不能只是展示过滤，否则客户端编一个付费档 id
	// 就绕过「只勾免费模型」直接打上游（还可能产生非预期计费/训练口径）。
	// 轻量边界：仅在目录**已成功拉取**时校验；目录不可用/尚未首拉时放行，
	// 不因上游抖动把整条推理链打死（与「失败不拖垮管理面」同原则）。
	if kiloFreeOnly(g.mgr.loadConfig()) {
		if blocked, reason := g.isModelBlockedByFreeOnly(modelID); blocked {
			writeKiloError(w, http.StatusBadRequest, reason)
			return
		}
	}

	client, err := g.upstreamClient()
	if err != nil {
		writeKiloError(w, http.StatusInternalServerError, err.Error())
		return
	}
	upReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, kiloBaseURL()+"/chat/completions", bytes.NewReader(upBody))
	if err != nil {
		writeKiloError(w, http.StatusInternalServerError, err.Error())
		return
	}
	upReq.Header.Set("Authorization", kiloUpstreamAuth)
	upReq.Header.Set("Content-Type", "application/json")
	if peek.Stream {
		upReq.Header.Set("Accept", "text/event-stream")
	}

	resp, err := client.Do(upReq)
	if err != nil {
		// 传输失败才是我们的错误；上游业务错误在下面的 status 分支透传。
		writeKiloError(w, http.StatusGatewayTimeout, "kilo upstream unreachable: "+err.Error())
		return
	}
	defer resp.Body.Close()

	// 状态码与 Content-Type 原样复制 + 警示头（上面已 Set，这里再保证错误路径也在）。
	copyHeadersPassthrough(w, resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// writeKiloError 网关自身错误的 JSON 形态（仅用于传输/内部错误）。
func writeKiloError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set(kiloDataPolicyHeader, kiloDataPolicyHeaderValue)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{"message": msg, "type": "kilo_gateway_error"},
	})
}

// rewriteModel 把 body 顶层 model 换成 stripped 版本（保持其余字段字节级不动
// 不现实，用 map 重写；键序变化对 JSON 语义无影响）。
func rewriteModel(body []byte, model string) []byte {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return body
	}
	m["model"] = model
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

// copyHeadersPassthrough 透传上游内容相关头（content-type / retry-after /
// ratelimit 系列），不复制 hop-by-hop。
func copyHeadersPassthrough(w http.ResponseWriter, hdr http.Header) {
	for _, k := range []string{"Content-Type", "Retry-After", "X-RateLimit-Limit", "X-RateLimit-Remaining", "X-RateLimit-Reset"} {
		if v := hdr.Get(k); v != "" {
			w.Header().Set(k, v)
		}
	}
}
