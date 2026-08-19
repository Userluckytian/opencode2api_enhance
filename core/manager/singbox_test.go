package manager

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func singleOutbound(t *testing.T, node ClashNode) map[string]any {
	t.Helper()
	b, err := buildSingboxConfig(node, 18100)
	if err != nil {
		t.Fatalf("singbox: %v", err)
	}
	var cfg map[string]any
	if json.Unmarshal(b, &cfg) != nil {
		t.Fatalf("bad json: %s", string(b))
	}
	return cfg["outbounds"].([]any)[0].(map[string]any)
}

func TestSingboxTrojan(t *testing.T) {
	out := singleOutbound(t, ClashNode{NodeType: "trojan", Server: "a.example", Port: 443, Password: "p1"})
	if out["type"] != "trojan" || out["password"] != "p1" {
		t.Fatalf("out = %+v", out)
	}
	tls := out["tls"].(map[string]any)
	if tls["enabled"] != true || tls["server_name"] != "a.example" {
		t.Fatalf("tls = %+v", tls)
	}
}

func TestSingboxVlessReality(t *testing.T) {
	node := ClashNode{NodeType: "vless", Server: "r.example", Port: 443, UUID: "u1", Network: "ws",
		RealityPublicKey: "pub", RealityShortID: "abcd", ClientFingerprint: "firefox", Flow: "xtls-rprx-vision"}
	out := singleOutbound(t, node)
	if out["type"] != "vless" || out["flow"] != "xtls-rprx-vision" {
		t.Fatalf("out = %+v", out)
	}
	tls := out["tls"].(map[string]any)
	reality := tls["reality"].(map[string]any)
	if reality["public_key"] != "pub" || reality["short_id"] != "abcd" {
		t.Fatalf("reality = %+v", reality)
	}
	if tls["utls"].(map[string]any)["fingerprint"] != "firefox" {
		t.Fatalf("utls = %+v", tls["utls"])
	}
	if out["transport"].(map[string]any)["type"] != "ws" {
		t.Fatalf("transport = %+v", out["transport"])
	}

	// 未指定 client-fingerprint 时默认回退 chrome；utls 必须嵌在 tls 内
	defOut := singleOutbound(t, ClashNode{NodeType: "vless", Server: "r2.example", Port: 443, UUID: "u2",
		RealityPublicKey: "pub2", RealityShortID: "efgh"})
	defTLS := defOut["tls"].(map[string]any)
	if defTLS["utls"].(map[string]any)["fingerprint"] != "chrome" {
		t.Fatalf("default utls = %+v", defTLS["utls"])
	}
	if _, ok := defOut["utls"]; ok {
		t.Fatalf("utls 必须在 tls 内，不得出现在 outbound 顶层: %+v", defOut)
	}
}

func TestSingboxShadowsocks(t *testing.T) {
	out := singleOutbound(t, ClashNode{NodeType: "ss", Server: "s.example", Port: 8388, Password: "sp"})
	if out["method"] != "aes-256-gcm" || out["password"] != "sp" {
		t.Fatalf("ss = %+v", out)
	}
}

func TestSingboxHysteria2(t *testing.T) {
	node := ClashNode{NodeType: "hysteria2", Server: "h.example", Port: 8448, Password: "hp", Obfs: "salamander",
		Up: "200", Down: "1.5 Gbps"}
	out := singleOutbound(t, node)
	if out["type"] != "hysteria2" || out["obfs"].(map[string]any)["type"] != "salamander" {
		t.Fatalf("hy2 = %+v", out)
	}
	// up/down 必须是数字 Mbps（字符串会让 sing-box 启动即崩）；
	// singleOutbound 经 JSON 往返，数字为 float64
	if out["up_mbps"] != float64(200) || out["down_mbps"] != float64(1500) {
		t.Fatalf("up/down must be numeric mbps, got %v / %v", out["up_mbps"], out["down_mbps"])
	}

	// 无法解析的带宽字段应省略，而不是写非法值导致 sing-box FATAL
	bad := singleOutbound(t, ClashNode{NodeType: "hysteria2", Server: "h2.example", Port: 8449, Password: "hp2",
		Up: "abc", Down: "∞"})
	if _, ok := bad["up_mbps"]; ok {
		t.Fatalf("unparseable up_mbps should be omitted: %+v", bad)
	}
	if _, ok := bad["down_mbps"]; ok {
		t.Fatalf("unparseable down_mbps should be omitted: %+v", bad)
	}
}

func TestParseBandwidthMbps(t *testing.T) {
	cases := []struct {
		in   string
		want any
	}{
		{"100", uint64(100)},
		{"100 Mbps", uint64(100)},
		{" 50 ", uint64(50)},
		{"1.5 Gbps", uint64(1500)},
		{"2gb", uint64(2000)},
		{"1000Kbps", uint64(1)},
		{"abc", nil},
		{"", nil},
		{"   ", nil},
	}
	for _, c := range cases {
		if got := parseBandwidthMbps(c.in); got != c.want {
			t.Errorf("parseBandwidthMbps(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// TestSingboxRealCheck 用真实 sing-box 校验生成的配置（CI/本机构建机有 bin/sing-box.exe；缺失则跳过）。
// 防止再出现「JSON 结构断言通过但 sing-box 拒绝」的回归（utls 层级、up_mbps 类型均可被此测试捕获）。
func TestSingboxRealCheck(t *testing.T) {
	var sb string
	for _, cand := range []string{"../../bin/sing-box", "../../bin/sing-box.exe"} {
		if _, err := os.Stat(cand); err == nil {
			sb = cand
			break
		}
	}
	if sb == "" {
		t.Skip("bin/sing-box 不存在，跳过真实配置校验")
	}
	nodes := []ClashNode{
		{NodeType: "vless", Server: "r.example", Port: 443, UUID: "u1", Network: "ws",
			RealityPublicKey: "4s9YTYQL3Zh7YFFVrTFORAMoIac2D32LSgVatvLcsnM",
			RealityShortID:   "abcd", ClientFingerprint: "firefox", Flow: "xtls-rprx-vision"},
		{NodeType: "hysteria2", Server: "h.example", Port: 8448, Password: "hp", Obfs: "salamander",
			ObfsPassword: "obfs-pass", Up: "200", Down: "1.5 Gbps"},
		{NodeType: "vmess", Server: "v.example", Port: 443, UUID: "u3"},
		{NodeType: "anytls", Server: "a.example", Port: 443, Password: "ap"},
	}
	for i, n := range nodes {
		b, err := buildSingboxConfig(n, 19001)
		if err != nil {
			t.Fatalf("node %d build: %v", i, err)
		}
		f := filepath.Join(t.TempDir(), "singbox.json")
		if err := os.WriteFile(f, b, 0o600); err != nil {
			t.Fatalf("node %d write: %v", i, err)
		}
		if out, err := exec.Command(sb, "check", "-c", f).CombinedOutput(); err != nil {
			t.Fatalf("node %d (%s) sing-box check failed: %v\n%s", i, n.NodeType, err, out)
		}
	}
}

func TestSingboxUnsupported(t *testing.T) {
	if _, err := buildSingboxConfig(ClashNode{NodeType: "relay", Server: "x", Port: 1}, 1); err == nil {
		t.Fatal("relay should error")
	}
	if _, err := buildSingboxConfig(ClashNode{NodeType: "trojan", Server: "x", Port: 443}, 1); err == nil {
		t.Fatal("trojan no password should error")
	}
	if _, err := buildSingboxConfig(ClashNode{NodeType: "vless", Server: "x", Port: 443}, 1); err == nil {
		t.Fatal("vless no uuid should error")
	}
}

func TestPickFreeModel(t *testing.T) {
	got := pickFreeModel([]map[string]any{{"id": "pro-x"}, {"id": "deepseek-v4-flash"}})
	if got != "deepseek-v4-flash" {
		t.Fatalf("got %q", got)
	}
	got = pickFreeModel([]map[string]any{{"id": "a"}, {"id": "b-free"}})
	if got != "b-free" {
		t.Fatalf("prefer -free, got %q", got)
	}
	if !isFreeModelID("big-pickle") || !isFreeModelID("X-free") {
		t.Fatal("free detection failed")
	}
	// 上游新增免费模型（hy3 / nemotron-3.5-lightning，剥 -free 后无后缀）
	if !isFreeModelID("hy3") || !isFreeModelID("nemotron-3.5-lightning") {
		t.Fatal("new free models should be detected")
	}
	// 兜底：本系统 /v1/models 只返回免费模型，data 非空即取首个非 auto 模型
	got = pickFreeModel([]map[string]any{{"id": "auto"}, {"id": "hy3"}})
	if got != "hy3" {
		t.Fatalf("fallback should skip auto and pick hy3, got %q", got)
	}
	got = pickFreeModel([]map[string]any{{"id": "auto"}})
	if got != "" {
		t.Fatalf("only auto should yield empty, got %q", got)
	}
	got = pickFreeModel([]map[string]any{})
	if got != "" {
		t.Fatalf("empty data should yield empty, got %q", got)
	}
}

func TestProbeHelpers(t *testing.T) {
	if !probeCompletionSuccess(200, []byte(`{"choices":[{}]}`)) {
		t.Fatal("choices should pass")
	}
	if probeCompletionSuccess(200, []byte(`{"choices":[]}`)) {
		t.Fatal("empty choices fail")
	}
	if probeCompletionSuccess(503, []byte(`{"choices":[{}]}`)) {
		t.Fatal("503 fail")
	}
	if n, ok := modelsCount([]byte(`{"data":[{"id":"a"},{"id":"b"}]}`)); !ok || n != 2 {
		t.Fatalf("modelsCount = %d %v", n, ok)
	}
}
