package manager

// 节点池（WARP 注册源）——只服务 OpenCode 免费层。
//
// 背景：引擎注册 WARP 设备时需要一个「注册源代理」；默认只用 proxies 里第一个
// 非 direct 项，导致同一个源反复拿回同一个 /64。这里让管理端导入订阅、把选中
// 节点变成一组本地端口（由 nodefarm 托管的 mihomo listeners），再写进
// engine_warp_registration_proxies 供引擎轮换。
//
// 与设置里的「出口代理」完全隔离：节点池只写 registration_proxies，
// 不改 engine_proxies。

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// PoolNode 订阅里的一个节点（只保留创建 listeners 需要的信息）。
type PoolNode struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Server      string `json:"server"`
	Port        int    `json:"port"`
	UDP         bool   `json:"udp"`
	Fingerprint string `json:"fingerprint"`
}

// nodePoolCache 节点池缓存（dataDir/node-pool/nodes.json）。
type nodePoolCache struct {
	Source    string            `json:"source"`
	FetchedAt string            `json:"fetched_at"`
	Nodes     []PoolNode        `json:"nodes"`
	Blocks    map[string]string `json:"blocks"` // fingerprint → 原始 YAML 块
}

// NodePoolStatus 面板视图。
type NodePoolStatus struct {
	Enabled     bool       `json:"enabled"`
	Running     bool       `json:"running"`
	PID         int        `json:"pid"`
	PortBase    int        `json:"port_base"`
	Ports       []int      `json:"ports"`
	Nodes       []PoolNode `json:"nodes"`
	Selected    []string   `json:"selected"`
	NodeCount   int        `json:"node_count"`
	SelectedLen int        `json:"selected_count"`
	MihomoPath  string     `json:"mihomo_path"`
	// SocksUDP 当前写进引擎的握手中继（空 = 直连）。
	SocksUDP  string `json:"socks_udp,omitempty"`
	Warning   string `json:"warning,omitempty"`
	LastError string `json:"last_error,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
	SyncedTo  string `json:"synced_to,omitempty"`
}

var (
	reName      = regexp.MustCompile(`(?m)^\s*-?\s*name:\s*(.+?)\s*$`)
	reType      = regexp.MustCompile(`(?m)^\s*type:\s*(\S+)\s*$`)
	reServer    = regexp.MustCompile(`(?m)^\s*server:\s*(\S+)\s*$`)
	rePort      = regexp.MustCompile(`(?m)^\s*port:\s*(\d+)\s*$`)
	reUDP       = regexp.MustCompile(`(?m)^\s*udp:\s*(true|false)\s*$`)
	reListStart = regexp.MustCompile(`^\s*-\s`)
)

func (m *Manager) nodePoolDir() string {
	return filepath.Join(m.paths.DataDir, "node-pool")
}

func (m *Manager) nodePoolCachePath() string {
	return filepath.Join(m.nodePoolDir(), "nodes.json")
}

// LoadNodePoolCache 读缓存；不存在返回空结构。
func (m *Manager) LoadNodePoolCache() nodePoolCache {
	out := nodePoolCache{Blocks: map[string]string{}}
	raw, err := os.ReadFile(m.nodePoolCachePath())
	if err != nil {
		return out
	}
	if json.Unmarshal(raw, &out) != nil {
		out = nodePoolCache{Blocks: map[string]string{}}
	}
	if out.Blocks == nil {
		out.Blocks = map[string]string{}
	}
	return out
}

func (m *Manager) saveNodePoolCache(c nodePoolCache) error {
	if err := os.MkdirAll(m.nodePoolDir(), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.nodePoolCachePath(), raw, 0o644)
}

// ImportNodePool 从订阅 URL 或直接粘贴的内容导入节点（只保存，不启动）。
func (m *Manager) ImportNodePool(source, content string) (nodePoolCache, int, error) {
	body := content
	if strings.TrimSpace(body) == "" {
		if strings.TrimSpace(source) == "" {
			return nodePoolCache{}, 0, fmt.Errorf("需要订阅 URL 或粘贴内容")
		}
		client := &http.Client{Timeout: 30 * time.Second}
		req, err := http.NewRequest(http.MethodGet, source, nil)
		if err != nil {
			return nodePoolCache{}, 0, err
		}
		req.Header.Set("User-Agent", "clash.meta")
		resp, err := client.Do(req)
		if err != nil {
			return nodePoolCache{}, 0, fmt.Errorf("拉取订阅失败: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nodePoolCache{}, 0, fmt.Errorf("订阅返回 %d", resp.StatusCode)
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		if err != nil {
			return nodePoolCache{}, 0, err
		}
		body = string(raw)
	}
	if strings.Contains(strings.ToLower(body), "<html") {
		return nodePoolCache{}, 0, fmt.Errorf("订阅返回的是网页，不是 Clash 配置")
	}
	nodes, blocks, bad := parseClashProxies(body)
	if len(nodes) == 0 {
		return nodePoolCache{}, bad, fmt.Errorf("没有解析到 proxies 节点（当前只支持 Clash 格式订阅）")
	}
	cache := nodePoolCache{
		Source:    source,
		FetchedAt: time.Now().Format(time.RFC3339),
		Nodes:     nodes,
		Blocks:    blocks,
	}
	if err := m.saveNodePoolCache(cache); err != nil {
		return cache, bad, err
	}
	return cache, bad, nil
}

// parseClashProxies 从 Clash YAML 里切出 proxies 块，逐节点保留原始 YAML。
func parseClashProxies(doc string) ([]PoolNode, map[string]string, int) {
	doc = strings.ReplaceAll(doc, "\r\n", "\n")
	lines := strings.Split(doc, "\n")
	start := -1
	for i, l := range lines {
		if strings.TrimRight(l, " \t") == "proxies:" {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return nil, map[string]string{}, 0
	}
	var nodes []PoolNode
	blocks := map[string]string{}
	bad := 0
	cur := make([]string, 0, 16)
	flush := func() {
		if len(cur) == 0 {
			return
		}
		block := strings.Join(cur, "\n")
		name, typ, server, port, udp := parseProxyBlock(block)
		if name == "" || typ == "" {
			// Clash Verge 等工具常把节点写成单行 flow 风格：
			//   - { name: 'xxx', type: trojan, server: 1.2.3.4, port: 443, udp: true }
			if n, t, srv, p, u, ok := parseFlowProxy(strings.TrimSpace(block)); ok {
				name, typ, server, port, udp = n, t, srv, p, u
			}
		}
		if name == "" || typ == "" {
			bad++
			cur = cur[:0]
			return
		}
		fp := proxyFingerprint(name, typ, server, port)
		nodes = append(nodes, PoolNode{
			Name: name, Type: typ, Server: server, Port: port, UDP: udp, Fingerprint: fp,
		})
		blocks[fp] = block
		cur = cur[:0]
	}
	for i := start; i < len(lines); i++ {
		l := lines[i]
		trimmed := strings.TrimRight(l, " \t")
		if strings.TrimSpace(trimmed) == "" {
			if len(cur) > 0 {
				cur = append(cur, l)
			}
			continue
		}
		// 顶层键 → proxies 段结束
		if !strings.HasPrefix(l, " ") && !strings.HasPrefix(l, "\t") && !strings.HasPrefix(l, "-") && strings.Contains(l, ":") {
			flush()
			break
		}
		if reListStart.MatchString(l) || strings.TrimSpace(l) == "-" {
			flush()
			cur = append(cur, l)
			continue
		}
		if len(cur) > 0 {
			cur = append(cur, l)
		}
	}
	flush()
	sort.SliceStable(nodes, func(i, j int) bool { return nodes[i].Name < nodes[j].Name })
	return nodes, blocks, bad
}

func parseProxyBlock(block string) (name, typ, server string, port int, udp bool) {
	if m := reName.FindStringSubmatch(block); len(m) > 1 {
		name = strings.Trim(strings.TrimSpace(m[1]), `"'`)
	}
	if m := reType.FindStringSubmatch(block); len(m) > 1 {
		typ = strings.ToLower(strings.TrimSpace(m[1]))
	}
	if m := reServer.FindStringSubmatch(block); len(m) > 1 {
		server = strings.Trim(strings.TrimSpace(m[1]), `"'`)
	}
	if m := rePort.FindStringSubmatch(block); len(m) > 1 {
		port, _ = strconv.Atoi(m[1])
	}
	if m := reUDP.FindStringSubmatch(block); len(m) > 1 {
		udp = strings.EqualFold(m[1], "true")
	}
	// hysteria/hysteria2/tuic 本身就是 UDP 型，订阅里通常不写 udp: true。
	// 不补这一条，「只勾选 UDP 可用」会把这些节点全部漏掉。
	if udpByProtocol(typ) {
		udp = true
	}
	return
}

// udpByProtocol 判断协议是否天然支持 UDP（无需配置显式声明）。
func udpByProtocol(typ string) bool {
	switch strings.ToLower(typ) {
	case "hysteria", "hysteria2", "tuic":
		return true
	}
	return false
}

// reFlowEntry 匹配单行 flow 风格节点：`- { name: ..., type: ... }`。
var reFlowEntry = regexp.MustCompile(`^\s*-\s*\{(.*)\}\s*$`)

// parseFlowProxy 解析 `- { k: v, k2: v2 }` 形式的节点。
//
// Clash Verge 把远程订阅转成 flow 风格后很常见；block 风格的正则匹配不到，
// 不兼容会导致「导入成功 0 个节点」。
func parseFlowProxy(line string) (name, typ, server string, port int, udp bool, ok bool) {
	m := reFlowEntry.FindStringSubmatch(strings.TrimSpace(line))
	if m == nil {
		return
	}
	for _, field := range splitFlowFields(m[1]) {
		key, value, found := strings.Cut(field, ":")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		switch key {
		case "name":
			name = value
		case "type":
			typ = strings.ToLower(value)
		case "server":
			server = value
		case "port":
			port, _ = strconv.Atoi(value)
		case "udp":
			udp = strings.EqualFold(value, "true")
		}
	}
	if udpByProtocol(typ) {
		udp = true
	}
	ok = name != "" && typ != ""
	return
}

// splitFlowFields 按顶层逗号切分 flow 内容，跳过引号与 []/{} 内部。
func splitFlowFields(s string) []string {
	var out []string
	depth := 0
	var quote byte
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == '[' || c == '{':
			depth++
		case c == ']' || c == '}':
			if depth > 0 {
				depth--
			}
		case c == ',' && depth == 0:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}

func proxyFingerprint(name, typ, server string, port int) string {
	h := sha1.Sum([]byte(strings.ToLower(name + "|" + typ + "|" + server + "|" + strconv.Itoa(port))))
	return hex.EncodeToString(h[:])[:16]
}

// SelectedNodes 返回当前选中且仍存在的节点（按缓存顺序）。
func (m *Manager) SelectedNodes() []PoolNode {
	cfg := m.loadConfig()
	sel := map[string]bool{}
	for _, fp := range cfg.NodePoolSelected {
		if v := strings.TrimSpace(fp); v != "" {
			sel[v] = true
		}
	}
	cache := m.LoadNodePoolCache()
	out := make([]PoolNode, 0, len(sel))
	for _, n := range cache.Nodes {
		if sel[n.Fingerprint] {
			out = append(out, n)
		}
	}
	return out
}
