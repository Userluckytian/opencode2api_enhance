package main

import (
	"os"
	"path/filepath"
	"testing"
)

// 多上游迁移：升级前只有 upstream_base_url 的配置必须零手工可用。
func TestEffectiveUpstreamsMigratesLegacy(t *testing.T) {
	c := &Config{
		UpstreamBaseURL: "http://127.0.0.1:18080/v1",
		UpstreamAPIKey:  "sk-legacy",
	}
	got := c.EffectiveUpstreams()
	if len(got) != 1 {
		t.Fatalf("want 1 synthesized upstream, got %d: %+v", len(got), got)
	}
	u := got[0]
	if u.ID != LegacyUpstreamID {
		t.Fatalf("id = %q, want %q", u.ID, LegacyUpstreamID)
	}
	if u.BaseURL != "http://127.0.0.1:18080/v1" || u.APIKey != "sk-legacy" {
		t.Fatalf("legacy fields not carried over: %+v", u)
	}
	if !u.Enabled {
		t.Fatal("synthesized upstream must be enabled")
	}
}

// 已配置 upstreams 时旧单上游字段必须被完全忽略，不能混入列表。
func TestEffectiveUpstreamsIgnoresLegacyWhenListed(t *testing.T) {
	c := &Config{
		UpstreamBaseURL: "http://legacy-should-be-ignored/v1",
		UpstreamAPIKey:  "sk-legacy",
		Upstreams: []Upstream{
			{ID: "a", BaseURL: "http://a/v1", Enabled: true},
			{ID: "b", BaseURL: "http://b/v1", Enabled: true},
		},
	}
	got := c.EffectiveUpstreams()
	if len(got) != 2 {
		t.Fatalf("want 2 upstreams, got %d: %+v", len(got), got)
	}
	for _, u := range got {
		if u.BaseURL == "http://legacy-should-be-ignored/v1" {
			t.Fatal("legacy upstream leaked into list")
		}
	}
}

// 两者都空 → 没有可用上游（不合成空条目）。
func TestEffectiveUpstreamsEmpty(t *testing.T) {
	c := &Config{}
	if got := c.EffectiveUpstreams(); len(got) != 0 {
		t.Fatalf("want nil, got %+v", got)
	}
}

// 迁移是纯函数：连续调用结果一致，不产生副作用。
func TestEffectiveUpstreamsIdempotent(t *testing.T) {
	c := &Config{UpstreamBaseURL: "http://127.0.0.1:18080/v1", UpstreamAPIKey: "k"}
	first := c.EffectiveUpstreams()
	second := c.EffectiveUpstreams()
	if len(first) != len(second) || len(first) != 1 {
		t.Fatalf("not idempotent: %d vs %d", len(first), len(second))
	}
	if first[0] != second[0] {
		t.Fatalf("not idempotent: %+v vs %+v", first[0], second[0])
	}
	// 纯函数：不得改写 Config
	if len(c.Upstreams) != 0 {
		t.Fatal("EffectiveUpstreams must not mutate Config")
	}
}

// 管理 API 首次写入上游列表前必须先把旧字段实体化，否则原上游会凭空消失。
func TestEnsureUpstreamsMaterialized(t *testing.T) {
	c := &Config{UpstreamBaseURL: "http://127.0.0.1:18080/v1", UpstreamAPIKey: "sk-legacy"}
	if !c.EnsureUpstreamsMaterialized() {
		t.Fatal("want materialized=true")
	}
	if len(c.Upstreams) != 1 || c.Upstreams[0].ID != LegacyUpstreamID {
		t.Fatalf("materialize failed: %+v", c.Upstreams)
	}
	if c.DefaultUpstreamID != LegacyUpstreamID {
		t.Fatalf("default id = %q, want %q", c.DefaultUpstreamID, LegacyUpstreamID)
	}
	// 幂等：已有列表时不再改动
	c.Upstreams = append(c.Upstreams, Upstream{ID: "b", BaseURL: "http://b/v1", Enabled: true})
	if c.EnsureUpstreamsMaterialized() {
		t.Fatal("want materialized=false on second call")
	}
	if len(c.Upstreams) != 2 {
		t.Fatalf("upstreams modified unexpectedly: %+v", c.Upstreams)
	}
}

// 上游列表必须能经 config.json 往返（含密钥与 enabled 标志）。
func TestUpstreamsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	c := DefaultConfig()
	c.AdminPasswordHash = "h"
	c.Upstreams = []Upstream{
		{ID: "main", Name: "主上游", BaseURL: "http://10.0.0.1:18080/v1", APIKey: "sk-a", Enabled: true},
		{ID: "backup", Name: "备用", BaseURL: "http://10.0.0.2:18080/v1", APIKey: "sk-b", Enabled: false},
	}
	c.DefaultUpstreamID = "main"
	if err := c.Save(dir); err != nil {
		t.Fatal(err)
	}
	c2, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(c2.Upstreams) != 2 {
		t.Fatalf("upstreams roundtrip failed: %+v", c2.Upstreams)
	}
	if c2.DefaultUpstreamID != "main" {
		t.Fatalf("default id roundtrip failed: %q", c2.DefaultUpstreamID)
	}
	if c2.Upstreams[0].APIKey != "sk-a" || c2.Upstreams[1].Enabled {
		t.Fatalf("upstream fields roundtrip failed: %+v", c2.Upstreams)
	}
	_ = os.Remove(filepath.Join(dir, "config.json"))
}
