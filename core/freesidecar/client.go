// Package freesidecar 把 OpenCode 免费层请求转发到本地 Bun sidecar。
//
// 背景：OpenCode 免费层（Authorization: Bearer public）校验客户端 TLS 指纹，
// 官方 opencode CLI 的 Bun/BoringSSL 指纹可过，Go 标准 crypto/tls 会被回
// 429 FreeUsageLimitError。本包只负责“Go → sidecar”的 HTTP 改写；
// 节点选择/健康/重试/统计仍由调用方（rootTransport）负责。
package freesidecar

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/6Kmfi6HP/opencode2api/core/contract"
	"github.com/6Kmfi6HP/opencode2api/core/manager"
)

// httpTransport 是 Go → 本地 sidecar 的传输（不走系统代理）。
// 与上游连接分离：sidecar 自己负责出站代理/TLS 指纹。
var httpTransport = &http.Transport{
	Proxy:               nil,
	MaxIdleConns:        32,
	MaxIdleConnsPerHost: 16,
	IdleConnTimeout:     90 * time.Second,
	// 本地 sidecar 用 HTTP/1.1 即可；关闭 h2 避免无意义的 ALPN 协商。
	ForceAttemptHTTP2: false,
}

// directTransport 用于非 chat/responses 请求（模型目录/版本探测）：
// 无节点代理时直连；有节点代理时优先走节点 HTTP inbound（见 transportForProxy）。
var directTransport = &http.Transport{
	Proxy:               nil,
	MaxIdleConns:        32,
	MaxIdleConnsPerHost: 16,
	IdleConnTimeout:     90 * time.Second,
	ForceAttemptHTTP2:   true,
}

// proxyTransports 缓存「按节点 HTTP 代理」的传输（GET 目录/版本探测用）。
var proxyTransports sync.Map // proxyURL -> *http.Transport

// warnedRemoteSOCKS 远程 SOCKS 告警去重（避免节点池每请求刷屏）。
var warnedRemoteSOCKS sync.Map

func transportForProxy(u *url.URL) *http.Transport {
	key := u.String()
	if v, ok := proxyTransports.Load(key); ok {
		return v.(*http.Transport)
	}
	tr := &http.Transport{
		Proxy:               http.ProxyURL(u),
		MaxIdleConns:        16,
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     90 * time.Second,
		ForceAttemptHTTP2:   true,
	}
	actual, _ := proxyTransports.LoadOrStore(key, tr)
	return actual.(*http.Transport)
}

type roundTripper struct {
	sidecarURL string
	proxyAddr  string // 池选中的出口地址（通常 127.0.0.1:<socks>）
	streaming  bool
}

// NewClient 返回走 sidecar 的 HTTP 客户端。
func NewClient(sidecarURL, proxyAddr string, streaming bool) *http.Client {
	c := &http.Client{
		Transport: &roundTripper{
			sidecarURL: strings.TrimRight(sidecarURL, "/"),
			proxyAddr:  proxyAddr,
			streaming:  streaming,
		},
	}
	// 流式请求不设总超时（长推理/长回答会被切断）；非流式给 5 分钟兜底。
	if !streaming {
		c.Timeout = 5 * time.Minute
	}
	return c
}

// RoundTrip 实现 http.RoundTripper。
func (t *roundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	// 只有 chat/responses 的 POST 需要 Bun TLS 指纹；模型目录/版本探测等 GET
	// 优先走节点 HTTP inbound（保持实例池出口），无节点时才直连。
	if req.Method != http.MethodPost || !isSidecarPath(req.URL.Path) {
		if proxy := ProxyURL(t.proxyAddr); proxy != "" {
			if u, err := url.Parse(proxy); err == nil {
				return transportForProxy(u).RoundTrip(req)
			}
		}
		return directTransport.RoundTrip(req)
	}
	var body []byte
	if req.Body != nil {
		var err error
		body, err = io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("free sidecar: read request body: %w", err)
		}
	}
	outReq, err := http.NewRequestWithContext(req.Context(), http.MethodPost, t.sidecarURL+"/proxy", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("free sidecar: build request: %w", err)
	}
	copyHeaders(outReq.Header, req.Header)
	outReq.Header.Set("X-Oc2a-Target-URL", req.URL.String())
	if proxy := ProxyURL(t.proxyAddr); proxy != "" {
		outReq.Header.Set("X-Oc2a-Proxy", proxy)
	}
	// 让 sidecar 的稳定 session 按下游身份隔离（同一实例/同一密钥复用）。
	if id := req.Header.Get("X-Oc2a-Identity"); id != "" {
		outReq.Header.Set("X-Oc2a-Identity", id)
	} else if auth := req.Header.Get("Authorization"); auth != "" {
		outReq.Header.Set("X-Oc2a-Identity", auth)
	}

	resp, err := httpTransport.RoundTrip(outReq)
	if err != nil {
		// 哨兵错误：本地 sidecar 不可用不是节点故障，厂商侧不应 Mark 节点。
		return nil, fmt.Errorf("%w: %v", contract.ErrFreeSidecarUnavailable, err)
	}
	return resp, nil
}

// isSidecarPath 判断该上游路径是否需要走 Bun sidecar。
func isSidecarPath(p string) bool {
	return strings.Contains(p, "/chat/completions") || strings.Contains(p, "/responses")
}

// copyHeaders 复制端到端头；跳过 hop-by-hop / 长度 / 本地转发头。
func copyHeaders(dst, src http.Header) {
	for k, vs := range src {
		lk := strings.ToLower(k)
		switch lk {
		case "content-length", "host", "connection", "transfer-encoding",
			"accept-encoding", "proxy-connection", "keep-alive", "te", "trailer", "upgrade":
			continue
		}
		if strings.HasPrefix(lk, "x-oc2a-") {
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

// ProxyURL 把池出口地址转换为 sidecar 可用的 HTTP 代理 URL。
//
//   - http:// / https:// → 原样（远程 HTTP 代理）；
//   - socks5:// / socks5h:// / socks:// 且本机 → 同一 sing-box 的 HTTP inbound（SOCKS+2000）；
//   - host:port（本机）→ 同上；
//   - 远程 SOCKS → Bun fetch 不支持，返回空（直连）并告警。
func ProxyURL(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}
	low := strings.ToLower(addr)
	switch {
	case strings.HasPrefix(low, "http://"), strings.HasPrefix(low, "https://"):
		return addr
	case strings.HasPrefix(low, "socks5://"), strings.HasPrefix(low, "socks5h://"), strings.HasPrefix(low, "socks://"):
		u, err := url.Parse(addr)
		if err == nil && isLocalHost(u.Hostname()) && u.Port() != "" {
			if p, err := strconv.Atoi(u.Port()); err == nil {
				if hp := manager.SingboxHTTPPort(uint16(p)); hp != 0 {
					return fmt.Sprintf("http://127.0.0.1:%d", hp)
				}
			}
		}
		if _, loaded := warnedRemoteSOCKS.LoadOrStore(addr, true); !loaded {
			slog.Warn("free sidecar: remote SOCKS proxy unsupported by Bun fetch, using direct", "proxy", addr)
		}
		return ""
	}
	// host:port（无 scheme）：按本机 sing-box 处理；非本机则当作 HTTP 代理。
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	if isLocalHost(host) {
		if p, err := strconv.Atoi(port); err == nil {
			if hp := manager.SingboxHTTPPort(uint16(p)); hp != 0 {
				return fmt.Sprintf("http://127.0.0.1:%d", hp)
			}
		}
		return ""
	}
	return "http://" + addr
}

func isLocalHost(host string) bool {
	switch strings.ToLower(strings.Trim(host, "[]")) {
	case "127.0.0.1", "localhost", "::1":
		return true
	}
	return false
}
