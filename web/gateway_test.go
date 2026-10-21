package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// upstream returns chat completions, optionally as SSE.
type upstreamHandler struct {
	stream bool
	usage  string
}

func (u *upstreamHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v1/models" {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "glm-4"}}})
		return
	}
	if u.stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":20,\"total_tokens\":30}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":"hi"}}],"usage":{%s}}`, u.usage)
}

func newGatewayApp(t *testing.T, stream bool, usage string) (*App, *httptest.Server) {
	t.Helper()
	up := httptest.NewServer(&upstreamHandler{stream: stream, usage: usage})
	cfg := DefaultConfig()
	cfg.UpstreamBaseURL = up.URL + "/v1"
	cfg.AllowedModels = []string{"glm-4"}
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := &App{
		Cfg:      cfg,
		Store:    store,
		Rl:       NewRateLimiter(cfg.RateLimitPerMin),
		Sessions: NewSessionStore(),
		Logger:   NewLogger(t.TempDir()),
		Client:   up.Client(),
	}
	return app, up
}

func mustCreateKey(t *testing.T, app *App, model string) *Key {
	t.Helper()
	k, err := app.Store.CreateKey(model, app.Cfg.TTLDays, app.Cfg.TokenQuota, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func callV1(t *testing.T, app *App, key string, path string, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.HandleV1(rec, req)
	return rec
}

func TestV1AuthRejections(t *testing.T) {
	app, _ := newGatewayApp(t, false, `"prompt_tokens":1,"completion_tokens":1,"total_tokens":2`)

	if rec := callV1(t, app, "", "/v1/chat/completions", `{"model":"glm-4"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing key code = %d, want 401", rec.Code)
	}
	if rec := callV1(t, app, "sk-nosuch", "/v1/chat/completions", `{"model":"glm-4"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad key code = %d, want 401", rec.Code)
	}

	k := mustCreateKey(t, app, "glm-4")
	if err := app.Store.SetDisabled(k.ID, true); err != nil {
		t.Fatal(err)
	}
	if rec := callV1(t, app, k.Key, "/v1/chat/completions", `{"model":"glm-4"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("disabled code = %d, want 403", rec.Code)
	}
	if err := app.Store.SetDisabled(k.ID, false); err != nil {
		t.Fatal(err)
	}

	// wrong model
	if rec := callV1(t, app, k.Key, "/v1/chat/completions", `{"model":"kimi-k2"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("wrong model code = %d, want 403", rec.Code)
	}
}

func TestV1QuotaExhausted(t *testing.T) {
	app, _ := newGatewayApp(t, false, `"prompt_tokens":1,"completion_tokens":1,"total_tokens":2`)
	k, err := app.Store.CreateKey("glm-4", 7, 100, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	app.Store.AddUsage(k.ID, 100) // 用满额度
	if rec := callV1(t, app, k.Key, "/v1/chat/completions", `{"model":"glm-4"}`); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("quota code = %d, want 429", rec.Code)
	}
}

func TestV1UpstreamSuccessAndUsage(t *testing.T) {
	app, _ := newGatewayApp(t, false, `"prompt_tokens":10,"completion_tokens":20,"total_tokens":30`)
	k := mustCreateKey(t, app, "glm-4")
	rec := callV1(t, app, k.Key, "/v1/chat/completions", `{"model":"glm-4"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"content":"hi"`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if got, _ := app.Store.GetByID(k.ID); got.TokenUsed != 30 {
		t.Fatalf("TokenUsed = %d, want 30", got.TokenUsed)
	}
}

func TestV1StreamUsage(t *testing.T) {
	app, _ := newGatewayApp(t, true, "")
	k := mustCreateKey(t, app, "glm-4")
	rec := callV1(t, app, k.Key, "/v1/chat/completions", `{"model":"glm-4","stream":true}`)
	if !strings.Contains(rec.Body.String(), `[DONE]`) {
		t.Fatalf("stream body = %s", rec.Body.String())
	}
	if got, _ := app.Store.GetByID(k.ID); got.TokenUsed != 30 {
		t.Fatalf("stream TokenUsed = %d, want 30", got.TokenUsed)
	}
}

func TestV1AnthropicStyleUsage(t *testing.T) {
	// /v1/messages 形态：usage 嵌套 input_tokens/output_tokens
	app, _ := newGatewayApp(t, false, `"input_tokens":5,"output_tokens":6`)
	k := mustCreateKey(t, app, "glm-4")
	rec := callV1(t, app, k.Key, "/v1/messages", `{"model":"glm-4"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", rec.Code, rec.Body.String())
	}
	if got, _ := app.Store.GetByID(k.ID); got.TokenUsed != 11 {
		t.Fatalf("TokenUsed = %d, want 11", got.TokenUsed)
	}
}

func TestV1RateLimit(t *testing.T) {
	app, _ := newGatewayApp(t, false, `"prompt_tokens":1,"completion_tokens":1,"total_tokens":2`)
	app.Rl = NewRateLimiter(1)
	k := mustCreateKey(t, app, "glm-4")
	if rec := callV1(t, app, k.Key, "/v1/chat/completions", `{"model":"glm-4"}`); rec.Code != http.StatusOK {
		t.Fatalf("first code = %d", rec.Code)
	}
	if rec := callV1(t, app, k.Key, "/v1/chat/completions", `{"model":"glm-4"}`); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second code = %d, want 429", rec.Code)
	}
}