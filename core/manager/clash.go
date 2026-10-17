// clash 节点类型与可插拔接缝（P4-2 先定型，P4-3 填真实解析/生成）。
package manager

import "strings"

// ClashNode 单个 clash 节点（serde 名与 Rust ClashNode 一致）。
type ClashNode struct {
	Name              string            `json:"name"`
	Server            string            `json:"server"`
	Port              uint16            `json:"port"`
	NodeType          string            `json:"type"`
	Password          string            `json:"password,omitempty"`
	Username          string            `json:"username,omitempty"` // http/socks 代理用户名（clash 节点原生字段）
	UUID              string            `json:"uuid,omitempty"`
	Cipher            string            `json:"cipher,omitempty"`
	SNI               string            `json:"sni,omitempty"`
	ServerName        string            `json:"servername,omitempty"`
	TLS               *bool             `json:"tls,omitempty"`
	SkipCertVerify    *bool             `json:"skip-cert-verify,omitempty"`
	Network           string            `json:"network,omitempty"`
	Up                string            `json:"up,omitempty"`
	Down              string            `json:"down,omitempty"`
	Obfs              string            `json:"obfs,omitempty"`
	ObfsPassword      string            `json:"obfs-password,omitempty"`
	WsPath            string            `json:"ws-opts.path,omitempty"`
	WSHeaders         map[string]string `json:"ws-headers,omitempty"`
	ClientFingerprint string            `json:"client-fingerprint,omitempty"`
	Flow              string            `json:"flow,omitempty"`
	RealityPublicKey  string            `json:"reality-opts.public-key,omitempty"`
	RealityShortID    string            `json:"reality-opts.short-id,omitempty"`
	PrivateKey        string            `json:"private-key,omitempty"` // wireguard 客户端私钥
	PublicKey         string            `json:"public-key,omitempty"`  // wireguard 对端公钥
	AuthStr           string            `json:"auth-str,omitempty"`    // hysteria v1 认证串
	Group             string            `json:"group,omitempty"`
}

// authlessProxyTypes 本身不需要凭据的代理类型：HTTP / SOCKS 代理可以完全无认证
// （内网、局域网、机房白名单 IP 代理都是这种形态）。
// 若不特判，「Password == "" && UUID == ""」会把它们误判成「无凭据节点」——
// 前端打上「✗无凭据」红字、扫描时被直接跳过，用户导入的可用节点因此永远扫不出来。
var authlessProxyTypes = map[string]bool{
	"http": true, "https": true,
	"socks": true, "socks5": true, "socks5h": true, "socks4": true,
}

// nodeHasCredential 节点是否具备连接凭据（含「该类型本就无需凭据」）。
// 供节点列表（has_cred）与扫描过滤共用，避免两处口径漂移。
func nodeHasCredential(n ClashNode) bool {
	if n.Password != "" || n.UUID != "" {
		return true
	}
	return authlessProxyTypes[strings.ToLower(n.NodeType)]
}

// SeamFuncs 汇聚 P4-3/4 才填充的可插拔能力（实例/探针/网关在启动时调用）。
type SeamFuncs struct {
	// ResolveNode 按名查找节点（P4-3 clash.go 填）。
	ResolveNode func(name string) (ClashNode, bool)
	// BuildSingbox 生成 sing-box 配置（P4-3 singbox.go 填）。
	BuildSingbox func(node ClashNode, listenPort uint16) ([]byte, error)
	// BuildOpenCfg 生成实例 opencode2api.json（opencode_cfg.go 填）。
	BuildOpenCfg func(singboxPort uint16) ([]byte, error)
	// ListNodes 全量节点列表（P4-3 clash.go 填，探针用）。
	ListNodes func() []ClashNode
}

// SetSeams 注入接缝（生产装配；测试可换）。
func (m *Manager) SetSeams(s *SeamFuncs) {
	m.seamsMu.Lock()
	defer m.seamsMu.Unlock()
	m.seamsFn = s
}

// currentSeams 获取当前接缝（未装配时返回空占位，调用方自行判空）。
func (m *Manager) currentSeams() *SeamFuncs {
	m.seamsMu.Lock()
	defer m.seamsMu.Unlock()
	if m.seamsFn == nil {
		return &SeamFuncs{}
	}
	return m.seamsFn
}
