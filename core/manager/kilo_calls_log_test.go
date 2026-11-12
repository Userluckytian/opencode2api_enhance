package manager

// 阶段 L / Task 1：Kilo 调用日志持久化单测。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKiloCallToLogRecord(t *testing.T) {
	rec := KiloCallRecord{
		TS:         "2026-11-12T09:00:00+08:00",
		ReqID:      "kilo-test-1",
		Requested:  "kilo-auto/free",
		Actual:     "nvidia/nemotron-3-ultra-550b-a55b:free",
		Status:     200,
		DurationMs: 1234,
		Stream:     true,
	}
	got := kiloCallToLogRecord(rec, 30082)
	if got.Vendor != "kilo" || got.Model != "kilo-auto/free" || got.ActualModel != rec.Actual {
		t.Fatalf("映射错误: %+v", got)
	}
	if got.Status != "ok" || got.ServingPort != "30082" || got.UpstreamStatus != 200 || !got.Stream {
		t.Fatalf("状态/端口/流标记错误: %+v", got)
	}
	if got.Path != "/v1/chat/completions" || got.TS != rec.TS {
		t.Fatalf("路径/时间戳错误: %+v", got)
	}
	// 失败请求也要入账且标记为 error（4xx/5xx 都算调用失败，日志页「只看失败」可见）
	bad := kiloCallToLogRecord(KiloCallRecord{Status: 429, TS: rec.TS}, 30080)
	if bad.Status != "error" || !strings.Contains(bad.ErrMsg, "429") {
		t.Fatalf("429 应标记为失败: %+v", bad)
	}
	trans := kiloCallToLogRecord(KiloCallRecord{Status: 504, TS: rec.TS}, 30080)
	if trans.Status != "error" || !strings.Contains(trans.ErrMsg, "504") {
		t.Fatalf("504 应标记失败: %+v", trans)
	}
}

func TestKiloCallLogWriteAndRead(t *testing.T) {
	m := New(t.TempDir())
	g := m.KiloGateway()
	if g.callWriter == nil {
		t.Fatal("callWriter 未初始化")
	}
	// 通过 recordCall 走完整链路（内存账本 + 落盘）
	g.recordCall(KiloCallRecord{
		ReqID: "kilo-e2e-1", Requested: "a:free", Actual: "b:free",
		Status: 200, DurationMs: 9, TS: "2026-11-12T09:01:00+08:00",
	})
	// 文件存在且是合法 JSONL
	path := m.KiloCallsLogPath()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("日志文件未生成: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 1 {
		t.Fatalf("应有 1 行，实际 %d: %s", len(lines), raw)
	}
	var line CallLogRecord
	if err := json.Unmarshal([]byte(lines[0]), &line); err != nil {
		t.Fatalf("日志行不是合法 CallLogRecord: %v / %s", err, lines[0])
	}
	if line.Vendor != "kilo" || line.ActualModel != "b:free" {
		t.Fatalf("落盘字段错误: %+v", line)
	}
	// 关键：ReadCallLog 必须能读到它（第三个来源合并）
	all := m.ReadCallLog(0)
	found := false
	for _, r := range all {
		if r.ReqID == "kilo-e2e-1" {
			found = true
			if r.Source != "Kilo 独立网关" {
				t.Fatalf("来源标注错误: %q", r.Source)
			}
		}
	}
	if !found {
		t.Fatalf("ReadCallLog 未合并 Kilo 记录，共 %d 条", len(all))
	}
	// 模拟重启：新建 Manager 指向同一目录，仍应读到历史
	m2 := New(m.Paths().DataDir)
	again := m2.ReadCallLog(0)
	found2 := false
	for _, r := range again {
		if r.ReqID == "kilo-e2e-1" {
			found2 = true
		}
	}
	if !found2 {
		t.Fatal("重启后 Kilo 历史记录丢失（持久化未生效）")
	}
}

func TestKiloCallLogNoRequestBody(t *testing.T) {
	// 安全红线：日志里不得出现请求体/响应体内容
	m := New(t.TempDir())
	g := m.KiloGateway()
	g.recordCall(KiloCallRecord{ReqID: "sec-1", Requested: "a:free", Status: 200, TS: "2026-11-12T09:02:00+08:00"})
	raw, _ := os.ReadFile(m.KiloCallsLogPath())
	for _, secret := range []string{"messages", "content", "Bearer", "sk-unified-local", "Authorization"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("日志泄漏了敏感串 %q: %s", secret, raw)
		}
	}
}

func TestKiloCallsLogPath(t *testing.T) {
	m := New(t.TempDir())
	p := m.KiloCallsLogPath()
	if !strings.HasSuffix(filepath.ToSlash(p), "/runtime/kilo/kilo_calls.jsonl") {
		t.Fatalf("日志路径不符合约定: %s", p)
	}
}
