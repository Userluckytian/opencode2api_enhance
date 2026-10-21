package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestApp(t *testing.T) *App {
	t.Helper()
	cfg := DefaultConfig()
	cfg.AllowedModels = []string{"glm-4", "kimi-k2"}
	// finding 548（fail-closed）：下发的 base_url 只来自部署方配置的 public_base_url，
	// 缺失即 503 拒绝下发，不再回退到 Host / X-Forwarded-Host。
	// 测试夹具注入一个合法的对外地址，等价于部署时写 config.json 的 public_base_url。
	cfg.PublicBaseURL = "http://example.com:23081"
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &App{Cfg: cfg, Store: store}
}

func TestHandleModels(t *testing.T) {
	app := newTestApp(t)
	rec := httptest.NewRecorder()
	app.HandleModels(rec, httptest.NewRequest(http.MethodGet, "/api/models", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	var body struct {
		Models []string `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Models) != 2 || body.Models[0] != "glm-4" {
		t.Fatalf("models = %v", body.Models)
	}
}

func TestHandleCreateKey(t *testing.T) {
	app := newTestApp(t)
	reqBody := bytes.NewBufferString(`{"model":"glm-4"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/keys", reqBody)
	req.Host = "example.com:23081"
	rec := httptest.NewRecorder()
	app.HandleCreateKey(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		BaseURL   string `json:"base_url"`
		Key       string `json:"key"`
		ExpiresAt string `json:"expires_at"`
		Model     string `json:"model"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.BaseURL != "http://example.com:23081/v1" {
		t.Fatalf("base_url = %q", body.BaseURL)
	}
	if body.Key[:3] != "sk-" {
		t.Fatalf("key = %q", body.Key)
	}
	if body.Model != "glm-4" {
		t.Fatalf("model = %q", body.Model)
	}
	// Key 应已入库且绑定模型
	if _, ok := app.Store.GetByKey(body.Key); !ok {
		t.Fatal("key not stored")
	}
}

func TestHandleCreateKeyRejectsNonWhitelisted(t *testing.T) {
	app := newTestApp(t)
	req := httptest.NewRequest(http.MethodPost, "/api/keys", bytes.NewBufferString(`{"model":"gpt-4"}`))
	rec := httptest.NewRecorder()
	app.HandleCreateKey(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", rec.Code)
	}
}

func TestHandleCreateKeyWithForwardedPrefix(t *testing.T) {
	app := newTestApp(t)
	req := httptest.NewRequest(http.MethodPost, "/api/keys", bytes.NewBufferString(`{"model":"glm-4"}`))
	req.Host = "example.com"
	req.Header.Set("X-Forwarded-Prefix", "/token-buffet")
	// 注意：public_base_url 里带了路径时，配置的路径优先于转发前缀；
	// 这里显式改用不带路径的配置，才能验证 X-Forwarded-Prefix 分支。
	app.Cfg.PublicBaseURL = "http://example.com"
	rec := httptest.NewRecorder()
	app.HandleCreateKey(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		BaseURL string `json:"base_url"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.BaseURL != "http://example.com/token-buffet/v1" {
		t.Fatalf("base_url = %q, want http://example.com/token-buffet/v1", body.BaseURL)
	}
}

func TestHandleCreateKeyBadJSON(t *testing.T) {
	app := newTestApp(t)
	req := httptest.NewRequest(http.MethodPost, "/api/keys", bytes.NewBufferString(`not-json`))
	rec := httptest.NewRecorder()
	app.HandleCreateKey(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", rec.Code)
	}
}

// finding 548（fail-closed）：未配置 public_base_url 时，POST /api/keys 必须 503，
// 且错误响应里不得出现任何客户端可控的 Host / X-Forwarded-Host。
func TestHandleCreateKeyFailsClosedWithoutPublicBaseURL(t *testing.T) {
	app := newTestApp(t)
	app.Cfg.PublicBaseURL = ""
	req := httptest.NewRequest(http.MethodPost, "/api/keys", bytes.NewBufferString(`{"model":"glm-4"}`))
	req.Host = "example.com:23081"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-Host", "evil.example.com")
	rec := httptest.NewRecorder()
	app.HandleCreateKey(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, want 503; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "public_base_url") {
		t.Fatalf("错误信息应说明 public_base_url 未配置: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "evil.example.com") {
		t.Fatalf("错误响应泄露了攻击者主机: %s", rec.Body.String())
	}
}
