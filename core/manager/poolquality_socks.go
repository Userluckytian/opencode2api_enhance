// 实例池链路探测的 SOCKS5 拨号与 HTTP 探测实现（性能模式 P1）。
//
// 网关层（根目录 socks.go）自带 SOCKS5 客户端，但属于 package main；
// core/manager 是独立包，这里自带一份轻量 SOCKS5 拨号（纯 stdlib），
// 用于经实例 sing-box 本地 SOCKS 出口向远端厂商 API 发真实 HTTP 探测。
package manager

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// socks5Connect 经 SOCKS5 代理（socksAddr，支持无鉴权）建立到 target（host:port）的 TCP 连接。
func socks5Connect(ctx context.Context, socksAddr, target string) (net.Conn, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", socksAddr)
	if err != nil {
		return nil, fmt.Errorf("socks5 dial %s: %w", socksAddr, err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	// 认证方法协商：仅无鉴权（实例 sing-box 本地入站无用户口令）。
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		conn.Close()
		return nil, fmt.Errorf("socks5 handshake write: %w", err)
	}
	buf := make([]byte, 2)
	if _, err := io.ReadFull(conn, buf); err != nil {
		conn.Close()
		return nil, fmt.Errorf("socks5 handshake read: %w", err)
	}
	if buf[0] != 0x05 || buf[1] != 0x00 {
		conn.Close()
		return nil, fmt.Errorf("socks5 auth method 0x%02x rejected", buf[1])
	}

	// CONNECT 请求（域名类型）。
	host, portStr, err := net.SplitHostPort(target)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("socks5 target %q: %w", target, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 0 || port > 65535 {
		conn.Close()
		return nil, fmt.Errorf("socks5 target port %q invalid", portStr)
	}
	req := []byte{0x05, 0x01, 0x00, 0x03, byte(len(host))}
	req = append(req, host...)
	req = append(req, byte(port>>8), byte(port))
	if _, err := conn.Write(req); err != nil {
		conn.Close()
		return nil, fmt.Errorf("socks5 connect write: %w", err)
	}

	// CONNECT 响应：VER REP RSV ATYP + BND.ADDR + BND.PORT；REP==0 才成功。
	resp := make([]byte, 4)
	if _, err := io.ReadFull(conn, resp); err != nil {
		conn.Close()
		return nil, fmt.Errorf("socks5 connect read: %w", err)
	}
	if resp[0] != 0x05 {
		conn.Close()
		return nil, fmt.Errorf("socks5 bad version 0x%02x", resp[0])
	}
	if resp[1] != 0x00 {
		conn.Close()
		return nil, errors.New(socks5RepText(resp[1]))
	}
	var skip int
	switch resp[3] {
	case 0x01:
		skip = 4 + 2 // IPv4 + 端口
	case 0x03:
		lbuf := make([]byte, 1)
		if _, err := io.ReadFull(conn, lbuf); err != nil {
			conn.Close()
			return nil, fmt.Errorf("socks5 read bind domain len: %w", err)
		}
		skip = int(lbuf[0]) + 2
	case 0x04:
		skip = 16 + 2 // IPv6 + 端口
	default:
		conn.Close()
		return nil, fmt.Errorf("socks5 unknown address type 0x%02x", resp[3])
	}
	if _, err := io.CopyN(io.Discard, conn, int64(skip)); err != nil {
		conn.Close()
		return nil, fmt.Errorf("socks5 read bind addr: %w", err)
	}
	return conn, nil
}

// socks5RepText SOCKS5 连接响应码人类可读文本。
func socks5RepText(rep byte) string {
	switch rep {
	case 0x01:
		return "socks5: general failure"
	case 0x02:
		return "socks5: connection not allowed"
	case 0x03:
		return "socks5: network unreachable"
	case 0x04:
		return "socks5: host unreachable"
	case 0x05:
		return "socks5: connection refused"
	case 0x06:
		return "socks5: ttl expired"
	case 0x07:
		return "socks5: command not supported"
	case 0x08:
		return "socks5: address type not supported"
	default:
		return fmt.Sprintf("socks5: connect failed, status 0x%02x", rep)
	}
}

// httpGetViaSocks 经本地 sing-box SOCKS 出口对 targetURL 发一次 GET。
// 返回链路是否可用：网络错误/超时/5xx 视为失败；4xx 表示服务端已响应（链路通）。
func httpGetViaSocks(socksPort uint16, targetURL string, timeout time.Duration) (bool, error) {
	socksAddr := net.JoinHostPort("127.0.0.1", itoa(socksPort))
	dialer := func(ctx context.Context, network, addr string) (net.Conn, error) {
		return socks5Connect(ctx, socksAddr, addr)
	}
	tr := &http.Transport{
		DialContext:         dialer,
		TLSHandshakeTimeout: timeout,
		DisableKeepAlives:   true, // 每次探测新建连接，保证超时生效
	}
	client := &http.Client{Transport: tr, Timeout: timeout}
	resp, err := client.Get(targetURL)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		return false, fmt.Errorf("probe status %d", resp.StatusCode)
	}
	return true, nil
}

// probeInstanceOnce 对单实例执行一次链路探测并返回样本。
//
// 2026-08-20 改造（问题 1 修复）：探测改为**经实例 API 端口（带实例 key）请求
// /v1/models**，对齐 freeCompletion 第一段——把「实例进程 + 鉴权 + 出口 + 上游」
// 整条链纳入探测范围；失败原因透传 LastError（旧实现直拨 sing-box SOCKS 且
// `ok, _ :=` 丢弃错误，UI 只能看到黑盒 0 分，与「测试按钮成功但质量不可用」
// 的现象矛盾（实际是两条不同链路各自为政））。
// 用标准 net/http 客户端（自动处理 chunked/gzip），与裸 TCP httpGetJSON
// 的 chunked 解码缺陷（问题 5，同事在改）互不影响。
func probeInstanceOnce(inst Instance, timeout time.Duration) ProbeSample {
	ts := time.Now()
	if inst.Port == 0 {
		return ProbeSample{OK: false, LatencyMS: 0, TS: ts.Unix(),
			LastError: "实例 API 端口未配置"}
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/v1/models", inst.Port)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return ProbeSample{OK: false, TS: ts.Unix(), LastError: "构造请求失败: " + err.Error()}
	}
	if inst.Password != "" {
		req.Header.Set("Authorization", "Bearer "+inst.Password)
	}
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return ProbeSample{OK: false, LatencyMS: time.Since(ts).Milliseconds(), TS: ts.Unix(),
			LastError: "实例 API 不可达: " + err.Error()}
	}
	defer resp.Body.Close()
	latency := time.Since(ts).Milliseconds()
	// 2xx = 实例 API + 上游链路通（对齐 freeCompletion：/v1/models 2xx 即节点可用）。
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return ProbeSample{OK: true, LatencyMS: latency, TS: ts.Unix()}
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	detail := strings.TrimSpace(string(body))
	if len(detail) > 200 {
		detail = detail[:200]
	}
	return ProbeSample{OK: false, LatencyMS: latency, TS: ts.Unix(),
		LastError: fmt.Sprintf("实例 /v1/models 返回 %d%s", resp.StatusCode, withPrefix(detail, ": "))}
}

// withPrefix 返回 prefix + detail（detail 非空时），用于组装错误详情。
func withPrefix(detail, prefix string) string {
	if detail == "" {
		return ""
	}
	return prefix + detail
}
