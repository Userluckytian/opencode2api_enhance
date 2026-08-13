package manager

import "testing"

// ws Host 头保留测试：Clash YAML / vless:// 订阅节点解析出的 WSHeaders
// 必须写入生成的 sing-box 配置（CDN 前置节点缺 Host 头会被 Cloudflare 403 拒绝）。

func TestNodeFromYAMLWsHeadersFlow(t *testing.T) {
	nodes, err := parseClashYAML(`proxies:
  - {name: CDN-01, server: 1.2.3.4, port: 443, type: vless, uuid: "u1", network: ws, ws-opts: {path: /, headers: {Host: cdn.example.com}}}
`)
	if err != nil || len(nodes) != 1 {
		t.Fatalf("parseClashYAML: %v nodes=%d", err, len(nodes))
	}
	n := nodes[0]
	if n.WSHeaders == nil || n.WSHeaders["Host"] != "cdn.example.com" {
		t.Fatalf("WSHeaders = %+v, want Host=cdn.example.com", n.WSHeaders)
	}
	if n.WsPath != "/" {
		t.Fatalf("WsPath = %q", n.WsPath)
	}
}

func TestNodeFromYAMLWsHeadersBlock(t *testing.T) {
	nodes, err := parseClashYAML(`proxies:
  - name: CDN-02
    server: 5.6.7.8
    port: 443
    type: vless
    uuid: "u2"
    network: ws
    ws-opts:
      path: /ws
      headers:
        Host: cdn2.example.com
`)
	if err != nil || len(nodes) != 1 {
		t.Fatalf("parseClashYAML: %v nodes=%d", err, len(nodes))
	}
	n := nodes[0]
	if n.WSHeaders == nil || n.WSHeaders["Host"] != "cdn2.example.com" {
		t.Fatalf("WSHeaders = %+v, want Host=cdn2.example.com", n.WSHeaders)
	}
	if n.WsPath != "/ws" {
		t.Fatalf("WsPath = %q", n.WsPath)
	}
}

func TestNodeFromYAMLFlatWsHeaders(t *testing.T) {
	nodes, err := parseClashYAML(`proxies:
  - {name: CDN-03, server: 9.9.9.9, port: 443, type: vless, uuid: "u3", network: ws, ws-headers: {Host: cdn3.example.com}}
`)
	if err != nil || len(nodes) != 1 {
		t.Fatalf("parseClashYAML: %v nodes=%d", err, len(nodes))
	}
	if n := nodes[0]; n.WSHeaders == nil || n.WSHeaders["Host"] != "cdn3.example.com" {
		t.Fatalf("WSHeaders = %+v, want Host=cdn3.example.com", n.WSHeaders)
	}
}

func TestSingboxVlessWsHeaders(t *testing.T) {
	node := ClashNode{NodeType: "vless", Server: "1.2.3.4", Port: 443, UUID: "u1", Network: "ws",
		WsPath: "/", WSHeaders: map[string]string{"Host": "cdn.example.com"}}
	out := singleOutbound(t, node)
	tr := out["transport"].(map[string]any)
	if tr["type"] != "ws" || tr["path"] != "/" {
		t.Fatalf("transport = %+v", tr)
	}
	hdrs, ok := tr["headers"].(map[string]any)
	if !ok || hdrs["Host"] != "cdn.example.com" {
		t.Fatalf("headers = %+v, want Host=cdn.example.com", tr["headers"])
	}
}
