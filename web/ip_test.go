package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

/* ---------- clientIP ---------- */

func TestClientIPIgnoresForwardedHeaderFromPublicPeer(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r.RemoteAddr = "203.0.113.9:5555"
	r.Header.Set("X-Forwarded-For", "1.2.3.4")
	if got := clientIP(r); got != "203.0.113.9" {
		t.Fatalf("clientIP = %q, want 203.0.113.9 (公网直连不得信任转发头)", got)
	}
}

// 私网对端不等于可信代理：只有显式列入可信名单的地址才采信转发头。
// （早期实现把「私网」当可信，导致内网客户端能靠伪造 XFF 绕过所有 IP 限制。）
func TestClientIPIgnoresForwardedHeaderFromPrivatePeer(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r.RemoteAddr = "10.0.0.5:5555"
	r.Header.Set("X-Forwarded-For", "1.2.3.4")
	if got := clientIP(r); got != "10.0.0.5" {
		t.Fatalf("clientIP = %q, 私网对端未列入可信代理时不得采信转发头", got)
	}
}

// 可信代理（默认本机）后带链式 XFF 时，取最右一跳的真实客户端。
// 需带反代共享密钥，否则视为直连（见 ip_spoof_test.go）。
func TestClientIPTakesRightmostHopFromTrustedProxy(t *testing.T) {
	restoreProxySecret(t, "test-secret")
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r.RemoteAddr = "127.0.0.1:5555"
	r.Header.Set("X-Forwarded-For", "1.2.3.4, 10.0.0.5")
	r.Header.Set("X-Proxy-Secret", "test-secret")
	if got := clientIP(r); got != "10.0.0.5" {
		t.Fatalf("clientIP = %q, want 10.0.0.5（最右一跳）", got)
	}
}

func TestClientIPFallsBackToRealIP(t *testing.T) {
	restoreProxySecret(t, "test-secret")
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r.RemoteAddr = "127.0.0.1:5555"
	r.Header.Set("X-Real-IP", "5.6.7.8")
	r.Header.Set("X-Proxy-Secret", "test-secret")
	if got := clientIP(r); got != "5.6.7.8" {
		t.Fatalf("clientIP = %q, want 5.6.7.8", got)
	}
}

func TestClientIPNormalizesIPv6(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r.RemoteAddr = "[2001:db8::1]:5555"
	if got := clientIP(r); got != "2001:db8::1" {
		t.Fatalf("clientIP = %q, want 2001:db8::1", got)
	}
}

/* ---------- 登录限速 ---------- */

func TestLoginLimiterLocksAfterRepeatedFailures(t *testing.T) {
	l := NewLoginLimiter(3, 5*time.Minute, time.Minute)
	now := time.Now()
	for i := 0; i < 3; i++ {
		l.Fail("1.2.3.4", now)
	}
	if ok, remain := l.Allow("1.2.3.4", now); ok || remain <= 0 {
		t.Fatalf("Allow = %v remain=%v, 期望锁定", ok, remain)
	}
	if ok, _ := l.Allow("1.2.3.5", now); !ok {
		t.Fatal("其它 IP 不应被牵连")
	}
	l.Success("1.2.3.4")
	if ok, _ := l.Allow("1.2.3.4", now); !ok {
		t.Fatal("成功后应解锁")
	}
}

// 登录限速阈值：口令强度下限提高到 8 位后，真实校验机会收紧到 5 次/10 分钟
// （旧值 10 次/10 分钟在并发齐射下可被放大到数十次，finding 544）。
// 仍需留出余量：管理端多人或同一出口 IP 连续误触时不至于把人锁死。
func TestLoginLimiterThresholdIsLenient(t *testing.T) {
	l := NewLoginLimiter(LoginMaxAttempts, LoginWindow, LoginLockDuration)
	now := time.Now()
	for i := 0; i < LoginMaxAttempts-1; i++ {
		l.Fail("1.2.3.4", now)
	}
	if ok, _ := l.Allow("1.2.3.4", now); !ok {
		t.Fatalf("%d 次失败不应触发锁定", LoginMaxAttempts-1)
	}
	for i := 0; i < 1; i++ {
		l.Fail("1.2.3.4", now)
	}
	if ok, _ := l.Allow("1.2.3.4", now); ok {
		t.Fatalf("%d 次失败应触发锁定", LoginMaxAttempts)
	}
}

/* ---------- 策略存储 ---------- */

func newIPPolicyFixture(t *testing.T) (*IPPolicyStore, *IPTracker, string) {
	t.Helper()
	dir := t.TempDir()
	p, err := NewIPPolicyStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := NewIPTracker(dir, p)
	if err != nil {
		t.Fatal(err)
	}
	return p, tr, dir
}

func TestIPPolicyOverrideWinsOverDefault(t *testing.T) {
	p, _, _ := newIPPolicyFixture(t)
	if err := p.SetDefaults(IPDefaults{WindowTokens: 100, WindowMinutes: 60, MaxActiveMinutes: 30, OutputTPS: 60}); err != nil {
		t.Fatal(err)
	}
	limit := 5
	if err := p.SetOverride("1.2.3.4", IPOverride{WindowTokens: &limit}); err != nil {
		t.Fatal(err)
	}
	eff := p.Effective("1.2.3.4")
	if eff.WindowTokens != 5 || eff.Source != "override" {
		t.Fatalf("Effective = %+v, 期望覆盖 5/override", eff)
	}
	if eff.MaxActiveMinutes != 30 || eff.OutputTPS != 60 {
		t.Fatalf("未覆盖字段应继承默认: %+v", eff)
	}
	other := p.Effective("9.9.9.9")
	if other.WindowTokens != 100 || other.Source != "default" {
		t.Fatalf("其它 IP 应继承默认: %+v", other)
	}
	if err := p.DeleteOverride("1.2.3.4"); err != nil {
		t.Fatal(err)
	}
	if got := p.Effective("1.2.3.4"); got.WindowTokens != 100 || got.Source != "default" {
		t.Fatalf("删除覆盖后应回落默认: %+v", got)
	}
}

func TestIPPolicyPersistsToDisk(t *testing.T) {
	p, _, dir := newIPPolicyFixture(t)
	limit := 7
	if err := p.SetOverride("1.2.3.4", IPOverride{WindowTokens: &limit, Note: "测试"}); err != nil {
		t.Fatal(err)
	}
	p2, err := NewIPPolicyStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	o, ok := p2.Override("1.2.3.4")
	if !ok || o.WindowTokens == nil || *o.WindowTokens != 7 || o.Note != "测试" {
		t.Fatalf("重载后覆盖丢失: %+v ok=%v", o, ok)
	}
}

func TestIPPolicyRejectsNegativeValues(t *testing.T) {
	p, _, _ := newIPPolicyFixture(t)
	if err := p.SetDefaults(IPDefaults{WindowTokens: -1}); err == nil {
		t.Fatal("负数应被拒绝")
	}
	neg := -3
	if err := p.SetOverride("1.2.3.4", IPOverride{OutputTPS: &neg}); err == nil {
		t.Fatal("负数覆盖应被拒绝")
	}
}

/* ---------- 窗口用量 ---------- */

func TestIPTrackerBlocksWhenTokenQuotaExhausted(t *testing.T) {
	p, tr, _ := newIPPolicyFixture(t)
	if err := p.SetDefaults(IPDefaults{WindowTokens: 100, WindowMinutes: 60}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if ok, reason, _ := tr.Check("1.2.3.4", now); !ok {
		t.Fatalf("首次应放行: %s", reason)
	}
	tr.Record("1.2.3.4", 60, 0, now)
	if ok, _, _ := tr.Check("1.2.3.4", now); !ok {
		t.Fatal("未到上限应放行")
	}
	tr.Record("1.2.3.4", 50, 0, now)
	ok, reason, resetAt := tr.Check("1.2.3.4", now)
	if ok {
		t.Fatal("超额后应拦截")
	}
	if !strings.Contains(reason, "额度已用尽") {
		t.Fatalf("原因不明确: %s", reason)
	}
	if resetAt.IsZero() {
		t.Fatal("应返回窗口重置时间")
	}
}

func TestIPTrackerBlocksWhenActiveDurationExhausted(t *testing.T) {
	p, tr, _ := newIPPolicyFixture(t)
	if err := p.SetDefaults(IPDefaults{WindowTokens: 0, WindowMinutes: 60, MaxActiveMinutes: 1}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	tr.Record("1.2.3.4", 0, 61*time.Second, now)
	ok, reason, _ := tr.Check("1.2.3.4", now)
	if ok {
		t.Fatal("时长超限应拦截")
	}
	if !strings.Contains(reason, "时长已用尽") {
		t.Fatalf("原因不明确: %s", reason)
	}
}

func TestIPTrackerResetsWindowAfterExpiry(t *testing.T) {
	p, tr, _ := newIPPolicyFixture(t)
	if err := p.SetDefaults(IPDefaults{WindowTokens: 10, WindowMinutes: 1}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	tr.Record("1.2.3.4", 10, 0, now)
	if ok, _, _ := tr.Check("1.2.3.4", now); ok {
		t.Fatal("窗口内超额应拦截")
	}
	later := now.Add(2 * time.Minute)
	if ok, reason, _ := tr.Check("1.2.3.4", later); !ok {
		t.Fatalf("窗口到期应自动重置: %s", reason)
	}
}

func TestIPTrackerBlocksDisabledIP(t *testing.T) {
	p, tr, _ := newIPPolicyFixture(t)
	if err := p.SetOverride("1.2.3.4", IPOverride{Disabled: true}); err != nil {
		t.Fatal(err)
	}
	ok, reason, _ := tr.Check("1.2.3.4", time.Now())
	if ok {
		t.Fatal("封禁 IP 应拦截")
	}
	if !strings.Contains(reason, "封禁") {
		t.Fatalf("原因不明确: %s", reason)
	}
}

func TestIPTrackerUsagePersistsAcrossRestart(t *testing.T) {
	p, tr, dir := newIPPolicyFixture(t)
	if err := p.SetDefaults(IPDefaults{WindowTokens: 50, WindowMinutes: 60}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	tr.Record("1.2.3.4", 80, time.Minute, now)

	p2, err := NewIPPolicyStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	tr2, err := NewIPTracker(dir, p2)
	if err != nil {
		t.Fatal(err)
	}
	usage := tr2.Snapshot(now)
	if len(usage) != 1 || usage[0].Tokens != 80 {
		t.Fatalf("重启后用量丢失: %+v", usage)
	}
	if ok, _, _ := tr2.Check("1.2.3.4", now); ok {
		t.Fatal("重启后仍应处于已用 80 token 的状态")
	}
}

/* ---------- 限速 ---------- */

func TestEstimateTokens(t *testing.T) {
	if got := estimateTokens(strings.Repeat("a", 400)); got != 100 {
		t.Fatalf("ASCII 估算 = %d, want 100", got)
	}
	if got := estimateTokens(strings.Repeat("中", 100)); got != 100 {
		t.Fatalf("中文估算 = %d, want 100", got)
	}
}

func TestSSEDeltaTextExtraction(t *testing.T) {
	if got := sseDeltaText([]byte(`{"choices":[{"delta":{"content":"hello"}}]}`)); got != "hello" {
		t.Fatalf("OpenAI delta 提取失败: %q", got)
	}
	if got := sseDeltaText([]byte(`{"type":"content_block_delta","delta":{"text":"world"}}`)); got != "world" {
		t.Fatalf("Anthropic delta 提取失败: %q", got)
	}
	if got := sseDeltaText([]byte(`{"type":"response.output_text.delta","delta":"hey"}`)); got != "hey" {
		t.Fatalf("Responses delta 提取失败: %q", got)
	}
}

func TestOutputThrottleDelaysToRespectRate(t *testing.T) {
	th := newOutputThrottle(1000, context.Background())
	start := time.Now()
	th.wait(2000) // 桶容量 1000 token，速率 1000/s → 至少等待约 1 秒
	if el := time.Since(start); el < 900*time.Millisecond {
		t.Fatalf("限速未生效，等待 %v 期望 ≥ 900ms", el)
	}
}

func TestOutputThrottleNilMeansUnlimited(t *testing.T) {
	var th *outputThrottle
	start := time.Now()
	th.wait(100000)
	if el := time.Since(start); el > 200*time.Millisecond {
		t.Fatalf("不限速时不应等待，实际 %v", el)
	}
}

/* ---------- 网关拦截 ---------- */

func newIPGatewayApp(t *testing.T, defaults IPDefaults) (*App, *httptest.Server, *Key) {
	t.Helper()
	up := httptest.NewServer(&upstreamHandler{usage: `"prompt_tokens":10,"completion_tokens":20,"total_tokens":30`})
	cfg := DefaultConfig()
	cfg.UpstreamBaseURL = up.URL + "/v1"
	cfg.AllowedModels = []string{"glm-4"}
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	policies, err := NewIPPolicyStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := policies.SetDefaults(defaults); err != nil {
		t.Fatal(err)
	}
	tracker, err := NewIPTracker(dir, policies)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{
		Cfg: cfg, Store: store, Rl: NewRateLimiter(1000), Sessions: NewSessionStore(),
		Logger: NewLogger(dir), Client: up.Client(), ConfigPath: dir, IPs: tracker,
	}
	k, err := store.CreateKey("glm-4", 1, 1000000, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return app, up, k
}

func TestGatewayBlocksWhenIPWindowExhausted(t *testing.T) {
	app, up, k := newIPGatewayApp(t, IPDefaults{WindowTokens: 5, WindowMinutes: 60})
	defer up.Close()

	body := `{"model":"glm-4","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+k.Key)
	rec := httptest.NewRecorder()
	app.HandleV1(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("首次请求 code=%d body=%s", rec.Code, rec.Body.String())
	}

	req2 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req2.Header.Set("Authorization", "Bearer "+k.Key)
	rec2 := httptest.NewRecorder()
	app.HandleV1(rec2, req2)
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("超额后 code=%d body=%s", rec2.Code, rec2.Body.String())
	}
	if !strings.Contains(rec2.Body.String(), "额度已用尽") {
		t.Fatalf("响应未说明原因: %s", rec2.Body.String())
	}
	if rec2.Header().Get("Retry-After") == "" {
		t.Fatal("应返回 Retry-After")
	}
	logs, _ := app.Logger.ReadLogs(time.Now().Format("2006-01-02"), "")
	found := false
	for _, e := range logs {
		if e.Error != "" && e.IP == "192.0.2.1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("拦截请求应落日志: %+v", logs)
	}
}

func TestGatewayRecordsIPOnSuccess(t *testing.T) {
	app, up, k := newIPGatewayApp(t, IPDefaults{WindowTokens: 1000000, WindowMinutes: 60})
	defer up.Close()
	body := `{"model":"glm-4","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+k.Key)
	rec := httptest.NewRecorder()
	app.HandleV1(rec, req)
	logs, _ := app.Logger.ReadLogs(time.Now().Format("2006-01-02"), "")
	if len(logs) != 1 || logs[0].IP != "192.0.2.1" {
		t.Fatalf("成功请求未记录 IP: %+v", logs)
	}
	usage := app.IPs.Snapshot(time.Now())
	if len(usage) != 1 || usage[0].Tokens != 30 {
		t.Fatalf("窗口用量未累计: %+v", usage)
	}
}

/* ---------- 管理端 API ---------- */

func newIPAdminServer(t *testing.T) (*App, *httptest.Server) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.AdminPasswordHash = HashPassword("admin123")
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	policies, err := NewIPPolicyStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	tracker, err := NewIPTracker(dir, policies)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{
		Cfg: cfg, Store: store, Rl: NewRateLimiter(30), Sessions: NewSessionStore(),
		Logger: NewLogger(dir), Client: http.DefaultClient, ConfigPath: dir, IPs: tracker,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/admin/login", app.handleLogin)
	mux.Handle("GET /api/admin/ip-policies", app.adminAuth(http.HandlerFunc(app.handleGetIPPolicies)))
	mux.Handle("PUT /api/admin/ip-policies/defaults", app.adminAuth(http.HandlerFunc(app.handlePutIPDefaults)))
	mux.Handle("PUT /api/admin/ip-policies/{ip}", app.adminAuth(http.HandlerFunc(app.handlePutIPOverride)))
	mux.Handle("DELETE /api/admin/ip-policies/{ip}", app.adminAuth(http.HandlerFunc(app.handleDeleteIPOverride)))
	mux.Handle("POST /api/admin/ip-policies/{ip}/reset", app.adminAuth(http.HandlerFunc(app.handleResetIPWindow)))
	mux.Handle("GET /api/admin/logs/aggregate", app.adminAuth(http.HandlerFunc(app.handleLogAggregate)))
	mux.Handle("GET /api/admin/keys", app.adminAuth(http.HandlerFunc(app.handleListKeys)))
	mux.Handle("GET /api/admin/overview", app.adminAuth(http.HandlerFunc(app.handleOverview)))
	return app, httptest.NewServer(mux)
}

func ipAdminRequest(t *testing.T, srv *httptest.Server, tok, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, srv.URL+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestAdminIPPolicyAPI(t *testing.T) {
	app, srv := newIPAdminServer(t)
	defer srv.Close()
	tok := adminToken(t, srv)

	code, out := ipAdminRequest(t, srv, tok, http.MethodGet, "/api/admin/ip-policies", nil)
	if code != http.StatusOK {
		t.Fatalf("GET ip-policies code=%d", code)
	}
	if _, ok := out["defaults"]; !ok {
		t.Fatalf("缺少 defaults: %v", out)
	}

	code, _ = ipAdminRequest(t, srv, tok, http.MethodPut, "/api/admin/ip-policies/defaults", IPDefaults{
		WindowTokens: 500000, WindowMinutes: 120, MaxActiveMinutes: 90, OutputTPS: 40,
	})
	if code != http.StatusOK {
		t.Fatalf("PUT defaults code=%d", code)
	}
	if got := app.IPs.Policies().Defaults().OutputTPS; got != 40 {
		t.Fatalf("默认限速未生效: %d", got)
	}

	limit := 12345
	code, _ = ipAdminRequest(t, srv, tok, http.MethodPut, "/api/admin/ip-policies/1.2.3.4", map[string]any{
		"window_tokens": limit, "window_minutes": nil, "max_active_minutes": nil,
		"output_tps": 30, "disabled": false, "note": "外包",
	})
	if code != http.StatusOK {
		t.Fatalf("PUT override code=%d", code)
	}
	eff := app.IPs.Policies().Effective("1.2.3.4")
	if eff.WindowTokens != 12345 || eff.OutputTPS != 30 {
		t.Fatalf("覆盖未生效: %+v", eff)
	}

	app.IPs.Record("1.2.3.4", 100, 0, time.Now())
	code, out = ipAdminRequest(t, srv, tok, http.MethodPost, "/api/admin/ip-policies/1.2.3.4/reset", nil)
	if code != http.StatusOK {
		t.Fatalf("reset code=%d", code)
	}
	usage := app.IPs.Snapshot(time.Now())
	if len(usage) != 1 || usage[0].Tokens != 0 {
		t.Fatalf("重置后用量未清零: %+v", usage)
	}

	code, _ = ipAdminRequest(t, srv, tok, http.MethodDelete, "/api/admin/ip-policies/1.2.3.4", nil)
	if code != http.StatusOK {
		t.Fatalf("DELETE override code=%d", code)
	}
	if got := app.IPs.Policies().Effective("1.2.3.4").Source; got != "default" {
		t.Fatalf("删除后应回落默认: %s", got)
	}
}

func TestAdminLogAggregateEndpoint(t *testing.T) {
	app, srv := newIPAdminServer(t)
	defer srv.Close()
	tok := adminToken(t, srv)
	day := time.Now().Format("2006-01-02")
	for _, e := range []LogEntry{
		{Time: time.Now().Format(time.RFC3339), IP: "1.1.1.1", KeyID: "k1", Model: "glm-4", Total: 100, Status: 200},
		{Time: time.Now().Format(time.RFC3339), IP: "1.1.1.1", KeyID: "k2", Model: "glm-4", Total: 50, Status: 500},
		{Time: time.Now().Format(time.RFC3339), IP: "2.2.2.2", KeyID: "k1", Model: "glm-4", Total: 10, Status: 200},
	} {
		if err := app.Logger.Write(e); err != nil {
			t.Fatal(err)
		}
	}
	code, out := ipAdminRequest(t, srv, tok, http.MethodGet, "/api/admin/logs/aggregate?date="+day, nil)
	if code != http.StatusOK {
		t.Fatalf("aggregate code=%d", code)
	}
	ips, _ := out["ips"].([]any)
	if len(ips) != 2 {
		t.Fatalf("应聚合出 2 个 IP: %v", out)
	}
	first, _ := ips[0].(map[string]any)
	if first["ip"] != "1.1.1.1" {
		t.Fatalf("应按 token 降序: %v", ips)
	}
	if int(first["requests"].(float64)) != 2 || int(first["keys"].(float64)) != 2 {
		t.Fatalf("聚合字段错误: %v", first)
	}
}

func TestAdminKeyListExposesStatus(t *testing.T) {
	_, srv := newIPAdminServer(t)
	defer srv.Close()
	tok := adminToken(t, srv)
	code, out := ipAdminRequest(t, srv, tok, http.MethodGet, "/api/admin/keys", nil)
	if code != http.StatusOK {
		t.Fatalf("keys code=%d", code)
	}
	keys, _ := out["keys"].([]any)
	if len(keys) != 0 {
		t.Fatalf("期望空列表: %v", keys)
	}
}

func TestPasswordChangeRevokesOtherSessions(t *testing.T) {
	_, srv := newAdminApp(t)
	defer srv.Close()
	tokA := adminToken(t, srv)
	resp, err := srv.Client().Post(srv.URL+"/api/admin/login", "application/json", bytes.NewBufferString(`{"password":"admin123"}`))
	if err != nil {
		t.Fatal(err)
	}
	var body struct{ Token string }
	_ = json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()
	tokB := body.Token
	if tokA == tokB {
		t.Fatal("两个会话应持有不同 token")
	}

	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/admin/password", bytes.NewBufferString(`{"password":"Zx9!Kw3mLp7Q"}`))
	req.Header.Set("Authorization", "Bearer "+tokA)
	req.Header.Set("Content-Type", "application/json")
	putResp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	putResp.Body.Close()
	if putResp.StatusCode != http.StatusOK {
		t.Fatalf("改密 code=%d", putResp.StatusCode)
	}

	old, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/admin/overview", nil)
	old.Header.Set("Authorization", "Bearer "+tokB)
	oldResp, err := srv.Client().Do(old)
	if err != nil {
		t.Fatal(err)
	}
	oldResp.Body.Close()
	if oldResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("旧会话应失效，code=%d", oldResp.StatusCode)
	}

	cur, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/admin/overview", nil)
	cur.Header.Set("Authorization", "Bearer "+tokA)
	curResp, err := srv.Client().Do(cur)
	if err != nil {
		t.Fatal(err)
	}
	curResp.Body.Close()
	if curResp.StatusCode != http.StatusOK {
		t.Fatalf("当前会话应保留，code=%d", curResp.StatusCode)
	}
}

/* ---------- AI 问数 ---------- */

// 管理员自用的 AI 问数不能被 IP 策略限制。
// 该接口走管理员会话直接调上游，不经过 /v1 网关，因此 IP 封禁 / 窗口额度 / 限速
// 都不应影响它——否则管理员自己会被自己的策略挡住。
// 这条用例把该边界钉死，防止后续改动把 AI 问数改走网关。
func TestAskEndpointBypassesIPPolicy(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"正常回答"}}]}`))
			return
		}
		w.WriteHeader(404)
	}))
	defer up.Close()

	dir := t.TempDir()
	policies, err := NewIPPolicyStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	// 把「管理员所在 IP」封禁，并把窗口额度设成 1 token、限速压到最低。
	if err := policies.SetOverride("192.0.2.1", IPOverride{Disabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := policies.SetDefaults(IPDefaults{WindowTokens: 1, WindowMinutes: 60, MaxActiveMinutes: 1, OutputTPS: 1}); err != nil {
		t.Fatal(err)
	}
	tracker, err := NewIPTracker(dir, policies)
	if err != nil {
		t.Fatal(err)
	}

	cfg := DefaultConfig()
	cfg.UpstreamBaseURL = up.URL + "/v1"
	cfg.AllowedModels = []string{"glm-4"}
	app := &App{
		Cfg: cfg, Store: mustStore(t), Rl: NewRateLimiter(30), Sessions: NewSessionStore(),
		Logger: NewLogger(dir), Client: up.Client(), IPs: tracker,
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/admin/ask", strings.NewReader(`{"question":"现在限速够用吗？"}`))
	req.RemoteAddr = "192.0.2.1:5555" // 来自被封禁的 IP
	app.handleAsk(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("IP 被封禁时 AI 问数应照常工作，code=%d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Answer string `json:"answer"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Answer == "" {
		t.Fatal("AI 问数未返回内容")
	}
	// 也不应把这次问数计入 IP 窗口用量。
	usage := tracker.Snapshot(time.Now())
	if len(usage) != 0 {
		t.Fatalf("AI 问数不应计入 IP 窗口用量: %+v", usage)
	}
}

func TestAskEndpointReturnsUpstreamAnswer(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"今天 3.2 号 IP 用得最多。"}}]}`))
			return
		}
		w.WriteHeader(404)
	}))
	defer up.Close()
	cfg := DefaultConfig()
	cfg.UpstreamBaseURL = up.URL + "/v1"
	cfg.AllowedModels = []string{"glm-4"}
	app := &App{Cfg: cfg, Store: mustStore(t), Rl: NewRateLimiter(30), Sessions: NewSessionStore(), Logger: NewLogger(t.TempDir()), Client: up.Client()}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/admin/ask", strings.NewReader(`{"question":"今天哪个 IP 用得最多？"}`))
	app.handleAsk(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("ask code=%d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Answer string `json:"answer"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Answer, "3.2 号 IP") {
		t.Fatalf("回答内容不对: %s", out.Answer)
	}
}

func TestAskEndpointRejectsWithoutModel(t *testing.T) {
	cfg := DefaultConfig()
	cfg.UpstreamBaseURL = "http://127.0.0.1:1/v1"
	cfg.AllowedModels = nil
	app := &App{Cfg: cfg, Store: mustStore(t), Rl: NewRateLimiter(30), Sessions: NewSessionStore(), Logger: NewLogger(t.TempDir()), Client: http.DefaultClient}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/admin/ask", strings.NewReader(`{"question":"hi"}`))
	app.handleAsk(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("无模型时 code=%d 期望 400", rec.Code)
	}
}

func mustStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

var _ = fmt.Sprintf

// IP 窗口的重置时间必须落在网格上（如 24h 窗口 → 次日 08:00），
// 而不是「首次使用时刻 + 窗口长度」（早期版本算出 11:17 → 次日 11:17）。
func TestIPWindowResetTimeAlignedToGrid(t *testing.T) {
	loc := time.Local
	dir := t.TempDir()
	policies, err := NewIPPolicyStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := policies.SetDefaults(IPDefaults{WindowTokens: 1000000, WindowMinutes: 1440}); err != nil {
		t.Fatal(err)
	}
	tracker, err := NewIPTracker(dir, policies)
	if err != nil {
		t.Fatal(err)
	}
	// 上午 11:17 首次使用（当天 08:00 已过 → 窗口为 08:00 → 次日 08:00）
	now := time.Date(2026, 10, 21, 11, 17, 3, 0, loc)
	tracker.Record("1.2.3.4", 100, 0, now)

	usage := tracker.Snapshot(now)
	if len(usage) != 1 {
		t.Fatalf("应有 1 条用量: %+v", usage)
	}
	want := time.Date(2026, 10, 22, 8, 0, 0, 0, loc)
	got, err := time.Parse(time.RFC3339, usage[0].ResetAt)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(want) {
		t.Fatalf("重置时间 = %v, 期望 %v（次日 08:00）", got, want)
	}
}

// 越界额度（MaxInt64 脏值）必须在启动时被钳制回合法值。
//
// 回归背景：ClampOutOfRangeQuota 早期只定义了函数却没有任何调用点，导致脏 Key
// 长期存在；MaxIssuedQuota 会把越界值当成「已发放的最大额度」，于是管理员
// 任何下调 token_quota 的操作都被 400 拒绝（活锁）。
func TestClampOutOfRangeQuotaRemovesDirtyKey(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	// 造一条越界的脏 Key
	s.mu.Lock()
	s.keys["dirty"] = &Key{
		ID: "dirty", Key: "sk-dirty", Model: "glm-4",
		TokenUsed: 0, Quota: 9223372036854775807,
	}
	_ = s.writeLocked()
	s.mu.Unlock()

	// MaxIssuedQuota 本就跳过越界值（这部分修复是对的），返回 0
	if got := s.MaxIssuedQuota(); got != 0 {
		t.Fatalf("越界值不应计入 MaxIssuedQuota，got %d", got)
	}

	res, err := s.ClampOutOfRangeQuota(10000000)
	if err != nil {
		t.Fatal(err)
	}
	if res.Clamped != 1 {
		t.Fatalf("应钳制 1 个 Key，实际 %d: %+v", res.Clamped, res.Details)
	}
	snap, _ := s.Snapshot("dirty")
	// 钳制上界是 MaxTokenQuota（1e12）而非 defaultQuota：钳制只针对真正越界的值，
	// 正常存量额度不受影响（额度变更次日 08:00 才生效）。
	if snap.Quota != MaxTokenQuota {
		t.Fatalf("越界额度应被钳制到 MaxTokenQuota=%d，实际 %d", MaxTokenQuota, snap.Quota)
	}
	// 钳制后：不应再阻塞管理员下调额度
	if got := s.MaxIssuedQuota(); got > MaxTokenQuota {
		t.Fatalf("钳制后 MaxIssuedQuota 应为合法值，got %d", got)
	}
}

// 调整单个 Key 额度：下调全局额度前，管理员需要先处理存量 Key 的出路。
func TestSetKeyQuotaAllowsLoweringGlobalQuota(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.SetDefaultQuota(10000000)
	k, _, err := s.CreateOrGetForIP("192.168.12.17", "glm-4", 0, 10000000, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// 存量 Key 额度是 1000 万 → 全局额度不能直接下调
	if got := s.MaxIssuedQuota(); got != 10000000 {
		t.Fatalf("MaxIssuedQuota = %d", got)
	}
	// 先调低存量 Key 额度
	if err := s.SetQuota(k.ID, 5000000); err != nil {
		t.Fatal(err)
	}
	// 现在全局额度就能下调了
	if got := s.MaxIssuedQuota(); got != 5000000 {
		t.Fatalf("调整后 MaxIssuedQuota = %d，期望 5000000", got)
	}
	snap, _ := s.Snapshot(k.ID)
	if snap.Quota != 5000000 {
		t.Fatalf("Key 额度应为 5000000，实际 %d", snap.Quota)
	}
}

/* ---------- 额度变更的生效时机 ---------- */

// 改额度必须**次日 08:00 生效**，当天不打断在用 Key。
//
// 回归背景：旧实现下调 token_quota 时立刻 ApplyQuotaDown，把所有存量 Key 的
// 额度砍到新值 —— 于是当天已用超过新额度的 Key 会当场超额，下一次请求直接被拒，
// 等于用一次配置操作打断别人的工作。
func TestQuotaChangeTakesEffectNextDay(t *testing.T) {
	loc := time.Local
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.SetDefaultQuota(10000000) // 1000W
	now := time.Date(2026, 10, 21, 10, 0, 0, 0, loc)
	k, _, _ := s.CreateOrGetForIP("192.168.12.17", "glm-4", 0, 10000000, now)
	// 该 Key 当天已用 500W
	s.ConsumeAt(k.ID, 5000000, now)

	// 管理员把额度改成 200W，并同步 store 的默认额度（handlePutSettings 的行为）
	s.SetDefaultQuota(2000000)

	// 当天：存量 Key 的额度仍是 1000W，用量 500W，不受影响
	snap, _ := s.Snapshot(k.ID)
	if snap.Quota != 10000000 {
		t.Fatalf("当天额度不应被改动，实际 %d", snap.Quota)
	}
	if snap.TokenUsed != 5000000 {
		t.Fatalf("当天用量不应被清零，实际 %d", snap.TokenUsed)
	}
	// 当天仍可继续用（未超额）
	if !s.ConsumeAt(k.ID, 1000000, now) {
		t.Fatal("当天应仍可使用，不应因改额度而被拒")
	}

	// 次日 08:00 跨额度日 → 额度恢复为新的默认值 200W，用量归零
	next := time.Date(2026, 10, 22, 8, 0, 0, 0, loc)
	s.RollQuota(2000000, next)
	snap2, _ := s.Snapshot(k.ID)
	if snap2.Quota != 2000000 {
		t.Fatalf("次日额度应恢复为新默认值 2000000，实际 %d", snap2.Quota)
	}
	if snap2.TokenUsed != 0 {
		t.Fatalf("次日用量应归零，实际 %d", snap2.TokenUsed)
	}
}

// 新领的 Key 立即按新额度发放（不必等次日）。
func TestNewKeyGetsCurrentQuotaImmediately(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.SetDefaultQuota(2000000)
	k, _, err := s.CreateOrGetForIP("192.168.12.99", "glm-4", 0, 2000000, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if k.Quota != 2000000 {
		t.Fatalf("新 Key 额度应为 2000000，实际 %d", k.Quota)
	}
}
