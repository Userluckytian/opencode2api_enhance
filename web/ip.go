package main

import (
	"crypto/subtle"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// parseIPStrict 严格解析 IP 文本：只有能被 netip 解析成合法 IP 时才返回规范化结果。
//
// 这是修复 finding 547 的核心：旧实现里 normalizeIP 解析失败会**原样返回字符串**，
// 于是 X-Real-IP: <img src=x>、X-Forwarded-For: abc 这类垃圾值会被当成"合法 IP"
// 继续流转，最终写进 keys.json 的 ip 字段、ClaimPolicy.ClaimedToday 与日志，
// 既污染审计数据，又让"按 IP 限领/白名单"的判定面对非 IP 的垃圾值。
//
// 解析失败 → ok=false，调用方必须把该值视为"没有这个头"，绝不能原样透传。
func parseIPStrict(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return "", false
	}
	return a.Unmap().String(), true
}

// normalizeIP 统一 IPv4/IPv6 文本表示。
//
// 解析失败时返回**空串**（不再原样返回输入）：调用方据此判定"这不是一个 IP"，
// 从而拒绝把它当作 IP 落库或采信。用于内部键归一化的场景
// （store / iptracker / ippolicy / claimpolicy）语义不变——非法值一律不成为键。
func normalizeIP(s string) string {
	if ip, ok := parseIPStrict(s); ok {
		return ip
	}
	return ""
}

// clientIP 解析真实客户端 IP。
//
// 安全要点（这是限领一个 Key / IP 策略 / 网段白名单三项限制的信任基础）：
//
//  1. X-Forwarded-For 与 X-Real-IP 都是**客户端可伪造**的请求头。
//     nginx 用 `proxy_add_x_forwarded_for` 追加时，伪造头会变成
//     "9.9.9.9, <真实IP>"；而用 `proxy_set_header` 覆盖时，客户端送的值
//     会被丢弃——但**只有经过 nginx 的请求**才享受这个保护。
//  2. web-server 与 nginx 同机时，直连 18081 的请求和反代过来的请求
//     对端地址都是 127.0.0.1，**无法靠对端地址区分**。若因此采信转发头，
//     就能在服务器本机伪造任意来源 IP，所有限制被绕过。
//  3. 因此引入**共享密钥头**：nginx 转发时带上 `X-Proxy-Secret`，
//     web-server 只有在密钥匹配时才采信任何转发头；否则一律用 RemoteAddr。
//     密钥在启动时随机生成并打印，或由配置项 proxy_secret 指定。
func clientIP(r *http.Request) string {
	// 对端地址本身就是不可信输入（能被伪造的部分由下面的信任链把关），
	// 这里只做严格解析：解析失败说明连对端地址都不是合法 IP。
	host, ok := parseIPStrict(strings.Trim(strings.TrimSpace(r.RemoteAddr), "[]"))
	if !ok {
		if h, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr)); err == nil {
			host, ok = parseIPStrict(strings.Trim(h, "[]"))
		}
	}
	if !ok {
		return ""
	}

	// 不在可信代理名单 → 直接拒绝一切转发头。
	if !a_trustedProxy(host) {
		return host
	}
	// 可信代理也必须有正确的共享密钥，否则视为直连（防止本机伪造）。
	// proxySecret 为空即视为未启用该机制（绝不因为缺省值而放宽）。
	if !proxySecretValid(r.Header.Get("X-Proxy-Secret")) {
		return host
	}
	// nginx 用 proxy_set_header 覆盖写入 X-Real-IP，值不可伪造。
	// 严格解析：非法值一律视为"没有这个头"，绝不原样采信（修复 finding 547）。
	if xr, ok := parseIPStrict(r.Header.Get("X-Real-IP")); ok {
		return xr
	}
	// 退路：取 XFF 最右一跳（代理追加的即为真实客户端）。
	// 同样严格解析：垃圾值被跳过，继续往左找；全都非法则回落到对端地址。
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		for i := len(parts) - 1; i >= 0; i-- {
			if p, ok := parseIPStrict(parts[i]); ok {
				return p
			}
		}
	}
	return host
}

// proxySecret 是反代共享密钥；为空表示不启用该机制（此时只靠可信代理名单）。
var proxySecret string

// SetProxySecret 配置反代共享密钥。
func SetProxySecret(s string) { proxySecret = strings.TrimSpace(s) }

func proxySecretValid(got string) bool {
	if proxySecret == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(proxySecret)) == 1
}

// 可信代理网段：默认只信本机（nginx 与 web-server 同机部署）。
// 直连 18081 绕过 nginx 的访问者，其 RemoteAddr 不在此列表 → 不采信转发头。
var trustedProxyCIDRs = []string{"127.0.0.1/32", "::1/128"}

// SetTrustedProxies 配置可信代理网段（供 main 启动时从配置读取）。
func SetTrustedProxies(cidrs []string) {
	if len(cidrs) == 0 {
		return
	}
	trustedProxyCIDRs = cidrs
}

func a_trustedProxy(host string) bool {
	addr, err := netip.ParseAddr(normalizeIP(host))
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	for _, c := range trustedProxyCIDRs {
		pfx, err := netip.ParsePrefix(c)
		if err != nil {
			continue
		}
		if pfx.Contains(addr) {
			return true
		}
	}
	return false
}

// 登录限流参数（集中定义，便于审计与测试引用）。
//
// 阈值从 10 次/10 分钟收紧到 5 次/10 分钟：finding 544 已证并发齐射会把
// 「先 Allow 后 Fail」的两段式判定放大成每分钟数十次真实口令校验；
// 修复 TryAttempt 原子语义后单次窗口内的真实校验机会回到阈值本身，
// 再配合口令强度下限提高到 8 位（web/passwordpolicy.go），
// 5 次/10 分钟足以挡住在线爆破，又不会把同一出口 IP 的管理员误锁死
// （锁定 1 分钟后自动恢复）。
const (
	// LoginMaxAttempts 是同一 IP 在一个窗口内允许的真实登录尝试次数。
	LoginMaxAttempts = 5
	// LoginWindow 是尝试次数统计窗口。
	LoginWindow = 10 * time.Minute
	// LoginLockDuration 是触发阈值后的锁定时长。
	LoginLockDuration = time.Minute
)

// LoginLimiter 登录失败限速：窗口内失败达到上限后锁定一段时间。
type LoginLimiter struct {
	mu       sync.Mutex
	failures map[string]*loginAttempt
	max      int
	window   time.Duration
	lock     time.Duration
}

type loginAttempt struct {
	count       int
	first       time.Time
	lockedUntil time.Time
}

func NewLoginLimiter(max int, window, lock time.Duration) *LoginLimiter {
	return &LoginLimiter{failures: map[string]*loginAttempt{}, max: max, window: window, lock: lock}
}

// Allow 返回是否允许尝试登录，以及锁定剩余时间。
//
// 注意：这是**只读判定**，不占用失败配额。登录入口必须改用 TryAttempt，
// 否则「先 Allow 判断、后 Fail 计数」之间会存在窗口，并发请求可一起通过判定
// （finding 544：实测 30 并发有 24 次真正执行了口令校验）。
func (l *LoginLimiter) Allow(ip string, now time.Time) (bool, time.Duration) {
	if l == nil || l.max <= 0 {
		return true, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	a := l.failures[ip]
	if a == nil {
		return true, 0
	}
	if now.Before(a.lockedUntil) {
		return false, a.lockedUntil.Sub(now)
	}
	if now.Sub(a.first) > l.window {
		delete(l.failures, ip)
	}
	return true, 0
}

// TryAttempt 原子地完成「判定 + 记账」：在同一个临界区内检查锁定状态并把本次尝试
// 计入失败配额（乐观计入，成功后再由 Success 清除）。
//
// 这样并发齐射时每个请求都会真实消耗一个配额，杜绝了旧实现中
// Allow 与 Fail 之间被并发穿透的问题：第 max 次之后的请求无论并发多少都会被 429。
// 返回值语义与 Allow 一致：ok=false 表示本次不允许尝试（已锁定），remain 为剩余时间。
func (l *LoginLimiter) TryAttempt(ip string, now time.Time) (bool, time.Duration) {
	if l == nil || l.max <= 0 {
		return true, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	a := l.failures[ip]
	if a != nil && now.Before(a.lockedUntil) {
		return false, a.lockedUntil.Sub(now)
	}
	if a == nil || now.Sub(a.first) > l.window {
		a = &loginAttempt{first: now}
		l.failures[ip] = a
	}
	a.count++
	if a.count >= l.max {
		// 本次仍放行（与旧语义一致：第 max 次能真正校验口令），但立刻置锁，
		// 后续并发请求在锁内即被拒绝。
		a.lockedUntil = now.Add(l.lock)
		a.count = 0
		a.first = now
	}
	return true, 0
}

// Fail 记录一次失败。保留给需要「先执行再记账」的调用方与测试使用；
// HTTP 登录入口已改用原子的 TryAttempt。
func (l *LoginLimiter) Fail(ip string, now time.Time) {
	if l == nil || l.max <= 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	a := l.failures[ip]
	if a == nil || now.Sub(a.first) > l.window {
		a = &loginAttempt{first: now}
		l.failures[ip] = a
	}
	a.count++
	if a.count >= l.max {
		a.lockedUntil = now.Add(l.lock)
		a.count = 0
		a.first = now
	}
}

func (l *LoginLimiter) Success(ip string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	delete(l.failures, ip)
	l.mu.Unlock()
}
