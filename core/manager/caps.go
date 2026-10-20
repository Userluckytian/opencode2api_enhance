// 协议能力矩阵（阶段 X T1）：协议别名 canonical + 「协议 → 内核能力/UDP/无凭据/强制TLS」唯一数据源。
// 设计借鉴 uniproxy（internal/importer/native.go:53-67、values.go:233-249）；
// 与它的关键差异：本项目的执行内核（sing-box 官方 1.13.x）**不支持 SSR**，
// 因此能力拆成两个维度——「解析层认识」（caps 表有行）与「可转发」（singbox=true）。
// 新增内核（如 mihomo）时只需扩列，不改消费方。
package manager

import "strings"

// udpMode 协议 UDP 能力三态（口径对齐 uniproxy native.go 注释「能力是解析产物，不是猜测」）。
type udpMode int

const (
	udpNone   udpMode = iota // 协议无 UDP 通道（恒 false）
	udpOptIn                  // 订阅带 udp 字段才为 true（默认 false）
	udpForce                  // 协议本身走 UDP（恒 true，订阅关不掉）
)

// protoCap 单个协议的能力定义。canonical 用于键与 outbound type 输出。
type protoCap struct {
	canonical string
	singbox   bool    // sing-box 内核可转发
	reason    string  // 不可转发时的完整原因（进 UI/API 文案，必须自解释）
	udp       udpMode // UDP 能力三态
	authless  bool    // 无凭据也算可用（http/socks 白名单机房形态）
	forceTLS  bool    // 协议强制 TLS（订阅里的 tls:false 是脏数据，忽略）
}

// caps 表：canonical 名 → 能力。所有类型字面量名单的唯一定义处。
// singbox=false 的行（ssr/snell）依然参与解析与导出，只是永不生成实例。
var caps = map[string]protoCap{
	"trojan":     {canonical: "trojan", singbox: true, udp: udpOptIn},
	"vless":      {canonical: "vless", singbox: true, udp: udpOptIn},
	"vmess":      {canonical: "vmess", singbox: true, udp: udpOptIn},
	"ss":         {canonical: "ss", singbox: true, udp: udpOptIn},
	"hysteria2":  {canonical: "hysteria2", singbox: true, udp: udpForce, forceTLS: true},
	"hysteria":   {canonical: "hysteria", singbox: true, udp: udpForce, forceTLS: true},
	"tuic":       {canonical: "tuic", singbox: true, udp: udpForce, forceTLS: true},
	"anytls":     {canonical: "anytls", singbox: true, udp: udpOptIn, forceTLS: true},
	"wireguard":  {canonical: "wireguard", singbox: true, udp: udpForce},
	"http":       {canonical: "http", singbox: true, udp: udpNone, authless: true},
	"https":      {canonical: "https", singbox: true, udp: udpNone, authless: true},
	"socks":      {canonical: "socks5", singbox: true, udp: udpOptIn, authless: true},
	"socks5":     {canonical: "socks5", singbox: true, udp: udpOptIn, authless: true},
	"socks5h":    {canonical: "socks5", singbox: true, udp: udpOptIn, authless: true},
	"socks4":     {canonical: "socks4", singbox: true, udp: udpNone, authless: true},
	"ssr":        {canonical: "ssr", singbox: false, reason: "sing-box 内核不支持 SSR（上游已移除该协议），节点可导出到第三方客户端使用", udp: udpOptIn},
	"snell":      {canonical: "snell", singbox: false, reason: "sing-box 内核不支持 snell，节点可导出到第三方客户端使用", udp: udpOptIn},
}

// protoAliases 别名 → canonical（uniproxy canonicalProtocol 同款单表；含 singbox.go 里的历史写法）。
var protoAliases = map[string]string{
	"shadowsocks":  "ss",
	"ss":           "ss",
	"hy2":          "hysteria2",
	"hysteria2":    "hysteria2",
	"hy":           "hysteria",
	"hy1":          "hysteria",
	"hysteria":     "hysteria",
	"wg":           "wireguard",
	"wireguard":    "wireguard",
	"socks5":       "socks5",
	"socks5h":      "socks5",
	"socks":        "socks5",
	"socks4":       "socks4",
	"sock":         "socks5",
	"vmess":        "vmess",
	"vless":        "vless",
	"trojan":       "trojan",
	"tuic":         "tuic",
	"anytls":       "anytls",
	"http":         "http",
	"https":        "https",
	"ssr":          "ssr",
	"shadowsocksr": "ssr",
	"snell":        "snell",
}

// canonicalProtocol 归一化协议名；未知返回 ""。大小写不敏感（与历史 strings.ToLower 口径一致）。
func canonicalProtocol(t string) string {
	return protoAliases[strings.ToLower(strings.TrimSpace(t))]
}

// capOf 查协议能力；未知协议 ok=false（调用方按「不认识」处理，不当成不支持转发）。
func capOf(t string) (protoCap, bool) {
	c, ok := caps[canonicalProtocol(t)]
	return c, ok
}

// kernelUsable 该节点类型能否交给 sing-box 内核转发。未知协议返回 false（配 reason 引导）。
func kernelUsable(t string) (bool, string) {
	c, ok := capOf(t)
	if !ok {
		return false, "未知节点类型，sing-box 内核无法转发"
	}
	if !c.singbox {
		return false, c.reason
	}
	return true, ""
}

// nodeUDP 节点的 UDP 能力（三态规则见 udpMode 注释）；nodeHasUDP 为订阅显式声明值。
func nodeUDP(t string, nodeHasUDP bool) bool {
	c, ok := capOf(t)
	if !ok {
		return false
	}
	switch c.udp {
	case udpForce:
		return true
	case udpOptIn:
		return nodeHasUDP
	default: // udpNone
		return false
	}
}
