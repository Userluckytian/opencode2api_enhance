// sing-box 客户端配置生成（Rust singbox.rs 移植）。
// 每个节点类型产出对应 outbound；本地 SOCKS 入站监听 127.0.0.1:listenPort。
package manager

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// buildSingboxConfig 生成 sing-box 配置 JSON。
func buildSingboxConfig(node ClashNode, listenPort uint16) ([]byte, error) {
	outbound, err := singboxOutbound(node)
	if err != nil {
		return nil, err
	}
	cfg := map[string]any{
		"log":      map[string]any{"level": "warn", "timestamp": true},
		"inbounds": []any{map[string]any{"type": "socks", "listen": "127.0.0.1", "listen_port": listenPort}},
		"outbounds": []any{
			outbound,
			map[string]any{"type": "direct", "tag": "direct"},
		},
		// DoH 解析目标/节点域名，绕过被劫持的系统 DNS（Clash TUN fake-ip 等）。
		// 多服务器冗余 + 直连出站（不套代理，避免 DNS 依赖代理先解析的鸡生蛋问题）。
		"dns": map[string]any{
			"servers": []any{
				map[string]any{"type": "https", "tag": "ali-doh", "server": "223.5.5.5", "server_port": 443},
				map[string]any{"type": "https", "tag": "tencent-doh", "server": "119.29.29.29", "server_port": 443},
				map[string]any{"type": "https", "tag": "google-doh", "server": "8.8.8.8", "server_port": 443},
			},
			"strategy": "ipv4_only",
			"final":    "ali-doh",
		},
		"route": map[string]any{
			"final": "proxy",
			// sing-box 1.12+ 要求显式指定默认域名解析器（否则 FATAL）。
			// 注意：不能加 {outbound:"direct", protocol:"dns", port:443} 这类 rule——它会把 443 端口的
			// DoH/业务流量也路由到 direct，导致探测失败。
			"default_domain_resolver": "ali-doh",
		},
	}
	return json.MarshalIndent(cfg, "", "  ")
}

// BuildSingboxConfigFor 导出包装（main 装配实例接缝用）。
func BuildSingboxConfigFor(node ClashNode, listenPort uint16) ([]byte, error) {
	return buildSingboxConfig(node, listenPort)
}

// parseBandwidthMbps 将 Clash hysteria2 的带宽字段解析为 Mbps 数值（Rust singbox.rs 移植）。
// 支持纯数字（"100"）、带单位字符串（"100 Mbps"）、单位换算（"1.5 Gbps" → 1500）；
// 无法解析时返回 nil（调用方省略该字段，sing-box 接受无带宽限速的 hy2 配置）。
func parseBandwidthMbps(s string) any {
	t := strings.TrimSpace(s)
	if t == "" {
		return nil
	}
	i := 0
	for i < len(t) && (t[i] == '.' || (t[i] >= '0' && t[i] <= '9')) {
		i++
	}
	num := t[:i]
	unit := strings.ToLower(t[i:])
	value, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return nil
	}
	var mbps float64
	switch {
	case strings.Contains(unit, "gbps") || strings.Contains(unit, "gbit"):
		mbps = value * 1000.0
	case strings.Contains(unit, "mbps") || strings.Contains(unit, "mbit"):
		mbps = value
	case strings.Contains(unit, "kbps") || strings.Contains(unit, "kbit"):
		mbps = value / 1000.0
	case strings.Contains(unit, "mb") || strings.Contains(unit, "m"):
		mbps = value
	case strings.Contains(unit, "gb") || strings.Contains(unit, "g"):
		mbps = value * 1000.0
	default:
		mbps = value
	}
	return uint64(mbps)
}

// buildSingboxOutbound 按类型构建 outbound。
func singboxOutbound(n ClashNode) (map[string]any, error) {
	tlsOn := func(def bool) bool {
		if n.TLS == nil {
			return def
		}
		return *n.TLS
	}
	serverName := func(def string) string {
		if n.ServerName != "" {
			return n.ServerName
		}
		if n.SNI != "" {
			return n.SNI
		}
		return def
	}
	insecure := func(def bool) bool {
		if n.SkipCertVerify == nil {
			return def
		}
		return *n.SkipCertVerify
	}

	switch strings.ToLower(n.NodeType) {
	case "trojan":
		if n.Password == "" {
			return nil, errors.New("缺少 password")
		}
		return map[string]any{
			"type":        "trojan",
			"tag":         "proxy",
			"server":      n.Server,
			"server_port": n.Port,
			"password":    n.Password,
			"tls": map[string]any{
				"enabled":     tlsOn(true),
				"server_name": serverName(n.Server),
				"insecure":    insecure(false),
			},
		}, nil
	case "vless":
		if n.UUID == "" {
			return nil, errors.New("缺少 uuid")
		}
		out := map[string]any{
			"type":        "vless",
			"tag":         "proxy",
			"server":      n.Server,
			"server_port": n.Port,
			"uuid":        n.UUID,
		}
		tls := map[string]any{
			"enabled":     tlsOn(true),
			"server_name": serverName(n.Server),
		}
		if n.RealityPublicKey != "" {
			reality := map[string]any{"enabled": true, "public_key": n.RealityPublicKey}
			if n.RealityShortID != "" {
				reality["short_id"] = n.RealityShortID
			}
			tls["reality"] = reality
			fp := n.ClientFingerprint
			if fp == "" {
				fp = "chrome"
			}
			// utls 必须是 tls 的子对象（放顶层 sing-box 报 unknown field "utls" 启动即崩）。
			tls["utls"] = map[string]any{"enabled": true, "fingerprint": fp}
		} else {
			tls["insecure"] = insecure(false)
		}
		out["tls"] = tls
		if n.Flow != "" {
			out["flow"] = n.Flow
		}
		if tr := singTransport(n); tr != nil {
			out["transport"] = tr
		}
		return out, nil
	case "vmess":
		if n.UUID == "" {
			return nil, errors.New("缺少 uuid")
		}
		out := map[string]any{
			"type":        "vmess",
			"tag":         "proxy",
			"server":      n.Server,
			"server_port": n.Port,
			"uuid":        n.UUID,
			"security":    "auto",
			"alter_id":    0,
			"tls": map[string]any{
				"enabled":     tlsOn(false),
				"server_name": serverName(n.Server),
				"insecure":    insecure(false),
			},
		}
		if tr := singTransport(n); tr != nil {
			out["transport"] = tr
		}
		return out, nil
	// http / socks 出站：用户手工导入的机房 / 家宽 / 内网代理（批量链接导入常见形态）。
	// 凭据槽位与解析器约定一致：Cipher = username、Password = password；两者都空则不出凭据。
	case "http", "https":
		out := map[string]any{
			"type":        "http",
			"tag":         "proxy",
			"server":      n.Server,
			"server_port": n.Port,
		}
		if n.Username != "" {
			out["username"] = n.Username
		}
		if n.Password != "" {
			out["password"] = n.Password
		}
		if tlsOn(false) {
			// https 代理：到代理本身走 TLS；sni 缺省用 server，证书名不符时由 skip-cert-verify 放行。
			out["tls"] = map[string]any{
				"enabled":     true,
				"server_name": serverName(n.Server),
				"insecure":    insecure(false),
			}
		}
		return out, nil
	case "socks", "socks5", "socks4":
		out := map[string]any{
			"type":        "socks",
			"tag":         "proxy",
			"server":      n.Server,
			"server_port": n.Port,
			"version":     "5",
		}
		if strings.EqualFold(n.NodeType, "socks4") {
			out["version"] = "4"
		}
		if n.Username != "" {
			out["username"] = n.Username
		}
		if n.Password != "" {
			out["password"] = n.Password
		}
		return out, nil
	case "ss", "shadowsocks":
		if n.Password == "" {
			return nil, errors.New("缺少 password")
		}
		method := n.Cipher
		if method == "" {
			method = "aes-256-gcm"
		}
		return map[string]any{
			"type":        "shadowsocks",
			"tag":         "proxy",
			"server":      n.Server,
			"server_port": n.Port,
			"method":      method,
			"password":    n.Password,
		}, nil
	case "hysteria2", "hy2":
		if n.Password == "" {
			return nil, errors.New("缺少 password")
		}
		out := map[string]any{
			"type":        "hysteria2",
			"tag":         "proxy",
			"server":      n.Server,
			"server_port": n.Port,
			"password":    n.Password,
			"tls": map[string]any{
				// hysteria2 协议强制 TLS：即使 Clash 节点标 tls:false 也恒 true，否则 sing-box 报 TLS required。
				"enabled":     true,
				"server_name": serverName(n.Server),
				"insecure":    insecure(false),
			},
		}
		if n.Obfs != "" {
			obfs := map[string]any{"type": n.Obfs}
			if n.ObfsPassword != "" {
				obfs["password"] = n.ObfsPassword
			}
			out["obfs"] = obfs
		}
		if up := parseBandwidthMbps(n.Up); up != nil {
			out["up_mbps"] = up
		}
		if down := parseBandwidthMbps(n.Down); down != nil {
			out["down_mbps"] = down
		}
		return out, nil
	case "anytls":
		if n.Password == "" {
			return nil, errors.New("缺少 password")
		}
		return map[string]any{
			"type":        "anytls",
			"tag":         "proxy",
			"server":      n.Server,
			"server_port": n.Port,
			"password":    n.Password,
			"tls": map[string]any{
				// anytls 基于 TLS：enabled 恒 true（即使 Clash YAML 写 tls:false）；
				// insecure 仅由 skip-cert-verify 控制，否则 sing-box 报 "TLS required"。
				"enabled":     true,
				"server_name": serverName(n.Server),
				"insecure":    insecure(false),
			},
		}, nil
	case "tuic":
		if n.UUID == "" {
			return nil, errors.New("缺少 uuid")
		}
		return map[string]any{
			"type":               "tuic",
			"tag":                "proxy",
			"server":             n.Server,
			"server_port":        n.Port,
			"uuid":               n.UUID,
			"password":           n.Password,
			"congestion_control": "bbr",
			"tls": map[string]any{
				"enabled":     true,
				"server_name": serverName(n.Server),
				"insecure":    insecure(false),
			},
		}, nil
	case "wireguard", "wg":
		privateKey := n.PrivateKey
		if privateKey == "" {
			privateKey = n.Password
		}
		if privateKey == "" {
			return nil, errors.New("缺少 private_key")
		}
		publicKey := n.PublicKey
		if publicKey == "" {
			publicKey = n.Cipher
		}
		return map[string]any{
			"type":        "wireguard",
			"tag":         "proxy",
			"server":      n.Server,
			"server_port": n.Port,
			"private_key": privateKey,
			"peers": []any{map[string]any{
				"server":      n.Server,
				"server_port": n.Port,
				"public_key":  publicKey,
			}},
		}, nil
	case "hysteria", "hy1":
		password := n.AuthStr
		if password == "" {
			password = n.Password
		}
		return map[string]any{
			"type":        "hysteria",
			"tag":         "proxy",
			"server":      n.Server,
			"server_port": n.Port,
			"auth_str":    password,
			"tls": map[string]any{
				"enabled":     true,
				"server_name": serverName(n.Server),
				"insecure":    insecure(false),
			},
		}, nil
	default:
		return nil, fmt.Errorf("不支持的节点类型: %s", n.NodeType)
	}
}

// singTransport ws/http 传输（network tcp/空则省略）。
func singTransport(n ClashNode) map[string]any {
	switch strings.ToLower(n.Network) {
	case "ws":
		ws := map[string]any{"type": "ws"}
		path := n.WsPath
		if path == "" {
			path = "/"
		}
		ws["path"] = path
		if n.WSHeaders != nil {
			ws["headers"] = n.WSHeaders
		}
		return ws
	case "http":
		return map[string]any{"type": "http"}
	default:
		return nil
	}
}
