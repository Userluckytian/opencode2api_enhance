package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// modelGuardUpstream 是一个可观测的上游：记录收到的请求，用于断言
// 「网关拒绝的请求根本没有到达上游」。
type modelGuardUpstream struct {
	hits   int64
	bodies []string
}

func (u *modelGuardUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	atomic.AddInt64(&u.hits, 1)
	buf := make([]byte, 4096)
	n, _ := r.Body.Read(buf)
	u.bodies = append(u.bodies, string(buf[:n]))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"hi"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
}

// newModelGuardApp 构造一个指向可观测上游的网关 App。
func newModelGuardApp(t *testing.T) (*App, *modelGuardUpstream) {
	t.Helper()
	up := &modelGuardUpstream{}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	cfg := DefaultConfig()
	cfg.UpstreamBaseURL = srv.URL + "/v1"
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
		Client:   srv.Client(),
	}
	return app, up
}

// callModelGuard 发起一次 /v1/chat/completions 请求。
func callModelGuard(t *testing.T, app *App, key, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.HandleV1(rec, req)
	return rec
}

// finding 546 残余缺口（fail-closed 补齐）：请求体不带 model / model 为空串 /
// model 为 null 时，网关必须自己 400 拒绝，**不得放行转发到上游**。
// 上游原文 "model is required" 只出现在上游的响应里，网关侧不得转发该原文。
func TestV1MissingModelIsRejectedByGateway(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"no_model_field", `{"messages":[{"role":"user","content":"hi"}]}`},
		{"empty_model", `{"model":"","messages":[{"role":"user","content":"hi"}]}`},
		{"null_model", `{"model":null,"messages":[{"role":"user","content":"hi"}]}`},
		{"blank_model", `{"model":"   ","messages":[{"role":"user","content":"hi"}]}`},
		{"non_string_model_number", `{"model":123,"messages":[{"role":"user","content":"hi"}]}`},
		{"non_string_model_object", `{"model":{"name":"glm-4"},"messages":[{"role":"user","content":"hi"}]}`},
		{"non_string_model_array", `{"model":["glm-4"],"messages":[{"role":"user","content":"hi"}]}`},
		{"duplicate_model_key", `{"model":"glm-4","model":"big-pickle","messages":[]}`},
		{"model_key_case_variant", `{"Model":"big-pickle","messages":[]}`},
		{"invalid_json_body", `{"model":"glm-4"`},
		{"not_json_object", `["glm-4"]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app, up := newModelGuardApp(t)
			k := mustCreateKey(t, app, "glm-4")
			rec := callModelGuard(t, app, k.Key, tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("code = %d, want 400; body=%s", rec.Code, rec.Body.String())
			}
			if n := atomic.LoadInt64(&up.hits); n != 0 {
				t.Fatalf("上游被命中 %d 次，请求本应被网关拒绝后不再转发", n)
			}
			body := rec.Body.String()
			if strings.Contains(body, "model is required") {
				t.Fatalf("响应体含上游原文, 说明错误来自上游而非网关: %s", body)
			}
			if !strings.Contains(body, "invalid request body") {
				t.Fatalf("响应体应为网关自己的 JSON 错误: %s", body)
			}
		})
	}
}

// 授权模型仍应 200 转发到上游（确认 fail-closed 没有把正常流量一起打死）。
func TestV1AllowedModelStillForwards(t *testing.T) {
	app, up := newModelGuardApp(t)
	k := mustCreateKey(t, app, "glm-4")
	rec := callModelGuard(t, app, k.Key, `{"model":"glm-4","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if n := atomic.LoadInt64(&up.hits); n != 1 {
		t.Fatalf("上游命中 %d 次, want 1", n)
	}
}

// 未授权模型仍是 403（不是 400），且不得转发上游。
func TestV1DisallowedModelIsForbidden(t *testing.T) {
	app, up := newModelGuardApp(t)
	k := mustCreateKey(t, app, "glm-4")
	rec := callModelGuard(t, app, k.Key, `{"model":"big-pickle","messages":[]}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("code = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
	if n := atomic.LoadInt64(&up.hits); n != 0 {
		t.Fatalf("上游被命中 %d 次, want 0", n)
	}
}

// model 首尾空白应被裁剪后再比对：授权模型带空格仍应放行。
func TestV1AllowedModelWithSurroundingWhitespace(t *testing.T) {
	app, up := newModelGuardApp(t)
	k := mustCreateKey(t, app, "glm-4")
	rec := callModelGuard(t, app, k.Key, `{"model":"  glm-4  ","messages":[]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if n := atomic.LoadInt64(&up.hits); n != 1 {
		t.Fatalf("上游命中 %d 次, want 1", n)
	}
}

// GET /v1/models 仍只回本地白名单（finding 546 主修复不得被本次改动回归）。
func TestV1ModelsReturnsLocalWhitelistOnly(t *testing.T) {
	app, up := newModelGuardApp(t)
	k := mustCreateKey(t, app, "glm-4")
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+k.Key)
	rec := httptest.NewRecorder()
	app.HandleV1(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Data) != 1 || out.Data[0]["id"] != "glm-4" {
		t.Fatalf("models = %v, want 仅 glm-4", out.Data)
	}
	if out.Data[0]["owned_by"] != "token-buffet" {
		t.Fatalf("owned_by = %v, want token-buffet（不得泄露上游提供方信息）", out.Data[0]["owned_by"])
	}
	if n := atomic.LoadInt64(&up.hits); n != 0 {
		t.Fatalf("上游被命中 %d 次, /v1/models 不得反代上游", n)
	}
}

// 非 GET/HEAD 的 /v1/models 必须 405，不得被反代到上游泄露全量模型列表。
func TestV1ModelsNonGETIsMethodNotAllowed(t *testing.T) {
	app, up := newModelGuardApp(t)
	k := mustCreateKey(t, app, "glm-4")
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		req := httptest.NewRequest(method, "/v1/models", strings.NewReader("{}"))
		req.Header.Set("Authorization", "Bearer "+k.Key)
		rec := httptest.NewRecorder()
		app.HandleV1(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s /v1/models code = %d, want 405; body=%s", method, rec.Code, rec.Body.String())
		}
	}
	if n := atomic.LoadInt64(&up.hits); n != 0 {
		t.Fatalf("上游被命中 %d 次, want 0", n)
	}
}

// 未绑定模型的历史 Key（k.Model 为空）退化为「必须在全局白名单内」，
// 白名单为空时一律拒绝（fail-closed）。
func TestModelAllowedForKeyUnboundKeyUsesGlobalWhitelist(t *testing.T) {
	k := Key{Model: ""}
	if modelAllowedForKey(k, []string{"glm-4"}, "glm-4") != true {
		t.Fatal("未绑定 Key 应放行白名单内的模型")
	}
	if modelAllowedForKey(k, []string{"glm-4"}, "big-pickle") != false {
		t.Fatal("未绑定 Key 不应放行白名单外的模型")
	}
	if modelAllowedForKey(k, nil, "glm-4") != false {
		t.Fatal("白名单为空时必须拒绝（fail-closed）")
	}
	if modelAllowedForKey(k, []string{"glm-4"}, "") != false {
		t.Fatal("空 model 必须拒绝（fail-closed）")
	}
}

// resolveRequestedModel 的单元级行为。
func TestResolveRequestedModel(t *testing.T) {
	if _, err := resolveRequestedModel([]byte(`{"messages":[]}`)); err == nil {
		t.Fatal("缺 model 必须报错")
	}
	if _, err := resolveRequestedModel([]byte(`{"model":null}`)); err == nil {
		t.Fatal("null model 必须报错")
	}
	if _, err := resolveRequestedModel([]byte(`{"model":""}`)); err == nil {
		t.Fatal("空 model 必须报错")
	}
	if _, err := resolveRequestedModel([]byte(`{"model":"  "}`)); err == nil {
		t.Fatal("纯空白 model 必须报错")
	}
	if _, err := resolveRequestedModel([]byte(`{"model":7}`)); err == nil {
		t.Fatal("非字符串 model 必须报错")
	}
	if _, err := resolveRequestedModel([]byte(`{"model":"a","model":"b"}`)); err == nil {
		t.Fatal("重复 model 键必须报错")
	}
	if _, err := resolveRequestedModel([]byte(`not json`)); err == nil {
		t.Fatal("非法 JSON 必须报错")
	}
	if _, err := resolveRequestedModel([]byte(`["a"]`)); err == nil {
		t.Fatal("顶层非对象必须报错")
	}
	m, err := resolveRequestedModel([]byte(`{"model":"  glm-4  "}`))
	if err != nil || m != "glm-4" {
		t.Fatalf("model = %q err = %v, want glm-4/nil", m, err)
	}
}

// 额度与限速等其它闸门仍先于模型校验生效（不因本次改动改变既有语义）：
// 额度用尽的 Key 带合法 model 也应 429。
func TestV1QuotaStillTakesPrecedence(t *testing.T) {
	app, _ := newModelGuardApp(t)
	k, err := app.Store.CreateKey("glm-4", 7, 100, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	app.Store.AddUsage(k.ID, 100)
	rec := callModelGuard(t, app, k.Key, `{"model":"glm-4","messages":[]}`)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("code = %d, want 429; body=%s", rec.Code, rec.Body.String())
	}
}
