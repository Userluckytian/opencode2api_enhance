// 订阅拉取与解析（Rust subscribe.rs 移植）：
// Clash YAML / V2Ray base64 / 明文链接（vmess/vless/trojan/ss/hysteria2）。
//
// 拉取的订阅节点持久化到本地缓存（dataDir/subscription.json），
// 节点列表（ListNodesWithGroup）一并读取，保证：
//   - 实例启动时能按节点名找到完整配置生成 sing-box
//   - 节点池页面可展示订阅节点
//
// 端点：/api/admin/subscribe/preview|import|import-pool
package manager

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// SubscribeNode 订阅节点（轻量结构，可落为实例；raw 保留原始链接）。
// JSON 字段与 Rust SubscribeNode（serde snake_case）一致。
type SubscribeNode struct {
	Name              string            `json:"name"`
	Server            string            `json:"server"`
	Port              uint16            `json:"port"`
	NodeType          string            `json:"node_type"`
	Password          string            `json:"password,omitempty"`
	UUID              string            `json:"uuid,omitempty"`
	Cipher            string            `json:"cipher,omitempty"`
	SNI               string            `json:"sni,omitempty"`
	Network           string            `json:"network,omitempty"`
	WsPath            string            `json:"ws_path,omitempty"`
	WsHeaders         map[string]string `json:"ws_headers,omitempty"`
	Flow              string            `json:"flow,omitempty"`
	TLS               bool              `json:"tls"`
	RealityPbk        string            `json:"reality_pbk,omitempty"`        // VLESS REALITY 公钥（pbk）
	RealitySid        string            `json:"reality_sid,omitempty"`        // VLESS REALITY short-id（sid）
	ClientFingerprint string            `json:"client_fingerprint,omitempty"` // TLS 指纹（fp，缺省 chrome）
	Obfs              string            `json:"obfs,omitempty"`               // hysteria2 Salamander 混淆类型
	ObfsPassword      string            `json:"obfs_password,omitempty"`
	SkipCertVerify    bool              `json:"skip_cert_verify,omitempty"`
	Group             string            `json:"group,omitempty"` // 订阅来源分组名
	Raw               string            `json:"raw"`
}

// SubscriptionMeta 订阅元信息（来自 HTTP 响应头，clash-verge-rev 同款解析）。
type SubscriptionMeta struct {
	Name     string `json:"name,omitempty"`
	Upload   uint64 `json:"upload,omitempty"`
	Download uint64 `json:"download,omitempty"`
	Total    uint64 `json:"total,omitempty"`
	Expire   uint64 `json:"expire,omitempty"`
	Home     string `json:"home,omitempty"`
}

// fetchSubscription 拉取并解析订阅 URL，返回节点列表。
func fetchSubscription(url string) ([]SubscribeNode, error) {
	nodes, _, err := fetchSubscriptionWithMeta(url)
	return nodes, err
}

// fetchSubscriptionWithMeta 拉取并解析订阅 URL，同时解析响应头元信息（订阅名/流量/到期）。
func fetchSubscriptionWithMeta(url string) ([]SubscribeNode, SubscriptionMeta, error) {
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, SubscriptionMeta{}, fmt.Errorf("订阅拉取失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, SubscriptionMeta{}, fmt.Errorf("订阅拉取失败: HTTP %d", resp.StatusCode)
	}
	meta := parseSubscriptionHeaders(resp.Header)
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, SubscriptionMeta{}, fmt.Errorf("读取订阅内容失败: %v", err)
	}
	nodes, err := parseSubscription(string(body))
	return nodes, meta, err
}

// parseSubscriptionHeaders 解析订阅响应头（clash-verge-rev 同款）：
// `*-subscription-userinfo`：upload/download/total/expire；`Content-Disposition`：订阅文件名；
// `profile-web-page-url`：订阅主页。
func parseSubscriptionHeaders(h http.Header) SubscriptionMeta {
	var meta SubscriptionMeta
	for k, vs := range h {
		key := strings.ToLower(k)
		switch {
		case strings.HasSuffix(key, "subscription-userinfo"):
			for _, v := range vs {
				for _, part := range strings.Fields(v) {
					k2, v2, ok := strings.Cut(part, "=")
					if !ok {
						continue
					}
					n, _ := strconv.ParseUint(v2, 10, 64)
					switch k2 {
					case "upload":
						meta.Upload = n
					case "download":
						meta.Download = n
					case "total":
						meta.Total = n
					case "expire":
						meta.Expire = n
					}
				}
			}
		case key == "content-disposition":
			if len(vs) > 0 {
				meta.Name = parseContentDispositionName(vs[0])
			}
		case key == "profile-web-page-url":
			if len(vs) > 0 {
				meta.Home = vs[0]
			}
		}
	}
	return meta
}

// parseContentDispositionName 解析 Content-Disposition 文件名：`filename*=UTF-8”%E5%AD%90...` 优先，
// 退化到 `filename=`。
func parseContentDispositionName(raw string) string {
	for _, part := range strings.Split(raw, ";") {
		part = strings.TrimSpace(part)
		if v, ok := strings.CutPrefix(part, "filename*="); ok {
			v = strings.Trim(v, `"`)
			if _, enc, ok := strings.Cut(v, "''"); ok {
				v = enc
			}
			return percentDecode(v)
		}
	}
	for _, part := range strings.Split(raw, ";") {
		part = strings.TrimSpace(part)
		if v, ok := strings.CutPrefix(part, "filename="); ok {
			return strings.Trim(v, `"`)
		}
	}
	return ""
}

// groupNameFor 确定订阅分组名：响应头订阅名 > URL 末段（去扩展名）> "订阅N"。
func (m *Manager) groupNameFor(url string, meta SubscriptionMeta) string {
	if meta.Name != "" {
		return meta.Name
	}
	trimmed := strings.TrimRight(url, "/")
	if idx := strings.LastIndex(trimmed, "/"); idx >= 0 && idx < len(trimmed)-1 {
		seg := trimmed[idx+1:]
		seg = strings.TrimSuffix(seg, ".yaml")
		seg = strings.TrimSuffix(seg, ".txt")
		if seg != "" && !strings.Contains(seg, ":") && !strings.Contains(seg, "=") {
			return percentDecode(seg)
		}
	}
	// 兜底：订阅N（N = 现有缓存分组数 + 1）
	groups := map[string]bool{}
	for _, n := range m.loadSubscriptionCache() {
		if n.Group != "" {
			groups[n.Group] = true
		}
	}
	for i := 1; ; i++ {
		name := fmt.Sprintf("订阅%d", i)
		if !groups[name] {
			return name
		}
	}
}

// parseSubscription 解析订阅内容（自动识别 Clash YAML / sing-box JSON / base64 / 明文链接）。
// 统一在出口过滤公告/信息伪节点（对齐 Rust is_info_pseudo_node，覆盖全部格式）。
func parseSubscription(body string) ([]SubscribeNode, error) {
	trimmed := strings.TrimSpace(strings.TrimPrefix(body, "\uFEFF"))
	if trimmed == "" {
		return nil, fmt.Errorf("订阅内容为空")
	}
	var nodes []SubscribeNode
	var err error
	switch {
	case strings.HasPrefix(trimmed, "proxies:") || (strings.Contains(trimmed, "proxies:") && strings.Contains(trimmed, "type:")):
		var clash []ClashNode
		clash, err = parseClashYAML(trimmed)
		if err == nil {
			nodes = make([]SubscribeNode, 0, len(clash))
			for i := range clash {
				nodes = append(nodes, subscribeFromClash(clash[i]))
			}
		}
	case strings.HasPrefix(trimmed, `{"outbounds"`) || strings.HasPrefix(trimmed, "{\n  \"outbounds\""):
		nodes, err = parseSingboxJSON(trimmed)
	default:
		if text, ok := decodeBase64Loose(trimmed); ok {
			t := strings.TrimSpace(text)
			if strings.HasPrefix(t, `{"outbounds"`) {
				nodes, err = parseSingboxJSON(t)
				break
			}
			if strings.Contains(t, "://") {
				nodes, err = parsePlainLinks(t)
				break
			}
		}
		nodes, err = parsePlainLinks(trimmed)
	}
	if err != nil {
		return nil, err
	}
	// 出口统一过滤公告/伪节点（官网/更新时间/剩余时长…，名称含全角冒号）
	filtered := nodes[:0]
	for _, n := range nodes {
		if !isInfoPseudoNode(n.Name) {
			filtered = append(filtered, n)
		}
	}
	if len(filtered) == 0 {
		return nil, fmt.Errorf("订阅内容中未解析到任何可用节点")
	}
	return filtered, nil
}

// subscribeFromClash ClashNode → SubscribeNode。
// wireguard: private-key→password、public-key→cipher；hysteria: auth-str→password；
// anytls/hysteria2/tuic/hysteria(v1) 强制 TLS（忽略 Clash 里错误的 tls:false）。
func subscribeFromClash(n ClashNode) SubscribeNode {
	sni := n.SNI
	if sni == "" {
		sni = n.ServerName
	}
	password := n.Password
	if password == "" {
		password = n.PrivateKey
	}
	if password == "" {
		password = n.AuthStr
	}
	cipher := n.Cipher
	if cipher == "" {
		cipher = n.PublicKey
	}
	tls := true
	if n.TLS != nil {
		tls = *n.TLS
	}
	switch strings.ToLower(n.NodeType) {
	case "anytls", "hysteria2", "hy2", "tuic", "hysteria", "hy1":
		tls = true
	}
	return SubscribeNode{
		Name:              n.Name,
		Server:            n.Server,
		Port:              n.Port,
		NodeType:          n.NodeType,
		Password:          password,
		UUID:              n.UUID,
		Cipher:            cipher,
		SNI:               sni,
		Network:           n.Network,
		WsPath:            n.WsPath,
		WsHeaders:         n.WSHeaders,
		Flow:              n.Flow,
		TLS:               tls,
		RealityPbk:        n.RealityPublicKey,
		RealitySid:        n.RealityShortID,
		ClientFingerprint: n.ClientFingerprint,
		Obfs:              n.Obfs,
		ObfsPassword:      n.ObfsPassword,
		SkipCertVerify:    n.SkipCertVerify != nil && *n.SkipCertVerify,
		Group:             n.Group,
		Raw:               fmt.Sprintf("%s@%s:%d", n.NodeType, n.Server, n.Port),
	}
}

// isBase64Like 粗判 base64：长度 >40 且前 60 字符均为 base64 字母表。
func isBase64Like(s string) bool {
	if len(s) <= 40 {
		return false
	}
	n := len(s)
	if n > 60 {
		n = 60
	}
	for _, c := range s[:n] {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '+' || c == '/' || c == '=' || c == '-') {
			return false
		}
	}
	return true
}

// decodeBase64Loose 容错 base64 解码（v2rayN 式）：去空白、`_`→`/`、`-`→`+`
// （URL-safe 变体）、补 padding，依次尝试 NOPAD/带 pad/URL-safe；失败返回 false。
func decodeBase64Loose(s string) (string, bool) {
	var sb strings.Builder
	for _, c := range s {
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			continue
		case c == '_':
			sb.WriteByte('/')
		case c == '-':
			sb.WriteByte('+')
		default:
			sb.WriteByte(byte(c))
		}
	}
	cleaned := sb.String()
	rem := len(cleaned) % 4
	if rem != 0 {
		cleaned += strings.Repeat("=", 4-rem)
	}
	for _, enc := range []*base64.Encoding{base64.RawStdEncoding, base64.StdEncoding, base64.RawURLEncoding, base64.URLEncoding} {
		if b, err := enc.DecodeString(cleaned); err == nil {
			if utf8.Valid(b) {
				return string(b), true
			}
		}
	}
	return "", false
}

// parsePlainLinks 逐行解析 vmess:// vless:// trojan:// ss:// hysteria2:// 链接。
func parsePlainLinks(s string) ([]SubscribeNode, error) {
	var nodes []SubscribeNode
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if node, ok, err := parseURILink(line); err != nil {
			// 对齐 Rust：跳过无法解析的行（仅记录，不中断）
			continue
		} else if ok {
			if isInfoPseudoNode(node.Name) {
				// 对齐 Rust is_info_pseudo_node：公告/信息伪节点（官网/更新时间/剩余时长…）过滤
				continue
			}
			nodes = append(nodes, node)
		}
	}
	if len(nodes) == 0 {
		return nil, fmt.Errorf("订阅内容中未解析到任何可用节点")
	}
	return nodes, nil
}

// infoPseudoPrefixes 订阅头部公告/信息伪节点名称前缀（Rust is_info_pseudo_node 同款）。
// 这些行伪装成 vless/anytls 链接指向占位服务器，解码后名称不含国家地区且带全角冒号。
var infoPseudoPrefixes = []string{
	"官网", "网站", "主页", "更新时间", "更新于", "剩余时长", "剩余流量", "到期时间",
	"过期时间", "套餐", "订阅", "公告", "通知", "电报", "频道", "群组", "客服", "工单",
	"说明", "注意", "流量", "账号", "节点数",
}

// isInfoPseudoNode 判断节点名是否为公告/信息伪节点：
// 先剥离 `[anytls]` 等协议前缀标签，再匹配公告前缀且名称含全角冒号。
func isInfoPseudoNode(name string) bool {
	n := strings.TrimSpace(name)
	if idx := strings.IndexByte(n, ']'); strings.HasPrefix(n, "[") && idx >= 0 {
		n = strings.TrimSpace(n[idx+1:])
	}
	if !strings.ContainsRune(n, '：') {
		return false
	}
	for _, p := range infoPseudoPrefixes {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	return false
}

// parseURILink 识别并解析单个 v2ray 风格链接。
func parseURILink(line string) (SubscribeNode, bool, error) {
	switch {
	case strings.HasPrefix(line, "vmess://"):
		n, err := parseVmess(strings.TrimPrefix(line, "vmess://"))
		if err != nil || n.Port == 0 {
			return SubscribeNode{}, false, err
		}
		return n, true, nil
	case strings.HasPrefix(line, "vless://"):
		n, err := parseVless(strings.TrimPrefix(line, "vless://"))
		return n, true, err
	case strings.HasPrefix(line, "trojan://"):
		n, err := parseTrojan(strings.TrimPrefix(line, "trojan://"))
		return n, true, err
	case strings.HasPrefix(line, "ss://"):
		n, err := parseSS(strings.TrimPrefix(line, "ss://"))
		return n, true, err
	case strings.HasPrefix(line, "hysteria2://"):
		n, err := parseHysteria2(strings.TrimPrefix(line, "hysteria2://"))
		return n, true, err
	case strings.HasPrefix(line, "hy2://"):
		n, err := parseHysteria2(strings.TrimPrefix(line, "hy2://"))
		return n, true, err
	case strings.HasPrefix(line, "hysteria://"):
		n, err := parseHysteria(strings.TrimPrefix(line, "hysteria://"))
		return n, true, err
	case strings.HasPrefix(line, "hy1://"):
		n, err := parseHysteria(strings.TrimPrefix(line, "hy1://"))
		return n, true, err
	case strings.HasPrefix(line, "tuic://"):
		n, err := parseTuic(strings.TrimPrefix(line, "tuic://"))
		return n, true, err
	case strings.HasPrefix(line, "wg://"):
		n, err := parseWireguard(strings.TrimPrefix(line, "wg://"))
		return n, true, err
	case strings.HasPrefix(line, "wireguard://"):
		n, err := parseWireguard(strings.TrimPrefix(line, "wireguard://"))
		return n, true, err
	case strings.HasPrefix(line, "anytls://"):
		n, err := parseAnyTLS(strings.TrimPrefix(line, "anytls://"))
		return n, true, err
	case strings.HasPrefix(line, "socks://"):
		n, err := parseSocks(strings.TrimPrefix(line, "socks://"))
		return n, true, err
	}
	return SubscribeNode{}, false, nil
}

// parseSingboxJSON 解析 sing-box JSON 订阅（{"outbounds":[...]}；字段与 singbox outbound 同构逆向映射）。
func parseSingboxJSON(body string) ([]SubscribeNode, error) {
	var doc map[string]any
	if json.Unmarshal([]byte(body), &doc) != nil {
		return nil, fmt.Errorf("解析 sing-box JSON 失败")
	}
	obs, _ := doc["outbounds"].([]any)
	if obs == nil {
		return nil, fmt.Errorf("sing-box JSON 缺少 outbounds 数组")
	}
	var nodes []SubscribeNode
	for _, o := range obs {
		ob, _ := o.(map[string]any)
		if ob == nil {
			continue
		}
		nodeType, _ := ob["type"].(string)
		if nodeType == "" {
			continue
		}
		tag, _ := ob["tag"].(string)
		server, _ := ob["server"].(string)
		port := jsonUint16(ob["server_port"])
		if server == "" || port == 0 {
			continue
		}
		tlsObj, _ := ob["tls"].(map[string]any)
		tlsEnabled, _ := tlsObj["enabled"].(bool)
		sni, _ := tlsObj["server_name"].(string)
		insecure, _ := tlsObj["insecure"].(bool)
		var realityPbk, realitySid, fingerprint string
		if reality, ok := tlsObj["reality"].(map[string]any); ok {
			realityPbk, _ = reality["public_key"].(string)
			realitySid, _ = reality["short_id"].(string)
		}
		if utls, ok := tlsObj["utls"].(map[string]any); ok {
			fingerprint, _ = utls["fingerprint"].(string)
		}
		var network, wsPath, flow string
		if tr, ok := ob["transport"].(map[string]any); ok {
			network, _ = tr["type"].(string)
			wsPath, _ = tr["path"].(string)
		}
		flow, _ = ob["flow"].(string)
		var obfs, obfsPassword string
		if of, ok := ob["obfs"].(map[string]any); ok {
			obfs, _ = of["type"].(string)
			obfsPassword, _ = of["password"].(string)
		}

		var password, uuid, cipher string
		switch nodeType {
		case "vless":
			uuid, _ = ob["uuid"].(string)
		case "vmess":
			uuid, _ = ob["uuid"].(string)
			cipher, _ = ob["security"].(string)
		case "trojan", "hysteria2", "tuic":
			password, _ = ob["password"].(string)
			if nodeType == "tuic" {
				uuid, _ = ob["uuid"].(string)
			}
		case "shadowsocks":
			password, _ = ob["password"].(string)
			cipher, _ = ob["method"].(string)
		case "wireguard":
			password, _ = ob["private_key"].(string)
		case "socks", "http":
			password, _ = ob["password"].(string)
			cipher, _ = ob["username"].(string)
		default:
			continue
		}

		name := server
		if tag != "" {
			name = percentDecode(tag)
		}
		raw, _ := json.Marshal(ob)
		nodes = append(nodes, SubscribeNode{
			Name:              name,
			Server:            server,
			Port:              port,
			NodeType:          nodeType,
			Password:          password,
			UUID:              uuid,
			Cipher:            cipher,
			SNI:               sni,
			Network:           network,
			WsPath:            wsPath,
			Flow:              flow,
			TLS:               tlsEnabled || realityPbk != "",
			RealityPbk:        realityPbk,
			RealitySid:        realitySid,
			ClientFingerprint: fingerprint,
			Obfs:              obfs,
			ObfsPassword:      obfsPassword,
			SkipCertVerify:    insecure,
			Raw:               string(raw),
		})
	}
	if len(nodes) == 0 {
		return nil, fmt.Errorf("sing-box JSON 中未解析到任何可用节点")
	}
	return nodes, nil
}
func parseVmess(rest string) (SubscribeNode, error) {
	text, ok := decodeBase64Loose(rest)
	if !ok {
		return SubscribeNode{}, fmt.Errorf("vmess:// 非 base64 编码")
	}
	if strings.HasPrefix(strings.TrimSpace(text), "vmess://") {
		// 二次 base64（vmess://base64(base64(json))），再解一层
		if inner, ok2 := decodeBase64Loose(strings.TrimPrefix(strings.TrimSpace(text), "vmess://")); ok2 {
			text = inner
		}
	}
	var v map[string]any
	if json.Unmarshal([]byte(text), &v) != nil {
		return SubscribeNode{}, fmt.Errorf("vmess JSON 解析失败")
	}
	server, _ := v["add"].(string)
	port := jsonUint16(v["port"])
	name, _ := v["ps"].(string)
	if name == "" {
		name = server
	}
	if server == "" || port == 0 {
		return SubscribeNode{}, nil
	}
	tls := v["tls"] == "tls"
	return SubscribeNode{
		Name:     name,
		Server:   server,
		Port:     port,
		NodeType: "vmess",
		UUID:     asString(v["id"]),
		Cipher:   asDefaultString(v["scy"], "auto"),
		SNI:      asString(v["sni"]),
		Network:  asDefaultString(v["net"], "tcp"),
		WsPath:   asString(v["path"]),
		TLS:      tls,
		Raw:      "vmess://" + rest,
	}, nil
}

// parseVless vless://uuid@host:port?params#name。
func parseVless(rest string) (SubscribeNode, error) {
	auth, name := splitFragment(rest)
	userinfo, hostport, ok := strings.Cut(auth, "@")
	if !ok {
		return SubscribeNode{}, fmt.Errorf("vless 链接缺少 @")
	}
	server, port, err := splitHostPort(hostport)
	if err != nil {
		return SubscribeNode{}, err
	}
	params := parseQuery(auth)
	uuid := strings.SplitN(userinfo, "?", 2)[0]
	network := params["type"]
	if network == "" {
		network = "tcp"
	}
	security := params["security"]
	path := params["path"]
	// host 参数是 ws/http Host 头（CDN 前置节点必须带上，否则 Cloudflare 403），不是 path。
	var hdrs map[string]string
	if h := params["host"]; h != "" && !strings.HasPrefix(h, ".") {
		hdrs = map[string]string{"Host": h}
	}
	nname := name
	if nname == "" {
		nname = server
	}
	fp := params["fp"]
	if fp == "" {
		fp = "chrome"
	}
	return SubscribeNode{
		Name:              nname,
		Server:            server,
		Port:              port,
		NodeType:          "vless",
		UUID:              uuid,
		SNI:               params["sni"],
		Network:           network,
		WsPath:            path,
		WsHeaders:         hdrs,
		Flow:              params["flow"],
		TLS:               security == "tls" || security == "reality",
		RealityPbk:        params["pbk"], // REALITY 公钥（缺失会导致 TLS 握手失败）
		RealitySid:        params["sid"], // REALITY short-id
		ClientFingerprint: fp,
		Raw:               "vless://" + rest,
	}, nil
}

// parseTrojan trojan://password@host:port?params#name。
func parseTrojan(rest string) (SubscribeNode, error) {
	auth, name := splitFragment(rest)
	userinfo, hostport, ok := strings.Cut(auth, "@")
	if !ok {
		return SubscribeNode{}, fmt.Errorf("trojan 链接缺少 @")
	}
	server, port, err := splitHostPort(hostport)
	if err != nil {
		return SubscribeNode{}, err
	}
	params := parseQuery(auth)
	password := strings.SplitN(userinfo, "?", 2)[0]
	nname := name
	if nname == "" {
		nname = server
	}
	network := params["type"]
	if network == "" {
		network = "tcp"
	}
	tls := params["security"] != "none"
	return SubscribeNode{
		Name:     nname,
		Server:   server,
		Port:     port,
		NodeType: "trojan",
		Password: password,
		SNI:      params["sni"],
		Network:  network,
		WsPath:   params["path"],
		TLS:      tls,
		Raw:      "trojan://" + rest,
	}, nil
}

// parseSS ss://base64(method:password)@host:port#name 或 ss://base64(method:password@host:port)#name。
func parseSS(rest string) (SubscribeNode, error) {
	auth, name := splitFragment(rest)
	userinfo := auth
	hostport := ""
	if u, h, ok := strings.Cut(auth, "@"); ok {
		userinfo, hostport = u, h
	} else if text, ok := decodeBase64Loose(auth); ok {
		u, h, ok := strings.Cut(text, "@")
		if !ok {
			return SubscribeNode{}, fmt.Errorf("ss 链接缺少 @")
		}
		userinfo, hostport = u, h
	}
	if hostport == "" {
		return SubscribeNode{}, fmt.Errorf("ss 链接缺少服务器地址")
	}
	server, port, err := splitHostPort(hostport)
	if err != nil {
		return SubscribeNode{}, err
	}
	method, password := userinfo, ""
	if m, p, ok := strings.Cut(userinfo, ":"); ok {
		method, password = m, p
	} else if text, ok := decodeBase64Loose(userinfo); ok {
		if m, p, ok := strings.Cut(text, ":"); ok {
			method, password = m, p
		}
	}
	nname := name
	if nname == "" {
		nname = server
	}
	return SubscribeNode{
		Name:     nname,
		Server:   server,
		Port:     port,
		NodeType: "ss",
		Password: password,
		Cipher:   method,
		Network:  "tcp",
		TLS:      false,
		Raw:      "ss://" + rest,
	}, nil
}

// parseHysteria2 hysteria2://password@host:port?params#name。
func parseHysteria2(rest string) (SubscribeNode, error) {
	auth, name := splitFragment(rest)
	userinfo, hostport, ok := strings.Cut(auth, "@")
	if !ok {
		return SubscribeNode{}, fmt.Errorf("hysteria2 链接缺少 @")
	}
	server, port, err := splitHostPort(hostport)
	if err != nil {
		return SubscribeNode{}, err
	}
	params := parseQuery(auth)
	password := strings.SplitN(userinfo, "?", 2)[0]
	nname := name
	if nname == "" {
		nname = server
	}
	return SubscribeNode{
		Name:         nname,
		Server:       server,
		Port:         port,
		NodeType:     "hysteria2",
		Password:     password,
		SNI:          params["sni"],
		Obfs:         params["obfs"], // Salamander 混淆类型
		ObfsPassword: params["obfs-password"],
		TLS:          true,
		Raw:          "hysteria2://" + rest,
	}, nil
}

// parseHysteria hysteria://host:port?auth=...&peer=...#name（v1）。
func parseHysteria(rest string) (SubscribeNode, error) {
	auth, name := splitFragment(rest)
	_, hostport, ok := strings.Cut(auth, "@")
	if !ok {
		hostport = auth
	}
	server, port, err := splitHostPort(hostport)
	if err != nil {
		return SubscribeNode{}, err
	}
	params := parseQuery(auth)
	password := params["auth"]
	if password == "" {
		password = params["auth_str"]
	}
	nname := name
	if nname == "" {
		nname = server
	}
	return SubscribeNode{
		Name:     nname,
		Server:   server,
		Port:     port,
		NodeType: "hysteria",
		Password: password,
		SNI:      params["peer"],
		TLS:      true,
		Raw:      "hysteria://" + rest,
	}, nil
}

// parseTuic tuic://uuid:password@host:port?sni=...#name。
func parseTuic(rest string) (SubscribeNode, error) {
	auth, name := splitFragment(rest)
	userinfo, hostport, ok := strings.Cut(auth, "@")
	if !ok {
		return SubscribeNode{}, fmt.Errorf("tuic 链接缺少 @")
	}
	server, port, err := splitHostPort(hostport)
	if err != nil {
		return SubscribeNode{}, err
	}
	params := parseQuery(auth)
	uuid, password := userinfo, ""
	if u, p, ok := strings.Cut(userinfo, ":"); ok {
		uuid, password = u, p
	}
	nname := name
	if nname == "" {
		nname = server
	}
	return SubscribeNode{
		Name:     nname,
		Server:   server,
		Port:     port,
		NodeType: "tuic",
		Password: password,
		UUID:     uuid,
		SNI:      params["sni"],
		TLS:      true,
		Raw:      "tuic://" + rest,
	}, nil
}

// parseWireguard wg://public_key@host:port?private_key=...#name。
func parseWireguard(rest string) (SubscribeNode, error) {
	auth, name := splitFragment(rest)
	userinfo, hostport, ok := strings.Cut(auth, "@")
	if !ok {
		return SubscribeNode{}, fmt.Errorf("wireguard 链接缺少 @")
	}
	server, port, err := splitHostPort(hostport)
	if err != nil {
		return SubscribeNode{}, err
	}
	params := parseQuery(auth)
	nname := name
	if nname == "" {
		nname = server
	}
	return SubscribeNode{
		Name:     nname,
		Server:   server,
		Port:     port,
		NodeType: "wireguard",
		Password: params["private_key"],               // 客户端私钥
		Cipher:   strings.SplitN(userinfo, "?", 2)[0], // 对端公钥（userinfo 部分）
		TLS:      true,
		Raw:      "wg://" + rest,
	}, nil
}

// parseAnyTLS anytls://password@host:port?insecure=1#name。
// 注意：anytls 基于 TLS，insecure=1 仅跳过证书校验，不能关闭 TLS。
func parseAnyTLS(rest string) (SubscribeNode, error) {
	auth, name := splitFragment(rest)
	userinfo, hostport, ok := strings.Cut(auth, "@")
	if !ok {
		return SubscribeNode{}, fmt.Errorf("anytls 链接缺少 @")
	}
	server, port, err := splitHostPort(hostport)
	if err != nil {
		return SubscribeNode{}, err
	}
	params := parseQuery(auth)
	password := strings.SplitN(userinfo, "?", 2)[0]
	nname := name
	if nname == "" {
		nname = server
	}
	return SubscribeNode{
		Name:           nname,
		Server:         server,
		Port:           port,
		NodeType:       "anytls",
		Password:       password,
		SNI:            params["sni"],
		SkipCertVerify: params["insecure"] == "1",
		TLS:            true,
		Raw:            "anytls://" + rest,
	}, nil
}

// parseSocks socks://user:pass@host:port#name。
func parseSocks(rest string) (SubscribeNode, error) {
	auth, name := splitFragment(rest)
	userinfo, hostport, ok := strings.Cut(auth, "@")
	if !ok {
		return SubscribeNode{}, fmt.Errorf("socks 链接缺少 @")
	}
	server, port, err := splitHostPort(hostport)
	if err != nil {
		return SubscribeNode{}, err
	}
	username, password := userinfo, ""
	if u, p, ok := strings.Cut(userinfo, ":"); ok {
		username, password = u, p
	}
	nname := name
	if nname == "" {
		nname = server
	}
	return SubscribeNode{
		Name:     nname,
		Server:   server,
		Port:     port,
		NodeType: "socks",
		Password: password,
		Cipher:   username, // 用户名（ClashNode.cipher 槽复用）
		TLS:      false,
		Raw:      "socks://" + rest,
	}, nil
}

// splitFragment 拆分 #名称（名称 percent-decode：URL 编码的中文/emoji），返回 (主体, 名称)。
func splitFragment(s string) (string, string) {
	if head, frag, ok := strings.Cut(s, "#"); ok {
		return head, percentDecode(strings.TrimSpace(frag))
	}
	return s, ""
}

// percentDecode percent-decode（application/x-www-form-urlencoded）：
// %XX 按字节解码、`+` → 空格。用于订阅链接 fragment / vmess ps 的 URL 编码节点名。
func percentDecode(s string) string {
	var out []byte
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			if h, ok := hexVal(s[i+1]); ok {
				if l, ok := hexVal(s[i+2]); ok {
					out = append(out, h*16+l)
					i += 2
					continue
				}
			}
		}
		if s[i] == '+' {
			out = append(out, ' ')
		} else {
			out = append(out, s[i])
		}
	}
	return string(out)
}

// hexVal 十六进制字符值。
func hexVal(b byte) (byte, bool) {
	switch {
	case b >= '0' && b <= '9':
		return b - '0', true
	case b >= 'a' && b <= 'f':
		return b - 'a' + 10, true
	case b >= 'A' && b <= 'F':
		return b - 'A' + 10, true
	}
	return 0, false
}

// splitHostPort 解析 host:port（支持 IPv6 字面量 [2001:db8::1]:443；端口后可能带 query）。
func splitHostPort(hostport string) (string, uint16, error) {
	var host, portStr string
	if strings.HasPrefix(hostport, "[") {
		rest := hostport[1:]
		if idx := strings.IndexByte(rest, ']'); idx >= 0 {
			host = rest[:idx]
			tail := rest[idx+1:]
			if !strings.HasPrefix(tail, ":") {
				return "", 0, fmt.Errorf("IPv6 链接缺少端口: %s", hostport)
			}
			portStr = tail[1:]
		} else {
			return "", 0, fmt.Errorf("IPv6 地址缺少 ]: %s", hostport)
		}
	} else {
		h, p, ok := strings.Cut(hostport, ":")
		if !ok {
			return "", 0, fmt.Errorf("链接缺少端口: %s", hostport)
		}
		host, portStr = h, p
	}
	portStr = strings.SplitN(portStr, "?", 2)[0]
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return "", 0, fmt.Errorf("端口无效: %s", portStr)
	}
	return host, uint16(port), nil
}

// parseQuery 解析链接 query（取第一个 ? 之后的 & 对）。
func parseQuery(full string) map[string]string {
	m := map[string]string{}
	_, q, _ := strings.Cut(full, "?")
	for _, pair := range strings.Split(q, "&") {
		if k, v, ok := strings.Cut(pair, "="); ok && k != "" {
			m[k] = v
		}
	}
	return m
}

// ---------- 订阅缓存（dataDir/subscription.json） ----------

// subscriptionCachePath 订阅缓存文件路径。
func (m *Manager) subscriptionCachePath() string {
	return filepath.Join(m.paths.DataDir, "subscription.json")
}

// saveSubscriptionCache 持久化订阅节点缓存。
func (m *Manager) saveSubscriptionCache(nodes []SubscribeNode) error {
	data, err := json.MarshalIndent(nodes, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化订阅缓存失败: %v", err)
	}
	return writeFileMkdir(m.subscriptionCachePath(), data)
}

// loadSubscriptionCache 读取订阅缓存（不存在/损坏返回空）。
func (m *Manager) loadSubscriptionCache() []SubscribeNode {
	data, err := os.ReadFile(m.subscriptionCachePath())
	if err != nil {
		return nil
	}
	var nodes []SubscribeNode
	if json.Unmarshal(data, &nodes) != nil {
		return nil
	}
	return nodes
}

// RemoveSubscriptionNode 从订阅缓存删除节点（按名称），返回删除数量。
// 供节点池「删除节点」——仅订阅缓存中的节点可删（外部 Clash 节点只读）。
func (m *Manager) RemoveSubscriptionNode(name string) (int, error) {
	nodes := m.loadSubscriptionCache()
	before := len(nodes)
	filtered := nodes[:0]
	for _, n := range nodes {
		if n.Name != name {
			filtered = append(filtered, n)
		}
	}
	if len(filtered) == before {
		return 0, nil
	}
	if err := m.saveSubscriptionCache(filtered); err != nil {
		return 0, err
	}
	return before - len(filtered), nil
}

// RemoveSubscriptionNodes 批量删除订阅缓存节点（一次加载+持久化），返回删除数量。
// 已入实例的节点照常列入（实例仍保留其完整配置），外部 Clash 节点静默跳过。
func (m *Manager) RemoveSubscriptionNodes(names []string) (int, error) {
	if len(names) == 0 {
		return 0, nil
	}
	wanted := map[string]bool{}
	for _, n := range names {
		wanted[n] = true
	}
	nodes := m.loadSubscriptionCache()
	before := len(nodes)
	filtered := nodes[:0]
	for _, n := range nodes {
		if !wanted[n.Name] {
			filtered = append(filtered, n)
		}
	}
	if len(filtered) == before {
		return 0, nil
	}
	if err := m.saveSubscriptionCache(filtered); err != nil {
		return 0, err
	}
	return before - len(filtered), nil
}

// toClashNode SubscribeNode → ClashNode（供 sing-box 生成与节点列表合并）。
func toClashNode(n SubscribeNode) ClashNode {
	skip := n.SkipCertVerify
	return ClashNode{
		Name:              n.Name,
		Server:            n.Server,
		Port:              n.Port,
		NodeType:          n.NodeType,
		Password:          n.Password,
		UUID:              n.UUID,
		Cipher:            n.Cipher,
		SNI:               n.SNI,
		ServerName:        n.SNI,
		TLS:               boolPtr(n.TLS),
		SkipCertVerify:    &skip,
		Network:           n.Network,
		WsPath:            n.WsPath,
		WSHeaders:         n.WsHeaders,
		Flow:              n.Flow,
		RealityPublicKey:  n.RealityPbk,
		RealityShortID:    n.RealitySid,
		ClientFingerprint: n.ClientFingerprint,
		Obfs:              n.Obfs,
		ObfsPassword:      n.ObfsPassword,
		PrivateKey:        n.Password, // wireguard: private-key 槽（singbox 生成用 password 回退）
		PublicKey:         n.Cipher,   // wireguard: public-key 槽（singbox 生成用 cipher 回退）
		AuthStr:           n.Password, // hysteria v1: auth-str 槽
		Group:             n.Group,
	}
}

// listSubscriptionNodes 订阅缓存节点 → ClashNode 列表（并入节点池）。
func (m *Manager) listSubscriptionNodes() []ClashNode {
	cache := m.loadSubscriptionCache()
	out := make([]ClashNode, 0, len(cache))
	for _, n := range cache {
		out = append(out, toClashNode(n))
	}
	return out
}

// ---------- 导入 ----------

// importSubscriptionPool 仅拉取并缓存订阅节点（不创建实例），返回节点数。
func (m *Manager) importSubscriptionPool(url string) (int, error) {
	nodes, meta, err := fetchSubscriptionWithMeta(url)
	if err != nil {
		return 0, err
	}
	if len(nodes) == 0 {
		return 0, fmt.Errorf("订阅中未解析到任何节点")
	}
	m.applyGroup(nodes, url, meta)
	if err := m.saveSubscriptionCacheGrouped(nodes); err != nil {
		return 0, err
	}
	return len(nodes), nil
}

// applyGroup 给节点标注订阅分组名（未标注的节点）。
func (m *Manager) applyGroup(nodes []SubscribeNode, url string, meta SubscriptionMeta) {
	group := m.groupNameFor(url, meta)
	for i := range nodes {
		if nodes[i].Group == "" {
			nodes[i].Group = group
		}
	}
}

// saveSubscriptionCacheGrouped 按分组合并订阅缓存：同分组节点替换、其他分组保留
// （多次导入不同订阅合并不顶替）。
func (m *Manager) saveSubscriptionCacheGrouped(nodes []SubscribeNode) error {
	group := ""
	if len(nodes) > 0 {
		group = nodes[0].Group
	}
	if group == "" {
		return m.saveSubscriptionCache(nodes)
	}
	old := m.loadSubscriptionCache()
	var keep []SubscribeNode
	for _, n := range old {
		if n.Group != group {
			keep = append(keep, n)
		}
	}
	return m.saveSubscriptionCache(append(keep, nodes...))
}

// importSubscription 批量导入订阅节点为实例（含持久化订阅缓存）。
// joinGateway 为 true 时导入的实例打上入池标记（不自动启动，启停由实例池页控制）。
// 按节点身份（节点名+端口）匹配已存在实例，重复的订阅节点不重复创建
// （自动拉取每轮调用本函数，否则实例会无限增长）。
func (m *Manager) importSubscription(url string, joinGateway bool) (int, error) {
	nodes, meta, err := fetchSubscriptionWithMeta(url)
	if err != nil {
		return 0, err
	}
	if len(nodes) == 0 {
		return 0, fmt.Errorf("订阅中未解析到任何节点")
	}
	m.applyGroup(nodes, url, meta)
	if err := m.saveSubscriptionCacheGrouped(nodes); err != nil {
		return 0, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	list := m.load()
	existingNames := map[string]bool{}
	usedPorts := map[uint16]bool{}
	existingIDs := map[string]bool{}
	for _, e := range list {
		existingNames[e.Name] = true
		usedPorts[e.Port] = true
		usedPorts[e.SingboxPort] = true
		// 节点身份 = 节点名 + 节点地址（server:port）；实例端口是本地监听端口，与节点身份无关。
		existingIDs[e.Node+"|"+e.IP] = true
	}
	imported := 0
	for _, node := range nodes {
		nodeID := node.Name + "|" + fmt.Sprintf("%s:%d", node.Server, node.Port)
		if existingIDs[nodeID] {
			continue
		}
		existingIDs[nodeID] = true
		name := sanitizeInstanceName(node.Name)
		if existingNames[name] {
			i := 2
			for existingNames[name+"-"+itoa(uint16(i))] {
				i++
			}
			name = name + "-" + itoa(uint16(i))
		}
		// 实例端口是本地 opencode2api 监听端口，与节点服务器端口无关：
		// 从 basePort 段分配空闲（443 等节点端口留给远端，不占用本地监听）。
		port := m.instanceBasePort()
		for usedPorts[port] || usedPorts[port+singboxPortOffset] || !isPortFree(port) || !isPortFree(port+singboxPortOffset) {
			port++
		}
		ip := fmt.Sprintf("%s:%d", node.Server, node.Port)
		inst := Instance{
			Name:        name,
			Port:        port,
			Node:        node.Name,
			Password:    genSkKey(),
			IP:          ip,
			SingboxPort: port + singboxPortOffset,
			JoinGateway: joinGateway,
			Status:      StatusStopped(),
		}
		// 锁内直接追加（复用 AddInstance 的校验语义，但不再二次加锁——
		// sync.Mutex 不可重入，循环内调 AddInstance 会死锁）。
		for i := range list {
			if list[i].Name == inst.Name {
				return imported, fmt.Errorf("导入实例 '%s' 失败: 已存在", node.Name)
			}
			if list[i].Port == inst.Port {
				return imported, fmt.Errorf("导入实例 '%s' 失败: 端口 %d 已占用", node.Name, inst.Port)
			}
		}
		list = append(list, inst)
		if err := m.save(list); err != nil {
			return imported, fmt.Errorf("保存实例清单失败: %v", err)
		}
		existingNames[name] = true
		usedPorts[port] = true
		usedPorts[port+10000] = true
		imported++
	}
	return imported, nil
}

// StartSubscribeLoop 后台订阅循环：按配置间隔自动拉取并入实例。
// intervalMin <= 0 或 URL 为空时休眠 30s 再查配置（配置变更无需重启）。
func (m *Manager) StartSubscribeLoop() {
	go func() {
		for {
			cfg := m.loadConfig()
			intervalMin := cfg.SubscribeIntervalMin
			url := cfg.SubscribeURL
			if intervalMin > 0 && url != "" {
				if n, err := m.importSubscription(url, false); err != nil {
					slog.Warn("订阅自动拉取失败", "error", err)
				} else {
					slog.Info("订阅自动拉取完成", "imported", n)
				}
			}
			wait := time.Duration(intervalMin) * time.Minute
			if wait < 30*time.Second {
				wait = 30 * time.Second
			}
			time.Sleep(wait)
		}
	}()
}

// ---------- HTTP handlers ----------

// SubscribePreviewHandler POST {url} → 拉取并解析节点列表（不落盘）。
func (m *Manager) SubscribePreviewHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodOK(w, r, http.MethodPost) {
			return
		}
		var req struct {
			URL string `json:"url"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.URL == "" {
			writeErr(w, http.StatusBadRequest, "url 必填")
			return
		}
		nodes, err := fetchSubscription(req.URL)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, map[string]any{"nodes": nodes, "count": len(nodes)})
	}
}

// SubscribeImportHandler POST {url, join_gateway} → 导入为实例。
func (m *Manager) SubscribeImportHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodOK(w, r, http.MethodPost) {
			return
		}
		var req struct {
			URL         string `json:"url"`
			JoinGateway bool   `json:"join_gateway"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.URL == "" {
			writeErr(w, http.StatusBadRequest, "url 必填")
			return
		}
		n, err := m.importSubscription(req.URL, req.JoinGateway)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, map[string]any{"status": "ok", "imported": n})
	}
}

// SubscribeImportPoolHandler POST {url} → 仅入订阅缓存（节点池页再勾选入池/独享）。
func (m *Manager) SubscribeImportPoolHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodOK(w, r, http.MethodPost) {
			return
		}
		var req struct {
			URL string `json:"url"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.URL == "" {
			writeErr(w, http.StatusBadRequest, "url 必填")
			return
		}
		n, err := m.importSubscriptionPool(req.URL)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, map[string]any{"status": "ok", "imported": n})
	}
}

// ---------- 小工具 ----------

// jsonUint16 兼容 JSON 里 port 的字符串/数字两种形态。
func jsonUint16(v any) uint16 {
	switch t := v.(type) {
	case float64:
		if t > 0 && t <= 65535 {
			return uint16(t)
		}
	case string:
		if p, err := strconv.Atoi(t); err == nil && p > 0 && p <= 65535 {
			return uint16(p)
		}
	}
	return 0
}

// asString map 取值转 string。
func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// asDefaultString 取值或默认。
func asDefaultString(v any, def string) string {
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return def
}

// boolPtr 便捷 bool 指针。
func boolPtr(b bool) *bool { return &b }
