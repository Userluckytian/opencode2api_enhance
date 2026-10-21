package main

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// isPrivateIPv4 判断是否私有/内网网段（含 CGNAT 100.64/10）。
func isPrivateIPv4(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsPrivate() {
		return true
	}
	// 100.64.0.0/10 运营商级 NAT，局域网常见
	return ip[0] == 100 && ip[1] >= 64 && ip[1] <= 127
}

// lanIPv4 返回本机可用于对外访问的 IPv4：优先私有网段，其次任意非回环地址。
func lanIPv4() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	fallback := ""
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipnet.IP.To4()
			if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
				continue
			}
			s := ip.String()
			if isPrivateIPv4(ip) {
				return s
			}
			if fallback == "" {
				fallback = s
			}
		}
	}
	return fallback
}

// hostPort 拆出 host 与 port；没有端口时 port 为空。
func hostPort(host string) (string, string) {
	host = strings.TrimSpace(host)
	if host == "" {
		return "", ""
	}
	if h, p, err := net.SplitHostPort(host); err == nil {
		return strings.Trim(h, "[]"), p
	}
	return strings.Trim(host, "[]"), ""
}

// isLocalHost 判断 host 是否指向本机（回环 / 0.0.0.0 / 空）。
// 这类地址发给使用者是无效的（他们会连到自己的电脑），需要换成本机内网 IP。
func isLocalHost(host string) bool {
	h, _ := hostPort(host)
	switch strings.ToLower(h) {
	case "", "localhost", "0.0.0.0", "::", "::1":
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback() || ip.IsUnspecified()
	}
	return false
}

/* ------------------------------------------------------------------ */
/* 对外访问地址（public_base_url）——修复 finding 548                    */
/* ------------------------------------------------------------------ */

// publicBaseHostPattern 是对外地址里允许出现的**主机名**白名单：
// 由字母数字与短横线组成的标签、点分隔；不允许下划线、空格、@、\ 等。
var publicBaseHostPattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$`)

// publicBaseSegPattern 是对外地址路径里允许出现的段字符集（字母数字 . _ ~ -）。
var publicBaseSegPattern = regexp.MustCompile(`^[A-Za-z0-9._~-]+$`)

// forwardedPrefixPattern 是反代前缀（X-Forwarded-Prefix）允许出现的形状：
// 以 / 开头，每段只允许字母数字与 . _ ~ -，段数不限。
var forwardedPrefixPattern = regexp.MustCompile(`^/[A-Za-z0-9._~-]+(/[A-Za-z0-9._~-]+)*$`)

// EnvPublicBaseURL 是部署方注入「对外访问地址」的环境变量名。
// 优先级：config.json 的 public_base_url > 本环境变量 > （都没有）拒绝下发 base_url。
const EnvPublicBaseURL = "TOKEN_BUFFET_PUBLIC_BASE_URL"

// ValidatePublicBaseURL 校验部署方配置的对外访问地址。
//
// 只允许 http/https；禁止 userinfo（user:pass@host）；禁止查询串与片段；
// 主机名必须命中白名单正则或是一个合法 IP 字面量；端口必须是合法数字端口；
// 路径逐段校验，拒绝 . / .. 与非法字符。
//
// public_base_url 是**部署方配置**而非请求可控数据，因此这里做的是
// 「配置写错就别下发」的可用性 + 卫生校验，而不是信任边界判断。
// 真正的信任边界在 buildBaseURL：对外地址只允许来自这份配置。
func ValidatePublicBaseURL(raw string) error {
	s := strings.TrimSpace(raw)
	if s == "" {
		return errors.New("public_base_url 未配置")
	}
	if strings.ContainsAny(s, " \t\r\n\v\f") {
		return errors.New("public_base_url 含空白字符")
	}
	u, err := url.Parse(s)
	if err != nil {
		return fmt.Errorf("public_base_url 解析失败: %v", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("public_base_url 的 scheme 只允许 http 或 https")
	}
	if u.User != nil {
		return errors.New("public_base_url 不得包含用户信息（userinfo）")
	}
	if u.Opaque != "" {
		return errors.New("public_base_url 格式非法")
	}
	if strings.ContainsAny(u.Host, `@\`) {
		return errors.New("public_base_url 的主机部分非法")
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return errors.New("public_base_url 不得包含查询串或片段")
	}
	host := u.Hostname()
	if host == "" {
		return errors.New("public_base_url 缺少主机名")
	}
	if port := u.Port(); port != "" {
		n, perr := strconv.Atoi(port)
		if perr != nil || n < 1 || n > 65535 {
			return errors.New("public_base_url 的端口非法")
		}
	}
	if ip, ierr := netip.ParseAddr(host); ierr == nil {
		if ip.IsUnspecified() {
			return errors.New("public_base_url 不得指向 0.0.0.0 / ::")
		}
	} else if !publicBaseHostPattern.MatchString(host) {
		return errors.New("public_base_url 的主机名不合法")
	}
	for _, seg := range strings.Split(strings.Trim(u.Path, "/"), "/") {
		if seg == "" {
			continue
		}
		if seg == "." || seg == ".." {
			return errors.New("public_base_url 的路径不得包含 . 或 ..")
		}
		if !publicBaseSegPattern.MatchString(seg) {
			return errors.New("public_base_url 的路径段含非法字符")
		}
	}
	return nil
}

// sanitizeForwardedPrefix 校验并归一化反代前缀（X-Forwarded-Prefix）。
//
// 只接受 ^/[A-Za-z0-9._~-]+(/[A-Za-z0-9._~-]+)*$：含 .. 、空段（重复斜杠）、
// 其它字符一律丢弃（返回空串），绝不把客户端给的值原样拼进对外地址
// （旧实现只做 TrimSuffix 后拼接，"/../../x" 会被原样下发）。
func sanitizeForwardedPrefix(raw string) string {
	p := strings.TrimSpace(raw)
	if p == "" {
		return ""
	}
	if !forwardedPrefixPattern.MatchString(p) {
		return ""
	}
	for _, seg := range strings.Split(strings.Trim(p, "/"), "/") {
		if seg == "" || seg == "." || seg == ".." {
			return ""
		}
	}
	return p
}

// buildBaseURL 计算下发给客户端的 base_url。
//
// 信任边界（修复 finding 548）：**只信任部署方配置的 public_base_url**。
// 旧实现的优先级是「public_base_url → X-Forwarded-Host → Host」，而后两个
// 都由客户端控制：直连 18081 或经 nginx 时只要带上
// `X-Forwarded-Host: evil.example.com`，下发的 base_url 就变成
// http://evil.example.com/v1，使用者会把 Bearer Key 发到攻击者域名。
//
// 现在的规则：
//   - public_base_url 缺失或非法 → 返回错误，调用方 fail-closed 拒绝下发，
//     绝不回退到任何客户端可控的请求头；
//   - public_base_url 带了路径（如 /token-buffet）就直接用它；
//   - 没带路径时，才补上**已校验**的 X-Forwarded-Prefix（反代前缀）。
func buildBaseURL(publicBase, fwdPrefix, lanIP string) (string, error) {
	base := strings.TrimSpace(publicBase)
	if base == "" {
		return "", errors.New("public_base_url 未配置：请配置对外访问地址（config.json 的 public_base_url 或环境变量 " + EnvPublicBaseURL + "）")
	}
	if err := ValidatePublicBaseURL(base); err != nil {
		return "", err
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("public_base_url 解析失败: %v", err)
	}
	// 配置里写了 127.0.0.1 / localhost 这类「只有本机能访问」的地址时，
	// 换成本机内网 IP（服务端自己算出来的，不含任何客户端可控输入）。
	host := u.Host
	if lanIP != "" && isLocalHost(host) {
		if _, port := hostPort(host); port != "" {
			host = net.JoinHostPort(lanIP, port)
		} else {
			host = lanIP
		}
	}
	path := strings.TrimRight(u.Path, "/")
	if path == "" {
		if pre := sanitizeForwardedPrefix(fwdPrefix); pre != "" {
			path = pre
		}
	}
	out := u.Scheme + "://" + host + path
	if !strings.HasSuffix(out, "/v1") {
		out += "/v1"
	}
	return out, nil
}
