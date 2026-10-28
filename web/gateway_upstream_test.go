package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// recordingUpstream 记录收到的请求，用于断言「转发到了哪个上游」与「上游看到的 model」。
type recordingUpstream struct {
	name string

	mu       sync.Mutex
	hits     int
	lastBody []byte
	lastAuth string
	lastPath string
}

func (u *recordingUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	u.mu.Lock()
	u.hits++
	u.lastBody = body
	u.lastAuth = r.Header.Get("Authorization")
	u.lastPath = r.URL.Path
	u.mu.Unlock()

	if strings.HasSuffix(r.URL.Path, "/models") {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{{"id": u.name + "-model"}},
		})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"hi"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
}

func (u *recordingUpstream) snapshot() (hits int, body []byte, auth, path string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.hits, append([]byte(nil), u.lastBody...), u.lastAuth, u.lastPath
}

// newMultiUpstreamApp 构造「默认上游 main + 备用上游 backup」的应用。
func newMultiUpstreamApp(t *testing.T, backupEnabled bool) (*App, *recordingUpstream, *recordingUpstream, func()) {
	t.Helper()
	mainUp := &recordingUpstream{name: "main"}
	backupUp := &recordingUpstream{name: "backup"}
	mainSrv := httptest.NewServer(mainUp)
	backupSrv := httptest.NewServer(backupUp)

	cfg := DefaultConfig()
	cfg.Upstreams = []Upstream{
		{ID: "main", Name: "主上游", BaseURL: mainSrv.URL + "/v1", APIKey: "sk-main", Enabled: true},
		{ID: "backup", Name: "备用", BaseURL: backupSrv.URL + "/v1", APIKey: "sk-backup", Enabled: backupEnabled},
	}
	cfg.DefaultUpstreamID = "main"
	cfg.AllowedModels = []string{"glm-4", "backup/glm-4"}

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
		Client:   mainSrv.Client(),
	}
	return app, mainUp, backupUp, func() {
		mainSrv.Close()
		backupSrv.Close()
	}
}

// 带前缀的 model 必须转发到对应上游，且上游收到剥离前缀后的 model。
func TestGatewayRoutesByPrefix(t *testing.T) {
	app, mainUp, backupUp, cleanup := newMultiUpstreamApp(t, true)
	defer cleanup()

	k := mustCreateKey(t, app, "backup/glm-4")
	rec := callV1(t, app, k.Key, "/v1/chat/completions",
		`{"model":"backup/glm-4","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}

	if hits, _, _, _ := mainUp.snapshot(); hits != 0 {
		t.Fatalf("main upstream should not be hit, got %d", hits)
	}
	hits, body, auth, path := backupUp.snapshot()
	if hits != 1 {
		t.Fatalf("backup upstream hits = %d, want 1", hits)
	}
	if path != "/v1/chat/completions" {
		t.Fatalf("upstream path = %q", path)
	}
	if auth != "Bearer sk-backup" {
		t.Fatalf("upstream auth = %q, want backup key", auth)
	}

	var got struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Model != "glm-4" {
		t.Fatalf("upstream saw model = %q, want %q (prefix must be stripped)", got.Model, "glm-4")
	}
}

// 不带前缀的 model 走默认上游，且 model 原样不动。
func TestGatewayDefaultUpstreamNoPrefix(t *testing.T) {
	app, mainUp, backupUp, cleanup := newMultiUpstreamApp(t, true)
	defer cleanup()

	k := mustCreateKey(t, app, "glm-4")
	rec := callV1(t, app, k.Key, "/v1/chat/completions", `{"model":"glm-4"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
	if hits, _, _, _ := backupUp.snapshot(); hits != 0 {
		t.Fatalf("backup upstream should not be hit, got %d", hits)
	}
	hits, body, auth, _ := mainUp.snapshot()
	if hits != 1 {
		t.Fatalf("main upstream hits = %d, want 1", hits)
	}
	if auth != "Bearer sk-main" {
		t.Fatalf("upstream auth = %q, want main key", auth)
	}
	if !strings.Contains(string(body), `"model":"glm-4"`) {
		t.Fatalf("model should be forwarded unchanged, body = %s", body)
	}
}

// 含斜杠的真实模型名不能被误剥前缀（回归：or/minimax/... 形态）。
func TestGatewayKeepsSlashyModelOnDefault(t *testing.T) {
	app, mainUp, _, cleanup := newMultiUpstreamApp(t, true)
	defer cleanup()

	// 白名单里放一个含斜杠但不是上游 ID 的模型名
	app.Cfg.AllowedModels = []string{"or/minimax/minimax-m3.1-flash:free"}
	k := mustCreateKey(t, app, "or/minimax/minimax-m3.1-flash:free")
	rec := callV1(t, app, k.Key, "/v1/chat/completions",
		`{"model":"or/minimax/minimax-m3.1-flash:free"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
	_, body, _, _ := mainUp.snapshot()
	var got struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Model != "or/minimax/minimax-m3.1-flash:free" {
		t.Fatalf("model = %q, want unchanged", got.Model)
	}
}

// 停用的上游不参与路由：前缀不剥离，请求落到默认上游。
func TestGatewayDisabledUpstreamFallsBack(t *testing.T) {
	app, mainUp, backupUp, cleanup := newMultiUpstreamApp(t, false) // backup 停用
	defer cleanup()

	k := mustCreateKey(t, app, "backup/glm-4")
	rec := callV1(t, app, k.Key, "/v1/chat/completions", `{"model":"backup/glm-4"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
	if hits, _, _, _ := backupUp.snapshot(); hits != 0 {
		t.Fatalf("disabled upstream must not be hit, got %d", hits)
	}
	_, body, _, _ := mainUp.snapshot()
	var got struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	// 前缀不剥离：整串当作普通模型名
	if got.Model != "backup/glm-4" {
		t.Fatalf("model = %q, want unchanged %q", got.Model, "backup/glm-4")
	}
}

// 没有任何启用上游时返回 503（而不是 502/500 或空指针）。
func TestGatewayNoEnabledUpstream(t *testing.T) {
	app, _, _, cleanup := newMultiUpstreamApp(t, false)
	defer cleanup()
	app.Cfg.Upstreams[0].Enabled = false // main 也停用

	k := mustCreateKey(t, app, "glm-4")
	rec := callV1(t, app, k.Key, "/v1/chat/completions", `{"model":"glm-4"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, want 503, body = %s", rec.Code, rec.Body.String())
	}
}

// 白名单仍以客户端原始 model（含前缀）为准：绑定 glm-4 的 Key 不能用 backup/glm-4。
func TestGatewayWhitelistUsesRawModel(t *testing.T) {
	app, _, backupUp, cleanup := newMultiUpstreamApp(t, true)
	defer cleanup()

	k := mustCreateKey(t, app, "glm-4") // 绑定不带前缀的 glm-4
	rec := callV1(t, app, k.Key, "/v1/chat/completions", `{"model":"backup/glm-4"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("code = %d, want 403, body = %s", rec.Code, rec.Body.String())
	}
	if hits, _, _, _ := backupUp.snapshot(); hits != 0 {
		t.Fatalf("unauthorized request must not reach upstream, got %d hits", hits)
	}
}

// 前缀后为空（backup/）必须 400，不能转发空模型名。
func TestGatewayEmptyModelAfterPrefix(t *testing.T) {
	app, _, _, cleanup := newMultiUpstreamApp(t, true)
	defer cleanup()
	app.Cfg.AllowedModels = []string{"backup/"}

	k := mustCreateKey(t, app, "backup/")
	rec := callV1(t, app, k.Key, "/v1/chat/completions", `{"model":"backup/"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}
