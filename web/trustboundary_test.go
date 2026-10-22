package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

/* ------------------------------------------------------------------ */
/* finding 548：客户端可控头不得污染下发的 base_url                     */
/* ------------------------------------------------------------------ */

// 端到端：携带 X-Forwarded-Host: evil.example.com 时，
// 下发的 base_url 里绝不能出现攻击者域名。
func TestCreateKeyDoesNotLeakForwardedHost(t *testing.T) {
	app := newTestApp(t)
	app.Cfg.PublicBaseURL = "https://gw.example.com"
	req := httptest.NewRequest(http.MethodPost, "/api/keys", strings.NewReader(`{"model":"glm-4"}`))
	req.Host = "192.168.12.17:18081"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-Host", "evil.example.com")
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()
	app.HandleCreateKey(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	base := respBaseURL(t, rec.Body.Bytes())
	if strings.Contains(base, "evil.example.com") {
		t.Fatalf("base_url 泄露了攻击者主机: %s", base)
	}
	if base != "https://gw.example.com/v1" {
		t.Fatalf("base_url = %q, 期望使用部署方配置的地址", base)
	}
}

// 端到端：X-Forwarded-Prefix: /../../x 不得原样拼进 base_url。
func TestCreateKeyDoesNotLeakMaliciousForwardedPrefix(t *testing.T) {
	app := newTestApp(t)
	app.Cfg.PublicBaseURL = "https://gw.example.com"
	req := httptest.NewRequest(http.MethodPost, "/api/keys", strings.NewReader(`{"model":"glm-4"}`))
	req.Host = "192.168.12.17:18081"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-Prefix", "/../../x")
	rec := httptest.NewRecorder()
	app.HandleCreateKey(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	base := respBaseURL(t, rec.Body.Bytes())
	if strings.Contains(base, "..") {
		t.Fatalf("base_url 含路径穿越片段: %s", base)
	}
	if base != "https://gw.example.com/v1" {
		t.Fatalf("base_url = %q, 非法前缀应被丢弃", base)
	}
}

// 部署方未配置 public_base_url 时，必须 fail-closed 拒绝下发，
// 而不是回退到 X-Forwarded-Host / Host。
func TestCreateKeyFailsClosedWithoutPublicBaseURL(t *testing.T) {
	app := newTestApp(t)
	app.Cfg.PublicBaseURL = ""
	req := httptest.NewRequest(http.MethodPost, "/api/keys", strings.NewReader(`{"model":"glm-4"}`))
	req.Host = "192.168.12.17:18081"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-Host", "evil.example.com")
	rec := httptest.NewRecorder()
	app.HandleCreateKey(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("未配置 public_base_url 时必须 503 拒绝下发，实际 code=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "evil.example.com") {
		t.Fatalf("错误响应里泄露了攻击者主机: %s", rec.Body.String())
	}
}

/* ------------------------------------------------------------------ */
/* finding 547：非法转发头必须回落到 RemoteAddr                        */
/* ------------------------------------------------------------------ */

// parseIPStrict 对垃圾值必须判定为「不是 IP」。
func TestParseIPStrictRejectsGarbage(t *testing.T) {
	for _, bad := range []string{"abc", "1.2.3.4:99", "<img src=x>", "999.999.999.999", "not-an-ip", "1.2.3.4/24"} {
		if ip, ok := parseIPStrict(bad); ok {
			t.Fatalf("%q 不应被解析为 IP（得到 %q）", bad, ip)
		}
	}
	for _, good := range []string{"1.2.3.4", " 8.8.8.8 ", "2001:db8::1", "::ffff:1.2.3.4"} {
		if _, ok := parseIPStrict(good); !ok {
			t.Fatalf("%q 应被解析为 IP", good)
		}
	}
}

// normalizeIP 解析失败时返回空串（不再原样透传输入）。
func TestNormalizeIPDoesNotPassThroughGarbage(t *testing.T) {
	for _, bad := range []string{"abc", "1.2.3.4:99", "<img src=x>"} {
		if got := normalizeIP(bad); got != "" {
			t.Fatalf("normalizeIP(%q) = %q, 期望空串", bad, got)
		}
	}
	if got := normalizeIP("1.2.3.4"); got != "1.2.3.4" {
		t.Fatalf("normalizeIP(1.2.3.4) = %q", got)
	}
}

// 可信代理 + 正确共享密钥时，非法 X-Real-IP 不得被采信，应回落到对端地址。
func TestClientIPRejectsInvalidRealIP(t *testing.T) {
	restoreProxySecret(t, "test-secret")
	r := httptest.NewRequest(http.MethodPost, "/api/keys", nil)
	r.RemoteAddr = "127.0.0.1:5555"
	r.Header.Set("X-Real-IP", "<img src=x>")
	r.Header.Set("X-Proxy-Secret", "test-secret")
	if got := clientIP(r); got != "127.0.0.1" {
		t.Fatalf("clientIP = %q, 非法 X-Real-IP 必须回落到 RemoteAddr", got)
	}
}

// 可信代理 + 正确共享密钥时，非法 XFF 不得被采信。
func TestClientIPRejectsInvalidForwardedFor(t *testing.T) {
	restoreProxySecret(t, "test-secret")
	r := httptest.NewRequest(http.MethodPost, "/api/keys", nil)
	r.RemoteAddr = "127.0.0.1:5555"
	r.Header.Set("X-Forwarded-For", "abc")
	r.Header.Set("X-Proxy-Secret", "test-secret")
	if got := clientIP(r); got != "127.0.0.1" {
		t.Fatalf("clientIP = %q, 非法 XFF 必须回落到 RemoteAddr", got)
	}
}

// XFF 最右一跳非法但左边一跳合法时，取左边的合法值（代理链中真实客户端）。
func TestClientIPSkipsInvalidForwardedHop(t *testing.T) {
	restoreProxySecret(t, "test-secret")
	r := httptest.NewRequest(http.MethodPost, "/api/keys", nil)
	r.RemoteAddr = "127.0.0.1:5555"
	r.Header.Set("X-Forwarded-For", "9.9.9.9, <script>, 1.2.3.4")
	r.Header.Set("X-Proxy-Secret", "test-secret")
	if got := clientIP(r); got != "1.2.3.4" {
		t.Fatalf("clientIP = %q, 期望跳过非法跳后取 1.2.3.4", got)
	}
}

// 端到端：伪造的非法 X-Real-IP 不得写进 keys.json 的 ip 字段。
func TestCreateKeyDoesNotPersistInvalidIP(t *testing.T) {
	restoreProxySecret(t, "test-secret")
	app := newTestApp(t)
	req := httptest.NewRequest(http.MethodPost, "/api/keys", strings.NewReader(`{"model":"glm-4"}`))
	req.RemoteAddr = "127.0.0.1:5555"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Real-IP", "<img src=x>")
	req.Header.Set("X-Proxy-Secret", "test-secret")
	rec := httptest.NewRecorder()
	app.HandleCreateKey(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	k, ok := app.Store.GetByKey(body.Key)
	if !ok {
		t.Fatal("key not stored")
	}
	if strings.ContainsAny(k.IP, "<>\"' ") {
		t.Fatalf("keys.json 的 ip 字段被污染: %q", k.IP)
	}
	if k.IP != "127.0.0.1" {
		t.Fatalf("ip 字段 = %q, 期望记录校验后的真实 IP 127.0.0.1", k.IP)
	}
}
