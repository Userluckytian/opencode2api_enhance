package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// AI 问答只读数据工具（2026-10-28）的测试。
// 重点：工具只读、参数校验、聚合结果正确、tool calling 循环能收敛。

func newAskToolApp(t *testing.T) *App {
	t.Helper()
	app := newTestApp(t)
	app.Logger = NewLogger(t.TempDir())
	app.Audit = NewAuditLog(t.TempDir())
	app.Ask = NewAskMeter(t.TempDir())
	return app
}

// 工具清单必须包含数据源说明书，否则模型不知道该查什么。
func TestAskToolsIncludesDataSourcesDoc(t *testing.T) {
	specs := askTools()
	names := map[string]bool{}
	for _, s := range specs {
		names[s.Name] = true
		if s.Description == "" {
			t.Fatalf("工具 %s 缺少描述", s.Name)
		}
	}
	for _, want := range []string{
		"list_data_sources", "query_call_logs", "aggregate_call_logs",
		"query_audit_logs", "query_ask_logs", "list_keys",
		"list_ip_usage", "get_ip_policies", "get_config_summary", "get_claim_policy",
	} {
		if !names[want] {
			t.Fatalf("缺少工具 %s", want)
		}
	}

	doc := askDataSourcesDoc("/tmp/x")
	for _, must := range []string{"log-YYYY-MM-DD.jsonl", "audit-", "ask-", "keys.json", "ip_usage.json"} {
		if !strings.Contains(doc, must) {
			t.Fatalf("数据源说明书缺少 %q", must)
		}
	}
}

// 日期参数：默认今天，非法格式要报错（防止被当成任意路径）。
func TestAskDateArgValidation(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.Local)

	if d, err := askDateArg(map[string]any{}, now); err != nil || d != "2026-10-28" {
		t.Fatalf("默认日期 = %q, err=%v", d, err)
	}
	if d, err := askDateArg(map[string]any{"date": "2026-01-05"}, now); err != nil || d != "2026-01-05" {
		t.Fatalf("显式日期 = %q, err=%v", d, err)
	}
	for _, bad := range []string{"../../etc/passwd", "2026-13-01", "2026/01/05", "abc", "2026-1-5"} {
		if _, err := askDateArg(map[string]any{"date": bad}, now); err == nil {
			t.Fatalf("非法日期 %q 应报错", bad)
		}
	}
}

// limit 参数有默认值与上限。
func TestAskLimitArg(t *testing.T) {
	if n := askLimitArg(map[string]any{}, 50, 500); n != 50 {
		t.Fatalf("默认 limit = %d", n)
	}
	if n := askLimitArg(map[string]any{"limit": float64(10)}, 50, 500); n != 10 {
		t.Fatalf("显式 limit = %d", n)
	}
	if n := askLimitArg(map[string]any{"limit": float64(99999)}, 50, 500); n != 500 {
		t.Fatalf("超上限应钳制到 500, got %d", n)
	}
	if n := askLimitArg(map[string]any{"limit": float64(-1)}, 50, 500); n != 50 {
		t.Fatalf("负值应回退默认, got %d", n)
	}
}

// 聚合：状态码分布 + errors_only 过滤。
func TestToolAggregateCallLogs(t *testing.T) {
	app := newAskToolApp(t)
	date := time.Now().Format("2006-01-02")
	entries := []LogEntry{
		{Time: date + "T10:00:00+08:00", IP: "1.1.1.1", KeyID: "k1", Model: "m", Status: 200, Total: 10},
		{Time: date + "T10:01:00+08:00", IP: "1.1.1.1", KeyID: "k1", Model: "m", Status: 429, Total: 0},
		{Time: date + "T10:02:00+08:00", IP: "2.2.2.2", KeyID: "k2", Model: "m", Status: 429, Total: 0},
		{Time: date + "T11:00:00+08:00", IP: "2.2.2.2", KeyID: "k2", Model: "m", Status: 400, Total: 0},
	}
	for _, e := range entries {
		if err := app.Logger.Write(e); err != nil {
			t.Fatal(err)
		}
	}

	now := time.Now()
	// 状态码分布（全部）
	out, err := app.runAskTool("aggregate_call_logs", map[string]any{"group_by": "status"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "429") || !strings.Contains(out, "2 条") {
		t.Fatalf("状态码聚合结果异常: %s", out)
	}

	// errors_only：只剩 429×2 + 400×1 = 3 条
	out, err = app.runAskTool("aggregate_call_logs", map[string]any{"group_by": "status", "errors_only": true}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "共 3 条") {
		t.Fatalf("errors_only 应只剩 3 条: %s", out)
	}

	// 按 IP
	out, err = app.runAskTool("aggregate_call_logs", map[string]any{"group_by": "ip"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "1.1.1.1") || !strings.Contains(out, "2.2.2.2") {
		t.Fatalf("IP 聚合缺少来源: %s", out)
	}

	// 非法 group_by
	if _, err := app.runAskTool("aggregate_call_logs", map[string]any{"group_by": "password"}, now); err == nil {
		t.Fatal("非法 group_by 应报错")
	}
}

// 明细查询：过滤条件生效。
func TestToolQueryCallLogs(t *testing.T) {
	app := newAskToolApp(t)
	date := time.Now().Format("2006-01-02")
	app.Logger.Write(LogEntry{Time: date + "T10:00:00+08:00", IP: "1.1.1.1", KeyID: "k1", Model: "a", Status: 200})
	app.Logger.Write(LogEntry{Time: date + "T10:01:00+08:00", IP: "2.2.2.2", KeyID: "k2", Model: "b", Status: 429, Error: "rate limit"})

	now := time.Now()
	out, err := app.runAskTool("query_call_logs", map[string]any{"errors_only": true}, now)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "1.1.1.1") {
		t.Fatalf("errors_only 不应包含 200 记录: %s", out)
	}
	if !strings.Contains(out, "2.2.2.2") || !strings.Contains(out, "rate limit") {
		t.Fatalf("应包含错误记录及其 error 字段: %s", out)
	}

	// 按 IP 过滤
	out, err = app.runAskTool("query_call_logs", map[string]any{"ip": "1.1.1.1"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "1.1.1.1") || strings.Contains(out, "2.2.2.2") {
		t.Fatalf("IP 过滤异常: %s", out)
	}
}

// 未知工具名要报错（不能静默成功）。
func TestRunAskToolUnknown(t *testing.T) {
	app := newAskToolApp(t)
	if _, err := app.runAskTool("rm_rf", map[string]any{}, time.Now()); err == nil {
		t.Fatal("未知工具应报错")
	}
}

// 配置摘要不得泄露明文密钥。
func TestToolGetConfigSummaryMasksKeys(t *testing.T) {
	app := newAskToolApp(t)
	app.Cfg.Upstreams = []Upstream{
		{ID: "u1", Name: "上游", BaseURL: "http://1.2.3.4/v1", APIKey: "sk-supersecret9999", Enabled: true},
	}
	app.Cfg.DefaultUpstreamID = "u1"
	out, err := app.runAskTool("get_config_summary", map[string]any{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "sk-supersecret9999") {
		t.Fatalf("配置摘要泄露了明文密钥: %s", out)
	}
	if !strings.Contains(out, "****9999") {
		t.Fatalf("应返回脱敏密钥: %s", out)
	}
}

// tool calling 循环：模型先要工具，拿到结果后给出最终回答。
func TestHandleAskToolCallingLoop(t *testing.T) {
	var mu sync.Mutex
	round := 0
	var seenTools bool
	var seenToolResult bool

	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []map[string]any `json:"messages"`
			Tools    []any            `json:"tools"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		round++
		cur := round
		if len(req.Tools) > 0 {
			seenTools = true
		}
		for _, m := range req.Messages {
			if m["role"] == "tool" {
				seenToolResult = true
			}
		}
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if cur == 1 {
			// 第一轮：要求调用工具
			w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"aggregate_call_logs","arguments":"{\"group_by\":\"status\"}"}}]}}],"usage":{"prompt_tokens":50,"completion_tokens":10,"total_tokens":60}}`))
			return
		}
		// 第二轮：给出最终回答
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"错误主要是 429 限流。"}}],"usage":{"prompt_tokens":80,"completion_tokens":20,"total_tokens":100}}`))
	}))
	defer up.Close()

	app := newAskToolApp(t)
	app.Cfg.Upstreams = []Upstream{{ID: "m", BaseURL: up.URL + "/v1", Enabled: true}}
	app.Cfg.DefaultUpstreamID = "m"
	app.Client = up.Client()

	req := httptest.NewRequest(http.MethodPost, "/api/admin/ask",
		strings.NewReader(`{"question":"错误来自哪里？","model":"any"}`))
	rec := httptest.NewRecorder()
	app.handleAsk(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Answer    string   `json:"answer"`
		ToolCalls []string `json:"tool_calls"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Answer, "429") {
		t.Fatalf("最终回答异常: %q", out.Answer)
	}
	if len(out.ToolCalls) != 1 || !strings.Contains(out.ToolCalls[0], "aggregate_call_logs") {
		t.Fatalf("tool_calls 记录异常: %v", out.ToolCalls)
	}
	mu.Lock()
	defer mu.Unlock()
	if !seenTools {
		t.Fatal("请求未携带 tools 字段")
	}
	if !seenToolResult {
		t.Fatal("第二轮未把工具结果回传给模型")
	}
	if round != 2 {
		t.Fatalf("应恰好 2 轮上游请求, got %d", round)
	}
}

// 工具轮次用尽时必须返回明确错误，而不是空回答。
func TestHandleAskToolRoundsExhausted(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// 永远要求调用工具 → 循环无法收敛
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"c","type":"function","function":{"name":"list_keys","arguments":"{}"}}]}}]}`))
	}))
	defer up.Close()

	app := newAskToolApp(t)
	app.Cfg.Upstreams = []Upstream{{ID: "m", BaseURL: up.URL + "/v1", Enabled: true}}
	app.Cfg.DefaultUpstreamID = "m"
	app.Client = up.Client()

	req := httptest.NewRequest(http.MethodPost, "/api/admin/ask",
		strings.NewReader(`{"question":"q","model":"any"}`))
	rec := httptest.NewRecorder()
	app.handleAsk(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("轮次用尽应返回 502, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "分析未完成") {
		t.Fatalf("错误信息应说明原因: %s", rec.Body.String())
	}
}
