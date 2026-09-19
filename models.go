// Part of the P1 (core split) refactor: code moved out of main.go.
// Same package (main) - do not change package clause manually.
//
// 模型目录缓存（唯一数据源：core/aggregator 聚合器）。
// 旧版 fetchModels/fetchGoModels 直连 opencode.ai 的硬编码拉取已删除——
// 目录一律由 syncModelsFromAggregator 从聚合器同步（启动、定时、reload 三个入口共用），
// 上游 URL/鉴权/免费判定等厂商专属逻辑全部收拢在 vendors/opencode。
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
	modelsCache   []ModelInfo
	goModelsCache []ModelInfo
	modelMu       sync.RWMutex
	modelsLoaded  bool
)

func containsModelWithID(models []ModelInfo, modelID string) bool {
	for _, model := range models {
		if model.ID == modelID {
			return true
		}
	}
	return false
}

func isModelInGoCatalog(modelID string) bool {
	modelMu.RLock()
	defer modelMu.RUnlock()
	return containsModelWithID(goModelsCache, modelID)
}

func isGoCatalogOnlyModel(modelID string) bool {
	modelMu.RLock()
	defer modelMu.RUnlock()
	return containsModelWithID(goModelsCache, modelID) && !containsModelWithID(modelsCache, modelID)
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

func getGoModelIDs() []string {
	modelMu.RLock()
	defer modelMu.RUnlock()
	ids := make([]string, len(goModelsCache))
	for i, m := range goModelsCache {
		ids[i] = m.ID
	}
	return ids
}

func filterFreeModels(ids []string) []string {
	free := make([]string, 0, len(ids))
	for _, id := range ids {
		if isFreeModel(id) {
			free = append(free, id)
		}
	}
	return free
}

// getCandidateModels 返回与当前认证权限一致的回退候选模型列表。
// public 模式只回退到免费模型；带 key 的模式只回退到与目标模型走相同端点的模型，避免跨目录 401。
func getCandidateModels(auth UpstreamAuth, modelID string) []string {
	if auth.Mode == AuthRoutePublic {
		return filterFreeModels(getModelIDs())
	}
	if auth.shouldUseGoEndpoint(modelID) {
		return getGoModelIDs()
	}
	return getModelIDs()
}

// startModelRefresh 定时刷新模型列表（每 10 分钟）；数据源为厂商聚合器（aggregator），
// 拉取后同步写入 modelsCache / goModelsCache。
func startModelRefresh() {
	go func() {
		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			// 节流版：厂商重建刚刷新过时跳过本轮，避免重复拉取。
			refreshModelCatalogIfDue()
			modelMu.RLock()
			n := len(modelsCache)
			ng := len(goModelsCache)
			modelMu.RUnlock()
			slog.Info("model catalog auto-refreshed", "zen", n, "go", ng)
		}
	}()
}
