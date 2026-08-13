// clash 节点类型与可插拔接缝（P4-2 先定型，P4-3 填真实解析/生成）。
package manager

// ClashNode 单个 clash 节点（serde 名与 Rust ClashNode 一致）。
type ClashNode struct {
	Name              string            `json:"name"`
	Server            string            `json:"server"`
	Port              uint16            `json:"port"`
	NodeType          string            `json:"type"`
	Password          string            `json:"password,omitempty"`
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
