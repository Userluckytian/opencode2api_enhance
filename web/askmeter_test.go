package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 上游密钥不能明文回传：拿到管理员会话就能直连上游，等于绕过全部 IP 策略。
// 多上游后密钥经 /api/admin/upstreams 管理，本用例验证该接口的脱敏行为。
func TestSettingsDoesNotLeakUpstreamKey(t *testing.T) {
	app, srv := newAdminApp(t)
	defer srv.Close()
	tok := adminToken(t, srv)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/admin/upstreams",
		bytes.NewBufferString(`{"id":"up1","base_url":"http://1.2.3.4:18080/v1","api_key":"sk-secret-value-1234","enabled":true}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create upstream code=%d", resp.StatusCode)
	}

	code, body := e2eGet(t, srv, tok, "/api/admin/upstreams")
	if code != http.StatusOK {
		t.Fatalf("get upstreams code=%d", code)
	}
	if strings.Contains(body, "sk-secret-value-1234") {
		t.Fatalf("GET upstreams 泄露了上游密钥明文: %s", body)
	}
	if !strings.Contains(body, "****1234") {
		t.Fatalf("未返回脱敏后的密钥: %s", body)
	}
	u, ok := FindUpstream(app.Cfg.Upstreams, "up1")
	if !ok || u.APIKey != "sk-secret-value-1234" {
		t.Fatalf("上游密钥未正确保存: %+v", app.Cfg.Upstreams)
	}
}

// 留空 = 保持原密钥不变（页面不再回传明文，无法原样回填）。
func TestSettingsBlankKeyKeepsExisting(t *testing.T) {
	app, srv := newAdminApp(t)
	defer srv.Close()
	tok := adminToken(t, srv)

	body := `{"id":"up1","base_url":"http://1.2.3.4:18080/v1","api_key":"sk-keep-me","enabled":true}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/admin/upstreams", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, _ := srv.Client().Do(req)
	resp.Body.Close()

	body2 := `{"base_url":"http://5.6.7.8:18080/v1","api_key":""}`
	req2, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/admin/upstreams/up1", bytes.NewBufferString(body2))
	req2.Header.Set("Authorization", "Bearer "+tok)
	resp2, _ := srv.Client().Do(req2)
	resp2.Body.Close()

	u, ok := FindUpstream(app.Cfg.Upstreams, "up1")
	if !ok {
		t.Fatalf("上游丢失: %+v", app.Cfg.Upstreams)
	}
	if u.APIKey != "sk-keep-me" {
		t.Fatalf("留空不应清空密钥: %q", u.APIKey)
	}
	if u.BaseURL != "http://5.6.7.8:18080/v1" {
		t.Fatalf("上游地址未更新: %q", u.BaseURL)
	}
}

// AI 问数不再有每日额度闸门（需求变更：管理员自用问答只计量、不拦截）。
// 计量仍然生效：用量会被记录下来。
func TestAskHasNoDailyQuotaGate(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"回答"}}],"usage":{"prompt_tokens":100,"completion_tokens":200,"total_tokens":300}}`))
	}))
	defer up.Close()

	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.UpstreamBaseURL = up.URL + "/v1"
	cfg.AllowedModels = []string{"glm-4"}
	meter := NewAskMeter(dir)
	app := &App{
		Cfg: cfg, Store: mustStore(t), Rl: NewRateLimiter(30), Sessions: NewSessionStore(),
		Logger: NewLogger(dir), Client: up.Client(), Ask: meter,
	}

	// 连续多次调用都应成功（无额度闸门）
	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/admin/ask", strings.NewReader(`{"question":"q"}`))
		app.handleAsk(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("第 %d 次应成功 code=%d body=%s", i+1, rec.Code, rec.Body.String())
		}
	}
	// 计量仍然生效
	st := meter.Snapshot(time.Now())
	if st.UsedTokens <= 0 || st.Requests != 3 {
		t.Fatalf("用量应被计量: %+v", st)
	}
}

// AI 问数要留下业务日志（与 /v1 调用日志分开）。
func TestAskWritesBusinessLog(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"今天 3.2 号 IP 用得最多。"}}],"usage":{"prompt_tokens":100,"completion_tokens":200,"total_tokens":300}}`))
	}))
	defer up.Close()

	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.UpstreamBaseURL = up.URL + "/v1"
	cfg.AllowedModels = []string{"glm-4"}
	meter := NewAskMeter(dir)
	app := &App{
		Cfg: cfg, Store: mustStore(t), Rl: NewRateLimiter(30), Sessions: NewSessionStore(),
		Logger: NewLogger(dir), Client: up.Client(), Ask: meter,
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/admin/ask", strings.NewReader(`{"question":"今天哪个 IP 用得最多？"}`))
	app.handleAsk(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("ask code=%d", rec.Code)
	}

	entries, err := meter.ReadAskLogs(time.Now().Format("2006-01-02"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("应有 1 条业务日志: %+v", entries)
	}
	e := entries[0]
	if e.Question != "今天哪个 IP 用得最多？" {
		t.Fatalf("问题未记录: %q", e.Question)
	}
	if !strings.Contains(e.Answer, "3.2 号 IP") {
		t.Fatalf("回答未记录: %q", e.Answer)
	}
	if e.Total != 300 || e.PromptTokens != 100 || e.CompletionTokens != 200 {
		t.Fatalf("token 用量未记录: %+v", e)
	}
	if !e.OK {
		t.Fatal("应标记为成功")
	}
	// 业务日志应与 /v1 调用日志分开存放
	if _, err := meter.ReadAskLogs("2000-01-01"); err != nil {
		t.Fatalf("读取其它日期应返回空而非错误: %v", err)
	}
}

// 上游没返回 usage 时，用字符估算兜底，避免额度被绕过。
func TestAskFallsBackToEstimatedTokens(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"短答案"}}]}`))
	}))
	defer up.Close()

	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.UpstreamBaseURL = up.URL + "/v1"
	cfg.AllowedModels = []string{"glm-4"}
	meter := NewAskMeter(dir)
	app := &App{
		Cfg: cfg, Store: mustStore(t), Rl: NewRateLimiter(30), Sessions: NewSessionStore(),
		Logger: NewLogger(dir), Client: up.Client(), Ask: meter,
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/admin/ask", strings.NewReader(`{"question":"q"}`))
	app.handleAsk(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("ask code=%d", rec.Code)
	}
	st := meter.Snapshot(time.Now())
	if st.UsedTokens <= 0 {
		t.Fatalf("无 usage 时应按估算计入用量: %+v", st)
	}
}

// 敏感配置修改要落审计（多上游后由 /api/admin/upstreams 记录上游变更）。
func TestSettingsChangeIsAudited(t *testing.T) {
	dir := t.TempDir()
	app, srv := newAdminApp(t)
	defer srv.Close()
	app.Audit = NewAuditLog(dir)
	tok := adminToken(t, srv)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/admin/upstreams",
		bytes.NewBufferString(`{"id":"up9","base_url":"http://9.9.9.9:18080/v1","api_key":"sk-new-key-9999","enabled":true}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, _ := srv.Client().Do(req)
	resp.Body.Close()

	entries := app.Audit.Read(time.Now().Format("2006-01-02"))
	if len(entries) != 1 {
		t.Fatalf("应有 1 条审计记录: %+v", entries)
	}
	if entries[0].Action != "create_upstream" {
		t.Fatalf("动作不对: %+v", entries[0])
	}
	if !strings.Contains(entries[0].Detail, "9.9.9.9") {
		t.Fatalf("审计应记录上游地址: %+v", entries[0])
	}
	// 审计里不能出现明文密钥
	if strings.Contains(entries[0].Detail, "sk-new-key-9999") {
		t.Fatalf("审计记录泄露了密钥明文: %+v", entries[0])
	}
}

func TestMaskSecret(t *testing.T) {
	if got := maskSecret("sk-abcdef1234"); got != "****1234" {
		t.Fatalf("got %q", got)
	}
	if got := maskSecret(""); got != "" {
		t.Fatalf("got %q", got)
	}
	if got := maskSecret("abc"); got != "****" {
		t.Fatalf("got %q", got)
	}
}

func TestAskUsageEndpoint(t *testing.T) {
	dir := t.TempDir()
	app, srv := newAdminApp(t)
	defer srv.Close()
	app.Ask = NewAskMeter(dir)
	tok := adminToken(t, srv)

	code, body := e2eGet(t, srv, tok, "/api/admin/ask/usage")
	if code != http.StatusOK {
		t.Fatalf("code=%d body=%s", code, body)
	}
	var out struct {
		Quota struct {
			DailyLimit int `json:"daily_limit"`
			UsedTokens int `json:"used_tokens"`
		} `json:"quota"`
		Recent []AskUsageRow `json:"recent"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	if out.Quota.UsedTokens != 0 {
		t.Fatalf("初始用量应为 0: %+v", out.Quota)
	}
}
