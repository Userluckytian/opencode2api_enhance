package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// 上游地址安全策略（对应 finding 541 / fact 569：管理端可把 upstream_base_url
// 指向任意地址，形成「管理端 SSRF」面 —— 攻击者拿到管理员会话后可让服务端去访问
// 内网服务、云元数据或其它本机端口，再通过 /v1 响应或 /api/admin/models 的报错回显读取结果）。
//
// 修复思路（四道闸门）：
//  0. 白名单来源：只允许部署方通过 config.json 的 upstream_allowed_hosts 或环境变量
//     下发，**不通过管理 API 下发**（否则拿到管理员会话的攻击者只要把自己的目标
//     加进白名单就能绕过整套防护）。默认白名单仅回环，兼容现网 http://127.0.0.1:18080/v1；
//  1. 写入时校验（ValidateUpstreamBaseURL）：scheme 只允许 http/https，禁止 userinfo，
//     解析主机名并对每个 IP 做归类，命中禁止网段且不在白名单 → 400 拒绝；
//  2. 连接时校验（upstreamGuard.DialContext）：真正拨号前再判定一次目标 IP，
//     覆盖 DNS Rebinding（写入时解析到公网、连接时解析到内网）与配置热更新；
//  3. 重定向校验（guardedCheckRedirect）：最多 5 跳，每一跳都重新走第 1 条的校验，
//     并剥离跨主机 Authorization。
var (
	errUpstreamScheme     = errors.New("上游地址只允许 http/https")
	errUpstreamUserinfo   = errors.New("上游地址不允许包含用户信息")
	errUpstreamHostEmpty  = errors.New("上游地址缺少主机名")
	errUpstreamBlockedIP  = errors.New("上游地址指向受限制的内网/保留地址")
	errUpstreamDnsFail    = errors.New("上游地址无法解析")
	errUpstreamTooManyHop = errors.New("上游重定向次数过多")
)

// upstreamAllowedHostsEnv 是「显式放行的上游主机白名单」的环境变量入口（逗号分隔）。
// 只给部署方使用；管理 API 不暴露该字段，避免管理员会话被攻破后自行扩白名单。
const upstreamAllowedHostsEnv = "TOKEN_BUFFET_UPSTREAM_ALLOWED_HOSTS"

// defaultUpstreamAllowedHosts 返回默认允许的回环主机：同机部署（nginx / 上游网关
// 监听 127.0.0.1）是本项目的默认形态，因此默认白名单里保留回环地址。
// 回环地址始终作为默认项保留（upstream_allowed_hosts 只能追加，不能移除回环）。
func defaultUpstreamAllowedHosts() []string {
	return []string{"127.0.0.1", "localhost", "::1"}
}

// ResolveUpstreamAllowedHosts 汇总部署方下发的上游白名单：
// 默认回环 + config.json 的 upstream_allowed_hosts + 环境变量（逗号分隔）三者并集。
func ResolveUpstreamAllowedHosts(cfgAllowed []string) []string {
	raw := make([]string, 0, len(cfgAllowed)+4)
	raw = append(raw, defaultUpstreamAllowedHosts()...)
	raw = append(raw, cfgAllowed...)
	for _, part := range strings.Split(os.Getenv(upstreamAllowedHostsEnv), ",") {
		raw = append(raw, part)
	}
	return normalizeAllowedHosts(raw)
}

func normalizeAllowedHosts(raw []string) []string {
	seen := make(map[string]struct{}, len(raw))
	out := make([]string, 0, len(raw))
	for _, h := range raw {
		h = normalizeHostKey(h)
		if h == "" {
			continue
		}
		if _, ok := seen[h]; ok {
			continue
		}
		seen[h] = struct{}{}
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

func normalizeHostKey(h string) string {
	return strings.ToLower(strings.TrimSpace(strings.Trim(h, "[]")))
}

// upstreamGuard 保存当前的白名单并在拨号时执行 IP 归类检查。
type upstreamGuard struct {
	mu      sync.RWMutex
	allowed map[string]struct{}
	dialer  *net.Dialer
	// lookup 允许单测注入解析结果（生产为 net.DefaultResolver），
	// 这样才能在无外网的环境里覆盖「域名解析到内网 IP」的分支。
	lookup func(ctx context.Context, host string) ([]netip.Addr, error)
}

func newUpstreamGuard(hosts []string) *upstreamGuard {
	g := &upstreamGuard{dialer: &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}}
	g.SetAllowedHosts(hosts)
	return g
}

// SetAllowedHosts 热替换白名单（仅启动加载 / 部署方配置变更时调用）。
func (g *upstreamGuard) SetAllowedHosts(hosts []string) {
	m := make(map[string]struct{}, len(hosts))
	for _, h := range hosts {
		h = normalizeHostKey(h)
		if h != "" {
			m[h] = struct{}{}
		}
	}
	g.mu.Lock()
	g.allowed = m
	g.mu.Unlock()
}

// AllowedHosts 返回当前白名单副本（只读展示用）。
func (g *upstreamGuard) AllowedHosts() []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]string, 0, len(g.allowed))
	for h := range g.allowed {
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

func (g *upstreamGuard) hostAllowed(host string) bool {
	host = normalizeHostKey(host)
	g.mu.RLock()
	defer g.mu.RUnlock()
	_, ok := g.allowed[host]
	return ok
}

// blockedIP 判断 IP 是否属于禁止访问的保留网段。
// 说明：
//   - 回环 / RFC1918 / 链路本地 只有在白名单中显式列出时才放行；
//   - 云元数据特殊地址（阿里云 100.100.100.200、AWS/GCP/Azure 169.254.169.254、
//     AWS IPv6 fd00:ec2::254）永远拒绝，即使白名单里写了也不放行；
//   - 未指定地址、组播、IPv6 链路本地同样拒绝。
func blockedIP(a netip.Addr) bool {
	if !a.IsValid() {
		return true
	}
	a = a.Unmap()
	if a.IsUnspecified() || a.IsMulticast() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() {
		return true
	}
	if a.IsLoopback() || a.IsPrivate() {
		return true
	}
	// 100.64.0.0/10 CGNAT：本项目的局域网探测会用到，但作为「上游」目标时仍属内网，
	// 需要白名单显式放行（该网段在 IsPrivate 之外，需单独判断）。
	if a.Is4() {
		b := a.As4()
		if b[0] == 100 && b[1] >= 64 && b[1] <= 127 {
			return true
		}
	}
	return false
}

// isMetadataAddr 判断是否云厂商元数据地址：任何情况下都拒绝，白名单也不能覆盖。
func isMetadataAddr(a netip.Addr) bool {
	if !a.IsValid() {
		return false
	}
	a = a.Unmap()
	for _, c := range []string{
		"169.254.169.254/32", // AWS / GCP / Azure IMDS
		"100.100.100.200/32", // 阿里云 metadata
		"100.100.100.0/24",   // 阿里云 metadata 网段
		"fd00:ec2::254/128",  // AWS IMDS IPv6
	} {
		if p, err := netip.ParsePrefix(c); err == nil && p.Contains(a) {
			return true
		}
	}
	return false
}

func (g *upstreamGuard) lookupHost(ctx context.Context, host string) ([]netip.Addr, error) {
	if g.lookup != nil {
		return g.lookup(ctx, host)
	}
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

// checkHost 解析主机名并逐个 IP 判定；host 在白名单中时直接放行（但仍跳过元数据地址）。
func (g *upstreamGuard) checkHost(ctx context.Context, host string) ([]netip.Addr, error) {
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if host == "" {
		return nil, errUpstreamHostEmpty
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		addr = addr.Unmap()
		if isMetadataAddr(addr) {
			return nil, fmt.Errorf("%w: %s", errUpstreamBlockedIP, addr)
		}
		if blockedIP(addr) && !g.hostAllowed(addr.String()) {
			return nil, fmt.Errorf("%w: %s", errUpstreamBlockedIP, addr)
		}
		return []netip.Addr{addr}, nil
	}
	// 主机名：先看白名单，避免对白名单域名做多余的 DNS 归类（其解析结果可能随时间变化）。
	// 注意这里只跳过「默认保留网段」判定，元数据地址仍由下面的解析结果判定兜住；
	// 白名单域名若解析到元数据地址同样被拒（见 DialContext）。
	if g.hostAllowed(host) {
		return nil, nil
	}
	addrs, err := g.lookupHost(ctx, host)
	if err != nil || len(addrs) == 0 {
		return nil, fmt.Errorf("%w: %s", errUpstreamDnsFail, host)
	}
	for _, a := range addrs {
		a = a.Unmap()
		if isMetadataAddr(a) {
			return nil, fmt.Errorf("%w: %s -> %s", errUpstreamBlockedIP, host, a)
		}
		if blockedIP(a) && !g.hostAllowed(a.String()) {
			return nil, fmt.Errorf("%w: %s -> %s", errUpstreamBlockedIP, host, a)
		}
	}
	return addrs, nil
}

// DialContext 在真正建立连接前对解析出的目标 IP 做最终检查。
// 这是防 DNS Rebinding 的关键：写入时的解析结果与实际连接时的解析结果可能不同。
func (g *upstreamGuard) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	host = strings.Trim(host, "[]")
	// 拨号时的最终检查：即使主机名在白名单里，只要解析结果是元数据地址也拒绝。
	if hostAllowed := g.hostAllowed(host); hostAllowed {
		if ip, perr := netip.ParseAddr(host); perr == nil && isMetadataAddr(ip) {
			return nil, fmt.Errorf("%w: %s", errUpstreamBlockedIP, host)
		}
	} else if _, err := g.checkHost(ctx, host); err != nil {
		return nil, err
	}
	conn, err := g.dialer.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	// 二次确认对端地址（防 DNS 在 dial 内部解析到内网）：白名单主机名解析到元数据地址
	// 的情况在此兜底，直接关闭连接。
	if ra := conn.RemoteAddr(); ra != nil {
		if tcp, ok := ra.(*net.TCPAddr); ok {
			if ip, ok2 := netip.AddrFromSlice(tcp.IP); ok2 {
				ip = ip.Unmap()
				if isMetadataAddr(ip) || (blockedIP(ip) && !g.hostAllowed(ip.String())) {
					_ = conn.Close()
					return nil, fmt.Errorf("%w: %s -> %s", errUpstreamBlockedIP, host, ip)
				}
			}
		}
	}
	return conn, nil
}

// guardLocked 返回 App 的 SSRF 防护器；调用方必须持有 a.guardMu。
func (a *App) guardLocked() *upstreamGuard {
	if a.Guard == nil {
		var cfgAllowed []string
		if a.Cfg != nil {
			cfgAllowed = a.Cfg.UpstreamAllowedHosts
		}
		a.Guard = newUpstreamGuard(ResolveUpstreamAllowedHosts(cfgAllowed))
	}
	return a.Guard
}

// guard 返回 App 的 SSRF 防护器；测试里直接构造 &App{} 时按默认白名单惰性初始化，
// 保证 handlePutSettings 在任何构造路径下都不会因为 Guard 为 nil 而跳过校验。
func (a *App) guard() *upstreamGuard {
	if a.Guard != nil {
		return a.Guard
	}
	a.guardMu.Lock()
	defer a.guardMu.Unlock()
	return a.guardLocked()
}

// client 返回 App 的上游 HTTP 客户端。生产路径由 main.go 注入 newUpstreamClient(guard)；
// 若某个构造路径（如单测里的 &App{}）没有注入，则惰性创建同样受保护的客户端，
// 确保任何代码路径都不会退回到「裸 http.Client」。
func (a *App) client() *http.Client {
	if a.Client != nil {
		return a.Client
	}
	a.guardMu.Lock()
	defer a.guardMu.Unlock()
	if a.Client == nil {
		a.Client = newUpstreamClient(a.guardLocked())
	}
	return a.Client
}
// ValidateUpstreamBaseURL 校验管理员提交的上游基址。
// 空字符串表示「不配置上游」，直接放行（业务层会给出未配置提示）。
func ValidateUpstreamBaseURL(raw string, g *upstreamGuard) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if g == nil {
		return errors.New("上游地址校验器未初始化")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("上游地址格式错误: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errUpstreamScheme
	}
	if u.User != nil {
		return errUpstreamUserinfo
	}
	if u.Hostname() == "" {
		return errUpstreamHostEmpty
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = g.checkHost(ctx, u.Hostname())
	return err
}

// newUpstreamClient 构造带 SSRF 防护与有界重定向的上游 HTTP 客户端。
// 原实现是 &http.Client{Timeout: 300s}：任意地址、无限跟随重定向（默认 10 跳）。
func newUpstreamClient(g *upstreamGuard) *http.Client {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		base = &http.Transport{Proxy: http.ProxyFromEnvironment}
	}
	tr := base.Clone()
	// 不走上游代理：一旦走 HTTP(S)_PROXY，实际拨号目标变成代理地址，
	// 校验对象与真实请求目标分离，SSRF 判定会被绕过；这里统一直连由 guard 校验。
	tr.Proxy = nil
	tr.DialContext = g.DialContext
	return &http.Client{
		Timeout:   300 * time.Second,
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errUpstreamTooManyHop
			}
			// 重定向的每一跳都重新校验（防止 302 跳到内网/元数据）。
			if err := ValidateUpstreamBaseURL(req.URL.String(), g); err != nil {
				return err
			}
			// 不把上游凭据带到其它主机（跨主机即剥离 Authorization）。
			if len(via) > 0 && req.URL.Host != via[0].URL.Host {
				req.Header.Del("Authorization")
			}
			return nil
		},
	}
}





