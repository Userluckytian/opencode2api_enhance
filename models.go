// Part of the P1 (core split) refactor: code moved out of main.go.
// Same package (main) - do not change package clause manually.
//
// 模型目录缓存（唯一数据源：core/aggregator 聚合器）。
// v2.0.0 起 OpenCode 双目录（zen/go）已随免费层移除，只剩一份平铺缓存。
package main

import (
	"log/slog"
	"sync"
	"time"
)

type ModelInfo struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
	// 扩展元数据（OpenRouter 风格；OpenAI 兼容客户端会忽略未知字段，
	// 支持的客户端据此展示上下文窗口/最大输出）。0 = 未知，不输出。
	ContextLength   int `json:"context_length,omitempty"`
	MaxOutputTokens int `json:"max_output_tokens,omitempty"`
}

var (
	modelsCache  []ModelInfo
	modelMu      sync.RWMutex
	modelsLoaded bool
)

func containsModelWithID(models []ModelInfo, modelID string) bool {
	for _, model := range models {
		if model.ID == modelID {
			return true
		}
	}
	return false
}

func getModelIDs() []string {
	modelMu.RLock()
	defer modelMu.RUnlock()
	ids := make([]string, len(modelsCache))
	for i, m := range modelsCache {
		ids[i] = m.ID
	}
	return ids
}

// startModelRefresh 定时刷新模型列表（每 10 分钟）；数据源为厂商聚合器（aggregator）。
func startModelRefresh() {
	go func() {
		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			// 节流版：厂商重建刚刷新过时跳过本轮，避免重复拉取。
			refreshModelCatalogIfDue()
			modelMu.RLock()
			n := len(modelsCache)
			modelMu.RUnlock()
			slog.Info("model catalog auto-refreshed", "count", n)
		}
	}()
}
