package manager

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 用替换 openExternalURL 捕获 handler 实际交给系统的 URL，
// 既能断言整串 URL 原样透传（含 & 参数），又不真的弹浏览器。
func stubOpener(t *testing.T, wantErr error) *string {
	t.Helper()
	got := new(string)
	orig := openExternalURL
	openExternalURL = func(target string) error {
		*got = target
		return wantErr
	}
	t.Cleanup(func() { openExternalURL = orig })
	return got
}

func postOpenURL(t *testing.T, body string) map[string]any {
	t.Helper()
	m := &Manager{}
	req := httptest.NewRequest(http.MethodPost, "/api/admin/open-url", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	m.OpenURLHandler()(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200（body=%s）", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应不是 JSON: %s", rec.Body.String())
	}
	return out
}

const panelURL = "http://127.0.0.1:58054/?gateway=http%3A%2F%2F127.0.0.1%3A40000%2Fv1&plugin=qw2api&models=a%2Cb"

// 应用内更新的「打开发布页」需要放行本仓库 Release（Tauri WebView 无法 window.open）。
const releaseURL = "https://github.com/Userluckytian/opencode2api_enhance/releases/tag/v1.7.10-beta.2"

func TestOpenURLHandlerAllowsReleasePage(t *testing.T) {
	got := stubOpener(t, nil)
	body, _ := json.Marshal(map[string]string{"url": releaseURL})

	res := postOpenURL(t, string(body))
	if res["ok"] != true {
		t.Fatalf("ok = %v，期望 true（error=%v）", res["ok"], res["error"])
	}
	if *got != releaseURL {
		t.Fatalf("交给系统的 URL = %q，期望 %q", *got, releaseURL)
	}
}

func TestOpenURLHandlerPassesFullURLThrough(t *testing.T) {
	got := stubOpener(t, nil)
	body, _ := json.Marshal(map[string]string{"url": panelURL})

	res := postOpenURL(t, string(body))
	if res["ok"] != true {
		t.Fatalf("ok = %v，期望 true（error=%v）", res["ok"], res["error"])
	}
	if *got != panelURL {
		t.Fatalf("交给系统的 URL 被改动了：\n得到 %q\n期望 %q", *got, panelURL)
	}
	if res["opened"] != panelURL {
		t.Fatalf("opened = %v，期望原样回显完整 URL", res["opened"])
	}
}

func TestOpenURLHandlerSurfacesOpenerError(t *testing.T) {
	stubOpener(t, errors.New(`exec: "cmd": executable file not found in %PATH%`))
	body, _ := json.Marshal(map[string]string{"url": panelURL})

	res := postOpenURL(t, string(body))
	if res["ok"] != false {
		t.Fatalf("ok = %v，期望 false", res["ok"])
	}
	// 前端把这段 error 提示给用户，不能被吞掉。
	msg, _ := res["error"].(string)
	if !strings.Contains(msg, "executable file not found") {
		t.Fatalf("error 未带出底层原因：%q", msg)
	}
}

func TestOpenURLHandlerRejectsBadTargets(t *testing.T) {
	got := stubOpener(t, nil)
	cases := []struct {
		name string
		url  string
	}{
		{"非 http 协议", "file:///C:/Windows/System32/calc.exe"},
		{"非回环主机", "http://example.com/"},
		{"非本仓库 GitHub 页面", "https://github.com/other/repo/releases"},
		{"本仓库页面必须是 https", "http://github.com/Userluckytian/opencode2api_enhance/releases"},
		{"换行注入", "http://127.0.0.1/\r\nX-Injected: 1"},
		{"伪装 localhost 前缀", "http://localhost.evil.com/"},
		{"query 里的裸引号（query 不做编码，必须拦住）", `http://127.0.0.1/?q="><x`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body, _ := json.Marshal(map[string]string{"url": c.url})
			res := postOpenURL(t, string(body))
			if res["ok"] != false {
				t.Fatalf("ok = %v，期望 false（url=%s）", res["ok"], c.url)
			}
		})
	}
	if *got != "" {
		t.Fatalf("非法 URL 不应触达系统打开器，却打开了 %q", *got)
	}
}

// path 里的特殊字符会被 url.String() 百分号编码，query 不会（上面已单独拦住），
// 所以交给 Shell 的串不可能带裸引号/尖括号。
func TestOpenURLHandlerEncodesSpecialChars(t *testing.T) {
	got := stubOpener(t, nil)
	body, _ := json.Marshal(map[string]string{"url": `http://127.0.0.1/"><x`})

	res := postOpenURL(t, string(body))
	if res["ok"] != true {
		t.Fatalf("ok = %v，期望 true", res["ok"])
	}
	if strings.ContainsAny(*got, `"<> `) {
		t.Fatalf("交给系统的 URL 仍含未编码的引号/尖括号/空格：%q", *got)
	}
	if !strings.Contains(*got, "%22") {
		t.Fatalf("引号未被编码：%q", *got)
	}
}

func TestOpenURLHandlerRejectsWrongMethod(t *testing.T) {
	stubOpener(t, nil)
	m := &Manager{}
	rec := httptest.NewRecorder()
	m.OpenURLHandler()(rec, httptest.NewRequest(http.MethodGet, "/api/admin/open-url", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("状态码 = %d，期望 405", rec.Code)
	}
}
