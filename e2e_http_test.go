package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/6Kmfi6HP/opencode2api/core/manager"
)

// TestE2EAdminHTTP 大步2 自动验收：登录会话 → 配置 → 节点 → 端口 → 实例增删 → 网关 → 统计/日志 → 清理。
// 用 Go http.Client（cookie jar 可靠），驱动真实 registerHTTPRoutes 组装的服务。
func TestE2EAdminHTTP(t *testing.T) {
	// 网关端口固定到测试专用端口：ClearCallLog/ResetStats 按 managerGatewayPort
	// 探测/复位，避免测试探测到本机真实生产网关（40080 槽位）而误发 DELETE——
	// 环境隔离纪律（此前 ResetStats 硬编码 40080，本机跑 e2e 会误清生产网关统计）。
	t.Setenv("OPCODE2API_GATEWAY_PORT", strconv.FormatUint(uint64(e2eFreePort(t)), 10))
	m := manager.New(t.TempDir())
	m.SetDeps(manager.NewRealRunner(), manager.NewGateway(m, 0), nil)

	// 打开鉴权（否则 requireAuth 直接放行，无法验证登录闭环）
	oldPwd := adminPassword
	adminPassword = "e2e-test-pass"
	t.Cleanup(func() { adminPassword = oldPwd })

	mux := http.NewServeMux()
	registerHTTPRoutes(mux, m, nil) // 插件路由由 pluginprovider 包自身单测覆盖，此处不装配
	srv := httptest.NewServer(mux)
	defer srv.Close()

	jar, _ := cookiejar.New(nil)
	client := &http.Client{
		Jar: jar,
		// 不自动跟随重定向：便于断言 302
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	var fails int
	check := func(name string, ok bool, format string, args ...any) {
		detail := fmt.Sprintf(format, args...)
		if ok {
			t.Logf("[PASS] "+name+"  %s", detail)
		} else {
			fails++
			t.Errorf("[FAIL] "+name+"  %s", detail)
		}
	}
	do := func(method, path string, body []byte, hdr http.Header) (int, string) {
		var rdr io.Reader
		if body != nil {
			rdr = strings.NewReader(string(body))
		}
		req, _ := http.NewRequest(method, srv.URL+path, rdr)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		for k, vs := range hdr {
			for _, v := range vs {
				req.Header.Set(k, v)
			}
		}
		resp, err := client.Do(req)
		if err != nil {
			return 0, err.Error()
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	post := func(path string, v any, hdr http.Header) (int, string) {
		b, _ := json.Marshal(v)
		return do("POST", path, b, hdr)
	}
	get := func(path string, hdr http.Header) (int, string) {
		return do("GET", path, nil, hdr)
	}

	// 0) 健康
	if code, _ := get("/health", nil); code != 200 {
		check("health", false, "code=%d", code)
	}

	// 1) 未登录 → 302（鉴权拦截）
	code, _ := get("/api/admin/instances", nil)
	check("auth gate no cookie", code == 302, "code=%d", code)

	// 2) 登录（表单）→ 302 + 会话 cookie
	form := url.Values{}
	form.Set("password", "e2e-test-pass")
	resp, err := client.PostForm(srv.URL+"/login", form)
	if err != nil {
		t.Fatalf("login req: %v", err)
	}
	resp.Body.Close()
	check("login", resp.StatusCode == 302, "code=%d", resp.StatusCode)

	// 3) 配置 get/set 回环
	code, body := get("/api/admin/config", nil)
	var cfg map[string]any
	_ = json.Unmarshal([]byte(body), &cfg)
	check("config get", code == 200 && cfg["default_password"] != nil, "code=%d", code)
	code, _ = post("/api/admin/config/set", map[string]any{"key": "show_node_prefix", "value": "true"}, nil)
	check("config set", code == 200, "code=%d", code)
	_, body = get("/api/admin/config", nil)
	_ = json.Unmarshal([]byte(body), &cfg)
	check("config set persisted", cfg["show_node_prefix"] == true, "show_node_prefix=%v", cfg["show_node_prefix"])

	// 4) 节点（无 clash → []）
	_, body = get("/api/admin/nodes", nil)
	check("nodes list", strings.TrimSpace(body) == "[]", "%q", body)

	// 5) 端口
	_, body = get("/api/admin/port/suggest", nil)
	var sug int
	_ = json.Unmarshal([]byte(body), &sug)
	check("port suggest", sug >= 18100+100 && sug < 18100+100+2000, "suggest=%d", sug)
	code, body = get("/api/admin/port/check?port=30123", nil)
	var pc map[string]any
	_ = json.Unmarshal([]byte(body), &pc)
	check("port check", code == 200 && pc["available"] == true, "available=%v", pc["available"])

	// 6) 实例 增→列→删
	code, _ = post("/api/admin/instances/add", map[string]any{"name": "e2e-1", "port": 30123, "node": "dummy", "password": "sk-e2e"}, nil)
	check("instance add", code == 200, "code=%d", code)
	code, body = get("/api/admin/instances", nil)
	var list []map[string]any
	_ = json.Unmarshal([]byte(body), &list)
	check("instance list", code == 200 && len(list) == 1 && list[0]["name"] == "e2e-1", "code=%d count=%d", code, len(list))
	code, _ = post("/api/admin/instances/remove", map[string]any{"name": "e2e-1"}, nil)
	check("instance remove", code == 200, "code=%d", code)
	_, body = get("/api/admin/instances", nil)
	_ = json.Unmarshal([]byte(body), &list)
	check("instance list empty", len(list) == 0, "count=%d", len(list))

	// 7) 网关状态
	code, body = get("/api/admin/gateway/status", nil)
	var gw map[string]any
	_ = json.Unmarshal([]byte(body), &gw)
	check("gateway status", code == 200 && gw["port"] != nil && gw["route_mode"] != nil, "port=%v mode=%v", gw["port"], gw["route_mode"])

	// 8) 统计 / 日志 / 自启 / 二进制
	code, _ = get("/api/admin/stats", nil)
	check("stats", code == 200, "code=%d", code)
	code, _ = get("/api/admin/call-log", nil)
	check("call-log", code == 200, "code=%d", code)
	code, _ = post("/api/admin/call-log/clear", map[string]any{}, nil)
	check("call-log clear", code == 200, "code=%d", code)
	code, body = get("/api/admin/autostart", nil)
	var auto map[string]any
	_ = json.Unmarshal([]byte(body), &auto)
	check("autostart get", code == 200 && auto["enabled"] == false, "enabled=%v", auto["enabled"])
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer e2e-test-pass")
	code, _ = get("/api/admin/binaries", hdr)
	check("binaries", code == 200, "code=%d", code)
	code, _ = post("/api/admin/stats/reset", nil, hdr)
	check("stats reset", code == 200, "code=%d", code)

	// 9) 非法清理级别应被拒
	code, _ = post("/api/admin/data/clean", map[string]any{"level": 9}, nil)
	check("data clean invalid level", code == 500, "code=%d", code)

	if fails > 0 {
		t.Fatalf("E2E: %d checks failed", fails)
	}
}

// e2eFreePort 取一个当前可用的本机端口（bind :0 后立即释放）。
func e2eFreePort(t *testing.T) uint16 {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bind free port: %v", err)
	}
	port := uint16(ln.Addr().(*net.TCPAddr).Port)
	_ = ln.Close()
	return port
}

// 复用 strconv，避免未使用告警（实际各步均已使用）。
var _ = strconv.Itoa
