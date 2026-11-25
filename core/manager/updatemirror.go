package manager

// 更新清单镜像加速：桌面壳的 Tauri updater 先向本机 127.0.0.1:40099 取
// latest.json，这里把安装包 URL 改写成「实测最快的公益加速源」；探测/拉取
// 失败时原样返回，壳的第二个 endpoint 仍会直连 GitHub。
//
// 为什么需要：Tauri updater 的安装包 URL 直接取自清单，多配几个镜像
// endpoint 只能加速几百字节的清单，加速不到几十 MB 的安装包。
//
// 安全边界（与 Denia 同一套思路）：
//   - 清单与 signature 原样取自 GitHub，不改一个字节；
//   - 安装包最终由 Tauri 用内置公钥验签（minisign），镜像只能影响下载字节，
//     无法伪造内容；
//   - 只监听 127.0.0.1，非回环请求直接 403，避免被当公网代理。
//
// 镜像表来源：Denia（crates/server/src/update_mirrors.rs）整理的公益源，
// 2026-10-15 实测过一轮；失效的源探测阶段会自然被排除，不需要改代码。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	updateRepo        = "Userluckytian/opencode2api_enhance"
	updateManifestURL = "https://github.com/" + updateRepo + "/releases/download/updater-latest/latest.json"
	// updateMirrorPort 固定本机端口：Tauri 的 endpoint 写死在编译期配置里，
	// 端口必须可预期；被占用时监听失败，壳自动回退到 GitHub 直连。
	updateMirrorPort = 40099
	// updateProbeTimeout 只探首字节，超过这个时间继续等也没有参考价值。
	updateProbeTimeout = 2500 * time.Millisecond
	updateFetchTimeout = 10 * time.Second
	// updateManifestTTL 缓存改写结果：面板每次打开都可能 check，不宜次次探测。
	updateManifestTTL = 5 * time.Minute
)

type updateMirror struct {
	label  string
	prefix string
}

// updateMirrorTable 公益 GitHub 加速源（前缀 + 官方前缀的剩余路径拼接）。
var updateMirrorTable = []updateMirror{
	{"gh.h233.eu.org", "https://gh.h233.eu.org/https://github.com"},
	{"gh.ddlc.top", "https://gh.ddlc.top/https://github.com"},
	{"gh-proxy", "https://gh-proxy.org/https://github.com"},
	{"gh-proxy Fastly", "https://cdn.gh-proxy.org/https://github.com"},
	{"gh-proxy EdgeOne", "https://edgeone.gh-proxy.org/https://github.com"},
	{"isteed CORS", "https://cors.isteed.cc/github.com"},
	{"ghproxy.it", "https://ghproxy.it/https://github.com"},
	{"boki.moe", "https://github.boki.moe/https://github.com"},
	{"gh.monlor.com", "https://gh.monlor.com/https://github.com"},
	{"ednovas", "https://github.ednovas.xyz/https://github.com"},
	{"geekertao", "https://ghfile.geekertao.top/https://github.com"},
	{"keleyaa", "https://ghp.keleyaa.com/https://github.com"},
	{"gh.chjina.com", "https://gh.chjina.com/https://github.com"},
	{"hwinziej", "https://ghpxy.hwinzniej.top/https://github.com"},
	{"crashmc", "https://cdn.crashmc.com/https://github.com"},
	{"yylx", "https://git.yylx.win/https://github.com"},
	{"mrhjx", "https://gitproxy.mrhjx.cn/https://github.com"},
	{"cxkpro", "https://ghproxy.cxkpro.top/https://github.com"},
	{"gh.idayer.com", "https://gh.idayer.com/https://github.com"},
	{"ghproxy.net", "https://ghproxy.net/https://github.com"},
	{"ghfast.top", "https://ghfast.top/https://github.com"},
	{"wget.la", "https://wget.la/https://github.com"},
}

type updateManifestEntry struct {
	at   time.Time
	body []byte
}

// StartUpdateManifestServer 启动本机更新清单中转（失败只记日志，不影响主服务）。
//
// 只服桌面壳的 updater：固定 127.0.0.1:40099，可用
// OPCODE2API_UPDATE_MIRROR_PORT 覆盖（调试多实例时用）。
func (m *Manager) StartUpdateManifestServer() {
	port := updateMirrorPort
	if v := strings.TrimSpace(os.Getenv("OPCODE2API_UPDATE_MIRROR_PORT")); v != "" {
		if n, err := net.LookupPort("tcp", v); err == nil && n > 0 && n < 65536 {
			port = n
		}
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		// 端口被占（例如正式版/dev 同时开）：本实例的更新检查回退直连 GitHub。
		slog.Info("update manifest mirror disabled (port busy)", "addr", addr, "error", err)
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/admin/update/manifest", m.updateManifestHandler)
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			slog.Warn("update manifest server stopped", "error", err)
		}
	}()
	slog.Info("update manifest mirror listening", "addr", addr)
}

func (m *Manager) updateManifestHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	if !isLoopbackRemote(r.RemoteAddr) {
		http.Error(w, "loopback only", http.StatusForbidden)
		return
	}
	target := strings.TrimSpace(r.URL.Query().Get("target"))
	arch := strings.TrimSpace(r.URL.Query().Get("arch"))

	body, err := m.updateManifest(r.Context(), target, arch)
	if err != nil {
		// 502 → Tauri 会继续尝试下一个 endpoint（GitHub 直连）。
		slog.Warn("update manifest unavailable", "error", err)
		http.Error(w, "manifest unavailable", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(body)
}

// updateManifest 取清单并把目标平台的 url 改写成最快镜像（带 5 分钟缓存）。
//
// 整条链路（探测 + 拉取 + 改写）用 20s 上限收口：极端网络下逐个尝试
// 23 个来源可能拖过壳的 30s 检查超时，超时后壳会直接回退 GitHub。
func (m *Manager) updateManifest(ctx context.Context, target, arch string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	key := target + "/" + arch
	m.updateManifestMu.Lock()
	if e, ok := m.updateManifestCache[key]; ok && time.Since(e.at) < updateManifestTTL {
		body := e.body
		m.updateManifestMu.Unlock()
		return body, nil
	}
	m.updateManifestMu.Unlock()

	raw, source, err := fetchManifest(ctx)
	if err != nil {
		return nil, err
	}
	body, err := rewriteManifestURLs(ctx, raw, target, arch, rankReachable)
	if err != nil {
		return nil, err
	}
	slog.Info("update manifest rewritten", "source", source, "platform", key)
	m.updateManifestMu.Lock()
	if m.updateManifestCache == nil {
		m.updateManifestCache = map[string]updateManifestEntry{}
	}
	m.updateManifestCache[key] = updateManifestEntry{at: time.Now(), body: body}
	m.updateManifestMu.Unlock()
	return body, nil
}

// fetchManifest 取 latest.json：**优先 GitHub 直连**，失败才退回镜像。
//
// 为什么不优先走镜像：镜像会缓存清单（实测发布 2.0.12 后仍返回 2.0.11），
// 而清单只有几 KB，直连代价很小。镜像只用来改写**安装包**地址。
func fetchManifest(ctx context.Context) ([]byte, string, error) {
	if body, err := httpGetLimited(ctx, updateManifestURL, 1<<20); err == nil && json.Valid(body) {
		return body, updateManifestURL, nil
	}
	// 直连拿不到（被墙/超时）：退回镜像，但宁可失败也不让壳拿到过期清单。
	return fetchFirstReachable(ctx, updateManifestCandidates())
}

// updateManifestCandidates 清单地址的候选：各镜像 + GitHub 直连兜底。
func updateManifestCandidates() []string {
	out := make([]string, 0, len(updateMirrorTable)+1)
	for _, mirror := range updateMirrorTable {
		if url, ok := buildMirrorURL(mirror.prefix, updateManifestURL); ok {
			out = append(out, url)
		}
	}
	out = append(out, updateManifestURL)
	return out
}

// buildMirrorURL 把官方 github.com 地址套到镜像前缀上。
func buildMirrorURL(prefix, canonical string) (string, bool) {
	rest, ok := strings.CutPrefix(canonical, "https://github.com")
	if !ok || !strings.HasSuffix(prefix, "github.com") {
		return "", false
	}
	return prefix + rest, true
}

// fetchFirstReachable 并发探测候选并按延迟排序，逐个拉取直到拿到合法 JSON。
func fetchFirstReachable(ctx context.Context, candidates []string) ([]byte, string, error) {
	ordered := rankReachable(ctx, candidates)
	var lastErr error
	for _, url := range ordered {
		body, err := httpGetLimited(ctx, url, 1<<20)
		if err != nil {
			lastErr = err
			continue
		}
		if !json.Valid(body) {
			lastErr = fmt.Errorf("%s: 返回的不是 JSON", url)
			continue
		}
		return body, url, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("没有可用的清单来源")
	}
	return nil, "", lastErr
}

// rankReachable 并发探测（Range 0-0），返回「能连上的按延迟升序 + 连不上的」。
// 输入最后一个元素约定是官方直连，永远保留在结果尾部兜底。
func rankReachable(ctx context.Context, urls []string) []string {
	if len(urls) == 0 {
		return nil
	}
	type probe struct {
		idx     int
		latency time.Duration
		ok      bool
	}
	client := &http.Client{Timeout: updateProbeTimeout}
	results := make([]probe, len(urls))
	var wg sync.WaitGroup
	for i, u := range urls {
		wg.Add(1)
		go func(i int, u string) {
			defer wg.Done()
			results[i] = probe{idx: i}
			started := time.Now()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
			if err != nil {
				return
			}
			// 只要首字节：拿到响应头就断开，不拉整个安装包。
			req.Header.Set("Range", "bytes=0-0")
			resp, err := client.Do(req)
			if err != nil {
				return
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
				return
			}
			results[i] = probe{idx: i, latency: time.Since(started), ok: true}
		}(i, u)
	}
	wg.Wait()

	reachable := make([]probe, 0, len(results))
	unreachable := make([]probe, 0, len(results))
	for _, p := range results {
		if p.ok {
			reachable = append(reachable, p)
		} else {
			unreachable = append(unreachable, p)
		}
	}
	sort.Slice(reachable, func(i, j int) bool { return reachable[i].latency < reachable[j].latency })
	ordered := make([]string, 0, len(urls))
	for _, p := range reachable {
		ordered = append(ordered, urls[p.idx])
	}
	// 探测超时≠一定下不动：把剩余候选按原顺序（镜像在前、官方直连在后）追加。
	for _, p := range unreachable {
		ordered = append(ordered, urls[p.idx])
	}
	return ordered
}

// manifestRank 是可注入的排序函数，测试里替换成确定性实现。
type manifestRank func(ctx context.Context, urls []string) []string

// rewriteManifestURLs 把清单里目标平台的 url 换成最快镜像。
//
// 只改 `platforms[*].url`，version / notes / pub_date / signature 原样保留；
// 目标平台没匹配到或没有可用镜像时保持 GitHub 原地址。没带 target/arch
// 时直接原样返回（避免一次请求探测几十个源）。
func rewriteManifestURLs(ctx context.Context, raw []byte, target, arch string, rank manifestRank) ([]byte, error) {
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("清单不是合法 JSON: %w", err)
	}
	platforms, ok := doc["platforms"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("清单缺少 platforms")
	}
	wanted := ""
	if target != "" && arch != "" {
		wanted = target + "-" + arch
	}
	if wanted == "" {
		return raw, nil
	}
	for key, value := range platforms {
		if key != wanted && !strings.HasPrefix(key, wanted+"-") {
			continue
		}
		entry, ok := value.(map[string]any)
		if !ok {
			continue
		}
		original, _ := entry["url"].(string)
		if original == "" || !strings.HasPrefix(original, "https://github.com/") {
			continue
		}
		candidates := make([]string, 0, len(updateMirrorTable))
		for _, mirror := range updateMirrorTable {
			if u, ok := buildMirrorURL(mirror.prefix, original); ok {
				candidates = append(candidates, u)
			}
		}
		if len(candidates) == 0 {
			continue
		}
		ordered := rank(ctx, candidates)
		if len(ordered) > 0 && ordered[0] != original {
			entry["url"] = ordered[0]
		}
	}
	return json.Marshal(doc)
}

// httpGetLimited GET 并限制响应体大小（清单只有几 KB）。
func httpGetLimited(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: updateFetchTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

// isLoopbackRemote 判断请求是否来自本机（endpoint 只服务桌面壳）。
func isLoopbackRemote(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
