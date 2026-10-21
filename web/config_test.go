package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	c := DefaultConfig()
	if c.Port != 23081 {
		t.Fatalf("Port = %d, want 23081", c.Port)
	}
	if c.TTLDays != 1 || c.TokenQuota != 30000000 || c.RateLimitPerMin != 30 {
		t.Fatalf("defaults wrong: %+v", c)
	}
	// finding 560：默认配置里不再出现硬编码上游密钥（sk-unified-local 已在公开前端包泄露）
	if c.UpstreamBaseURL != "http://127.0.0.1:18080/v1" || c.UpstreamAPIKey != "" {
		t.Fatalf("upstream defaults wrong: %+v", c)
	}
}

func TestLoadConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	// 目录为空：LoadConfig 应返回默认值并落盘
	c, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.UpstreamBaseURL != "http://127.0.0.1:18080/v1" {
		t.Fatalf("default upstream wanted, got %q", c.UpstreamBaseURL)
	}
	c.AdminPasswordHash = "hash"
	c.UpstreamBaseURL = "http://127.0.0.1:18080/v1"
	c.UpstreamAPIKey = "sk-custom"
	c.AllowedModels = []string{"glm-4"}
	if err := c.Save(dir); err != nil {
		t.Fatal(err)
	}
	// 重新加载应从文件读回
	c2, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c2.UpstreamBaseURL != "http://127.0.0.1:18080/v1" {
		t.Fatalf("roundtrip failed: %+v", c2)
	}
	if c2.UpstreamAPIKey != "sk-custom" {
		t.Fatalf("api key roundtrip failed: %+v", c2.UpstreamAPIKey)
	}
	if len(c2.AllowedModels) != 1 || c2.AllowedModels[0] != "glm-4" {
		t.Fatalf("allowed models roundtrip failed: %+v", c2.AllowedModels)
	}
	_ = os.Remove(filepath.Join(dir, "config.json"))
}

// finding 560：老配置里的 sk-unified-local 必须被轮换成随机值，且环境变量优先。
func TestLoadConfigRotatesLeakedDefaultKey(t *testing.T) {
	dir := t.TempDir()
	legacy := DefaultConfig()
	legacy.UpstreamAPIKey = DeprecatedUpstreamAPIKey
	legacy.AdminPasswordHash = "h"
	if err := legacy.Save(dir); err != nil {
		t.Fatal(err)
	}
	c, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.UpstreamAPIKey == DeprecatedUpstreamAPIKey || c.UpstreamAPIKey == "" {
		t.Fatalf("leaked default key not rotated: %q", c.UpstreamAPIKey)
	}
	// 落盘后再次加载应保持轮换后的值（不会每次都变）
	c2, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c2.UpstreamAPIKey != c.UpstreamAPIKey {
		t.Fatalf("rotation not persisted: %q vs %q", c2.UpstreamAPIKey, c.UpstreamAPIKey)
	}

	t.Setenv(EnvUpstreamAPIKey, "sk-from-env-0123456789")
	c3, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c3.UpstreamAPIKey != "sk-from-env-0123456789" {
		t.Fatalf("env override not applied: %q", c3.UpstreamAPIKey)
	}
}
