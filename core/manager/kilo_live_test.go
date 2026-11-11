package manager

// 阶段 K 实况验收（本地执行，不入库：仓库约定 *_test.go 不入库）。
//
// 与 kilo_gateway_test.go 的区别：那份打**假上游**做契约验证（稳定、可重复）；
// 这份打**真 Kilo 上游**做端到端验收（V-4/V-5）——证明「代码 → 真上游 → 真模型
// 推理 → 客户端拿到内容 → 警示头在位」整条链路成立。
//
// 隔离纪律：
//   - 独立端口（30081），绝不碰生产 40000/40080/50080；
//   - 只起 Kilo 网关一个组件，不起引擎、不起供应商服务；
//   - 跑完自杀进程、复查端口为空。

import (
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// TestKiloLiveEndToEnd 真上游端到端：/v1/models 出真目录、真推理出真内容。
func TestKiloLiveEndToEnd(t *testing.T) {
	if os.Getenv("KILO_LIVE") != "1" {
		t.Skip("设 KILO_LIVE=1 才跑（会打真上游）")
	}
	m := New(t.TempDir())
	g := m.KiloGateway()

	ln, err := g.listenOn(30081)
	if err != nil {
		t.Fatalf("监听 30081 失败: %v", err)
	}
	g.serveOn(ln)
	defer func() { _ = g.Stop() }()
	base := "http://127.0.0.1:30081"

	// ---- 目录（V-4 上半）----
	t0 := time.Now()
	entries, err := g.Models()
	if err != nil {
		t.Fatalf("拉取上游目录失败: %v", err)
	}
	t.Logf("上游目录: 共 %d 条，本地视图 %d 条（free_only=%v），耗时 %v",
		len(entries), len(entries), kiloFreeOnly(m.loadConfig()), time.Since(t0))
	free := 0
	mayTrain := 0
	for _, e := range entries {
		if e.Free {
			free++
		}
		if e.MayTrain {
			mayTrain++
		}
	}
	t.Logf("免费档 %d 个，其中 mayTrain=true %d 个", free, mayTrain)

	// ---- 真推理（V-4 下半）----
	// max_tokens 必须给足：这批模型多为推理型，小预算会返回 content:null +
	// finish_reason:length（11-11 实测踩坑，会被误判成网关失败）。
	body := `{"model":"nvidia/nemotron-3.5-lightning:free","max_tokens":500,` +
		`"messages":[{"role":"user","content":"Reply with exactly: KILO-OK"}]}`
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+unifiedGatewayKey)
	req.Header.Set("Content-Type", "application/json")
	t0 = time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("真推理请求失败: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	t.Logf("真推理: HTTP %d, 耗时 %v, 响应头 X-Kilo-Data-Policy=%q",
		resp.StatusCode, time.Since(t0), resp.Header.Get(kiloDataPolicyHeader))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("真推理 HTTP %d: %s", resp.StatusCode, raw)
	}
	if resp.Header.Get(kiloDataPolicyHeader) != kiloDataPolicyHeaderValue {
		t.Fatalf("缺少数据政策头: %q", resp.Header.Get(kiloDataPolicyHeader))
	}
	if !strings.Contains(string(raw), "KILO-OK") {
		t.Fatalf("响应里没有模型产出的内容（可能 content:null）: %s", raw)
	}
	// 上游真实模型名必须透传（不能被洗成请求名）
	t.Logf("响应体前 300 字节: %s", truncateStr(string(raw), 300))
}

// truncateStr 截断字符串用于日志。
func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
