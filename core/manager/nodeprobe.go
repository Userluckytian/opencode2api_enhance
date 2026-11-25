package manager

// 节点池应用前的可达性预检。
//
// 背景（2026-11-25 事故）：订阅里的节点全部不可达（mihomo 连 TCP 都拨不出去），
// 但「应用」照样返回成功并写下一组死端口给引擎；引擎握手 90 秒后放弃 WARP、
// 以「无出口」模式启动，面板看起来在跑、实际一个出口都没有。
//
// 这里在写引擎配置之前，用农场端口各做一次 HTTPS 探测（与注册请求同一条路），
// 全挂就拒绝应用；部分挂就写进告警，让用户知道哪些节点是死的。

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

// probeNodeFarmSlot 通过一个 mixed 监听端口访问 Cloudflare trace。
// 选择这个目标是因为 WARP 注册与握手最终都要到 Cloudflare：能从这里出去，
// 才谈得上注册。超时给足（6s），避免把慢节点误判成死节点。
func probeNodeFarmSlot(port int, timeout time.Duration) error {
	proxyURL := &url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(port))}
	tr := &http.Transport{
		Proxy:               http.ProxyURL(proxyURL),
		TLSHandshakeTimeout: timeout,
		DisableKeepAlives:   true,
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{Timeout: timeout, Transport: tr}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.cloudflare.com/cdn-cgi/trace", nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

type slotProbe struct {
	Port int
	Err  error
}

// probeNodeFarmSlots 并发探测所有槽位（最多 8 个，串行会把应用拖到 48s）。
func probeNodeFarmSlots(ports []int) []slotProbe {
	out := make([]slotProbe, len(ports))
	var wg sync.WaitGroup
	for i, p := range ports {
		wg.Add(1)
		go func(i, port int) {
			defer wg.Done()
			out[i] = slotProbe{Port: port, Err: probeNodeFarmSlot(port, 6*time.Second)}
		}(i, p)
	}
	wg.Wait()
	return out
}
