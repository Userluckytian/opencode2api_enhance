package contract

import (
	"net"
	"net/http"
	"net/url"
	"time"
)

// StaticProxyTransport 是一个只带「单个固定代理」的 Transport 实现。
//
// 它取代历史上的「节点池 + via_proxy 布尔开关」模型：
//   - 节点池（扫描/订阅/质量分/坏池/竞速）已随免费层自研路径一并移除；
//   - 但「某供应商必须走代理才通」是真实需求，所以保留最小形态：
//     一个厂商最多绑定一个代理地址，空 = 直连。
//
// 与池化 Transport 的区别：没有健康维护、没有冷却、没有出口轮换。
// Mark 是空操作——单代理没有可选的替代出口，标记它只会让后续请求全部失败。
type StaticProxyTransport struct {
	// ProxyURL 是代理地址（http / https / socks5 / socks5h）。
	// 空字符串 = 直连（不读取 HTTP_PROXY 等环境变量）。
	ProxyURL string

	// Timeout 是非流式请求的总超时；0 = 用 DefaultStaticProxyTimeout。
	Timeout time.Duration
}

// DefaultStaticProxyTimeout 是非流式调用的默认总超时。
// 与历史节点池行为对齐（长推理由流式路径规避）。
const DefaultStaticProxyTimeout = 300 * time.Second

// Client 实现 Transport。addr 返回值恒为空：单代理没有「出口节点」概念，
// 上层据此跳过按节点记账/统计的逻辑。
func (t StaticProxyTransport) Client(_ Tier, streaming bool) (*http.Client, string) {
	timeout := t.Timeout
	if timeout <= 0 {
		timeout = DefaultStaticProxyTimeout
	}
	tr := &http.Transport{
		Proxy: func(*http.Request) (*url.URL, error) {
			raw := t.ProxyURL
			if raw == "" {
				// 显式返回 nil：直连，且不读环境变量代理。
				return nil, nil
			}
			return url.Parse(raw)
		},
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	c := &http.Client{Transport: tr}
	// 流式请求不设总超时：Client.Timeout 覆盖整个请求生命周期，
	// 会截断长推理流。流的空闲保护由厂商层的逐事件超时负责。
	if !streaming {
		c.Timeout = timeout
	}
	return c, ""
}

// Mark 实现 Transport（空操作）。
func (StaticProxyTransport) Mark(string, int, error) {}
