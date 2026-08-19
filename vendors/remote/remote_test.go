// vendors/remote 单元测试：全部基于 httptest / 关闭端口，不触网、不占固定端口。
// mock 即"插件子进程"（127.0.0.1 上的 OpenAI 兼容子集：/v1/models + /v1/chat/completions）。
package remote

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/6Kmfi6HP/opencode2api/core/contract"
)

// newTestVendor 构造指向 mock 子进程的桥接厂商（令牌固定，便于断言请求头）。
func newTestVendor(t *testing.T, baseURL string) *Vendor {
	t.Helper()
	v, err := New(Config{ID: "loomy", Name: "LOOMy", BaseURL: baseURL, AuthToken: "tok-1"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return v
}

// childModels mock 子进程：校验路径与鉴权头，回 2 个模型。
func childModels(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("path = %s, want /v1/models", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok-1" {
			t.Errorf("Authorization = %q, want Bearer tok-1", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "loomy-pro"}, {"id": "loomy-free"}}})
	}))
	return srv
}

func TestNewValidation(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("want error for missing id")
	}
	if _, err := New(Config{ID: "x"}); err == nil {
		t.Fatal("want error for missing base_url")
	}
	if _, err := New(Config{ID: "x", BaseURL: "https://u/"}); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}

func TestListModels(t *testing.T) {
	srv := childModels(t)
	defer srv.Close()
	v := newTestVendor(t, srv.URL)

	models, err := v.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("models = %d, want 2", len(models))
	}
	// 前缀 {id}/ + Provider + Free（令牌由网关持有 → 对外免费可用）。
	if models[0].ID != "loomy/loomy-pro" || models[0].Provider != "loomy" || !models[0].Free {
		t.Fatalf("model[0] = %+v", models[0])
	}
}

func TestListModelsEmptyIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()
	v := newTestVendor(t, srv.URL)

	if _, err := v.ListModels(context.Background()); err == nil {
		t.Fatal("空目录（坍缩）应按失败处理，防上游抖动清空聚合目录")
	}
}

func TestListModelsAuthFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"bad token"}}`))
	}))
	defer srv.Close()
	v := newTestVendor(t, srv.URL)

	if _, err := v.ListModels(context.Background()); err == nil {
		t.Fatal("401 应返回错误")
	}
	h := v.Health()
	if h.Available || !strings.Contains(h.LastError, "401") {
		t.Fatalf("health = %+v, want Available=false + LastError 含 401", h)
	}
}

func TestChatRawBodyPassthrough(t *testing.T) {
	var gotBody map[string]any
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"id":"c1","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"hi"}}]}`))
	}))
	defer srv.Close()
	v := newTestVendor(t, srv.URL)

	raw := `{"model":"loomy/loomy-pro","messages":[{"role":"user","content":"hello"}],"temperature":0.7}`
	msg := &contract.Message{Model: "loomy/loomy-pro", Extra: map[string]any{keyRawBody: []byte(raw)}}
	reply, err := v.Chat(context.Background(), msg)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if reply.Status != http.StatusOK {
		t.Fatalf("status = %d", reply.Status)
	}
	if gotAuth != "Bearer tok-1" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	// 子进程收到的 model 必须已剥掉本源前缀；stream=false；其余字段透传。
	if m, _ := gotBody["model"].(string); m != "loomy-pro" {
		t.Fatalf("upstream model = %q, want loomy-pro", m)
	}
	if temp, _ := gotBody["temperature"].(float64); temp != 0.7 {
		t.Fatalf("temperature passthrough = %v", gotBody["temperature"])
	}
	if s, _ := gotBody["stream"].(bool); s {
		t.Fatal("non-stream chat must set stream=false")
	}
	if !strings.Contains(string(reply.Body), "chat.completion") {
		t.Fatalf("reply body passthrough: %s", reply.Body)
	}
}

func TestChatErrorPassthrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"bad key"}}`))
	}))
	defer srv.Close()
	v := newTestVendor(t, srv.URL)

	msg := &contract.Message{Model: "loomy/x", Extra: map[string]any{keyRawBody: []byte(`{"model":"loomy/x","messages":[]}`)}}
	reply, err := v.Chat(context.Background(), msg)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if reply.Status != http.StatusUnauthorized || !strings.Contains(string(reply.Body), "bad key") {
		t.Fatalf("error passthrough: status=%d body=%s", reply.Status, reply.Body)
	}
	if h := v.Health(); h.Available {
		t.Fatalf("401 后 health 应不可用: %+v", h)
	}
}

func TestChatStreamSSE(t *testing.T) {
	var gotAccept string
	var gotStream bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAccept = r.Header.Get("Accept")
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotStream, _ = body["stream"].(bool)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer srv.Close()
	v := newTestVendor(t, srv.URL)

	msg := &contract.Message{Model: "loomy/x", Stream: true, Extra: map[string]any{keyRawBody: []byte(`{"model":"loomy/x","messages":[]}`)}}
	st, err := v.ChatStream(context.Background(), msg)
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	defer st.Close()
	all, _ := io.ReadAll(st)
	if gotAccept != "text/event-stream" {
		t.Errorf("Accept = %q, want text/event-stream", gotAccept)
	}
	if !gotStream {
		t.Error("stream body 必须为 true")
	}
	if !strings.Contains(string(all), "[DONE]") || !strings.Contains(string(all), `"content":"a"`) {
		t.Fatalf("sse passthrough: %q", all)
	}
}

func TestConnectionFailure(t *testing.T) {
	// 关闭端口模拟子进程不可达（httptest server 关闭即端口关闭）。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()
	v := newTestVendor(t, url)

	if _, err := v.ListModels(context.Background()); err == nil {
		t.Fatal("连接失败应返回错误（上层 failover 接管）")
	}
	if h := v.Health(); h.Available || h.LastError == "" {
		t.Fatalf("连接失败后 health = %+v, want Available=false + LastError", h)
	}

	msg := &contract.Message{Model: "loomy/x", Extra: map[string]any{keyRawBody: []byte(`{"model":"loomy/x","messages":[]}`)}}
	if _, err := v.Chat(context.Background(), msg); err == nil {
		t.Fatal("Chat 连接失败应返回错误")
	}
}

func TestHealthAfterSuccess(t *testing.T) {
	srv := childModels(t)
	defer srv.Close()
	v := newTestVendor(t, srv.URL)

	// 构造时默认可用（子进程就绪后注册）。
	if h := v.Health(); !h.Available {
		t.Fatalf("初始 health = %+v, want Available=true", h)
	}
	if _, err := v.ListModels(context.Background()); err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	h := v.Health()
	if !h.Available || h.LastError != "" || h.LastSuccess == "" {
		t.Fatalf("成功后 health = %+v", h)
	}
}

func TestBuildBodyFallbackFromMessages(t *testing.T) {
	v := newTestVendor(t, "https://127.0.0.1:1")
	body, err := v.buildBody(&contract.Message{
		Model:    "loomy/x",
		Messages: []contract.Msg{{Role: "user", Content: "hi"}},
	}, true)
	if err != nil {
		t.Fatalf("buildBody: %v", err)
	}
	var m map[string]any
	if json.Unmarshal(body, &m) != nil {
		t.Fatalf("bad json: %s", body)
	}
	if m["model"] != "x" || m["stream"] != true {
		t.Fatalf("fallback body = %s", body)
	}
	msgs, _ := m["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("messages = %v", m["messages"])
	}
}

// ---------------------------------------------------------------------------
// 注册表
// ---------------------------------------------------------------------------

func TestRegistryFactory(t *testing.T) {
	if !contract.HasType("remote") {
		t.Fatal("remote 类型应已注册")
	}
	v, err := contract.Create("remote", contract.ProviderSpec{
		Type: "remote", ID: "reg1", Name: "R",
		Params: map[string]any{"base_url": "https://127.0.0.1:1", "auth_token": "t"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if v.ID() != "reg1" || v.Auth(nil) != "Bearer t" {
		t.Fatalf("vendor = %+v auth=%q", v, v.Auth(nil))
	}
	// 缺 base_url → 构造失败（自动注册路径被 main 侧排除，这里直接验证工厂语义）。
	if _, err := contract.Create("remote", contract.ProviderSpec{Type: "remote", ID: "x"}); err == nil {
		t.Fatal("缺 base_url 应构造失败")
	}
}
