package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newE2EApp 构造与生产一致的 App（真实路由表 buildMux）。
func newE2EApp(t *testing.T) (*App, *httptest.Server) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.AdminPasswordHash = HashPassword("admin123")
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	policies, err := NewIPPolicyStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	tracker, err := NewIPTracker(dir, policies)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{
		Cfg: cfg, Store: store, Rl: NewRateLimiter(30), Sessions: NewSessionStore(),
		Logger: NewLogger(dir), Client: http.DefaultClient, ConfigPath: dir,
		IPs: tracker, Login: NewLoginLimiter(5, 5*time.Minute, time.Minute),
	}
	return app, httptest.NewServer(buildMux(app))
}

func e2eLogin(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	resp, err := srv.Client().Post(srv.URL+"/api/admin/login", "application/json",
		bytes.NewBufferString(`{"password":"admin123"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Token == "" {
		t.Fatal("未返回 token")
	}
	return body.Token
}

func e2eGet(t *testing.T, srv *httptest.Server, tok, path string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

func TestE2EAdminPagesExposeNewViews(t *testing.T) {
	_, srv := newE2EApp(t)
	defer srv.Close()

	code, body := e2eGet(t, srv, "", "/admin.html")
	if code != http.StatusOK {
		t.Fatalf("admin.html code=%d", code)
	}
	for _, want := range []string{"IP 管理", "AI 问答", "调用日志", "loginForm"} {
		if !strings.Contains(body, want) {
			t.Fatalf("admin.html 缺少 %q", want)
		}
	}
	// 没有密码的人也要能从登录页退回领取页。
	for _, want := range []string{"返回领取页", "data-claim-link", "没有密码？联系管理员获取"} {
		if !strings.Contains(body, want) {
			t.Fatalf("admin.html 登录页缺少 %q", want)
		}
	}

	code, body = e2eGet(t, srv, "", "/admin.js")
	if code != http.StatusOK {
		t.Fatalf("admin.js code=%d", code)
	}
	for _, want := range []string{"api/admin/ip-policies", "api/admin/logs/aggregate", "api/admin/ask", "api/admin/overview"} {
		if !strings.Contains(body, want) {
			t.Fatalf("admin.js 未对接 %s", want)
		}
	}
}

func TestE2EAdminAPIsThroughRealRoutes(t *testing.T) {
	_, srv := newE2EApp(t)
	defer srv.Close()
	tok := e2eLogin(t, srv)

	for _, path := range []string{
		"/api/admin/overview",
		"/api/admin/keys",
		"/api/admin/logs",
		"/api/admin/logs/aggregate",
		"/api/admin/ip-policies",
		"/api/admin/settings",
	} {
		code, body := e2eGet(t, srv, tok, path)
		if code != http.StatusOK {
			t.Fatalf("%s code=%d body=%s", path, code, body)
		}
	}

	code, _ := e2eGet(t, srv, "", "/api/admin/overview")
	if code != http.StatusUnauthorized {
		t.Fatalf("未登录访问管理接口应 401，实际 %d", code)
	}
}

func TestE2EAskRouteWired(t *testing.T) {
	_, srv := newE2EApp(t)
	defer srv.Close()
	tok := e2eLogin(t, srv)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/admin/ask",
		bytes.NewBufferString(`{"question":"今天哪个 IP 用得最多？"}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	// 未配置可用模型时必须给出明确错误，而不是 404/500。
	if resp.StatusCode != http.StatusBadRequest {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("ask code=%d 期望 400，body=%s", resp.StatusCode, raw)
	}
}

// 领取页「留个门」：页面与脚本不得暴露 IP 策略信息。
func TestClaimPageDoesNotExposeIPPolicy(t *testing.T) {
	for _, name := range []string{"index.html", "app.js"} {
		raw, err := os.ReadFile(filepath.Join("static", name))
		if err != nil {
			t.Fatal(err)
		}
		body := string(raw)
		for _, banned := range []string{"IP 管理", "窗口额度", "输出限速", "ip-policies", "活跃时长"} {
			if strings.Contains(body, banned) {
				t.Fatalf("领取页 %s 不应包含 %q", name, banned)
			}
		}
	}
}
