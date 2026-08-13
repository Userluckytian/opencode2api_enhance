package manager

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ---------- 解析 ----------

// V1: 公告/信息伪节点过滤（对齐 Rust is_info_pseudo_node）。
func TestIsInfoPseudoNode(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"官网：https://t.me/example", true},
		{"更新时间：2026-08-01", true},
		{"剩余流量：100GB", true},
		{"[anytls]官网：xuelian.pro", true}, // 剥离协议标签后仍命中
		{"电报频道：@abc_news", true},
		{"节点数：12", true},
		{"HK-01", false},           // 常规节点不误伤
		{"余量：100GB", false},        // 前缀不在列表
		{"官网 xuelian.pro", false},  // 无全角冒号
		{"[anytls]US-Best", false}, // 剥离标签后是正常名
	}
	for _, c := range cases {
		if got := isInfoPseudoNode(c.name); got != c.want {
			t.Errorf("isInfoPseudoNode(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

// V1: parseSubscription 出口统一过滤伪节点（明文链接含公告行）。
func TestParseSubscriptionFiltersPseudoNodes(t *testing.T) {
	body := "官网：https://t.me/official\n" +
		"更新时间：2026-08-01\n" +
		"vless://uuid@hk.example.com:443?security=tls#HK-01\n" +
		"vless://uuid@jp.example.com:443?security=tls#JP-02\n"
	nodes, err := parseSubscription(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("nodes = %d, want 2 (公告行被过滤)", len(nodes))
	}
	for _, n := range nodes {
		if isInfoPseudoNode(n.Name) {
			t.Fatalf("pseudo node leaked: %q", n.Name)
		}
	}
}

// V1: Clash YAML 路径同样过滤伪节点。
func TestParseSubscriptionClashYAMLFiltersPseudo(t *testing.T) {
	body := `proxies:
  - name: 官网：xuelian.pro
    type: trojan
    server: x.example.com
    port: 443
    password: s
  - name: HK-01
    type: trojan
    server: hk.example.com
    port: 443
    password: s
`
	nodes, err := parseSubscription(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(nodes) != 1 || nodes[0].Name != "HK-01" {
		t.Fatalf("nodes = %+v, want only HK-01", nodes)
	}
}

func TestParseSubscriptionClashYAML(t *testing.T) {
	body := `proxies:
  - name: HK-01
    type: trojan
    server: hk.example.com
    port: 443
    password: secret123
  - name: JP-01
    type: vless
    server: jp.example.com
    port: 8443
    uuid: 12345678-1234-1234-1234-123456789012
    network: ws
    tls: true
`
	nodes, err := parseSubscription(body)
	if err != nil {
		t.Fatalf("parseSubscription: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("len = %d, want 2", len(nodes))
	}
	if nodes[0].NodeType != "trojan" || nodes[0].Server != "hk.example.com" {
		t.Errorf("nodes[0] = %+v", nodes[0])
	}
	if nodes[1].NodeType != "vless" || nodes[1].UUID != "12345678-1234-1234-1234-123456789012" {
		t.Errorf("nodes[1] = %+v", nodes[1])
	}
	if !nodes[1].TLS {
		t.Errorf("vless tls should be true")
	}
}

func TestParseSubscriptionVlessLink(t *testing.T) {
	body := "vless://abc-uuid-xyz@example.com:443?security=tls&sni=cdn.example.com&type=ws&path=%2Fws#MyNode"
	nodes, err := parseSubscription(body)
	if err != nil {
		t.Fatalf("parseSubscription: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("len = %d, want 1", len(nodes))
	}
	n := nodes[0]
	if n.Name != "MyNode" || n.Server != "example.com" || n.Port != 443 || n.NodeType != "vless" {
		t.Errorf("n = %+v", n)
	}
	if n.UUID != "abc-uuid-xyz" || n.SNI != "cdn.example.com" || n.WsPath != "%2Fws" || !n.TLS {
		t.Errorf("n = %+v", n)
	}
}

func TestParseSubscriptionTrojanLink(t *testing.T) {
	body := "trojan://password123@tg.example.com:443?security=tls&sni=tg.example.com#TG"
	nodes, err := parseSubscription(body)
	if err != nil {
		t.Fatalf("parseSubscription: %v", err)
	}
	n := nodes[0]
	if n.Name != "TG" || n.Password != "password123" || n.NodeType != "trojan" || !n.TLS {
		t.Errorf("n = %+v", n)
	}
}

func TestParseSubscriptionSSLink(t *testing.T) {
	body := "ss://YWVzLTI1Ni1nY206cGFzc3dvcmQ=@ss.example.com:8388#SS1"
	nodes, err := parseSubscription(body)
	if err != nil {
		t.Fatalf("parseSubscription: %v", err)
	}
	n := nodes[0]
	if n.Name != "SS1" || n.NodeType != "ss" || n.Cipher != "aes-256-gcm" || n.Password != "password" {
		t.Errorf("n = %+v", n)
	}
}

func TestParseSubscriptionBase64Wrapped(t *testing.T) {
	plain := "vless://uuid1@a.example.com:443?security=tls&sni=a.example.com#A\ntrojan://pw@b.example.com:443#B"
	encoded := base64.StdEncoding.EncodeToString([]byte(plain))
	nodes, err := parseSubscription(encoded)
	if err != nil {
		t.Fatalf("parseSubscription: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("len = %d, want 2", len(nodes))
	}
	if nodes[0].Name != "A" || nodes[1].Name != "B" {
		t.Errorf("nodes names = %s, %s", nodes[0].Name, nodes[1].Name)
	}
}

func TestParseSubscriptionEmpty(t *testing.T) {
	if _, err := parseSubscription(""); err == nil {
		t.Error("empty subscription should error")
	}
}

// ---------- 缓存与删除 ----------

func TestSubscriptionCacheAndRemove(t *testing.T) {
	m := New(t.TempDir())
	mk := func(name string) SubscribeNode {
		return SubscribeNode{Name: name, Server: "1.2.3.4", Port: 443, NodeType: "trojan",
			Password: "pw", TLS: true, Raw: "trojan://pw@1.2.3.4:443#" + name}
	}
	if err := m.saveSubscriptionCache([]SubscribeNode{mk("A"), mk("B"), mk("C")}); err != nil {
		t.Fatalf("save: %v", err)
	}
	// 删两个存在的 + 一个不存在的 → 只删 2
	removed, err := m.RemoveSubscriptionNodes([]string{"A", "B", "X"})
	if err != nil || removed != 2 {
		t.Fatalf("RemoveSubscriptionNodes = %d, %v; want 2", removed, err)
	}
	left := m.loadSubscriptionCache()
	if len(left) != 1 || left[0].Name != "C" {
		t.Fatalf("left = %+v", left)
	}
	// 空列表 → 0
	if n, _ := m.RemoveSubscriptionNodes(nil); n != 0 {
		t.Fatalf("empty remove = %d, want 0", n)
	}
	// 单删
	if n, _ := m.RemoveSubscriptionNode("C"); n != 1 {
		t.Fatalf("single remove = %d, want 1", n)
	}
	if len(m.loadSubscriptionCache()) != 0 {
		t.Fatal("cache should be empty")
	}
}

// ---------- 导入实例 ----------

func TestImportSubscriptionDedupAndSuffix(t *testing.T) {
	m := New(t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := "vless://uuid1@a.example.com:443?security=tls#NodeA\nvless://uuid2@b.example.com:8443?security=tls#NodeB"
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	n, err := m.importSubscription(srv.URL, false)
	if err != nil || n != 2 {
		t.Fatalf("import = %d, %v; want 2", n, err)
	}
	// 再次导入（同订阅）→ 全部去重，不新增
	n, err = m.importSubscription(srv.URL, false)
	if err != nil || n != 0 {
		t.Fatalf("second import = %d, %v; want 0 (dedup)", n, err)
	}
	insts := m.ListInstances()
	if len(insts) != 2 {
		t.Fatalf("instances = %d, want 2", len(insts))
	}
	if insts[0].Node != "NodeA" || insts[0].Password == "" {
		t.Errorf("inst[0] = %+v", insts[0])
	}
	// join_gateway 标记
	m2 := New(t.TempDir())
	_, _ = m2.importSubscription(srv.URL, true)
	for _, inst := range m2.ListInstances() {
		if !inst.JoinGateway {
			t.Errorf("instance %s should be join_gateway", inst.Name)
		}
	}
}

// ---------- toClashNode ----------

func TestToClashNode(t *testing.T) {
	n := SubscribeNode{Name: "NodeX", Server: "1.2.3.4", Port: 443, NodeType: "trojan",
		Password: "pw", TLS: true, Raw: "trojan://pw@1.2.3.4:443"}
	cn := toClashNode(n)
	if cn.Name != "NodeX" || cn.Server != "1.2.3.4" || cn.NodeType != "trojan" || cn.Password != "pw" {
		t.Errorf("cn = %+v", cn)
	}
	if cn.TLS == nil || !*cn.TLS {
		t.Errorf("tls not preserved")
	}
}

// ---------- HTTP handlers ----------

func TestSubscribeHandlersHTTP(t *testing.T) {
	m := New(t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("vless://uuid@x.example.com:443?security=tls#X"))
	}))
	defer srv.Close()

	// preview
	h := m.SubscribePreviewHandler()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/admin/subscribe/preview", strings.NewReader(`{"url":"`+srv.URL+`"}`))
	req.Header.Set("Content-Type", "application/json")
	h(rec, req)
	if rec.Code != 200 {
		t.Fatalf("preview code = %d, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"count":1`) {
		t.Errorf("preview body = %s", rec.Body.String())
	}

	// import-pool（仅缓存）
	h2 := m.SubscribeImportPoolHandler()
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("POST", "/api/admin/subscribe/import-pool", strings.NewReader(`{"url":"`+srv.URL+`"}`))
	req2.Header.Set("Content-Type", "application/json")
	h2(rec2, req2)
	if rec2.Code != 200 {
		t.Fatalf("import-pool code = %d, body=%s", rec2.Code, rec2.Body.String())
	}
	if len(m.loadSubscriptionCache()) != 1 {
		t.Fatalf("cache = %+v", m.loadSubscriptionCache())
	}

	// import（建实例）
	h3 := m.SubscribeImportHandler()
	rec3 := httptest.NewRecorder()
	req3 := httptest.NewRequest("POST", "/api/admin/subscribe/import", strings.NewReader(`{"url":"`+srv.URL+`","join_gateway":true}`))
	req3.Header.Set("Content-Type", "application/json")
	h3(rec3, req3)
	if rec3.Code != 200 {
		t.Fatalf("import code = %d, body=%s", rec3.Code, rec3.Body.String())
	}
	insts := m.ListInstances()
	if len(insts) != 1 || !insts[0].JoinGateway {
		t.Fatalf("instances = %+v", insts)
	}

	// 缺 url → 400
	rec4 := httptest.NewRecorder()
	req4 := httptest.NewRequest("POST", "/api/admin/subscribe/preview", strings.NewReader(`{}`))
	h(rec4, req4)
	if rec4.Code != 400 {
		t.Fatalf("empty url code = %d", rec4.Code)
	}

	// 订阅节点并入节点池（ListNodesWithGroup 包含缓存节点）
	nodes := m.ListNodesWithGroup()
	found := false
	for _, n := range nodes {
		if n.Name == "X" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("subscription node not in ListNodesWithGroup: %+v", nodes)
	}
}

func TestParseVlessHostHeader(t *testing.T) {
	n, err := parseVless("uuid-123@cdn.example.com:443?security=tls&sni=cdn.example.com&type=ws&path=%2F&host=cdn.example.com#CDN")
	if err != nil {
		t.Fatalf("parseVless: %v", err)
	}
	if n.WsHeaders == nil || n.WsHeaders["Host"] != "cdn.example.com" {
		t.Fatalf("WsHeaders = %+v, want Host=cdn.example.com", n.WsHeaders)
	}
	// host 参数不应再被当作 path 兜底
	if n.WsPath == "cdn.example.com" {
		t.Fatalf("host 被错误当作 path: %q", n.WsPath)
	}
	cn := toClashNode(n)
	if cn.WSHeaders == nil || cn.WSHeaders["Host"] != "cdn.example.com" {
		t.Fatalf("toClashNode WSHeaders = %+v", cn.WSHeaders)
	}
}
