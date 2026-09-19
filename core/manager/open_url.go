package manager

// OpenURLHandler：用系统默认浏览器打开一个**本机回环** URL。
//
// 专供管理前端在 Tauri WebView 内打开插件自带面板使用——WebView 会静默拦截
// window.open（跨源新窗口请求），由 core 代为调用平台浏览器是唯一可靠通路；
// 浏览器直接访问管理页时该端点同样适用（同源 fetch，无跨域问题）。
//
// 安全边界：端点位于 requireAuth 之后的 /api/admin/* 域；仅接受 http/https 且
// 主机为 127.0.0.1/localhost/[::1] 的 URL（插件面板全部绑定回环），并拒绝含
// 引号/尖括号/换行的串（防 cmd 参数逃逸）。端点不返回目标内容，只负责"打开"。

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
)

// OpenURLHandler POST {url}：系统默认浏览器打开回环 URL。
func (m *Manager) OpenURLHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethod(w, r, http.MethodPost) {
			return
		}
		var body struct {
			URL string `json:"url"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&body); err != nil {
			writeJSON(w, map[string]any{"ok": false, "error": "bad JSON body: " + err.Error()})
			return
		}
		raw := strings.TrimSpace(body.URL)
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			writeJSON(w, map[string]any{"ok": false, "error": "仅支持 http/https URL"})
			return
		}
		host := u.Hostname()
		if host != "127.0.0.1" && host != "localhost" && host != "::1" {
			writeJSON(w, map[string]any{"ok": false, "error": "仅允许打开本机回环地址（插件面板均在 127.0.0.1）"})
			return
		}
		target := u.String()
		if strings.ContainsAny(target, "\"`<>") || strings.ContainsAny(raw, "\r\n") {
			writeJSON(w, map[string]any{"ok": false, "error": "URL 含非法字符"})
			return
		}

		var cmd *exec.Cmd
		switch runtime.GOOS {
		case "windows":
			// start 的第一个引号参数是窗口标题占位，第二个才是目标
			cmd = exec.Command("cmd", "/c", "start", "", target)
		case "darwin":
			cmd = exec.Command("open", target)
		default:
			cmd = exec.Command("xdg-open", target)
		}
		if err := cmd.Start(); err != nil {
			writeJSON(w, map[string]any{"ok": false, "error": fmt.Sprintf("启动浏览器失败: %v", err)})
			return
		}
		go func() { _ = cmd.Wait() }() // 回收子进程，避免僵尸
		writeJSON(w, map[string]any{"ok": true, "opened": target})
	}
}
