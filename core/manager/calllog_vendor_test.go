package manager

// 阶段 L / Task 2：供应商分类与过滤单测。

import "testing"

func TestClassifyVendor(t *testing.T) {
	cases := []struct {
		name string
		rec  CallLogRecord
		want string
	}{
		{"kilo by vendor", CallLogRecord{Vendor: "kilo", Model: "a:free", ServingPort: "30082"}, VendorKilo},
		{"kilo by source fallback", CallLogRecord{Source: "Kilo 独立网关", Model: "a:free"}, VendorKilo},
		{"opencode by vendor", CallLogRecord{Vendor: "opencode", Model: "deepseek-v4.1-flash-free"}, VendorOpencode},
		{"opencode by engine source", CallLogRecord{Source: "OpenCode 引擎", Model: "big-pickle"}, VendorOpencode},
		{"opencode by pool verdict", CallLogRecord{RouteVerdict: "proxied", Model: "big-pickle"}, VendorOpencode},
		{"custom by specific vendor", CallLogRecord{Vendor: "xhx", Model: "xhx/sn-mock-model"}, VendorCustom},
		{"custom by model prefix", CallLogRecord{Model: "nv/deepseek-v3"}, VendorCustom},
		{"custom by direct verdict", CallLogRecord{RouteVerdict: "direct_by_design", Model: "foo/bar"}, VendorCustom},
		{"legacy no fields", CallLogRecord{Model: "big-pickle"}, VendorOpencode},
	}
	// 真 nil 指针（审查面 6 指出旧「nil record」用例实传空结构体，名不副实）
	if got := ClassifyVendor(nil); got != VendorCustom {
		t.Fatalf("ClassifyVendor(nil) = %q, want %q", got, VendorCustom)
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ClassifyVendor(&c.rec); got != c.want {
				t.Fatalf("ClassifyVendor = %q, want %q (rec=%+v)", got, c.want, c.rec)
			}
		})
	}
}

func TestCallLogFilterVendor(t *testing.T) {
	recs := []CallLogRecord{
		{ReqID: "1", TS: "2026-11-12T09:00:00+08:00", Vendor: "kilo", Model: "a:free", Status: "ok"},
		{ReqID: "2", TS: "2026-11-12T09:01:00+08:00", Vendor: "opencode", Model: "big-pickle", Status: "ok"},
		{ReqID: "3", TS: "2026-11-12T09:02:00+08:00", Vendor: "xhx", Model: "xhx/m", Status: "ok"},
	}
	count := func(vendor string) int {
		f := &CallLogFilter{Vendor: vendor}
		n := 0
		for i := range recs {
			if f.matches(&recs[i]) {
				n++
			}
		}
		return n
	}
	if got := count(""); got != 3 {
		t.Fatalf("空 vendor 应不过滤，实际 %d", got)
	}
	if got := count(VendorKilo); got != 1 {
		t.Fatalf("kilo 应命中 1 条，实际 %d", got)
	}
	if got := count(VendorOpencode); got != 1 {
		t.Fatalf("opencode 应命中 1 条，实际 %d", got)
	}
	if got := count(VendorCustom); got != 1 {
		t.Fatalf("custom 应命中 1 条，实际 %d", got)
	}
	// 大小写与空白容错
	if got := count(" KILO "); got != 1 {
		t.Fatalf("大写/空白 vendor 应容错命中 1 条，实际 %d", got)
	}
}
