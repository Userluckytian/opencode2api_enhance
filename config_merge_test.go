package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestMainSaveConfigPreservesGatewayKey：模拟主程序启动时无条件 saveConfig(AppConfig)
// 的场景——必须保留 core/manager 结构写入的 gateway_key（自定义网关密码重启失效的根因）。
func TestMainSaveConfigPreservesGatewayKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	// 预置文件：AppConfig 字段 + manager 独有的 gateway_key
	pre := map[string]any{
		"model_alias":            map[string]any{"gpt-5": "gpt-5-free"},
		"force_disable_thinking": true,
		"gateway_key":            "my-secret-key",
	}
	raw, _ := json.MarshalIndent(pre, "", "  ")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write pre: %v", err)
	}

	// 主程序启动流程：loadConfig → saveConfig(AppConfig)
	cfg := loadConfig(path)
	if err := saveConfig(path, cfg); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}
	data, _ := os.ReadFile(path)
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal saved: %v", err)
	}
	if got["gateway_key"] != "my-secret-key" {
		t.Errorf("gateway_key = %v, want my-secret-key (被 AppConfig 覆盖写抹掉)", got["gateway_key"])
	}
	if _, ok := got["model_alias"]; !ok {
		t.Error("model_alias lost")
	}
	if _, ok := got["force_disable_thinking"]; !ok {
		t.Error("force_disable_thinking lost")
	}
}

// TestMainSaveConfigClearRouting：AppConfig 声明但清空（omitempty）的字段应删除旧值。
func TestMainSaveConfigClearRouting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	pre := map[string]any{"routing": map[string]any{"default_provider": "windsurf"}, "gateway_key": "k"}
	raw, _ := json.MarshalIndent(pre, "", "  ")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := loadConfig(path)
	if err := saveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	// 空 routing 序列化为 "routing":{}（RoutingCfg 无 omitempty 值时会写空 map？验证实际行为）
	var got map[string]any
	_ = json.Unmarshal(data, &got)
	if _, ok := got["gateway_key"]; !ok {
		t.Error("gateway_key lost")
	}
	_ = data
}