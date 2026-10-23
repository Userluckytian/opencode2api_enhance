package manager

// OpenURLHandler：用系统默认浏览器打开一个**本机回环** URL。
//
// 专供管理前端在 Tauri WebView 内打开插件自带面板使用——WebView 会静默拦截
// window.open（跨源新窗口请求），由 core 代为调用平台浏览器是唯一可靠通路；
// 浏览器直接访问管理页时该端点同样适用（同源 fetch，无跨域问题）。
//
// 安全边界：端点位于 requireAuth 之后的 /api/admin/* 域；仅接受 http/https 且
// 主机为 127.0.0.1/localhost/[::1]（插件面板）或项目 GitHub Release 页
// （应用内更新的「打开发布页」），并拒绝含引号/尖括号/换行的串（防 cmd 参数逃逸）。
// 端点不返回目标内容，只负责"打开"。

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
		loopback := host == "127.0.0.1" || host == "localhost" || host == "::1"
		// 应用内更新的「打开发布页」需要打开项目 Release；只放行本仓库的 https 页面。
		releasePage := u.Scheme == "https" && host == "github.com" &&
			strings.HasPrefix(u.Path, "/Userluckytian/opencode2api_enhance/")
		if !loopback && !releasePage {
			writeJSON(w, map[string]any{"ok": false, "error": "仅允许打开本机回环地址或项目发布页"})
			return
		}
		target := u.String()
		if strings.ContainsAny(target, "\"`<>") || strings.ContainsAny(raw, "\r\n") {
			writeJSON(w, map[string]any{"ok": false, "error": "URL 含非法字符"})
			return
		}

		if err := openExternalURL(target); err != nil {
			writeJSON(w, map[string]any{"ok": false, "error": fmt.Sprintf("启动浏览器失败: %v", err)})
			return
		}
		writeJSON(w, map[string]any{"ok": true, "opened": target})
	}
}

// openExternalURL 把 URL 交给系统默认浏览器。抽成包级变量供测试替换。
var openExternalURL = openWithShellExecute
