package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

// TestValidateUpstreamBaseURL_Blocks 覆盖 finding 541 的四类必拒输入：
// 云元数据、内网直连 IP、非 http(s) scheme、解析到内网的域名。
func TestValidateUpstreamBaseURL_Blocks(t *testing.T) {
	g := newUpstreamGuard(ResolveUpstreamAllowedHosts(nil))
	// 模拟「域名解析到内网 IP」：evil.example → 10.0.0.5（RFC1918）。
	g.lookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
		if host == "evil.example" {
			return []netip.Addr{netip.MustParseAddr("10.0.0.5")}, nil
		}
		return nil, fmt.Errorf("no such host")
	}
	cases := []struct {
		name, url string
	}{
		{"云元数据 AWS", "http://169.254.169.254/latest/meta-data/"},
		{"云元数据 阿里云", "http://100.100.100.200/latest/meta-data/"},
		{"云元数据 IPv6", "http://[fd00:ec2::254]/"},
		{"内网直连 IP", "http://10.0.0.5/v1"},
		{"内网直连 IP 2", "http://192.168.1.1:8080/v1"},
		{"非 http scheme", "file:///etc/passwd"},
		{"含 userinfo", "http://user:pass@127.0.0.1:18080/v1"},
		{"域名解析到内网", "http://evil.example/v1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := ValidateUpstreamBaseURL(c.url, g); err == nil {
				t.Fatalf("%s 应被拒绝，实际通过校验", c.url)
			}
		})
	}
	// 元数据地址写进白名单也必须拒绝。
	g2 := newUpstreamGuard([]string{"169.254.169.254", "100.100.100.200"})
	if err := ValidateUpstreamBaseURL("http://169.254.169.254/", g2); err == nil {
		t.Fatal("元数据地址即使显式加入白名单也必须拒绝")
	}
	// 默认回环（现网 http://127.0.0.1:18080/v1）必须放行，否则修复会让现网不可用。
	if err := ValidateUpstreamBaseURL("http://127.0.0.1:18080/v1", g); err != nil {
		t.Fatalf("默认回环上游应放行，实际: %v", err)
	}
	if err := ValidateUpstreamBaseURL("http://localhost:18080/v1", g); err != nil {
		t.Fatalf("localhost 上游应放行，实际: %v", err)
	}
	if err := ValidateUpstreamBaseURL("", g); err != nil {
		t.Fatalf("空上游表示未配置，应放行，实际: %v", err)
	}
}

// TestPutSettingsRejectsSSRF 是意图要求的接口级单测：
// POST /api/admin/upstreams 携带非法 base_url 必须 400，且不落盘。
func TestPutSettingsRejectsSSRF(t *testing.T) {
	app, srv := newAdminApp(t)
	defer srv.Close()
	before := len(app.Cfg.Upstreams)
	tok := adminToken(t, srv)

	bad := []string{
		"http://169.254.169.254/latest/meta-data/",
		"http://10.0.0.5/v1",
		"file:///etc/passwd",
	}
	for _, u := range bad {
		body := fmt.Sprintf(`{"id":"bad","base_url":%q,"enabled":true}`, u)
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/admin/upstreams", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatalf("%s: %v", u, err)
		}
		var out map[string]string
		_ = json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s 应返回 400，实际 %d", u, resp.StatusCode)
		}
		if len(app.Cfg.Upstreams) != before {
			t.Fatalf("%s 被拒绝后仍写入了上游列表: %+v", u, app.Cfg.Upstreams)
		}
	}

	// 未在白名单的内网域名（解析到内网 IP）同样 400。
	g := newUpstreamGuard(ResolveUpstreamAllowedHosts(nil))
	g.lookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
		if host == "internal.evil.example" {
			return []netip.Addr{netip.MustParseAddr("10.1.2.3")}, nil
		}
		return nil, fmt.Errorf("no such host")
	}
	app.Guard = g
	body := `{"id":"bad2","base_url":"http://internal.evil.example/v1","enabled":true}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/admin/upstreams", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("内网域名应 400，实际 %d", resp.StatusCode)
	}
	if len(app.Cfg.Upstreams) != before {
		t.Fatalf("内网域名被拒后仍写入了上游列表: %+v", app.Cfg.Upstreams)
	}
}

// TestPutSettingsAcceptsLoopbackUpstream 验证修复不破坏现网形态：
// POST http://127.0.0.1:<port>/v1 应 200，且后续 /v1/models 仍可用。
func TestPutSettingsAcceptsLoopbackUpstream(t *testing.T) {
	up := httptest.NewServer(&upstreamHandler{stream: false, usage: `"prompt_tokens":1,"completion_tokens":1,"total_tokens":2`})
	defer up.Close()

	app, _ := newAdminApp(t)
	// 让 App 的上游客户端也走 ssrfguard（与生产 main.go 接线一致）。
	app.Guard = newUpstreamGuard(ResolveUpstreamAllowedHosts(nil))
	app.Client = newUpstreamClient(app.Guard)
	// 用完整 mux（含 /v1/）起服务，验证修复后业务链路仍然可用。
	srv := httptest.NewServer(buildMux(app))
	defer srv.Close()
	tok := adminToken(t, srv)

	body := fmt.Sprintf(`{"id":"local","base_url":%q,"enabled":true}`, up.URL+"/v1")
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/admin/upstreams", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("合法回环上游应 200，实际 %d", resp.StatusCode)
	}
	added, ok := FindUpstream(app.Cfg.Upstreams, "local")
	if !ok || added.BaseURL != up.URL+"/v1" {
		t.Fatalf("配置未生效: %+v", app.Cfg.Upstreams)
	}

	// /v1/models 由本地按 Key 应答（finding 546），应仍然 200。
	k, err := app.Store.CreateKey("glm-4", 1, 10000, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	req2, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/models", nil)
	req2.Header.Set("Authorization", "Bearer "+k.Key)
	resp2, err := srv.Client().Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("/v1/models 应 200，实际 %d", resp2.StatusCode)
	}

	// 通过受保护 client 真正打一次上游（证明回环上游在防护下仍可用）。
	models, err := FetchModels(added.BaseURL, "", app.Client)
	if err != nil {
		t.Fatalf("受保护 client 访问白名单回环上游失败: %v", err)
	}
	_ = models
}
// TestGuardedClientBlocksDial 验证第二道闸门：即使绕过写入校验（例如配置文件被
// 直接改成内网地址、或 DNS Rebinding），真实拨号也会被 DialContext 拒绝。
func TestGuardedClientBlocksDial(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("secret"))
	}))
	defer up.Close()

	// 白名单里没有 127.0.0.1 → 连接期必须拒绝。
	g := newUpstreamGuard([]string{"upstream.example.com"})
	client := newUpstreamClient(g)
	_, err := client.Get(up.URL + "/v1/models")
	if err == nil {
		t.Fatal("未白名单的回环地址应在拨号阶段被拒绝")
	}
	if !strings.Contains(err.Error(), "受限制") {
		t.Fatalf("错误信息不符: %v", err)
	}

	// 元数据地址在拨号阶段也必须拒绝（白名单里写了也一样）。
	g2 := newUpstreamGuard([]string{"169.254.169.254"})
	if _, err := g2.DialContext(context.Background(), "tcp", "169.254.169.254:80"); err == nil {
		t.Fatal("元数据地址应在 dial 阶段被拒绝")
	}

	// 白名单内的回环则允许连接。
	g3 := newUpstreamGuard(ResolveUpstreamAllowedHosts(nil))
	c3 := newUpstreamClient(g3)
	resp, err := c3.Get(up.URL + "/v1/models")
	if err != nil {
		t.Fatalf("白名单回环应可连接: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	resp.Body.Close()
}

// TestGuardedClientRedirectPolicy 验证重定向闸门：跨主机剥离 Authorization；
// 指向未白名单主机的重定向必须被拦截。
func TestGuardedClientRedirectPolicy(t *testing.T) {
	var seenAuth string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte("ok"))
	}))
	defer target.Close()

	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redir.Close()

	g := newUpstreamGuard(ResolveUpstreamAllowedHosts(nil))
	client := newUpstreamClient(g)
	req, _ := http.NewRequest(http.MethodGet, redir.URL, nil)
	req.Header.Set("Authorization", "Bearer sk-secret")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("同白名单内重定向应成功: %v", err)
	}
	resp.Body.Close()
	if seenAuth != "" {
		t.Fatalf("跨主机重定向后 Authorization 应被剥离，实际 %q", seenAuth)
	}

	// 白名单只放行回环：用「未在白名单里的域名」作为重定向目标无法在此环境构造 DNS，
	// 改为直接构造一个白名单为空、且上游客户端不允许任何主机的 guard，
	// 验证重定向的每一跳都会走 ValidateUpstreamBaseURL。
	gEmpty := newUpstreamGuard(nil)
	clientEmpty := newUpstreamClient(gEmpty)
	resp2, err := clientEmpty.Get(redir.URL)
	if err == nil {
		resp2.Body.Close()
		t.Fatal("白名单为空时任何上游（含重定向目标）都应被拒绝")
	}
	if !strings.Contains(err.Error(), "受限制") && !strings.Contains(err.Error(), "解析") {
		t.Fatalf("错误信息不符: %v", err)
	}
}
// TestRedirectMaxHops 直接构造 6 跳重定向链，第 6 跳应报「重定向次数过多」。
func TestRedirectMaxHops(t *testing.T) {
	var servers []*httptest.Server
	var next string
	for i := 0; i < 6; i++ {
		n := next
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, n, http.StatusFound)
		}))
		servers = append(servers, s)
		next = s.URL
	}
	defer func() {
		for _, s := range servers {
			s.Close()
		}
	}()
	g := newUpstreamGuard(ResolveUpstreamAllowedHosts(nil))
	client := newUpstreamClient(g)
	_, err := client.Get(next)
	if err == nil {
		t.Fatal("超过 5 跳重定向应被拒绝")
	}
	if !strings.Contains(err.Error(), "重定向次数过多") {
		t.Fatalf("错误信息不符: %v", err)
	}
}

// TestResolveAllowedHostsFromEnv 验证环境变量可下发显式放行的上游主机。
func TestResolveAllowedHostsFromEnv(t *testing.T) {
	t.Setenv(upstreamAllowedHostsEnv, " upstream.internal.example , 10.20.30.40 ")
	got := ResolveUpstreamAllowedHosts([]string{"api.internal.example"})
	joined := strings.Join(got, ",")
	for _, want := range []string{"127.0.0.1", "localhost", "::1", "upstream.internal.example", "10.20.30.40", "api.internal.example"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("白名单缺少 %q: %v", want, got)
		}
	}
	// 环境变量放行的内网主机应能通过校验（部署方显式授权）。
	g := newUpstreamGuard(got)
	if err := ValidateUpstreamBaseURL("http://10.20.30.40/v1", g); err != nil {
		t.Fatalf("环境变量白名单内的主机应放行: %v", err)
	}
}



