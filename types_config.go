// Part of the P1 (core split) refactor: code moved out of main.go.
// Same package (main) - do not change package clause manually.
//
// 根进程配置结构（AppConfig）。
//
// v2.0.0 精简：OpenCode 免费层（节点池/实例/订阅/扫描/竞速/sidecar）相关字段
// 全部移除——那部分已由 40080 的外部引擎承担，其配置在 core/manager 里生成。
// 本结构只剩「自定义源 + 插件供应商」与全局行为开关。
package main

import "github.com/6Kmfi6HP/opencode2api/core/manager"

// AppConfig 根进程配置。
type AppConfig struct {
	ModelAlias           map[string]string `json:"model_alias,omitempty"`
	ReasoningEffortMap   map[string]string `json:"reasoning_effort_map,omitempty"`
	ForceDisableThinking bool              `json:"force_disable_thinking,omitempty"`

	// CallLogMax 调用日志读取上限（0 = 全部历史）。
	CallLogMax int `json:"call_log_max,omitempty"`
	// UiPollIntervalSec 界面轮询间隔（秒）：nil = 默认 5，0 = 关闭。
	UiPollIntervalSec *int `json:"ui_poll_interval_sec,omitempty"`

	// Providers 厂商注册表（custom / remote）。
	Providers []ProviderCfg `json:"providers,omitempty"`
	// Routing 模型→厂商路由。
	Routing RoutingCfg `json:"routing,omitempty"`
	// AutoModel auto 虚拟模型配置。
	AutoModel *manager.AutoModelCfg `json:"auto_model,omitempty"`
}

// ProviderCfg 单个 provider 条目（配置驱动）。
type ProviderCfg struct {
	ID   string `json:"id"`   // 厂商标识，与厂商实现 ID() 一致（如 "mysrc"）
	Type string `json:"type"` // 厂商类型（"custom" | "remote"），用于选择实现
	Name string `json:"name,omitempty"`
	// Enabled 未设置（nil）视为启用。
	Enabled *bool `json:"enabled,omitempty"`
	// Params 条目级参数（base_url / api_keys / protocol / proxy 等；"_" 前缀为运行时注入）。
	Params map[string]any `json:"params,omitempty"`
}

// RoutingCfg 模型路由配置。
type RoutingCfg struct {
	// ModelProvider 模型名 → 厂商 ID 的强制映射（优先于厂商目录匹配）。
	ModelProvider map[string]string `json:"model_provider_map,omitempty"`
	// DefaultProvider 兜底厂商（缺省取聚合器第一个厂商）。
	DefaultProvider string `json:"default_provider,omitempty"`
}
