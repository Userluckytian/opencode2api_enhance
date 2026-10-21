package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// 客户端伪造 X-Forwarded-For 不得被采信——这是「同一 IP 限领一个 Key」
// 「网段白名单」「IP 策略」三项限制的信任基础。
func TestClientIPIgnoresSpoofedForwardedHeader(t *testing.T) {
	// 场景：经 nginx 反代，真实客户端是内网 192.168.12.17，
	// 客户端自己塞了 X-Forwarded-For: 9.9.9.9
	// nginx 追加后 → "9.9.9.9, 192.168.12.17"
	// 若按「第一个非可信地址」取值会拿到伪造的 9.9.9.9 → 全部限制被绕过。
	restoreProxySecret(t, "test-secret")
	r := httptest.NewRequest(http.MethodPost, "/api/keys", nil)
	r.RemoteAddr = "127.0.0.1:5555" // 直连对端是本机 nginx（可信代理）
	r.Header.Set("X-Forwarded-For", "9.9.9.9, 192.168.12.17")
	r.Header.Set("X-Real-IP", "192.168.12.17") // nginx 用 $remote_addr 覆盖写入
	r.Header.Set("X-Proxy-Secret", "test-secret")
	got := clientIP(r)
	if got != "192.168.12.17" {
		t.Fatalf("clientIP = %q, 期望真实客户端 192.168.12.17", got)
	}
}

// 没有 X-Real-IP 时，XFF 的最右一跳是代理追加的真实客户端。
func TestClientIPTakesRightmostForwardedHop(t *testing.T) {
	restoreProxySecret(t, "test-secret")
	r := httptest.NewRequest(http.MethodPost, "/api/keys", nil)
	r.RemoteAddr = "127.0.0.1:5555"
	r.Header.Set("X-Forwarded-For", "9.9.9.9, 192.168.12.17")
	r.Header.Set("X-Proxy-Secret", "test-secret")
	if got := clientIP(r); got != "192.168.12.17" {
		t.Fatalf("clientIP = %q, 期望取最右一跳 192.168.12.17", got)
	}
}

// 直连 web-server（绕过 nginx）时，伪造头必须完全无效。
func TestClientIPIgnoresHeadersWhenNotBehindProxy(t *testing.T) {
	restoreProxySecret(t, "test-secret")
	r := httptest.NewRequest(http.MethodPost, "/api/keys", nil)
	r.RemoteAddr = "192.168.12.99:5555" // 直连，非可信代理
	r.Header.Set("X-Forwarded-For", "9.9.9.9")
	r.Header.Set("X-Real-IP", "9.9.9.9")
	r.Header.Set("X-Proxy-Secret", "test-secret")
	got := clientIP(r)
	if got != "192.168.12.99" {
		t.Fatalf("clientIP = %q, 直连时必须用 RemoteAddr", got)
	}
}

// 内网客户端直连 18081 时也不能靠伪造头换 IP。
func TestClientIPIgnoresSpoofedHeaderFromLANPeer(t *testing.T) {
	restoreProxySecret(t, "test-secret")
	r := httptest.NewRequest(http.MethodPost, "/api/keys", nil)
	r.RemoteAddr = "192.168.12.99:5555"
	r.Header.Set("X-Forwarded-For", "192.168.12.17")
	r.Header.Set("X-Proxy-Secret", "test-secret")
	if got := clientIP(r); got != "192.168.12.99" {
		t.Fatalf("clientIP = %q, 内网直连也不得采信伪造头", got)
	}
}

// 关键场景：web-server 与 nginx 同机，直连与反代的 RemoteAddr 都是 127.0.0.1，
// 无法靠对端地址区分。此时必须靠共享密钥头，否则在服务器上的人可伪造任意 IP。
func TestClientIPIgnoresSpoofedHeaderWithoutProxySecret(t *testing.T) {
	restoreProxySecret(t, "test-secret")
	r := httptest.NewRequest(http.MethodPost, "/api/keys", nil)
	r.RemoteAddr = "127.0.0.1:5555" // 本机，与 nginx 无法区分
	r.Header.Set("X-Real-IP", "9.9.9.9")
	r.Header.Set("X-Forwarded-For", "9.9.9.9")
	// 没有 X-Proxy-Secret（或密钥不对）→ 视为直连
	if got := clientIP(r); got != "127.0.0.1" {
		t.Fatalf("clientIP = %q, 无共享密钥时不得采信转发头", got)
	}
	r2 := httptest.NewRequest(http.MethodPost, "/api/keys", nil)
	r2.RemoteAddr = "127.0.0.1:5555"
	r2.Header.Set("X-Real-IP", "9.9.9.9")
	r2.Header.Set("X-Proxy-Secret", "wrong-secret")
	if got := clientIP(r2); got != "127.0.0.1" {
		t.Fatalf("clientIP = %q, 密钥不匹配时不得采信转发头", got)
	}
}

// 可信代理名单可配置（多机部署时把 nginx 所在网段加进来）。
func TestTrustedProxyListIsConfigurable(t *testing.T) {
	orig := trustedProxyCIDRs
	defer func() { trustedProxyCIDRs = orig }()
	restoreProxySecret(t, "test-secret")

	SetTrustedProxies([]string{"10.9.9.0/24"})
	r := httptest.NewRequest(http.MethodPost, "/api/keys", nil)
	r.RemoteAddr = "10.9.9.5:5555"
	r.Header.Set("X-Real-IP", "192.168.12.17")
	r.Header.Set("X-Proxy-Secret", "test-secret")
	if got := clientIP(r); got != "192.168.12.17" {
		t.Fatalf("配置为可信代理后应采信 X-Real-IP，got %q", got)
	}

	r2 := httptest.NewRequest(http.MethodPost, "/api/keys", nil)
	r2.RemoteAddr = "127.0.0.1:5555"
	r2.Header.Set("X-Real-IP", "192.168.12.17")
	r2.Header.Set("X-Proxy-Secret", "test-secret")
	if got := clientIP(r2); got != "127.0.0.1" {
		t.Fatalf("不在名单内的对端不应采信转发头，got %q", got)
	}
}

// restoreProxySecret 临时设置共享密钥并在测试后恢复。
func restoreProxySecret(t *testing.T, s string) {
	t.Helper()
	orig := proxySecret
	SetProxySecret(s)
	t.Cleanup(func() { SetProxySecret(orig) })
}
