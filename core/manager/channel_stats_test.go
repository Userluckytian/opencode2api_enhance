package manager

// 阶段 O / Task 2：通道字段映射与聚合统计单测。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEngineChannelMapping(t *testing.T) {
	cases := []struct {
		name     string
		raw      engineLogLine
		wantChan string
		wantVend string
	}{
		{
			name:     "warp anonymous channel",
			raw:      engineLogLine{Time: "2026-11-12T10:00:00+08:00", Channel: "anonymous", Anonymous: true, Outcome: "success", Status: 200},
			wantChan: "warp", wantVend: "opencode",
		},
		{
			name:     "key channel zen",
			raw:      engineLogLine{Time: "2026-11-12T10:00:01+08:00", Channel: "key", Tier: "zen", Outcome: "success", Status: 200},
			wantChan: "key", wantVend: "zen",
		},
		{
			name:     "key channel go",
			raw:      engineLogLine{Time: "2026-11-12T10:00:02+08:00", Channel: "key", Tier: "go", Outcome: "success", Status: 200},
			wantChan: "key", wantVend: "go",
		},
		{
			name:     "legacy anonymous bool only",
			raw:      engineLogLine{Time: "2026-11-12T10:00:03+08:00", Anonymous: true, Outcome: "success", Status: 200},
			wantChan: "warp", wantVend: "opencode",
		},
		{
			name:     "legacy tier only",
			raw:      engineLogLine{Time: "2026-11-12T10:00:04+08:00", Tier: "zen", Outcome: "success", Status: 200},
			wantChan: "key", wantVend: "zen",
		},
		{
			name:     "legacy no channel fields",
			raw:      engineLogLine{Time: "2026-11-12T10:00:05+08:00", Outcome: "success", Status: 200},
			wantChan: "", wantVend: "opencode",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := engineRecordFromLine(c.raw)
			if rec.Channel != c.wantChan {
				t.Fatalf("Channel = %q, want %q", rec.Channel, c.wantChan)
			}
			if rec.Vendor != c.wantVend {
				t.Fatalf("Vendor = %q, want %q", rec.Vendor, c.wantVend)
			}
		})
	}
}

func TestChannelOf(t *testing.T) {
	cases := []struct {
		name         string
		rec          CallLogRecord
		wantBucket   string
		wantInferred bool
	}{
		{"explicit warp", CallLogRecord{Channel: "warp"}, "warp", false},
		{"explicit key", CallLogRecord{Channel: "key"}, "key", false},
		{"kilo stays other", CallLogRecord{Vendor: "kilo", Source: "Kilo 独立网关"}, "other", false},
		{"custom stays other", CallLogRecord{Vendor: "xhx"}, "other", false},
		{"legacy opencode inferred warp", CallLogRecord{Vendor: "opencode", Source: "OpenCode 引擎"}, "warp", true},
		{"legacy zen inferred key", CallLogRecord{Vendor: "zen", Source: "OpenCode 引擎"}, "key", true},
		// 独立审查 P2 #2：收紧后「非空非引擎来源 + vendor=opencode」必须归 other，不算推断
		{"non-engine source with opencode vendor", CallLogRecord{Vendor: "opencode", Source: "供应商服务"}, "other", false},
		{"non-engine source with zen vendor", CallLogRecord{Vendor: "zen", Source: "自定义源"}, "other", false},
		{"empty source with opencode vendor", CallLogRecord{Vendor: "opencode", Source: ""}, "other", false},
		{"legacy empty vendor", CallLogRecord{Source: "OpenCode 引擎"}, "other", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, inferred := channelOf(&c.rec)
			if got != c.wantBucket || inferred != c.wantInferred {
				t.Fatalf("channelOf = (%q,%v), want (%q,%v)", got, inferred, c.wantBucket, c.wantInferred)
			}
		})
	}
}

func TestComputeChannelStats(t *testing.T) {
	// 造一个临时引擎 stdout 日志，让 ReadCallLog 能读到（其余来源为空）。
	m := New(t.TempDir())
	dir := filepath.Join(m.Paths().RuntimeDir, "free-tier")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	lines := []engineLogLine{
		{Event: "request_routed", Time: "2026-11-12T10:00:00+08:00", RequestID: "r1", Channel: "anonymous", Anonymous: true, Outcome: "success", Status: 200},
		{Event: "request_routed", Time: "2026-11-12T10:00:01+08:00", RequestID: "r2", Channel: "anonymous", Anonymous: true, Outcome: "client_error", Status: 429},
		{Event: "request_routed", Time: "2026-11-12T10:00:02+08:00", RequestID: "r3", Channel: "key", Tier: "zen", Outcome: "success", Status: 200},
		{Event: "request_routed", Time: "2026-11-12T10:00:03+08:00", RequestID: "r4", Channel: "key", Tier: "zen", Outcome: "success", Status: 200},
		{Event: "request_routed", Time: "2026-11-12T10:00:04+08:00", RequestID: "r5", Channel: "key", Tier: "zen", Outcome: "success", Status: 200},
	}
	f, err := os.Create(filepath.Join(dir, "stdout.log"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	enc := json.NewEncoder(f)
	for _, l := range lines {
		if err := enc.Encode(l); err != nil {
			t.Fatalf("encode: %v", err)
		}
	}
	_ = f.Close()

	st := m.ComputeChannelStats()
	if st.Total != 5 {
		t.Fatalf("Total = %d, want 5", st.Total)
	}
	if st.EngineTotal != 5 {
		t.Fatalf("EngineTotal = %d, want 5 (warp+key, 不含 other)", st.EngineTotal)
	}
	if st.Warp.Requests != 2 || st.Warp.OK != 1 || st.Warp.Fail != 1 {
		t.Fatalf("Warp = %+v, want requests=2 ok=1 fail=1", st.Warp)
	}
	if st.Key.Requests != 3 || st.Key.OK != 3 || st.Key.Fail != 0 {
		t.Fatalf("Key = %+v, want requests=3 ok=3 fail=0", st.Key)
	}
	if st.Inferred != 0 {
		t.Fatalf("Inferred = %d, want 0 (all lines have channel)", st.Inferred)
	}
	if _, err := time.Parse(time.RFC3339, st.GeneratedAt); err != nil {
		t.Fatalf("GeneratedAt 非法: %v", err)
	}
}
