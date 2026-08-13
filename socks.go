// Part of the P1 (core split) refactor: code moved out of main.go.
// Same package (main) - do not change package clause manually.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

type Socks5Proxy struct {
	Addr     string `json:"addr"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	Name     string `json:"name,omitempty"`
}

func socks5Dial(proxy Socks5Proxy) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, target string) (net.Conn, error) {
		conn, err := net.DialTimeout("tcp", proxy.Addr, 10*time.Second)
		if err != nil {
			return nil, fmt.Errorf("socks5 connect to %s: %w", proxy.Addr, err)
		}
		deadline := time.Now().Add(15 * time.Second)
		conn.SetDeadline(deadline)

		// 认证方法协商
		auth := byte(0x00) // no auth
		if proxy.Username != "" {
			auth = 0x02 // username/password
		}
		if _, err := conn.Write([]byte{0x05, 0x01, auth}); err != nil {
			conn.Close()
			return nil, fmt.Errorf("socks5 handshake write: %w", err)
		}
		buf := make([]byte, 2)
		if _, err := io.ReadFull(conn, buf); err != nil {
			conn.Close()
			return nil, fmt.Errorf("socks5 handshake read: %w", err)
		}
		if buf[0] != 0x05 {
			conn.Close()
			return nil, fmt.Errorf("socks5: not socks5 protocol")
		}

		// 用户名/密码认证
		if buf[1] == 0x02 {
			if proxy.Username == "" {
				conn.Close()
				return nil, fmt.Errorf("socks5: server requires auth but no credentials")
			}
			ulen := len(proxy.Username)
			plen := len(proxy.Password)
			authBuf := make([]byte, 3+ulen+plen)
			authBuf[0] = 0x01
			authBuf[1] = byte(ulen)
			copy(authBuf[2:], proxy.Username)
			authBuf[2+ulen] = byte(plen)
			copy(authBuf[3+ulen:], proxy.Password)
			if _, err := conn.Write(authBuf); err != nil {
				conn.Close()
				return nil, fmt.Errorf("socks5 auth write: %w", err)
			}
			authResp := make([]byte, 2)
			if _, err := io.ReadFull(conn, authResp); err != nil {
				conn.Close()
				return nil, fmt.Errorf("socks5 auth read: %w", err)
			}
			if authResp[1] != 0x00 {
				conn.Close()
				return nil, fmt.Errorf("socks5: auth failed")
			}
		} else if buf[1] != 0x00 {
			conn.Close()
			return nil, fmt.Errorf("socks5: unsupported auth method 0x%02x", buf[1])
		}

		// CONNECT 请求
		host, portStr, err := net.SplitHostPort(target)
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("socks5: invalid target %s: %w", target, err)
		}
		port := 0
		fmt.Sscanf(portStr, "%d", &port)

		req := []byte{0x05, 0x01, 0x00} // VER, CMD=CONNECT, RSV
		ip := net.ParseIP(host)
		if ip != nil {
			if ip4 := ip.To4(); ip4 != nil {
				req = append(req, 0x01) // IPv4
				req = append(req, ip4...)
			} else {
				req = append(req, 0x04) // IPv6
				req = append(req, ip.To16()...)
			}
		} else {
			if len(host) > 255 {
				conn.Close()
				return nil, fmt.Errorf("socks5: hostname too long")
			}
			req = append(req, 0x03) // Domain
			req = append(req, byte(len(host)))
			req = append(req, []byte(host)...)
		}
		req = append(req, byte(port>>8), byte(port))

		if _, err := conn.Write(req); err != nil {
			conn.Close()
			return nil, fmt.Errorf("socks5 connect write: %w", err)
		}

		// 读取响应
		resp := make([]byte, 4)
		if _, err := io.ReadFull(conn, resp); err != nil {
			conn.Close()
			return nil, fmt.Errorf("socks5 connect read: %w", err)
		}
		if resp[1] != 0x00 {
			conn.Close()
			return nil, fmt.Errorf("socks5: connect failed, status 0x%02x", resp[1])
		}

		// 读取绑定地址
		switch resp[3] {
		case 0x01: // IPv4
			if _, err := io.ReadFull(conn, make([]byte, 4+2)); err != nil {
				conn.Close()
				return nil, fmt.Errorf("socks5: read bind ipv4: %w", err)
			}
		case 0x03: // Domain
			dlen := make([]byte, 1)
			if _, err := io.ReadFull(conn, dlen); err != nil {
				conn.Close()
				return nil, fmt.Errorf("socks5: read bind domain len: %w", err)
			}
			if _, err := io.ReadFull(conn, make([]byte, int(dlen[0])+2)); err != nil {
				conn.Close()
				return nil, fmt.Errorf("socks5: read bind domain: %w", err)
			}
		case 0x04: // IPv6
			if _, err := io.ReadFull(conn, make([]byte, 16+2)); err != nil {
				conn.Close()
				return nil, fmt.Errorf("socks5: read bind ipv6: %w", err)
			}
		default:
			conn.Close()
			return nil, fmt.Errorf("socks5: unknown address type 0x%02x", resp[3])
		}

		conn.SetDeadline(time.Time{})
		return conn, nil
	}
}

var (
	socks5Proxies []Socks5Proxy
	activeSocks5  string // 启用的代理 Addr，空表示直连，__round_robin__ 表示轮询
	socks5Mu      sync.RWMutex
)

const socks5RR = "__round_robin__"

var socks5RRIndex uint32

// routeMode 代理池/网关路由模式：
//   - smart（默认）：failover 游标逻辑 + 健康计数/坏池/超时切换完整容错
//   - failover：成功不动游标、失败才切换（无健康计数附加层）
//   - round_robin：轮询分发
var routeMode = "smart"

var (
	socks5Client     *http.Client // 缓存的 SOCKS5 客户端
	socks5ClientAddr string       // 缓存对应的代理地址
)

// 按代理地址缓存的 HTTP 客户端（连接池复用）；配置变更（proxies 列表变化）时整体清空。
var (
	proxyClientMu sync.Mutex
	proxyClients  = map[string]*http.Client{}
)

// ======================== 代理健康池 ========================

// badStatusCodes 坏状态码组：遇到这些状态码 → 立即切换节点并计数，
// 连续 badThreshold 次后标记该节点为"坏"（badReason），不再选用。
// 可配置（config.json 的 bad_status_codes），默认覆盖 401/402/429/503 等。
var badStatusCodes = map[int]string{
	http.StatusUnauthorized:       "401：认证失败",
	http.StatusPaymentRequired:    "402：额度受限",
	http.StatusTooManyRequests:    "429：最大额度上限",
	http.StatusServiceUnavailable: "503：服务不可用",
}

// badThreshold 连续坏状态码次数阈值，达到后节点进入"坏池"（禁用）
const badThreshold = 3

type socks5HealthState struct {
	failures  int
	until     time.Time
	badReason string // 非空 = 已进坏池（如 "429：最大额度上限"），不再选用
	badCount  int    // 连续坏状态码计数
}

var (
	socks5HealthMu sync.Mutex
	socks5Health   = map[string]socks5HealthState{}
)

// pickHealthyProxy 从 start 位置起轮询，跳过冷却中代理；全冷→返回冷却最早结束的兜底。
func pickHealthyProxy(proxies []Socks5Proxy, start int) Socks5Proxy {
	// S5: 单出口退化——池中仅 1 个可用出口时不叠加质量加权/熔断选择，
	// 直接走默认请求（请求层的健康计数/超时切换兜底），避免聪明逻辑只服务同一个节点。
	if len(proxies) <= 1 {
		if len(proxies) == 1 {
			return proxies[0]
		}
		return Socks5Proxy{}
	}
	// P2 性能模式：质量加权路由 + 熔断/半开（关闭时走基线逻辑）。
	if poolPerfMode {
		return pickWeightedProxy(proxies, start)
	}
	now := time.Now()
	var fallback Socks5Proxy
	var fallbackUntil time.Time
	socks5HealthMu.Lock()
	defer socks5HealthMu.Unlock()
	for offset := 0; offset < len(proxies); offset++ {
		proxy := proxies[(start+offset)%len(proxies)]
		state := socks5Health[proxy.Addr]
		// 坏池节点（401/402/429/503 连续 badThreshold 次）彻底不选
		if state.badReason != "" {
			continue
		}
		if state.until.IsZero() || !now.Before(state.until) {
			return proxy
		}
		if fallback.Addr == "" || state.until.Before(fallbackUntil) {
			fallback = proxy
			fallbackUntil = state.until
		}
	}
	return fallback
}

// markSocks5Result 记录代理健康/冷却状态。
// 失败分类：
//   - 坏状态码（401/402/429/503，见 badStatusCodes）：立即切换节点并计数，
//     连续 badThreshold 次 → 标记 badReason 进坏池（彻底禁用）
//   - 其他失败：临时冷却（连接错→20s、429→45s、连续 3 次→2min）
func markSocks5Result(addr string, status int, requestErr error) {
	if addr == "" {
		return
	}
	socks5HealthMu.Lock()

	// 坏状态码：计数 + 达到阈值标记坏池
	if reason, ok := badStatusCodes[status]; ok {
		state := socks5Health[addr]
		state.badCount++
		if state.badCount >= badThreshold {
			state.badReason = reason
			slog.Warn("proxy entered bad pool", "addr", addr, "reason", reason, "count", state.badCount)
		}
		// 坏状态码也标记临时冷却（后续请求跳过，避免连续踩雷）
		state.failures++
		state.until = time.Now().Add(45 * time.Second)
		socks5Health[addr] = state
	} else {
		failed := requestErr != nil || status == http.StatusBadGateway ||
			status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout
		if !failed {
			if status >= 200 && status < 300 {
				delete(socks5Health, addr)
			}
		} else {
			state := socks5Health[addr]
			state.failures++
			cooldown := 20 * time.Second
			if state.failures >= 3 {
				cooldown = 2 * time.Minute
			}
			state.until = time.Now().Add(cooldown)
			socks5Health[addr] = state
		}
	}
	socks5HealthMu.Unlock()

	// P2：请求结果回填（熔断 + 反馈窗口），所有路径统一收口——
	// 成功（2xx）清除熔断（半开恢复），失败累计计数触发熔断。
	applyPoolResult(addr, status, requestErr)
}

// getHTTPClientWithProxy 返回 HTTP 客户端及所用代理地址。
// round_robin 模式下通过 pickHealthyProxy 跳过冷却中代理。
func getHTTPClientWithProxy() (*http.Client, string) {
	socks5Mu.RLock()
	defer socks5Mu.RUnlock()

	if activeSocks5 == "" {
		return httpClient, ""
	}

	var proxy Socks5Proxy
	var useRR bool

	if activeSocks5 == socks5RR {
		if len(socks5Proxies) == 0 {
			return httpClient, ""
		}
		if routeMode == "round_robin" {
			// round_robin：每次请求推进游标
			start := int(atomic.AddUint32(&socks5RRIndex, 1) % uint32(len(socks5Proxies)))
			proxy = pickHealthyProxy(socks5Proxies, start)
		} else {
			// failover / smart（默认）：成功不动游标，失败（冷却）才切下一个健康代理
			// smart 额外启用健康计数/坏池/超时切换（附加层，与游标逻辑无关）
			start := int(atomic.LoadUint32(&socks5RRIndex) % uint32(len(socks5Proxies)))
			proxy = pickHealthyProxy(socks5Proxies, start)
			// 游标推进到实际选中的代理（若起始代理冷却被跳过则切换）
			for i := range socks5Proxies {
				if socks5Proxies[i].Addr == proxy.Addr {
					atomic.StoreUint32(&socks5RRIndex, uint32(i))
					break
				}
			}
		}
		useRR = true
	} else {
		if socks5Client != nil && socks5ClientAddr == activeSocks5 {
			return socks5Client, activeSocks5
		}
		var found bool
		for i := range socks5Proxies {
			if socks5Proxies[i].Addr == activeSocks5 {
				proxy = socks5Proxies[i]
				found = true
				break
			}
		}
		if !found {
			return httpClient, ""
		}
	}

	client := clientForProxy(proxy)

	if !useRR {
		socks5Client = client
		socks5ClientAddr = activeSocks5
	}
	return client, proxy.Addr
}

// clientForProxy 为指定代理构造 HTTP 客户端（复用一个可用池参数）。
// 竞速（raceCandidates）与单发路径共用，保证行为一致。
// 按代理地址缓存复用：历史实现每请求新建 http.Client+Transport，
// 连接无法跨请求复用（keep-alive 失效、TLS 握手重复）；配置变更时整体失效。
func clientForProxy(proxy Socks5Proxy) *http.Client {
	proxyClientMu.Lock()
	defer proxyClientMu.Unlock()
	if c, ok := proxyClients[proxy.Addr]; ok {
		return c
	}
	c := buildProxyClient(proxy)
	proxyClients[proxy.Addr] = c
	return c
}

func buildProxyClient(proxy Socks5Proxy) *http.Client {
	dial := socks5Dial(proxy)
	return &http.Client{
		Timeout: 300 * time.Second,
		Transport: &http.Transport{
			DialContext:         dial,
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 20,
			IdleConnTimeout:     90 * time.Second,
		},
	}
}

func getHTTPClient() *http.Client {
	client, _ := getHTTPClientWithProxy()
	return client
}

// getHTTPClientForTier 根据层级返回 HTTP 客户端
// 付费层走直连，免费层（默认）走 SOCKS5 代理（如配置）
func getHTTPClientForTier(tier TierType) *http.Client {
	client, _ := getHTTPClientForTierWithProxy(tier)
	return client
}

func getHTTPClientForTierWithProxy(tier TierType) (*http.Client, string) {
	if tier == TierPaid {
		return httpClient, ""
	}
	return getHTTPClientWithProxy()
}

// getStreamingHTTPClientForTierWithProxy keeps the connection/setup limits of
// the normal client but removes http.Client.Timeout. That field is a total
// request lifetime limit, so using it for SSE would terminate healthy long
// reasoning streams after exactly five minutes.
func getStreamingHTTPClientForTierWithProxy(tier TierType) (*http.Client, string) {
	client, proxyAddr := getHTTPClientForTierWithProxy(tier)
	if client == nil || client.Timeout == 0 {
		return client, proxyAddr
	}
	streamClient := *client
	streamClient.Timeout = 0
	return &streamClient, proxyAddr
}

// ======================== 随机 ID ========================
