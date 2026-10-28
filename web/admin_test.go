package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newAdminApp(t *testing.T) (*App, *httptest.Server) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.AdminPasswordHash = HashPassword("admin123")
	cfg.UpstreamBaseURL = "http://127.0.0.1:1/v1" // 不可达，仅测试 warning 分支
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := &App{
		Cfg:        cfg,
		Store:      store,
		Rl:         NewRateLimiter(cfg.RateLimitPerMin),
		Sessions:   NewSessionStore(),
		Logger:     NewLogger(t.TempDir()),
		Client:     http.DefaultClient,
		ConfigPath: t.TempDir(),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/admin/login", app.handleLogin)
	mux.Handle("GET /api/admin/overview", app.adminAuth(http.HandlerFunc(app.handleOverview)))
	mux.Handle("GET /api/admin/settings", app.adminAuth(http.HandlerFunc(app.handleGetSettings)))
	mux.Handle("PUT /api/admin/settings", app.adminAuth(http.HandlerFunc(app.handlePutSettings)))
	// 多上游管理路由（2026-10-28）：上游已从「参数设置」拆出独立接口。
	mux.Handle("GET /api/admin/upstreams", app.adminAuth(http.HandlerFunc(app.handleListUpstreams)))
	mux.Handle("POST /api/admin/upstreams", app.adminAuth(http.HandlerFunc(app.handleCreateUpstream)))
	mux.Handle("PUT /api/admin/upstreams/{id}", app.adminAuth(http.HandlerFunc(app.handleUpdateUpstream)))
	mux.Handle("DELETE /api/admin/upstreams/{id}", app.adminAuth(http.HandlerFunc(app.handleDeleteUpstream)))
	mux.Handle("POST /api/admin/upstreams/{id}/test", app.adminAuth(http.HandlerFunc(app.handleTestUpstream)))
	mux.Handle("GET /api/admin/models", app.adminAuth(http.HandlerFunc(app.handleAdminModels)))
	mux.Handle("POST /api/admin/models", app.adminAuth(http.HandlerFunc(app.handleSaveModels)))
	mux.Handle("GET /api/admin/keys", app.adminAuth(http.HandlerFunc(app.handleListKeys)))
	mux.Handle("POST /api/admin/keys/{id}/disable", app.adminAuth(http.HandlerFunc(app.handleDisableKey)))
	mux.Handle("POST /api/admin/keys/{id}/enable", app.adminAuth(http.HandlerFunc(app.handleEnableKey)))
	mux.Handle("DELETE /api/admin/keys/{id}", app.adminAuth(http.HandlerFunc(app.handleDeleteKey)))
	mux.Handle("PUT /api/admin/password", app.adminAuth(http.HandlerFunc(app.handlePutPassword)))
	mux.Handle("GET /api/admin/logs", app.adminAuth(http.HandlerFunc(app.handleLogs)))
	mux.Handle("GET /api/admin/ask/usage", app.adminAuth(http.HandlerFunc(app.handleAskUsage)))
	mux.Handle("GET /api/admin/audit", app.adminAuth(http.HandlerFunc(app.handleAuditList)))
	return app, httptest.NewServer(mux)
}

func adminToken(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	resp, err := srv.Client().Post(srv.URL+"/api/admin/login", "application/json",
		bytes.NewBufferString(`{"password":"admin123"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login code = %d", resp.StatusCode)
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body.Token
}

func TestAdminLogin(t *testing.T) {
	_, srv := newAdminApp(t)
	defer srv.Close()
	if adminToken(t, srv) == "" {
		t.Fatal("empty token")
	}
	resp, err := srv.Client().Post(srv.URL+"/api/admin/login", "application/json",
		bytes.NewBufferString(`{"password":"wrong"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong password code = %d, want 401", resp.StatusCode)
	}
}

func TestAdminRequiresToken(t *testing.T) {
	_, srv := newAdminApp(t)
	defer srv.Close()
	resp, err := srv.Client().Get(srv.URL + "/api/admin/keys")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", resp.StatusCode)
	}
}

func TestAdminSettingsRoundTrip(t *testing.T) {
	app, srv := newAdminApp(t)
	defer srv.Close()
	tok := adminToken(t, srv)

	// 上游已拆到 /api/admin/upstreams；本接口只管额度/有效期/限速。
	body := `{"ttl_days":3,"token_quota":500000,"rate_limit_per_min":10}`
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/admin/settings", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put settings code = %d", resp.StatusCode)
	}
	if app.Cfg.TTLDays != 3 || app.Cfg.TokenQuota != 500000 || app.Cfg.RateLimitPerMin != 10 {
		t.Fatalf("cfg not updated: %+v", app.Cfg)
	}
	// 无效值应被拒
	req2, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/admin/settings",
		bytes.NewBufferString(`{"ttl_days":0,"token_quota":-1,"rate_limit_per_min":0}`))
	req2.Header.Set("Authorization", "Bearer "+tok)
	resp2, err := srv.Client().Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid settings code = %d, want 400", resp2.StatusCode)
	}
}

func TestAdminPasswordChange(t *testing.T) {
	app, srv := newAdminApp(t)
	defer srv.Close()
	tok := adminToken(t, srv)
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/admin/password",
		bytes.NewBufferString(`{"password":"Zx9!Kw3mLp7Q"}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("code = %d", resp.StatusCode)
	}
	if !VerifyPassword(app.Cfg.AdminPasswordHash, "Zx9!Kw3mLp7Q") {
		t.Fatal("password not updated")
	}
}

func TestAdminKeyLifecycle(t *testing.T) {
	app, srv := newAdminApp(t)
	defer srv.Close()
	tok := adminToken(t, srv)

	k, _ := app.Store.CreateKey("glm-4", 7, 1000, time.Now())

	do := func(method, path, body string) *http.Response {
		var rdr *bytes.Reader
		if body == "" {
			rdr = bytes.NewReader(nil)
		} else {
			rdr = bytes.NewReader([]byte(body))
		}
		req, _ := http.NewRequest(method, srv.URL+path, rdr)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	resp := do(http.MethodGet, "/api/admin/keys", "")
	var list struct {
		Keys []map[string]any `json:"keys"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	if len(list.Keys) != 1 {
		t.Fatalf("keys len = %d", len(list.Keys))
	}

	resp = do(http.MethodPost, "/api/admin/keys/"+k.ID+"/disable", "")
	resp.Body.Close()
	if got, _ := app.Store.GetByID(k.ID); !got.Disabled {
		t.Fatal("expected disabled")
	}
	resp = do(http.MethodPost, "/api/admin/keys/"+k.ID+"/enable", "")
	resp.Body.Close()
	if got, _ := app.Store.GetByID(k.ID); got.Disabled {
		t.Fatal("expected enabled")
	}
	resp = do(http.MethodDelete, "/api/admin/keys/"+k.ID, "")
	resp.Body.Close()
	if _, ok := app.Store.GetByID(k.ID); ok {
		t.Fatal("expected deleted")
	}
}

func TestAdminLogsEndpoint(t *testing.T) {
	app, srv := newAdminApp(t)
	defer srv.Close()
	tok := adminToken(t, srv)

	_ = app.Logger.Write(LogEntry{Time: time.Now().Format(time.RFC3339), KeyID: "k1", Model: "glm-4", Total: 10, Status: 200, Ms: 1})

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/admin/logs", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		Logs []LogEntry `json:"logs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Logs) != 1 || body.Logs[0].KeyID != "k1" {
		t.Fatalf("logs = %+v", body.Logs)
	}
}
