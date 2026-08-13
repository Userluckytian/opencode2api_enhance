// clash 节点解析与列表（Rust clash_yaml.rs 移植）：
// 本地 profiles（%APPDATA%\io.github.clash-verge-rev\...\profiles\*.yaml）+ 外部控制 API。
package manager

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// parseClashURL 解析 http://host[:port][/path] → (port, path)；非法 → (0, "")。
func parseClashURL(base string) (uint16, string) {
	rest := strings.TrimPrefix(base, "http://")
	if rest == base {
		return 0, ""
	}
	host, path, _ := strings.Cut(rest, "/")
	host = strings.TrimSpace(host)
	if host == "" {
		return 0, ""
	}
	port := uint16(80)
	if i := strings.LastIndexByte(host, ':'); i >= 0 {
		p, err := strconv.Atoi(host[i+1:])
		if err != nil || p <= 0 || p > 65535 {
			return 0, ""
		}
		port = uint16(p)
		host = host[:i]
	}
	if host == "" {
		return 0, ""
	}
	return port, "/" + path
}

// parseClashYAML 解析 clash 文本 → ClashNode 列表（顶层 proxies 数组）。
func parseClashYAML(content string) ([]ClashNode, error) {
	root, err := yamlParse(content)
	if err != nil {
		return nil, err
	}
	var out []ClashNode
	for _, item := range root.sliceOf("proxies") {
		n := nodeFromYAML(item)
		if n.Name == "" || n.Server == "" || n.Port == 0 || n.NodeType == "" {
			continue
		}
		out = append(out, n)
	}
	return out, nil
}

// nodeFromYAML 把 YAML map 转 ClashNode。
func nodeFromYAML(n *yamlNode) ClashNode {
	c := ClashNode{
		Name:              n.string("name"),
		Server:            n.string("server"),
		NodeType:          n.string("type"),
		Password:          n.string("password"),
		UUID:              n.string("uuid"),
		Cipher:            n.string("cipher"),
		SNI:               n.string("sni"),
		ServerName:        n.string("servername"),
		TLS:               n.boolPtr("tls"),
		SkipCertVerify:    n.boolPtr("skip-cert-verify"),
		Network:           n.string("network"),
		Up:                n.string("up"),
		Down:              n.string("down"),
		Obfs:              n.string("obfs"),
		ObfsPassword:      n.string("obfs-password"),
		ClientFingerprint: n.string("client-fingerprint"),
		Flow:              n.string("flow"),
		PrivateKey:        n.string("private-key"), // wireguard 客户端私钥
		PublicKey:         n.string("public-key"),  // wireguard 对端公钥
		AuthStr:           n.string("auth-str"),    // hysteria v1 认证串
	}
	c.Port = uint16(n.intVal("port"))
	if ws := n.mapOf("ws-opts"); ws != nil {
		c.WsPath = ws.string("path")
		c.WSHeaders = nodeHeaders(ws.mapOf("headers"))
	}
	if ro := n.mapOf("reality-opts"); ro != nil {
		c.RealityPublicKey = ro.string("public-key")
		c.RealityShortID = ro.string("short-id")
	}
	// 兼容扁平写法 ws-headers: {Host: ...}（老式 clash 配置）。
	if c.WSHeaders == nil {
		c.WSHeaders = nodeHeaders(n.mapOf("ws-headers"))
	}
	return c
}

// nodeHeaders 把 YAML headers map 转成 map[string]string；非 map/空则返回 nil。
func nodeHeaders(h *yamlNode) map[string]string {
	if h == nil || h.kind != kindMap {
		return nil
	}
	hm := make(map[string]string, len(h.vs))
	for k, v := range h.vs {
		if v.kind == kindScalar {
			hm[k] = v.scalar
		}
	}
	if len(hm) == 0 {
		return nil
	}
	return hm
}

// clashProfileDir 找到 Clash Verge 的 profiles 目录（Windows only；其它平台返回空）。
func clashProfileDir() string {
	appData := os.Getenv("APPDATA")
	if appData == "" {
		return ""
	}
	dir := filepath.Join(appData, "io.github.clash-verge-rev.clash-verge-rev", "profiles")
	if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
		return dir
	}
	return ""
}

// parseProfileNameMap 读取 profiles.yaml：uid（文件 stem）→ 订阅别名。
func parseProfileNameMap(dir string) map[string]string {
	mp := map[string]string{}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(dir), "profiles.yaml"))
	if err != nil {
		return mp
	}
	root, err := yamlParse(string(data))
	if err != nil {
		return mp
	}
	for _, item := range root.sliceOf("items") {
		uid := cleanProfileName(item.string("uid"))
		name := strings.TrimSpace(item.string("name"))
		if uid == "" || name == "" {
			continue
		}
		mp[uid] = name
	}
	return mp
}

// cleanProfileName trim 并去 .yaml/.js 后缀。
func cleanProfileName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.TrimSuffix(name, ".yaml")
	name = strings.TrimSuffix(name, ".js")
	return strings.TrimSpace(name)
}

// listLocalNodes 读取本地 profiles 全部 yaml，去重并按节点名去杂。
func listLocalNodes(dir string) []ClashNode {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	nameMap := parseProfileNameMap(dir)
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".yaml") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	var out []ClashNode
	seen := map[string]bool{}
	for _, fname := range files {
		data, err := os.ReadFile(filepath.Join(dir, fname))
		if err != nil {
			continue
		}
		nodes, err := parseClashYAML(string(data))
		if err != nil {
			continue
		}
		stem := cleanProfileName(fname)
		group := nameMap[stem]
		if group == "" {
			group = stem
		}
		for _, n := range nodes {
			if isJunkNode(n.Name) || seen[n.Name] {
				continue
			}
			seen[n.Name] = true
			if n.Group == "" {
				n.Group = group
			}
			out = append(out, n)
		}
	}
	return out
}

// listExternalNodes 经外部控制 API 拉取节点（/configs）。
func (m *Manager) listExternalNodes() []ClashNode {
	cfg := m.loadConfig()
	base := strings.TrimSuffix(cfg.ClashExternalURL, "/")
	if base == "" || !strings.HasPrefix(base, "http://") {
		return nil
	}
	port, path := parseClashURL(base)
	if port == 0 {
		return nil
	}
	status, body, err := httpRequest("GET", path+"/configs", port, 8*time.Second, cfg.ClashAuthToken, nil, 8*time.Second)
	if err != nil || status < 200 || status >= 300 {
		return nil
	}
	var payload struct {
		Proxies []map[string]any `json:"proxies"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return nil
	}
	var out []ClashNode
	seen := map[string]bool{}
	for _, p := range payload.Proxies {
		n, ok := nodeFromJSONMap(p)
		if ok && !isJunkNode(n.Name) && !seen[n.Name] {
			seen[n.Name] = true
			out = append(out, n)
		}
	}
	return out
}

// ListNodesWithGroup 汇总节点（外部 API 优先去重，本地 profiles 补，订阅缓存并入）；按 (group, name) 排序。
func (m *Manager) ListNodesWithGroup() []ClashNode {
	var out []ClashNode
	seen := map[string]bool{}
	for _, n := range m.listExternalNodes() {
		seen[n.Name] = true
		out = append(out, n)
	}
	if dir := clashProfileDir(); dir != "" {
		for _, n := range listLocalNodes(dir) {
			if seen[n.Name] {
				continue
			}
			seen[n.Name] = true
			out = append(out, n)
		}
	}
	// 订阅缓存节点并入（节点池页「从订阅导入」的节点在此展示；去重优先已有来源）。
	for _, n := range m.listSubscriptionNodes() {
		if seen[n.Name] {
			continue
		}
		seen[n.Name] = true
		if n.Group == "" {
			n.Group = "订阅"
		}
		out = append(out, n)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Group != out[j].Group {
			return out[i].Group < out[j].Group
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// isJunkNode 过滤垃圾节点（Rust is_junk_node 语义）。
func isJunkNode(name string) bool {
	if strings.HasPrefix(name, "-----") {
		return true
	}
	n := strings.ReplaceAll(name, "：", ":")
	for _, kw := range []string{"登录账号", "邮箱:", "官网:", "电报:", "消息:", "体验套餐:", "时间:", "流量重置:", "剩余流量:"} {
		if strings.Contains(n, kw) {
			return true
		}
	}
	return false
}

// nodeFromJSONMap 从外部 API 的 JSON map 构造 ClashNode。
func nodeFromJSONMap(mp map[string]any) (ClashNode, bool) {
	str := func(k string) string {
		if v, ok := mp[k].(string); ok {
			return v
		}
		return ""
	}
	n := ClashNode{
		Name:     str("name"),
		Server:   str("server"),
		NodeType: str("type"),
		Password: str("password"),
		UUID:     str("uuid"),
		Cipher:   str("cipher"),
		SNI:      str("sni"),
		Group:    str("group"),
	}
	if p, ok := mp["port"].(float64); ok {
		n.Port = uint16(p)
	}
	// 外部 API（mihomo /configs）同样把 ws-opts 作为嵌套对象返回，需要解析 path 与 Host 头。
	if ws, ok := mp["ws-opts"].(map[string]any); ok {
		n.WsPath, _ = ws["path"].(string)
		if hdrs, ok := ws["headers"].(map[string]any); ok {
			hm := map[string]string{}
			for k, v := range hdrs {
				if s, ok := v.(string); ok {
					hm[k] = s
				}
			}
			if len(hm) > 0 {
				n.WSHeaders = hm
			}
		}
	}
	if n.Name == "" || n.Server == "" || n.Port == 0 || n.NodeType == "" {
		return n, false
	}
	return n, true
}
