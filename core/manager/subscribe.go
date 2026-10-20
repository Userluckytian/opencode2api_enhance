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
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// SubscribeNode 订阅节点（轻量结构，可落为实例；raw 保留原始链接）。
// JSON 字段与 Rust SubscribeNode（serde snake_case）一致。
// 阶段 X：SSR/grpc/UDP/Fingerprint 为协议模块化补充，全部 omitempty——旧缓存无迁移读入。
type SubscribeNode struct {
	Name              string            `json:"name"`
	Server            string            `json:"server"`
	Port              uint16            `json:"port"`
	NodeType          string            `json:"node_type"`
	Password          string            `json:"password,omitempty"`
	Username          string            `json:"username,omitempty"` // http/socks 代理用户名（无认证代理留空）
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
	Obfs              string            `json:"obfs,omitempty"`               // hysteria2 Salamander / SSR obfs 类型（plain/http_simple/…）
	ObfsPassword      string            `json:"obfs_password,omitempty"`
	// —— SSR 专有（类型=ssr 时有值；sing-box 内核不可转发，解析/保存/导出全支持，见 caps.go）——
	SSRProtocol    string `json:"ssr_protocol,omitempty"`       // 传输协议 auth_aes128_md5 等
	SSRProtoParam  string `json:"ssr_protocol_param,omitempty"` // protocol-param
	SSRObfsParam   string `json:"ssr_obfs_param,omitempty"`     // obfs-param
	GrpcService    string `json:"grpc_service,omitempty"`       // grpc transport 的 service_name
	UDP            bool   `json:"udp,omitempty"`                // caps.nodeUDP 三态判定结果（解析产物，非猜测）
	SkipCertVerify bool   `json:"skip_cert_verify,omitempty"`
	Group          string `json:"group,omitempty"` // 订阅来源分组名
	Raw            string `json:"raw"`
	// Fingerprint 节点身份指纹（uniproxy parser.go:386 同款思路：协议/服务器/凭据哈希，
	// 排除显示名——机场改节点备注不再打断实例匹配与刷新 diff）。
	// 解析入库时由 refreshSubscriptionFingerprints/导入路径填充；空 = 旧缓存未迁移（diff 回退 name 匹配）。
	Fingerprint string `json:"fingerprint,omitempty"`
}

// SubscriptionMeta 订阅元信息（来自 HTTP 响应头，clash-verge-rev 同款解析；
// Profile 额外取自订阅内容本身的配置名）。
type SubscriptionMeta struct {
	Name     string `json:"name,omitempty"`
	Upload   uint64 `json:"upload,omitempty"`
	Download uint64 `json:"download,omitempty"`
	Total    uint64 `json:"total,omitempty"`
	Expire   uint64 `json:"expire,omitempty"`
	Home     string `json:"home,omitempty"`
	Profile  string `json:"profile,omitempty"`
}

// fetchSubscription 拉取并解析订阅 URL，返回节点列表。
func fetchSubscription(url string) ([]SubscribeNode, error) {
	nodes, _, err := fetchSubscriptionWithMeta(url)
	return nodes, err
}

// fetchSubscriptionWithMeta 拉取并解析订阅 URL，同时解析响应头元信息（订阅名/流量/到期）。
func fetchSubscriptionWithMeta(url string) ([]SubscribeNode, SubscriptionMeta, error) {
	nodes, meta, _, err := fetchSubscriptionWithMetaRep(url)
	return nodes, meta, err
}

// fetchSubscriptionWithMetaRep 同 fetchSubscriptionWithMeta，额外返回解析问题汇总（阶段 X B2）。
func fetchSubscriptionWithMetaRep(url string) ([]SubscribeNode, SubscriptionMeta, *ParseReport, error) {
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, SubscriptionMeta{}, nil, fmt.Errorf("订阅拉取失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, SubscriptionMeta{}, nil, fmt.Errorf("订阅拉取失败: HTTP %d", resp.StatusCode)
	}
	meta := parseSubscriptionHeaders(resp.Header)
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, SubscriptionMeta{}, nil, fmt.Errorf("读取订阅内容失败: %v", err)
	}
	nodes, rep, err := parseSubscriptionWithReport(string(body))
	meta.Profile = extractProfileName(string(body))
	return nodes, meta, rep, err
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

// extractProfileName 从订阅内容头部提取配置名（Clash Verge/mihomo 社区约定）。
// 优先级：1) 注释行 `# Profile: 名称`（兼容 `##`/大小写变体）；
// 2) 顶层 `profile:` 块内的 `name:` 字段；3) 单行 `profile-name/profile_name` 等变体。
// 只扫头部 30 行，避免正文（大幅 proxies 配置）误匹配；找不到返回空串。
func extractProfileName(body string) string {
	raws := strings.SplitN(body, "\n", 31)
	if len(raws) > 30 {
		raws = raws[:30]
	}
	t := func(i int) string { return strings.TrimSpace(raws[i]) }
	// 1) 注释行 # Profile: 名称
	for i := range raws {
		c := strings.TrimSpace(strings.TrimLeft(raws[i], " \t"))
		if !strings.HasPrefix(c, "#") {
			continue
		}
		c = strings.TrimSpace(strings.TrimLeft(c, "#"))
		// 前缀大小写不敏感匹配；值从原串取，保留原始大小写
		if strings.HasPrefix(strings.ToLower(c), "profile:") {
			name := strings.TrimSpace(c[len("profile:"):])
			// 去掉行内尾注（# 后内容）
			if before, _, found := strings.Cut(name, "#"); found {
				name = strings.TrimSpace(before)
			}
			if name != "" {
				return name
			}
		}
	}
	// 2) 顶层 profile: 块（单独成行）→ 其后 name: 字段（限 10 行内）
	for i := range raws {
		if !strings.EqualFold(t(i), "profile:") {
			continue
		}
		for j := 1; j <= 10 && i+j < len(raws); j++ {
			sub := raws[i+j]
			if strings.TrimSpace(sub) == "" {
				continue
			}
			// 已进入下一顶层键（无缩进且非注释/列表项）则放弃本块
			if !strings.HasPrefix(sub, " ") && !strings.HasPrefix(sub, "\t") && !strings.HasPrefix(sub, "#") && !strings.HasPrefix(sub, "-") {
				break
			}
			if tsub := strings.TrimSpace(sub); strings.HasPrefix(strings.ToLower(tsub), "name:") {
				parts := strings.SplitN(strings.TrimSpace(tsub[len("name:"):]), "#", 2)
				if name := strings.TrimSpace(parts[0]); name != "" {
					return name
				}
			}
		}
		return ""
	}
	// 3) 单行变体：profile-name / profile_name / profilename : 或 = 名称
	re := regexp.MustCompile(`(?im)^\s*profile[_.-]?name\s*[:=]\s*([^\r\n#]+?)\s*$`)
	if m := re.FindStringSubmatch(body); len(m) > 1 {
		if name := strings.TrimSpace(m[1]); name != "" {
			return name
		}
	}
	return ""
}

// groupNameFor 确定订阅分组名：内容配置名 > 响应头订阅名 > URL 末段（去扩展名）> "订阅N"。
func (m *Manager) groupNameFor(url string, meta SubscriptionMeta) string {
	if meta.Profile != "" {
		return meta.Profile
	}
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
	nodes, _, err := parseSubscriptionWithReport(body)
	return nodes, err
}

// parseSubscriptionWithReport 同 parseSubscription，额外返回解析过程问题汇总（阶段 X B2）。
func parseSubscriptionWithReport(body string) ([]SubscribeNode, *ParseReport, error) {
	rep := &ParseReport{}
	trimmed := strings.TrimSpace(strings.TrimPrefix(body, "\uFEFF"))
	if trimmed == "" {
		return nil, rep, fmt.Errorf("订阅内容为空")
	}
	var nodes []SubscribeNode
	var err error
	switch {
	case strings.HasPrefix(trimmed, "proxies:") || (strings.Contains(trimmed, "proxies:") && strings.Contains(trimmed, "type:")):
		var clash []ClashNode
		clash, err = parseClashYAMLRep(trimmed, rep)
		if err == nil {
			nodes = make([]SubscribeNode, 0, len(clash))
			for i := range clash {
				nodes = append(nodes, subscribeFromClash(clash[i]))
			}
		}
	case strings.HasPrefix(trimmed, `{"outbounds"`) || strings.HasPrefix(trimmed, "{\n  \"outbounds\""):
		nodes, err = parseSingboxJSONRep(trimmed, rep)
	default:
		if text, ok := decodeBase64Loose(trimmed); ok {
			t := strings.TrimSpace(text)
			if strings.HasPrefix(t, `{"outbounds"`) {
				nodes, err = parseSingboxJSONRep(t, rep)
				break
			}
			if strings.Contains(t, "://") {
				nodes, err = parsePlainLinksRep(t, rep)
				break
			}
		}
		nodes, err = parsePlainLinksRep(trimmed, rep)
	}
	if err != nil {
		return nil, rep, err
	}
	// 出口统一过滤公告/伪节点（官网/更新时间/剩余流量…，全角/半角冒号均覆盖，口径同 Clash）
	filtered := nodes[:0]
	for _, n := range nodes {
		if !isPseudoNode(n.Name) {
			filtered = append(filtered, n)
		}
	}
	if len(filtered) == 0 {
		return nil, rep, fmt.Errorf("订阅内容中未解析到任何可用节点")
	}
	// 阶段 X（B1）：统一出口补指纹身份（排除显示名；机场改名不断实例/diff）。
	fillFingerprints(filtered)
	return filtered, rep, nil
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
	// 强制 TLS 名单唯一数据源 caps.go（原内联 anytls/hysteria2/hy2/tuic/hysteria/hy1 并入；
	// canonical 归一后 hy2/hy1 别名行为不变）。
	if c, ok := capOf(n.NodeType); ok && c.forceTLS {
		tls = true
	}
	return SubscribeNode{
		Name:              n.Name,
		Server:            n.Server,
		Port:              n.Port,
		NodeType:          n.NodeType,
		Password:          password,
		Username:          n.Username,
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
		// 阶段 X：SSR 三键直通 + UDP 能力（caps 三态：force 恒真 / opt-in 看声明 / none 恒假）。
		SSRProtocol:    n.SSRProtocol,
		SSRProtoParam:  n.SSRProtoParam,
		SSRObfsParam:   n.SSRObfsParam,
		GrpcService:    n.GrpcService,
		UDP:            nodeUDP(n.NodeType, n.UDP != nil && *n.UDP),
		SkipCertVerify: n.SkipCertVerify != nil && *n.SkipCertVerify,
		Group:          n.Group,
		Raw:            fmt.Sprintf("%s@%s:%d", n.NodeType, n.Server, n.Port),
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
	return parsePlainLinksRep(s, nil)
}

// parsePlainLinksRep 同 parsePlainLinks，坏行不杀好节点、逐条收集为 rep（阶段 X B2）。
func parsePlainLinksRep(s string, rep *ParseReport) ([]SubscribeNode, error) {
	var nodes []SubscribeNode
	for i, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if node, ok, err := parseURILink(line); err != nil {
			// 对齐 Rust：跳过无法解析的行（仅记录，不中断）；rep 记账含行号。
			if rep != nil {
				rep.add(fmt.Sprintf("第 %d 行", i+1), "%v", err)
			}
			continue
		} else if ok {
			if isPseudoNode(node.Name) {
				// 公告/信息伪节点（官网/时间/流量/邮箱…，口径同 Clash）过滤
				continue
			}
			nodes = append(nodes, node)
		} else if rep != nil && strings.Contains(line, "://") {
			// 形似链接但 scheme 不认识 / 缺关键段（parseURILink 返回 ok=false,err=nil）：
			// 不静默丢，计入跳过（uniproxy「坏条目不杀好节点但绝不悄悄丢」口径）。
			scheme := line[:strings.Index(line, "://")]
			rep.addSkip(fmt.Sprintf("第 %d 行", i+1), "未识别的链接协议 %s://（跳过）", scheme)
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

// isPseudoNode 统一伪节点判定：公告前缀（isInfoPseudoNode）+ Clash 垃圾关键词（isJunkNode）并集。
// 全角冒号由 isInfoPseudoNode 覆盖、半角冒号/`-----` 前缀由 isJunkNode 覆盖；两个基础谓词语义不变。
func isPseudoNode(name string) bool {
	return isInfoPseudoNode(name) || isJunkNode(name)
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
	// ssr:// 解析入库（阶段 X）：节点池可见、可导出到第三方客户端；
	// 但 caps.singbox=false——永不生成实例（导入摘要明示，非静默丢弃）。
	case strings.HasPrefix(line, "ssr://"):
		n, err := parseSSR(strings.TrimPrefix(line, "ssr://"))
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
		n, err := parseSocks(strings.TrimPrefix(line, "socks://"), false)
		return n, true, err
	// socks5:// / socks5h:// / socks4:// 与 socks:// 同义（v2rayN / Clash 社区两种写法都会出现）。
	// socks5h 表示 DNS 也走代理；sing-box 的 socks 出站默认由远端解析域名，无需额外字段。
	case strings.HasPrefix(line, "socks5://"):
		n, err := parseSocks(strings.TrimPrefix(line, "socks5://"), false)
		return n, true, err
	case strings.HasPrefix(line, "socks5h://"):
		n, err := parseSocks(strings.TrimPrefix(line, "socks5h://"), false)
		return n, true, err
	case strings.HasPrefix(line, "socks4://"):
		n, err := parseSocks(strings.TrimPrefix(line, "socks4://"), true)
		return n, true, err
	// http:// / https:// 代理链接（机房 / 家宽 HTTP 代理）。https 仅表示「到代理本身走 TLS」，
	// sing-box http 出站的 TLS 由 tls.enabled 表达，故需要把 scheme 传进解析器。
	case strings.HasPrefix(line, "http://"):
		n, err := parseHTTPProxy(strings.TrimPrefix(line, "http://"), false)
		return n, true, err
	case strings.HasPrefix(line, "https://"):
		n, err := parseHTTPProxy(strings.TrimPrefix(line, "https://"), true)
		return n, true, err
	}
	return SubscribeNode{}, false, nil
}

// parseSingboxJSON 解析 sing-box JSON 订阅（{"outbounds":[...]}；字段与 singbox outbound 同构逆向映射）。
func parseSingboxJSON(body string) ([]SubscribeNode, error) {
	return parseSingboxJSONRep(body, nil)
}

// parseSingboxJSONRep 同 parseSingboxJSON；未知类型/缺 server 的 outbound 计 skipped（阶段 X B2）。
func parseSingboxJSONRep(body string, rep *ParseReport) ([]SubscribeNode, error) {
	var doc map[string]any
	if json.Unmarshal([]byte(body), &doc) != nil {
		return nil, fmt.Errorf("解析 sing-box JSON 失败")
	}
	obs, _ := doc["outbounds"].([]any)
	if obs == nil {
		return nil, fmt.Errorf("sing-box JSON 缺少 outbounds 数组")
	}
	var nodes []SubscribeNode
	for oi, o := range obs {
		ob, _ := o.(map[string]any)
		if ob == nil {
			continue
		}
		nodeType, _ := ob["type"].(string)
		// sing-box 配置里的路由类 outbound（直连/拦截/DNS）不是代理节点，静默忽略不记账。
		if nodeType == "direct" || nodeType == "block" || nodeType == "dns" {
			continue
		}
		if nodeType == "" {
			if rep != nil {
				rep.addSkip(outboundWhere(oi), "outbound 缺 type 字段被跳过")
			}
			continue
		}
		tag, _ := ob["tag"].(string)
		server, _ := ob["server"].(string)
		port := jsonUint16(ob["server_port"])
		if server == "" || port == 0 {
			if rep != nil {
				rep.addSkip(outboundWhere(oi), "%s 缺 server/server_port 被跳过", nodeType)
			}
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
		var network, wsPath, flow, grpcService string
		var wsHeaders map[string]string
		if tr, ok := ob["transport"].(map[string]any); ok {
			network, _ = tr["type"].(string)
			wsPath, _ = tr["path"].(string)
			// 阶段 X：grpc service_name 往返（消灭「导入再导出丢信息」循环）。
			if network == "grpc" {
				grpcService, _ = tr["service_name"].(string)
			}
			if hdrs, ok := tr["headers"].(map[string]any); ok {
				hm := make(map[string]string, len(hdrs))
				for k, v := range hdrs {
					if s, ok := v.(string); ok {
						hm[k] = s
					}
				}
				if len(hm) > 0 {
					wsHeaders = hm
				}
			}
		}
		flow, _ = ob["flow"].(string)
		var obfs, obfsPassword string
		if of, ok := ob["obfs"].(map[string]any); ok {
			obfs, _ = of["type"].(string)
			obfsPassword, _ = of["password"].(string)
		}

		var password, uuid, cipher, username string
		var ssrProtocol, ssrProtoParam, ssrObfsParam string
		// 别名收敛（hy2→hysteria2、shadowsocks→ss、shadowsocksr→ssr）；未知保留原值（报错文案可读）。
		if canon := canonicalProtocol(nodeType); canon != "" {
			nodeType = canon
		}
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
		case "ss":
			password, _ = ob["password"].(string)
			cipher, _ = ob["method"].(string)
		case "wireguard":
			password, _ = ob["private_key"].(string)
		case "socks5", "socks4", "http", "https":
			password, _ = ob["password"].(string)
			username, _ = ob["username"].(string)
		// 补全既有缺口：hysteria v1 / anytls 此前落 default 被静默丢。
		case "hysteria":
			password, _ = ob["password"].(string)
			if password == "" {
				password, _ = ob["auth_str"].(string)
			}
		case "anytls":
			password, _ = ob["password"].(string)
		// 阶段 X：fork 版 sing-box（nekobox 等带 SSR 的衍生内核）导出的 JSON 订阅可含
		// shadowsocksr outbound——解析入池（官方 sing-box 不可转发，caps 明示过滤）。
		case "ssr":
			password, _ = ob["password"].(string)
			cipher, _ = ob["method"].(string)
			ssrProtocol, _ = ob["protocol"].(string)
			ssrProtoParam, _ = ob["protocol_param"].(string)
			obfs, _ = ob["obfs"].(string)
			ssrObfsParam, _ = ob["obfs_param"].(string)
		default:
			if rep != nil {
				rep.addSkip(outboundWhere(oi), "未识别的 outbound 类型 %q 被跳过", nodeType)
			}
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
			Username:          username,
			UUID:              uuid,
			Cipher:            cipher,
			SNI:               sni,
			Network:           network,
			WsPath:            wsPath,
			WsHeaders:         wsHeaders,
			GrpcService:       grpcService,
			Flow:              flow,
			TLS:               tlsEnabled || realityPbk != "",
			RealityPbk:        realityPbk,
			RealitySid:        realitySid,
			ClientFingerprint: fingerprint,
			Obfs:              obfs,
			ObfsPassword:      obfsPassword,
			SSRProtocol:       ssrProtocol,
			SSRProtoParam:     ssrProtoParam,
			SSRObfsParam:      ssrObfsParam,
			UDP:               nodeUDP(nodeType, false),
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
	// vmess 的 host 字段是 ws/http 传输的 Host 头（CDN 前置节点缺了会被 403）。
	var hdrs map[string]string
	if h := asString(v["host"]); h != "" && !strings.HasPrefix(h, ".") {
		hdrs = map[string]string{"Host": h}
	}
	return SubscribeNode{
		Name:      name,
		Server:    server,
		Port:      port,
		NodeType:  "vmess",
		UUID:      asString(v["id"]),
		Cipher:    asDefaultString(v["scy"], "auto"),
		SNI:       asString(v["sni"]),
		Network:   asDefaultString(v["net"], "tcp"),
		WsPath:    asString(v["path"]),
		WsHeaders: hdrs,
		TLS:       tls,
		Raw:       "vmess://" + rest,
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
	// host 参数是 ws/http Host 头（CDN 前置节点必须带上，否则 Cloudflare 403）。
	var hdrs map[string]string
	if h := params["host"]; h != "" && !strings.HasPrefix(h, ".") {
		hdrs = map[string]string{"Host": h}
	}
	return SubscribeNode{
		Name:      nname,
		Server:    server,
		Port:      port,
		NodeType:  "trojan",
		Password:  password,
		SNI:       params["sni"],
		Network:   network,
		WsPath:    params["path"],
		WsHeaders: hdrs,
		TLS:       tls,
		Raw:       "trojan://" + rest,
	}, nil
}

// parseSSR 解析 ssr://（fancy-ssr 规范，兼容 mihomo/v2rayN 导出形态）：
//
//	ssr://base64url( host:port:protocol:cipher:password:base64url( obfs[/?k=v&...] ) )[/?外层k=v]#name
//
// 内层 query 键：obfs / obfsparam / protoparam / protocol / uid / sid / group / tls-auth /
// udp-port / ssrport；obfsparam、protoparam、uid、sid、group 为 **二次编码**
// （utf8 → percentEncode → base64url），解码顺序相反。
// 阶段 X 定位：SSR 仅解析入库（节点池可见、可导出到第三方客户端）；
// sing-box 内核不可转发（caps.singbox=false），实例生成端明示过滤。
func parseSSR(rest string) (SubscribeNode, error) {
	body, name := splitFragment(rest)
	var outerQuery string
	// 外层 /?k=v（少数导出把参数放外层；与内层合并，内层优先由后写覆盖决定）
	if h, q, ok := strings.Cut(body, "/?"); ok {
		body, outerQuery = h, q
	} else if i := strings.IndexByte(body, '?'); i >= 0 {
		body, outerQuery = body[:i], body[i+1:]
	}
	raw, ok := decodeBase64Loose(body)
	if !ok {
		return SubscribeNode{}, fmt.Errorf("ssr payload 非 base64")
	}
	parts := strings.SplitN(strings.TrimSpace(raw), ":", 6)
	if len(parts) < 5 {
		return SubscribeNode{}, fmt.Errorf("ssr payload 段数不足（期望 host:port:protocol:cipher:password[:inner]）")
	}
	server, port, err := splitHostPort(parts[0] + ":" + strings.TrimSpace(parts[1]))
	if err != nil {
		return SubscribeNode{}, fmt.Errorf("ssr host:port: %w", err)
	}
	protocol, cipher, password := parts[2], parts[3], parts[4]
	obfs := "plain"
	var protoParam, obfsParam, group, udpPort string
	if len(parts) == 6 { // 第 6 段 base64url(obfs[/?query])（旧导出可能为空）
		if inner, ok := decodeBase64Loose(parts[5]); ok {
			if hq, q, ok := strings.Cut(inner, "/?"); ok {
				obfs = hq
				protoParam, obfsParam, group, udpPort = ssrInnerParams(q)
			} else if inner != "" {
				obfs = inner
			}
		}
	}
	// 外层参数并入（可覆盖内层）
	if outerQuery != "" {
		if p, o, g, u := ssrInnerParams(outerQuery); true {
			if p != "" {
				protoParam = p
			}
			if o != "" {
				obfsParam = o
			}
			if g != "" {
				group = g
			}
			if u != "" {
				udpPort = u
			}
		}
	}
	if name == "" {
		if g := ssrQueryValue(outerQuery, "remarks"); g != "" {
			name = ssrDoubleDecode(g)
		}
	}
	if name == "" {
		name = server
	}
	udp := udpPort != "" && udpPort != "0"
	return SubscribeNode{
		Name:          name,
		Server:        server,
		Port:          port,
		NodeType:      "ssr",
		Password:      password,
		Cipher:        cipher,
		Obfs:          obfs,       // SSR 的 obfs 类型（plain/http_simple/tls1.2_ticket_auth…）
		SSRProtocol:   protocol,   // 传输协议（origin/auth_aes128_md5/auth_chain_a…）
		SSRProtoParam: protoParam, // protocol-param
		SSRObfsParam:  obfsParam,  // obfs-param
		Network:       "tcp",
		UDP:           nodeUDP("ssr", udp),
		TLS:           false,
		Group:         group,
		Raw:           "ssr://" + rest,
	}, nil
}

// ssrInnerParams 提取内层 query 的 protoparam/obfsparam/group/udp-port（中文参数为
// percentEncode→base64url 二次编码；单重编码/原文也容错接受）。
func ssrInnerParams(q string) (protoParam, obfsParam, group, udpPort string) {
	for _, pair := range strings.Split(q, "&") {
		k, v, ok := strings.Cut(pair, "=")
		if !ok {
			continue
		}
		switch k {
		case "protoparam", "protocol-param":
			protoParam = ssrDoubleDecode(v)
		case "obfsparam", "obfs-param":
			obfsParam = ssrDoubleDecode(v)
		case "group":
			group = ssrDoubleDecode(v)
		case "udp-port", "ssrport":
			udpPort = v
		}
	}
	return
}

// ssrQueryValue 取 query 里某键的原始值。
func ssrQueryValue(q, key string) string {
	for _, pair := range strings.Split(q, "&") {
		if k, v, ok := strings.Cut(pair, "="); ok && k == key {
			return v
		}
	}
	return ""
}

// ssrDoubleDecode fancy-ssr 二次编码参数：先 base64url 再 percent；
// 解不出可打印 UTF-8 时退回单层 percent（各机场/客户端实现不一致，取可用者）。
func ssrDoubleDecode(v string) string {
	if v == "" {
		return ""
	}
	if b, ok := decodeBase64Loose(v); ok && utf8.ValidString(b) {
		return percentDecode(b)
	}
	return percentDecode(v)
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

// parseSocks socks://user:pass@host:port#name（socks5:// / socks5h:// / socks4:// 同义）。
// 无认证写法 socks5://host:port 也合法（内网/局域网代理常见）：缺 @ 时整串当 hostport，凭据留空。
// isV4 决定 NodeType（"socks4" / "socks"）——sing-box 出站的 version 字段据此生成。
func parseSocks(rest string, isV4 bool) (SubscribeNode, error) {
	auth, name := splitFragment(rest)
	userinfo, hostport, ok := strings.Cut(auth, "@")
	if !ok {
		userinfo, hostport = "", auth
	}
	server, port, err := splitHostPort(hostport)
	if err != nil {
		return SubscribeNode{}, err
	}
	username, password := userinfo, ""
	if u, p, ok := strings.Cut(userinfo, ":"); ok {
		username, password = u, p
	}
	username = percentDecodeStrict(username)
	password = percentDecodeStrict(password)
	nname := name
	if nname == "" {
		nname = server
	}
	nodeType := "socks"
	if isV4 {
		nodeType = "socks4"
	}
	return SubscribeNode{
		Name:     nname,
		Server:   server,
		Port:     port,
		NodeType: nodeType,
		Password: password,
		Username: username,
		TLS:      false,
		Raw:      "socks://" + rest,
	}, nil
}

// parseHTTPProxy http://user:pass@host:port#name（https:// 同义，tls=true）。
// 用户名与密码分别落到 Username / Password（sing-box http 出站直接对应）。
func parseHTTPProxy(rest string, tls bool) (SubscribeNode, error) {
	auth, name := splitFragment(rest)
	userinfo, hostport, ok := strings.Cut(auth, "@")
	if !ok {
		userinfo, hostport = "", auth
	}
	server, port, err := splitHostPort(hostport)
	if err != nil {
		return SubscribeNode{}, err
	}
	username, password := userinfo, ""
	if u, p, ok := strings.Cut(userinfo, ":"); ok {
		username, password = u, p
	}
	username = percentDecodeStrict(username)
	password = percentDecodeStrict(password)
	nname := name
	if nname == "" {
		nname = server
	}
	scheme := "http://"
	if tls {
		scheme = "https://"
	}
	return SubscribeNode{
		Name:     nname,
		Server:   server,
		Port:     port,
		NodeType: "http",
		Password: password,
		Username: username,
		TLS:      tls,
		Raw:      scheme + rest,
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

// percentDecodeStrict 只解 %XX，**不**把 `+` 当空格。
// userinfo（用户名/密码）里 `+` 是字面字符——`+`→空格 只是 query/form 编码的约定，
// 用它解密码会把 `a+b` 悄悄改成 `a b`，导致认证失败且极难排查。
func percentDecodeStrict(s string) string {
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
		out = append(out, s[i])
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

// 订阅缓存读写全程持 subscriptionCacheMu：load→merge→save 必须成对在同一临界区内
// 完成（load 旧快照 → 另一路径写入 → 我方基于旧快照覆盖会让一组节点从缓存消失）。
// 落盘走临时文件+Rename 原子替换（G9 模式），读方任一时刻看到完整旧文件或完整新文件。

// saveSubscriptionCache 持久化订阅节点缓存（覆盖写）。
func (m *Manager) saveSubscriptionCache(nodes []SubscribeNode) error {
	m.subscriptionCacheMu.Lock()
	defer m.subscriptionCacheMu.Unlock()
	return m.saveSubscriptionCacheLocked(nodes)
}

// saveSubscriptionCacheLocked 持久化订阅节点缓存（锁内调用）。
func (m *Manager) saveSubscriptionCacheLocked(nodes []SubscribeNode) error {
	data, err := json.MarshalIndent(nodes, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化订阅缓存失败: %v", err)
	}
	return m.writeSubscriptionCache(data)
}

// writeSubscriptionCache 临时文件+Rename 原子落盘订阅缓存。
func (m *Manager) writeSubscriptionCache(data []byte) error {
	path := m.subscriptionCachePath()
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("创建订阅缓存目录失败: %v", err)
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "subscription-*.tmp")
	if err != nil {
		return fmt.Errorf("创建订阅缓存临时文件失败: %v", err)
	}
	defer os.Remove(tmp.Name()) // 失败路径清理；成功后目标已不存在，无副作用
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("写入订阅缓存临时文件失败: %v", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭订阅缓存临时文件失败: %v", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("原子替换订阅缓存失败: %v", err)
	}
	return nil
}

// loadSubscriptionCache 读取订阅缓存（不存在/损坏返回空）。
func (m *Manager) loadSubscriptionCache() []SubscribeNode {
	m.subscriptionCacheMu.Lock()
	defer m.subscriptionCacheMu.Unlock()
	return m.loadSubscriptionCacheLocked()
}

// loadSubscriptionCacheLocked 读取订阅缓存（锁内调用）。
func (m *Manager) loadSubscriptionCacheLocked() []SubscribeNode {
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
	m.subscriptionCacheMu.Lock()
	defer m.subscriptionCacheMu.Unlock()
	return m.removeSubscriptionNodesLocked([]string{name})
}

// RemoveSubscriptionNodes 批量删除订阅缓存节点（一次加载+持久化），返回删除数量。
// 已入实例的节点照常列入（实例仍保留其完整配置），外部 Clash 节点静默跳过。
func (m *Manager) RemoveSubscriptionNodes(names []string) (int, error) {
	if len(names) == 0 {
		return 0, nil
	}
	m.subscriptionCacheMu.Lock()
	defer m.subscriptionCacheMu.Unlock()
	return m.removeSubscriptionNodesLocked(names)
}

// removeSubscriptionNodesLocked 锁内 load→filter→save 成对（防并发覆盖丢数据）。
func (m *Manager) removeSubscriptionNodesLocked(names []string) (int, error) {
	wanted := map[string]bool{}
	for _, n := range names {
		wanted[n] = true
	}
	nodes := m.loadSubscriptionCacheLocked()
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
	if err := m.saveSubscriptionCacheLocked(filtered); err != nil {
		return 0, err
	}
	return before - len(filtered), nil
}

// RemoveSubscriptionGroupNodes 从订阅缓存删除某分组全部节点（一次加载+持久化），
// 返回删除数量。删除订阅时同步清理该分组节点，节点池页不再残留（P4）。
func (m *Manager) RemoveSubscriptionGroupNodes(group string) (int, error) {
	if group == "" {
		return 0, nil
	}
	m.subscriptionCacheMu.Lock()
	defer m.subscriptionCacheMu.Unlock()
	nodes := m.loadSubscriptionCacheLocked()
	before := len(nodes)
	filtered := nodes[:0]
	for _, n := range nodes {
		if n.Group != group {
			filtered = append(filtered, n)
		}
	}
	if len(filtered) == before {
		return 0, nil
	}
	if err := m.saveSubscriptionCacheLocked(filtered); err != nil {
		return 0, err
	}
	return before - len(filtered), nil
}

// toClashNode SubscribeNode → ClashNode（供 sing-box 生成与节点列表合并）。
func toClashNode(n SubscribeNode) ClashNode {
	skip := n.SkipCertVerify
	udp := n.UDP
	return ClashNode{
		Name:              n.Name,
		Server:            n.Server,
		Port:              n.Port,
		NodeType:          n.NodeType,
		Password:          n.Password,
		Username:          n.Username,
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
		// 阶段 X：SSR/grpc/UDP 反向直通（节点池展示与导出用；sing-box 生成端对 ssr 在 caps 拦截）。
		SSRProtocol:   n.SSRProtocol,
		SSRProtoParam: n.SSRProtoParam,
		SSRObfsParam:  n.SSRObfsParam,
		GrpcService:   n.GrpcService,
		UDP:           &udp,
	}
}

// listSubscriptionNodes 订阅缓存节点 → ClashNode 列表（并入节点池）。
// 展示时套统一伪节点谓词：旧缓存残留的伪节点从节点池隐藏（不改缓存文件）。
func (m *Manager) listSubscriptionNodes() []ClashNode {
	cache := m.loadSubscriptionCache()
	out := make([]ClashNode, 0, len(cache))
	for _, n := range cache {
		if isPseudoNode(n.Name) {
			continue
		}
		out = append(out, toClashNode(n))
	}
	return out
}

// ---------- 导入 ----------

// importSubscriptionPool 仅拉取并缓存订阅节点（不创建实例），返回节点数与分组名。
// 旧签名兼容包装：走带闸路径（force=false），拒绝时错误文案保持旧语义。
func (m *Manager) importSubscriptionPool(url string) (int, string, error) {
	res, err := m.importSubscriptionPoolFull(url, false)
	if err != nil {
		if strings.Contains(err.Error(), "refresh_rejected") {
			return 0, res.Group, fmt.Errorf("订阅刷新被保护闸拦截（节点数骤减或为空），已保留原节点；确认无误可在界面上强制刷新")
		}
		return 0, res.Group, err
	}
	return res.Imported, res.Group, nil
}

// UnsupportedBucket 不可转发类型聚合（导入摘要 / 节点 API 共用契约）。
type UnsupportedBucket struct {
	Type   string `json:"type"`
	Count  int    `json:"count"`
	Reason string `json:"reason"`
}

// SubscriptionImportResult 订阅导入摘要（阶段 X 契约：usable/unsupported 明示、issues 上报）。
type SubscriptionImportResult struct {
	Imported    int                 `json:"imported"`
	Group       string              `json:"group,omitempty"`
	Usable      int                 `json:"usable"`
	Unsupported []UnsupportedBucket `json:"unsupported,omitempty"`
	Issues      []ParseIssue        `json:"issues,omitempty"`
	IssuesTotal int                 `json:"issues_total"`
	Truncated   bool                `json:"issues_truncated,omitempty"`
}

// summarizeUsability 按 caps 统计可转发/不可转发桶。
func summarizeUsability(nodes []SubscribeNode) (int, []UnsupportedBucket) {
	usable := 0
	byType := map[string]*UnsupportedBucket{}
	var order []string
	for _, n := range nodes {
		ok, why := kernelUsable(n.NodeType)
		if ok {
			usable++
			continue
		}
		b, seen := byType[n.NodeType]
		if !seen {
			b = &UnsupportedBucket{Type: n.NodeType, Reason: why}
			byType[n.NodeType] = b
			order = append(order, n.NodeType)
		}
		b.Count++
	}
	out := make([]UnsupportedBucket, 0, len(order))
	for _, t := range order {
		out = append(out, *byType[t])
	}
	return usable, out
}

// importSubscriptionPoolFull 带闸导入 + 摘要（阶段 X B2/B3/T4）：
// 拉取 → 解析（issues 收集）→ 能力摘要 → shrink 闸写缓存（force 可控）。
// 解析错误（闸一的一部分语义，见 saveSubscriptionCacheGroupedGuarded 注释）不阻断入库，
// 只进摘要上报；骤减/空组（闸二）阻断并保留旧节点。
func (m *Manager) importSubscriptionPoolFull(url string, force bool) (SubscriptionImportResult, error) {
	nodes, meta, rep, err := fetchSubscriptionWithMetaRep(url)
	if err != nil {
		return SubscriptionImportResult{}, err
	}
	group := m.applyGroup(nodes, url, meta)
	res := SubscriptionImportResult{Imported: len(nodes), Group: group}
	if rep != nil {
		res.Issues, res.IssuesTotal, res.Truncated = rep.Issues, rep.Total, rep.Truncated
	}
	res.Usable, res.Unsupported = summarizeUsability(nodes)
	if err := m.saveSubscriptionCacheGroupedGuarded(nodes, force); err != nil {
		return res, err
	}
	return res, nil
}

// applyGroup 给节点标注订阅分组名（未标注的节点），返回分组名。
// 优先级：用户手动固定名（源记录 NamePinned）> 自动推导（groupNameFor）。
func (m *Manager) applyGroup(nodes []SubscribeNode, url string, meta SubscriptionMeta) string {
	group := m.groupNameFor(url, meta)
	if s, ok := m.subscriptionSourceByURL(url); ok && s.NamePinned && s.Group != "" {
		group = s.Group
	}
	for i := range nodes {
		if nodes[i].Group == "" {
			nodes[i].Group = group
		}
	}
	return group
}

// saveSubscriptionCacheGrouped 按分组合并订阅缓存：同分组节点替换、其他分组保留
// （多次导入不同订阅合并不顶替）。load→merge→save 全程持锁，与后台/手动导入并发安全。
func (m *Manager) saveSubscriptionCacheGrouped(nodes []SubscribeNode) error {
	m.subscriptionCacheMu.Lock()
	defer m.subscriptionCacheMu.Unlock()
	group := ""
	if len(nodes) > 0 {
		group = nodes[0].Group
	}
	if group == "" {
		return m.saveSubscriptionCacheLocked(nodes)
	}
	old := m.loadSubscriptionCacheLocked()
	var keep []SubscribeNode
	for _, n := range old {
		if n.Group != group {
			keep = append(keep, n)
		}
	}
	return m.saveSubscriptionCacheLocked(append(keep, nodes...))
}

// saveSubscriptionCacheGroupedGuarded 带 shrink 保护闸（阶段 X B3，uniproxy imports.go:264-284 思路）：
//   - 同一分组内，若按指纹 diff 的「将删除数」超过旧节点一半（或新结果为空）→ 拒绝写入
//     （错误含 refresh_rejected 哨兵），保留旧节点；force=true 越过。
//
// 与 uniproxy 的差异（刻意）：解析「非致命问题」（个别坏行/未知类型）不阻断写入——
// 我们的存量订阅普遍含杂字段，照抄「任一错误全留旧」会把正常刷新大面积打挂；
// 错误如实进导入摘要上报。致命解析失败（0 可用节点）在调用方 fetch 层已被 err 阻断，天然全留旧。
// 手动粘贴导入（ImportLinks/本地文件）走无闸版本：那是用户显式的小批量编辑，不该被百分比误伤。
func (m *Manager) saveSubscriptionCacheGroupedGuarded(nodes []SubscribeNode, force bool) error {
	group := ""
	if len(nodes) > 0 {
		group = nodes[0].Group
	}
	if group == "" || force {
		return m.saveSubscriptionCacheGrouped(nodes)
	}
	m.subscriptionCacheMu.Lock()
	defer m.subscriptionCacheMu.Unlock()
	old := m.loadSubscriptionCacheLocked()
	var oldGroup []SubscribeNode
	for _, n := range old {
		if n.Group == group {
			oldGroup = append(oldGroup, n)
		}
	}
	if len(oldGroup) > 0 {
		_, removed, _ := diffByFingerprint(oldGroup, nodes)
		if len(nodes) == 0 || len(removed)*2 > len(oldGroup) {
			return fmt.Errorf("refresh_rejected: 订阅 %q 本次结果较上次减少 %d/%d（超半数或为空），已保留原节点",
				group, len(removed), len(oldGroup))
		}
	}
	var keep []SubscribeNode
	for _, n := range old {
		if n.Group != group {
			keep = append(keep, n)
		}
	}
	return m.saveSubscriptionCacheLocked(append(keep, nodes...))
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
	existingFPs := map[string]bool{} // 阶段 X B1：实例指纹索引
	for _, e := range list {
		existingNames[e.Name] = true
		usedPorts[e.Port] = true
		usedPorts[e.SingboxPort] = true
		// 节点身份 = 节点名 + 节点地址（server:port）；实例端口是本地监听端口，与节点身份无关。
		// 阶段 X B1：另建指纹索引，让「同节点改备注」在下一轮拉取时被识别为已有实例。
		existingIDs[e.Node+"|"+e.IP] = true
		if e.NodeFP != "" {
			existingFPs[e.NodeFP] = true
		}
	}
	imported := 0
	for _, node := range nodes {
		nodeID := node.Name + "|" + fmt.Sprintf("%s:%d", node.Server, node.Port)
		// 指纹命中（含 name|ip 旧口径兜底）→ 已建过实例，跳过重复创建。
		if existingIDs[nodeID] || (node.Fingerprint != "" && existingFPs[node.Fingerprint]) {
			continue
		}
		existingIDs[nodeID] = true
		if node.Fingerprint != "" {
			existingFPs[node.Fingerprint] = true
		}
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
			NodeFP:      node.Fingerprint, // 阶段 X B1：机场改备注后靠指纹识别同节点
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
		// H6：sing-box 端口须按真实偏移记入 usedPorts——旧值 +10000 与本批后续节点
		// 选端（port+singboxPortOffset）不一致，同批导入时「后节点 API 端口 == 先节点
		// sing-box 端口」会静默通过，两实例同时启动必有一方 bind 失败。
		// 记账口径与 1221 行选端检查（usedPorts[port+singboxPortOffset]）及
		// BatchAdd 的 isPortUsedByInstance 对齐：本批内 API 端与 sing-box 端双向互查，
		// 无其它漏查点（候选 port 查 usedPorts[port] 覆盖撞先节点 sing-box，候选
		// port+offset 查 usedPorts[port+offset] 覆盖撞先节点 API）。
		usedPorts[port+singboxPortOffset] = true
		imported++
	}
	return imported, nil
}

// StartSubscribeLoop 旧版单条订阅后台循环（迁移期遗留，已废弃）：
// 旧 config.subscribe_url 会经 loadSubscriptions 迁移进多源订阅列表，功能已由
// RunAllSubscriptionLoop 取代。保留入口仅为兼容旧调用方：与多源调度共享
// subscribeLoopOnce 唯一启动保护，二者不会双启、不会并发拉同一 URL。
func (m *Manager) StartSubscribeLoop() {
	m.RunAllSubscriptionLoop()
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
		n, _, err := m.importSubscriptionPool(req.URL)
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
