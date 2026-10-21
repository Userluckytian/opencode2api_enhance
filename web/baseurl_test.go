package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBuildBaseURLRequiresConfiguredPublicBase(t *testing.T) {
	// public_base_url 缺失 → 不得回退到任何客户端可控头，必须报错（fail-closed）。
	if _, err := buildBaseURL("", "", "192.168.1.20"); err == nil {
		t.Fatal("public_base_url 缺失时必须报错，不得回退到请求头")
	}
}

func TestBuildBaseURLUsesConfiguredPublicBase(t *testing.T) {
	got, err := buildBaseURL("https://gw.example.com", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://gw.example.com/v1" {
		t.Fatalf("got %q", got)
	}
}

func TestBuildBaseURLKeepsExistingV1(t *testing.T) {
	got, err := buildBaseURL("https://gw.example.com/v1", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://gw.example.com/v1" {
		t.Fatalf("got %q, 不应重复追加 /v1", got)
	}
}

func TestBuildBaseURLKeepsConfiguredPath(t *testing.T) {
	got, err := buildBaseURL("https://gw.example.com/token-buffet", "/other", "")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://gw.example.com/token-buffet/v1" {
		t.Fatalf("got %q, 配置里的路径应优先于转发头", got)
	}
}

func TestBuildBaseURLAppendsValidatedPrefix(t *testing.T) {
	got, err := buildBaseURL("https://gw.example.com", "/token-buffet", "")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://gw.example.com/token-buffet/v1" {
		t.Fatalf("got %q", got)
	}
}

func TestBuildBaseURLDropsMaliciousPrefix(t *testing.T) {
	// 含 .. / 重复斜杠 / 非法字符的前缀一律丢弃，绝不下发。
	for _, bad := range []string{"/../../x", "/token-buffet/../x", "//token-buffet", "/token buffet", "/tok<script>"} {
		got, err := buildBaseURL("https://gw.example.com", bad, "")
		if err != nil {
			t.Fatal(err)
		}
		if got != "https://gw.example.com/v1" {
			t.Fatalf("prefix %q 未被丢弃: %q", bad, got)
		}
	}
}

func TestBuildBaseURLReplacesLoopbackWithLANIP(t *testing.T) {
	got, err := buildBaseURL("http://127.0.0.1:23099", "", "192.168.1.20")
	if err != nil {
		t.Fatal(err)
	}
	if got != "http://192.168.1.20:23099/v1" {
		t.Fatalf("got %q, 期望换成内网 IP", got)
	}
}

func TestBuildBaseURLRejectsInvalidPublicBase(t *testing.T) {
	for _, bad := range []string{
		"ftp://gw.example.com",
		"https://user:pass@gw.example.com",
		"https://gw.example.com/../x",
		"https://gw example.com",
		"https://",
	} {
		if _, err := buildBaseURL(bad, "", ""); err == nil {
			t.Fatalf("public_base_url %q 应当被拒绝", bad)
		}
	}
}

func TestIsLocalHost(t *testing.T) {
	for _, h := range []string{"127.0.0.1", "localhost", "0.0.0.0", "", "::1", "127.0.0.1:8080"} {
		if !isLocalHost(h) {
			t.Fatalf("%q 应判定为本机", h)
		}
	}
	for _, h := range []string{"192.168.1.20", "example.com", "8.8.8.8"} {
		if isLocalHost(h) {
			t.Fatalf("%q 不应判定为本机", h)
		}
	}
}

// 端到端：直连本机时领到的地址必须不是 127.0.0.1。
func TestCreateKeyReturnsNonLoopbackBaseURL(t *testing.T) {
	app := newTestApp(t)
	// finding 548：base_url 只认部署方配置的 public_base_url。
	// 配成「只有本机能访问」的地址：下发时必须由 buildBaseURL 换成本机内网 IP，
	// 而不是把 127.0.0.1 直接发给使用者。
	app.Cfg.PublicBaseURL = "http://127.0.0.1:23099"
	req := httptest.NewRequest(http.MethodPost, "/api/keys", strings.NewReader(`{"model":"glm-4"}`))
	req.Host = "127.0.0.1:23099"
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.HandleCreateKey(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	base := respBaseURL(t, rec.Body.Bytes())
	if base == "" {
		t.Fatal("未返回 base_url")
	}
	if isLocalHost(hostOnly(base)) {
		t.Fatalf("base_url 仍是本机地址: %s", base)
	}
}

func hostOnly(base string) string {
	h, _ := hostPort(base)
	return h
}

func respBaseURL(t *testing.T, body []byte) string {
	t.Helper()
	var out struct {
		BaseURL string `json:"base_url"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	return out.BaseURL
}
