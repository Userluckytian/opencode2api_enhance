package manager

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestFetchGatewayModelsKeepsNonFreeSuffix 验证：fetchGatewayModels 原样返回
// /v1/models 响应的全部 id——不做 isFreeModelID 后缀二次过滤。
// 回归 2026-08-20 问题#6：自定义/插件供应商模型（如 myglm/glm-4.7、
// loomy/qwen3.8-max）名字不带 -free，旧实现按后缀过滤会误杀，导致实例池
// 「网关可用免费模型」与 auto 模型列表缺失这些模型。
func TestFetchGatewayModelsKeepsNonFreeSuffix(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			t.Fatal("expected Authorization header")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{
				{"id": "deepseek-v4-flash-free"},   // opencode 免费（-free 后缀）
				{"id": "myglm/glm-4.7"},            // 自定义供应商（无 -free）
				{"id": "loomy/qwen3.8-max"},        // 插件式供应商（无 -free）
				{"id": "auto"},                     // 虚拟模型
			},
		})
	}))
	defer upstream.Close()

	port := uint16(upstream.Listener.Addr().(*net.TCPAddr).Port)
	got, err := fetchGatewayModels(port, "sk-test")
	if err != nil {
		t.Fatalf("fetchGatewayModels: %v", err)
	}
	want := []string{"deepseek-v4-flash-free", "myglm/glm-4.7", "loomy/qwen3.8-max", "auto"}
	if len(got) != len(want) {
		t.Fatalf("got %d models, want %d: %v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("models[%d] = %q, want %q (full: %v)", i, got[i], w, got)
		}
	}
}
