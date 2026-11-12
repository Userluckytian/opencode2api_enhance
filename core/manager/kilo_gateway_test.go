package manager

// Kilo 独立网关（30080）契约单测。
//
// 覆盖计划 Task 4 的 K1–K8：
//   K1 免费过滤 / K2 静默换模型透传 / K3 鉴权 / K4 上游错误透传
//   K5 警示头 / K6 前缀剥离 / K7 端口与开关 / K8 独立性红线
//
// 手法：全部打**本地假上游**（httptest），绝不在单测里打真 Kilo ——
// 真上游会漂移（免费档一天内 11→12）、会限流、会让 CI 不稳定。
// 真上游只在本阶段人工验收（V-4）里打一次。

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// kiloFakeUpstream 假上游：/models 返回给定目录；/chat/completions 按 chatHandler 行为。
type kiloFakeUpstream struct {
	srv         *httptest.Server
	chatHandler func(w http.ResponseWriter, r *http.Request, body []byte)
}

func newKiloFakeUpstream(t *testing.T, modelsJSON string, chat func(w http.ResponseWriter, r *http.Request, body []byte)) *kiloFakeUpstream {
	t.Helper()
	f := &kiloFakeUpstream{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/models"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, modelsJSON)
		case strings.HasSuffix(r.URL.Path, "/chat/completions"):
			body, _ := io.ReadAll(r.Body)
			if chat != nil {
				chat(w, r, body)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"x","object":"chat.completion","model":"fake/model:free","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// kiloTestManager 造一个指向临时目录的 Manager，并把上游指到假服务器。
func kiloTestManager(t *testing.T, upstream string) *Manager {
	t.Helper()
	m := New(t.TempDir())
	old := kiloUpstreamBaseOverride
	kiloUpstreamBaseOverride = strings.TrimRight(upstream, "/") + "/v1"
	t.Cleanup(func() { kiloUpstreamBaseOverride = old })
	return m
}

// kiloTestGW 起一个网关（端口 0 → 由系统分配，回填实际端口）。
func kiloTestGW(t *testing.T, m *Manager) (*kiloGateway, string) {
	t.Helper()
	g := m.KiloGateway()
	ln, err := g.listenOn(0)
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	g.serveOn(ln)
	t.Cleanup(func() { _ = g.Stop() })
	return g, "http://" + ln.Addr().String()
}

// ---- K1 免费过滤 ----

func TestKiloK1FreeFilter(t *testing.T) {
	models := `{"data":[
		{"id":"a/free","isFree":true,"mayTrainOnYourPrompts":true,"supported_parameters":["tools"]},
		{"id":"b/paid","isFree":false,"mayTrainOnYourPrompts":true},
		{"id":"c/free","isFree":true,"mayTrainOnYourPrompts":false}
	]}`
	up := newKiloFakeUpstream(t, models, nil)
	m := kiloTestManager(t, up.srv.URL)

	// free_only 默认开（nil → true）
	got, err := m.KiloGateway().Models()
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("free_only 默认开时应有 2 个免费模型，实际 %d: %+v", len(got), got)
	}
	for _, e := range got {
		if !e.Free {
			t.Fatalf("非免费模型泄漏: %+v", e)
		}
	}
	// 付费模型在关闭 free_only 后出现
	if err := m.ConfigSet("kilo_free_only", "false"); err != nil {
		t.Fatalf("ConfigSet: %v", err)
	}
	got2, err := m.KiloGateway().Models()
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(got2) != 3 {
		t.Fatalf("free_only=false 时应有 3 个模型，实际 %d", len(got2))
	}
}

// ---- K2 静默换模型透传（不许洗成请求名） ----

func TestKiloK2SilentModelSubstitutionPassedThrough(t *testing.T) {
	// 上游把请求的模型换成另一个（实测 kilo-auto/free → nemotron-3-ultra-550b）
	up := newKiloFakeUpstream(t, `{"data":[{"id":"kilo-auto/free","isFree":true,"mayTrainOnYourPrompts":true}]}`,
		func(w http.ResponseWriter, r *http.Request, body []byte) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"model":"nvidia/nemotron-3-ultra-550b-a55b:free","choices":[{"message":{"content":"hi"}}]}`)
		})
	m := kiloTestManager(t, up.srv.URL)
	g, base := kiloTestGW(t, m)

	resp, body := kiloPost(t, base, `{"model":"kilo-auto/free","messages":[{"role":"user","content":"x"}]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "nvidia/nemotron-3-ultra-550b-a55b:free") {
		t.Fatalf("上游真实模型名必须原样透传，实际响应: %s", body)
	}
	if strings.Contains(body, `"model":"kilo-auto/free"`) {
		t.Fatalf("响应 model 被洗成请求名了（等于骗客户端）: %s", body)
	}
	_ = g
}

// ---- K3 鉴权 ----

func TestKiloK3Auth(t *testing.T) {
	up := newKiloFakeUpstream(t, `{"data":[{"id":"a:free","isFree":true}]}`, nil)
	m := kiloTestManager(t, up.srv.URL)
	_, base := kiloTestGW(t, m)

	for _, bad := range []string{"", "Bearer wrong", "sk-unified-local"} {
		req, _ := http.NewRequest(http.MethodGet, base+"/v1/models", nil)
		if bad != "" {
			req.Header.Set("Authorization", bad)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("请求失败: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("Authorization=%q 时 status = %d, want 401", bad, resp.StatusCode)
		}
	}

	req, _ := http.NewRequest(http.MethodGet, base+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+unifiedGatewayKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("正确 key 时 status = %d, want 200", resp.StatusCode)
	}
}

// ---- K4 上游错误原样透传（403 不得变 502） ----

func TestKiloK4UpstreamErrorPassthrough(t *testing.T) {
	up := newKiloFakeUpstream(t, `{"data":[]}`,
		func(w http.ResponseWriter, r *http.Request, body []byte) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"error":{"message":"not available in your country","type":"forbidden"}}`)
		})
	m := kiloTestManager(t, up.srv.URL)
	_, base := kiloTestGW(t, m)

	resp, body := kiloPost(t, base, `{"model":"x:free","messages":[]}`)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("上游 403 必须原样透传，实际 status = %d: %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "not available in your country") {
		t.Fatalf("上游错误体必须原样透传: %s", body)
	}
}

// ---- K5 警示头 ----

func TestKiloK5DataPolicyHeader(t *testing.T) {
	up := newKiloFakeUpstream(t, `{"data":[{"id":"a:free","isFree":true,"mayTrainOnYourPrompts":true}]}`, nil)
	m := kiloTestManager(t, up.srv.URL)
	_, base := kiloTestGW(t, m)

	resp, _ := kiloPost(t, base, `{"model":"a:free","messages":[]}`)
	if got := resp.Header.Get(kiloDataPolicyHeader); got != kiloDataPolicyHeaderValue {
		t.Fatalf("%s = %q, want %q", kiloDataPolicyHeader, got, kiloDataPolicyHeaderValue)
	}
	// 401 路径也要带警示头（警示不可被错误路径绕开）
	req, _ := http.NewRequest(http.MethodGet, base+"/v1/models", nil)
	r2, _ := http.DefaultClient.Do(req)
	r2.Body.Close()
	if got := r2.Header.Get(kiloDataPolicyHeader); got != kiloDataPolicyHeaderValue {
		t.Fatalf("401 响应缺少 %s（警示不可绕过）", kiloDataPolicyHeader)
	}

	// 独立审查 B6：/healthz 也必须带警示头（「每个响应」不留例外）。
	r3, err := http.DefaultClient.Get(base + "/healthz")
	if err != nil {
		t.Fatalf("healthz 请求失败: %v", err)
	}
	defer r3.Body.Close()
	if got := r3.Header.Get(kiloDataPolicyHeader); got != kiloDataPolicyHeaderValue {
		t.Fatalf("/healthz 缺少 %s", kiloDataPolicyHeader)
	}
	// 独立审查 B1：/healthz 无鉴权，绝不能回传明文统一网关密钥。
	raw, _ := io.ReadAll(r3.Body)
	var health map[string]any
	if err := json.Unmarshal(raw, &health); err != nil {
		t.Fatalf("healthz 非 JSON: %s", raw)
	}
	for _, k := range []string{"api_key", "address", "proxy", "enabled", "port", "free_only"} {
		if _, ok := health[k]; ok {
			t.Fatalf("/healthz 泄漏了管理面字段 %q（B1）: %s", k, raw)
		}
	}
	if strings.Contains(string(raw), unifiedGatewayKey) {
		t.Fatalf("/healthz 响应里出现明文密钥（B1）: %s", raw)
	}
	for _, k := range []string{"status", "models_shown", "may_train", "last_refresh"} {
		if _, ok := health[k]; !ok {
			t.Fatalf("/healthz 缺计划约定字段 %q: %s", k, raw)
		}
	}
}

// TestKiloB7FreeOnlyChatBoundary：free_only 不只过滤 /v1/models，也必须
// 约束 chat —— 目录已拉取时，编造的付费档 id 不得直达上游（独立审查 B7）。
func TestKiloB7FreeOnlyChatBoundary(t *testing.T) {
	up := newKiloFakeUpstream(t, `{"data":[
		{"id":"a:free","isFree":true,"mayTrainOnYourPrompts":true},
		{"id":"b:paid","isFree":false}
	]}`, nil)
	m := kiloTestManager(t, up.srv.URL)
	g, base := kiloTestGW(t, m)
	// 强制先拉目录，让边界处于「已拉取」状态
	if _, err := g.refreshCatalog(); err != nil {
		t.Fatalf("refreshCatalog: %v", err)
	}

	resp, body := kiloPost(t, base, `{"model":"b:paid","messages":[]}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("free_only=true 时付费档应被 400 拦下，实际 %d: %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "免费档") {
		t.Fatalf("错误体应说明 free_only 边界: %s", body)
	}
	// 免费档仍放行
	resp2, body2 := kiloPost(t, base, `{"model":"a:free","messages":[]}`)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("免费档应放行，实际 %d: %s", resp2.StatusCode, body2)
	}
	// 关掉开关后付费档放行
	if err := m.ConfigSet("kilo_free_only", "false"); err != nil {
		t.Fatalf("ConfigSet: %v", err)
	}
	resp3, body3 := kiloPost(t, base, `{"model":"b:paid","messages":[]}`)
	if resp3.StatusCode != http.StatusOK {
		t.Fatalf("free_only=false 时应放行，实际 %d: %s", resp3.StatusCode, body3)
	}
}

// TestKiloB2StatusErrorMerge：启动错误不能被目录错误覆盖（独立审查 B2）。
func TestKiloB2StatusErrorMerge(t *testing.T) {
	m := New(t.TempDir())
	g := m.KiloGateway()
	g.mu.Lock()
	g.state, g.lastErr = "failed", "监听失败: port busy"
	g.mu.Unlock()
	g.cMu.Lock()
	g.cErr = "kilo upstream /models HTTP 500"
	g.cMu.Unlock()

	st := g.Status()
	if !strings.Contains(st.LastError, "port busy") {
		t.Fatalf("启动错误被吞: %q", st.LastError)
	}
	if !strings.Contains(st.LastError, "HTTP 500") {
		t.Fatalf("目录错误未合并: %q", st.LastError)
	}
}

// TestKiloB9ConcurrentStart：并发 Start 不得把 ready 覆盖成 failed（独立审查 B9）。
func TestKiloB9ConcurrentStart(t *testing.T) {
	m := New(t.TempDir())
	g := m.KiloGateway()
	ln, err := g.listenOn(0)
	if err != nil {
		t.Fatalf("listenOn: %v", err)
	}
	port := uint16(ln.Addr().(*net.TCPAddr).Port)
	g.serveOn(ln)
	// 用 env 把 Start 指到同一已占用端口：所有并发 Start 都会 bind 失败，
	// 但因 state 已是 ready，必须全部幂等返回、状态保持 ready。
	t.Setenv("OPCODE2API_KILO_PORT", strconv.Itoa(int(port)))
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = g.Start() }()
	}
	wg.Wait()
	if got := g.Status().State; got != "ready" {
		t.Fatalf("并发 Start 后 state = %q, want ready（不得被 bind 失败覆盖）", got)
	}
}

// ---- K6 kilo/ 前缀剥离 ----

func TestKiloK6PrefixStrip(t *testing.T) {
	var seen string
	up := newKiloFakeUpstream(t, `{"data":[{"id":"nvidia/x:free","isFree":true}]}`,
		func(w http.ResponseWriter, r *http.Request, body []byte) {
			var in struct {
				Model string `json:"model"`
			}
			_ = json.Unmarshal(body, &in)
			seen = in.Model
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"model":"nvidia/x:free","choices":[{"message":{"content":"ok"}}]}`)
		})
	m := kiloTestManager(t, up.srv.URL)
	_, base := kiloTestGW(t, m)

	kiloPost(t, base, `{"model":"kilo/nvidia/x:free","messages":[]}`)
	if seen != "nvidia/x:free" {
		t.Fatalf("前缀未剥离，转发到上游的 model = %q", seen)
	}

	kiloPost(t, base, `{"model":"nvidia/x:free","messages":[]}`)
	if seen != "nvidia/x:free" {
		t.Fatalf("裸 id 应原样转发，实际 = %q", seen)
	}
}

// ---- K7 端口与开关 ----

func TestKiloK7PortAndToggle(t *testing.T) {
	m := New(t.TempDir())

	// 默认端口 30080
	if got := m.managerKiloPort(); got != kiloGatewayPortDefault {
		t.Fatalf("默认端口 = %d, want %d", got, kiloGatewayPortDefault)
	}
	// config 覆盖
	if err := m.ConfigSet("kilo_port", "31080"); err != nil {
		t.Fatalf("ConfigSet: %v", err)
	}
	if got := m.managerKiloPort(); got != 31080 {
		t.Fatalf("config 端口 = %d, want 31080", got)
	}
	// env 优先
	t.Setenv("OPCODE2API_KILO_PORT", "32080")
	if got := m.managerKiloPort(); got != 32080 {
		t.Fatalf("env 端口 = %d, want 32080（env 应优先于 config）", got)
	}

	// 默认开关语义：nil = 开
	cfg := m.loadConfig()
	if !kiloEnabled(cfg) {
		t.Fatal("kilo_enabled 未设置时应默认开启")
	}
	if !kiloFreeOnly(cfg) {
		t.Fatal("kilo_free_only 未设置时应默认开启")
	}
	// 关闭后监听不起
	if err := m.ConfigSet("kilo_enabled", "false"); err != nil {
		t.Fatalf("ConfigSet: %v", err)
	}
	g := m.KiloGateway()
	if err := g.Start(); err != nil {
		t.Fatalf("disabled 时 Start 应静默返回 nil，实际 %v", err)
	}
	if g.Status().Running {
		t.Fatal("kilo_enabled=false 时不应在监听")
	}
}

// ---- K8 独立性红线：kilo 链路不得引用 vendors / router / aggregator ----
//
// 独立审查指出旧版只查 kilo_gateway.go 一个文件、不查接线，守卫力度不够：
// 现在覆盖「网关本体 + main.go 的 Kilo 段 + config.go 的 Kilo 段」三处，
// 并额外用 go list -deps 证明没有引入本仓库的供应商/聚合包。
func TestKiloK8IndependenceRedLine(t *testing.T) {
	banned := []string{
		"vendors/custom",
		"vendors/",
		"core/router",
		"aggregator",
		"globalAgg",
		"chatRouterVar",
		"ProviderService",
		"providerservice",
		"40080",
		"50080",
	}
	// 去掉整行 // 注释再比对（注释里允许解释红线本身）；行内注释不算代码。
	stripComments := func(b []byte) string {
		var codeLines []string
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			codeLines = append(codeLines, line)
		}
		return strings.Join(codeLines, "\n")
	}
	// (1) 网关本体：整文件零引用
	b, err := os.ReadFile("kilo_gateway.go")
	if err != nil {
		t.Fatalf("读 kilo_gateway.go 失败: %v", err)
	}
	code := stripComments(b)
	for _, ban := range banned {
		if strings.Contains(code, ban) {
			t.Fatalf("kilo_gateway.go 代码里出现被禁引用 %q（独立性红线）", ban)
		}
	}
	// (2) main.go 的 Kilo 段：只准用 managerInst.KiloGateway() 与配置/状态，
	// 不得把 globalAgg / chatRouterVar / providerSvc 接进 30080 链路。
	mb, err := os.ReadFile("../../main.go")
	if err != nil {
		t.Fatalf("读 main.go 失败: %v", err)
	}
	for _, ln := range strings.Split(string(mb), "\n") {
		s := strings.TrimSpace(ln)
		if !strings.Contains(s, "kilo") && !strings.Contains(s, "Kilo") {
			continue
		}
		if strings.HasPrefix(s, "//") {
			continue
		}
		for _, ban := range []string{"globalAgg", "chatRouterVar", "providerSvc", "ProviderService", "globalAgg."} {
			if strings.Contains(s, ban) {
				t.Fatalf("main.go 的 Kilo 接线行引用了 %q，独立性红线被破: %s", ban, s)
			}
		}
	}
	// (3) 依赖闭包：core/manager 不得传递依赖到 vendors 或 core/router。
	out, err := exec.Command("go", "list", "-deps", "./").Output()
	if err != nil {
		t.Fatalf("go list -deps 失败: %v", err)
	}
	for _, dep := range strings.Split(string(out), "\n") {
		if strings.Contains(dep, "/vendors") || strings.Contains(dep, "/core/router") {
			t.Fatalf("core/manager 依赖闭包里出现 %q（独立性红线）", dep)
		}
	}
}

// ---- 目录解析：isFree 口径 + 不硬编码清单 ----

func TestKiloParseModels(t *testing.T) {
	body := `{"data":[
		{"id":"a","isFree":true,"mayTrainOnYourPrompts":true,"supported_parameters":["tools","temperature"],"context_length":128000,"name":"A"},
		{"id":"b","isFree":false,"mayTrainOnYourPrompts":false},
		{"id":"","isFree":true}
	]}`
	entries, err := parseKiloModels([]byte(body))
	if err != nil {
		t.Fatalf("parseKiloModels: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("空 id 应被丢弃，期望 2 条实际 %d", len(entries))
	}
	if !entries[0].Free || !entries[0].MayTrain || !entries[0].SupportsTools {
		t.Fatalf("字段解析错误: %+v", entries[0])
	}
	if entries[0].ContextWindow != 128000 {
		t.Fatalf("ContextWindow = %d, want 128000", entries[0].ContextWindow)
	}
	if entries[1].Free {
		t.Fatal("isFree=false 不应被标成免费")
	}
}

// kiloPost 发一次聊天请求并返回状态码与响应体。
func kiloPost(t *testing.T, base, body string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, base+"/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+unifiedGatewayKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp, string(raw)
}

// ---- 防「永真用例」守卫 ----

// TestKiloK4bRequestActuallyReachesUpstream：证明网关**真的把请求发到了上游**
// （而不是本地伪造响应）。此前 K2/K3/K4 只断言状态码，理论上「无论上游做什么都
// 返回 200」的假实现也能通过 —— 这里用「上游计数 + 上游收到 Authorization」
// 双断言把这条路堵死。
func TestKiloK4bRequestActuallyReachesUpstream(t *testing.T) {
	var calls int
	var gotAuth, gotPath, gotBody string
	up := newKiloFakeUpstream(t, `{"data":[{"id":"a:free","isFree":true,"mayTrainOnYourPrompts":true}]}`,
		func(w http.ResponseWriter, r *http.Request, body []byte) {
			calls++
			gotAuth = r.Header.Get("Authorization")
			gotPath = r.URL.Path
			gotBody = string(body)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"model":"a:free","choices":[{"message":{"content":"pong"}}]}`)
		})
	m := kiloTestManager(t, up.srv.URL)
	_, base := kiloTestGW(t, m)

	resp, body := kiloPost(t, base, `{"model":"a:free","messages":[{"role":"user","content":"ping"}],"temperature":0.2}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, body)
	}
	if calls != 1 {
		t.Fatalf("上游被调用 %d 次，want 1（网关必须真的转发，不能本地伪造）", calls)
	}
	if gotAuth != kiloUpstreamAuth {
		t.Fatalf("上游收到的 Authorization = %q, want %q", gotAuth, kiloUpstreamAuth)
	}
	if !strings.HasSuffix(gotPath, "/chat/completions") {
		t.Fatalf("上游路径 = %q", gotPath)
	}
	if !strings.Contains(gotBody, `"ping"`) {
		t.Fatalf("上游收到的请求体丢了用户内容: %s", gotBody)
	}
	if !strings.Contains(body, "pong") {
		t.Fatalf("上游响应体未透传给客户端: %s", body)
	}
}

// ---- 需求 #1：每次请求的上游真实响应模型观测 ----

// TestKiloCallRecordsActualModel：非流式 + 流式（SSE）都要记下真实 model，
// 且响应体必须原样透传（观测不能改写客户端拿到的内容）。
func TestKiloCallRecordsActualModel(t *testing.T) {
	models := `{"data":[{"id":"a:free","isFree":true,"mayTrainOnYourPrompts":true}]}`
	t.Run("json", func(t *testing.T) {
		var gotBody string
		up := newKiloFakeUpstream(t, models, func(w http.ResponseWriter, r *http.Request, body []byte) {
			gotBody = string(body)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"model":"nvidia/real-model:free","choices":[{"message":{"content":"hi"}}]}`)
		})
		m := kiloTestManager(t, up.srv.URL)
		g, base := kiloTestGW(t, m)
		resp, body := kiloPost(t, base, `{"model":"a:free","messages":[]}`)
		if resp.StatusCode != 200 || !strings.Contains(body, "nvidia/real-model:free") {
			t.Fatalf("响应必须原样透传，实际 %d: %s", resp.StatusCode, body)
		}
		if !strings.Contains(gotBody, `"model":"a:free"`) {
			t.Fatalf("上游没收到请求: %s", gotBody)
		}
		calls := g.RecentCalls()
		if len(calls) != 1 {
			t.Fatalf("应记 1 条调用，实际 %d", len(calls))
		}
		if calls[0].Requested != "a:free" || calls[0].Actual != "nvidia/real-model:free" || !calls[0].ActualKnown {
			t.Fatalf("真实模型观测错误: %+v", calls[0])
		}
		if calls[0].Status != 200 || calls[0].Stream {
			t.Fatalf("状态/流标记错误: %+v", calls[0])
		}
	})

	t.Run("sse", func(t *testing.T) {
		up := newKiloFakeUpstream(t, models, func(w http.ResponseWriter, r *http.Request, body []byte) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"model\":\"sse/real:free\",\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		})
		m := kiloTestManager(t, up.srv.URL)
		g, base := kiloTestGW(t, m)
		req, _ := http.NewRequest(http.MethodPost, base+"/v1/chat/completions",
			strings.NewReader(`{"model":"a:free","stream":true,"messages":[]}`))
		req.Header.Set("Authorization", "Bearer "+unifiedGatewayKey)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("请求失败: %v", err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if !strings.Contains(string(raw), "sse/real:free") {
			t.Fatalf("SSE 响应未原样透传: %s", raw)
		}
		calls := g.RecentCalls()
		if len(calls) != 1 || calls[0].Actual != "sse/real:free" || !calls[0].Stream {
			t.Fatalf("SSE 真实模型观测错误: %+v", calls)
		}
	})
}

// TestKiloCallHistoryBounded：观测账本有界，不会无限增长。
func TestKiloCallHistoryBounded(t *testing.T) {
	m := New(t.TempDir())
	g := m.KiloGateway()
	for i := 0; i < kiloCallHistoryMax+10; i++ {
		g.recordCall(KiloCallRecord{Time: "t", Requested: "r", Actual: "a"})
	}
	if got := len(g.RecentCalls()); got != kiloCallHistoryMax {
		t.Fatalf("账本应有界 %d，实际 %d", kiloCallHistoryMax, got)
	}
	if g.RecentCalls()[0].Actual != "a" {
		t.Fatal("最新记录应在最前")
	}
}

// TestKiloRefreshCatalogNow：手动刷新绕过节流，强制实拉上游。
func TestKiloRefreshCatalogNow(t *testing.T) {
	up := newKiloFakeUpstream(t, `{"data":[{"id":"fresh:free","isFree":true}]}`, nil)
	m := kiloTestManager(t, up.srv.URL)
	g := m.KiloGateway()
	if _, err := g.refreshCatalog(); err != nil {
		t.Fatalf("首次拉取: %v", err)
	}
	// 立刻强制刷新（10s 节流内），应仍发起真实上游请求并成功
	n, err := g.RefreshCatalogNow()
	if err != nil {
		t.Fatalf("强制刷新失败: %v", err)
	}
	if n != 1 {
		t.Fatalf("目录条数 = %d, want 1", n)
	}
}

// TestKiloK5bModelsEndpointHitsUpstream：/v1/models 必须真的来自上游目录，
// 不能是硬编码清单（上游免费档会漂移：11-11 一天内 11→12 个）。
func TestKiloK5bModelsEndpointHitsUpstream(t *testing.T) {
	up := newKiloFakeUpstream(t, `{"data":[
		{"id":"upstream-only-model:free","isFree":true,"mayTrainOnYourPrompts":true}
	]}`, nil)
	m := kiloTestManager(t, up.srv.URL)
	_, base := kiloTestGW(t, m)

	req, _ := http.NewRequest(http.MethodGet, base+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+unifiedGatewayKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(raw), "upstream-only-model:free") {
		t.Fatalf("/v1/models 未反映上游目录（疑似硬编码）: %s", raw)
	}
}
