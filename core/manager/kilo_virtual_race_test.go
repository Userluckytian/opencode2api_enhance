package manager

// 阶段 M / Task 2：虚拟模型三策略选路单测（全部打本地假上游）。

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newVirtualTestGW 造一个带假上游的网关，并注入一个虚拟模型。
func newVirtualTestGW(t *testing.T, vm VirtualModel, handler func(w http.ResponseWriter, r *http.Request, model string)) *kiloGateway {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			_, _ = io.WriteString(w, `{"data":[{"id":"a","isFree":true},{"id":"b","isFree":true},{"id":"c","isFree":true}]}`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var in struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(body, &in)
		handler(w, r, in.Model)
	}))
	t.Cleanup(srv.Close)
	m := New(t.TempDir())
	old := kiloUpstreamBaseOverride
	kiloUpstreamBaseOverride = strings.TrimRight(srv.URL, "/") + "/v1"
	t.Cleanup(func() { kiloUpstreamBaseOverride = old })
	g := m.KiloGateway()
	if err := g.virtual.Upsert(vm); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	return g
}

func okResponse(w http.ResponseWriter, model string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"model": model, "choices": []any{map[string]any{"message": map[string]any{"content": "OK-" + model}}},
	})
}

func TestKiloVirtualFallback(t *testing.T) {
	var mu sync.Mutex
	var tried []string
	g := newVirtualTestGW(t, VirtualModel{Name: "fb", Strategy: KiloVirtualStrategyFallback, Enabled: true,
		Models: []string{"a", "b", "c"}}, func(w http.ResponseWriter, r *http.Request, model string) {
		mu.Lock()
		tried = append(tried, model)
		mu.Unlock()
		if model == "a" {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"message":"rate limited"}}`)
			return
		}
		okResponse(w, model)
	})
	r, tag := g.selectByFallback(context.Background(), []byte(`{"messages":[]}`), []string{"a", "b", "c"})
	if r.err != nil || r.status != 200 {
		t.Fatalf("应命中 b 的 200，实际 status=%d err=%v body=%s", r.status, r.err, r.body)
	}
	if r.model != "b" {
		t.Fatalf("胜出真实 model = %q, want b", r.model)
	}
	if !strings.Contains(string(r.body), "OK-b") {
		t.Fatalf("响应体不是 b 的: %s", r.body)
	}
	_ = tag
	mu.Lock()
	defer mu.Unlock()
	if len(tried) != 2 || tried[0] != "a" || tried[1] != "b" {
		t.Fatalf("应按序试 a(429)→b(200)，实际 %v", tried)
	}
}

func TestKiloVirtualFallbackAllFailed(t *testing.T) {
	g := newVirtualTestGW(t, VirtualModel{Name: "fb", Strategy: KiloVirtualStrategyFallback, Enabled: true,
		Models: []string{"a", "b"}}, func(w http.ResponseWriter, r *http.Request, model string) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":{"message":"upstream gone"}}`)
	})
	r, tag := g.selectByFallback(context.Background(), []byte(`{"messages":[]}`), []string{"a", "b"})
	if r.status != http.StatusServiceUnavailable {
		t.Fatalf("全败应返回最后一个 503，实际 %d", r.status)
	}
	if !strings.Contains(string(r.body), "upstream gone") {
		t.Fatal("全败时错误体应原样保留，不得洗成 502")
	}
	if tag != "all-failed" {
		t.Fatalf("tag = %q", tag)
	}
}

func TestKiloVirtualRoundRobin(t *testing.T) {
	g := newVirtualTestGW(t, VirtualModel{Name: "rr", Strategy: KiloVirtualStrategyRoundRobin, Enabled: true,
		Models: []string{"a", "b", "c"}}, func(w http.ResponseWriter, r *http.Request, model string) {
		okResponse(w, model)
	})
	// 所有候选都成功 → 每次起点应轮换，胜出 model 依次 a,b,c,a
	want := []string{"a", "b", "c", "a"}
	for i, w := range want {
		r, tag := g.selectByRoundRobin(context.Background(), []byte(`{"messages":[]}`), []string{"a", "b", "c"}, "rr")
		if r.model != w {
			t.Fatalf("第 %d 次胜出 = %q, want %q (tag=%s)", i, r.model, w, tag)
		}
	}
}

func TestKiloVirtualRace(t *testing.T) {
	var fastWins atomic.Int32
	g := newVirtualTestGW(t, VirtualModel{Name: "rc", Strategy: KiloVirtualStrategyRace, Enabled: true,
		Models: []string{"a", "b", "c"}}, func(w http.ResponseWriter, r *http.Request, model string) {
		// b 最快，a/c 慢；慢请求应被 cancel 掉（context 取消会让 client.Do 提前返回）
		if model == "b" {
			fastWins.Add(1)
			okResponse(w, model)
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(2 * time.Second):
			okResponse(w, model)
		}
	})
	start := time.Now()
	r, tag := g.selectByRace(context.Background(), []byte(`{"messages":[]}`), []string{"a", "b", "c"})
	elapsed := time.Since(start)
	if r.err != nil || r.status != 200 {
		t.Fatalf("race 应有 2xx 胜出，status=%d err=%v", r.status, r.err)
	}
	if r.model != "b" {
		t.Fatalf("最快的 b 应胜出，实际 %q (tag=%s)", r.model, tag)
	}
	if elapsed > 1500*time.Millisecond {
		t.Fatalf("race 不应等慢候选，耗时 %v", elapsed)
	}
	if !strings.Contains(string(r.body), "OK-b") {
		t.Fatalf("只应回胜出者 b 的 body: %s", r.body)
	}
	if fastWins.Load() != 1 {
		t.Fatalf("b 应只被调 1 次，实际 %d", fastWins.Load())
	}
}

func TestKiloVirtualRaceAllFailed(t *testing.T) {
	g := newVirtualTestGW(t, VirtualModel{Name: "rc", Strategy: KiloVirtualStrategyRace, Enabled: true,
		Models: []string{"a", "b"}}, func(w http.ResponseWriter, r *http.Request, model string) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, `{"error":{"message":"bad"}}`)
	})
	r, tag := g.selectByRace(context.Background(), []byte(`{"messages":[]}`), []string{"a", "b"})
	if r.status != http.StatusBadGateway {
		t.Fatalf("全败应返回 502，实际 %d", r.status)
	}
	if tag != "race-all-failed" {
		t.Fatalf("tag = %q", tag)
	}
}

func TestKiloVirtualResolveAndWrite(t *testing.T) {
	g := newVirtualTestGW(t, VirtualModel{Name: "mix", Strategy: KiloVirtualStrategyFallback, Enabled: true,
		Models: []string{"a"}}, func(w http.ResponseWriter, r *http.Request, model string) {
		okResponse(w, "nvidia/real:free") // 上游静默换模型
	})
	vr, _, err := g.resolveVirtual(context.Background(), []byte(`{"model":"kilo/mix","messages":[]}`), "mix")
	if err != nil || vr == nil {
		t.Fatalf("应命中虚拟模型，err=%v vr=%v", err, vr)
	}
	if vr.model != "nvidia/real:free" {
		t.Fatalf("应记录上游真实 model，实际 %q", vr.model)
	}
	// 未命中返回 nil
	none, _, err := g.resolveVirtual(context.Background(), []byte(`{"model":"a"}`), "a")
	if err != nil || none != nil {
		t.Fatalf("真实模型不应命中虚拟层，err=%v none=%v", err, none)
	}
}
