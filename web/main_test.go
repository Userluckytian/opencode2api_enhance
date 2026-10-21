package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newFullApp(t *testing.T) *App {
	t.Helper()
	up := httptest.NewServer(&upstreamHandler{stream: false, usage: `"prompt_tokens":10,"completion_tokens":20,"total_tokens":30`})
	t.Cleanup(up.Close)
	cfg := DefaultConfig()
	cfg.AdminPasswordHash = HashPassword("admin123")
	cfg.UpstreamBaseURL = up.URL + "/v1"
	cfg.AllowedModels = []string{"glm-4"}
	// finding 548（fail-closed）：base_url 只来自 public_base_url，缺失即 503 拒绝下发。
	cfg.PublicBaseURL = "http://example.com:23081"
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &App{
		Cfg: cfg, Store: store,
		Rl: NewRateLimiter(cfg.RateLimitPerMin), Sessions: NewSessionStore(),
		Logger: NewLogger(t.TempDir()), Client: up.Client(), ConfigPath: t.TempDir(),
	}
}

func TestServeIndexPage(t *testing.T) {
	mux := buildMux(newFullApp(t))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "领取 API Key") {
		t.Fatalf("index code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestServeAdminPage(t *testing.T) {
	mux := buildMux(newFullApp(t))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin.html", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "管理后台") {
		t.Fatalf("admin code=%d", rec.Code)
	}
}

func TestEndToEndKeyFlow(t *testing.T) {
	app := newFullApp(t)
	mux := buildMux(app)

	// 选模型
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/models", nil))
	if !strings.Contains(rec.Body.String(), "glm-4") {
		t.Fatalf("models body=%s", rec.Body.String())
	}

	// 领 Key
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/keys",
		bytes.NewBufferString(`{"model":"glm-4"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("create key code=%d body=%s", rec.Code, rec.Body.String())
	}
	var issued struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}

	// 用 Key 调用网关
	body := `{"model":"glm-4","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+issued.Key)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"content":"hi"`) {
		t.Fatalf("v1 code=%d body=%s", rec.Code, rec.Body.String())
	}

	// Key 被禁用后应 403
	id, _ := app.Store.GetByKey(issued.Key)
	_ = app.Store.SetDisabled(id.ID, true)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("disabled code=%d, want 403", rec.Code)
	}
}
