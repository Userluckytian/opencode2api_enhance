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

// 目录刷新节奏。
//
// 为什么不再固定 10 分钟：插件子进程「就绪」与「模型清单到位」是异步的，自定义源
// 上游目录也可能几分钟后才返回。固定 10 分钟意味着某个源晚到 1 秒，就要干等到
// 下一轮 —— 用户感知为「应用起来好一会儿，/v1/models 里还是没有那个源的模型」。
//
// 改用前密后疏：启动后的热窗口内每 10s 一轮（覆盖各子进程/上游陆续就绪），
// 热窗口结束后回到 10 分钟稳态。热窗口内的每轮刷新仍经 refreshModelCatalogIfDue
// 的 10s 最小间隔 + catalogRefreshMu 串行化，不会放大成请求风暴。
const (
	modelRefreshWarmWindow  = 3 * time.Minute // 启动后按高频刷新的时长
	modelRefreshWarmEvery   = 10 * time.Second
	modelRefreshSteadyEvery = 10 * time.Minute
)

// startModelRefresh 定时刷新模型列表；数据源为厂商聚合器（aggregator）。
//
// 启动后 modelRefreshWarmWindow 内用 10s 间隔快速收敛（插件陆续就绪、上游目录
// 陆续返回都能很快反映到 /v1/models），之后切到 10 分钟稳态，避免长期高频打上游。
func startModelRefresh() {
	go func() {
		started := time.Now()
		warm := true
		ticker := time.NewTicker(modelRefreshWarmEvery)
		defer ticker.Stop()
		for range ticker.C {
			if warm && time.Since(started) >= modelRefreshWarmWindow {
				warm = false
				ticker.Reset(modelRefreshSteadyEvery)
			}
			// 节流版：厂商重建/插件就绪刚刷过时跳过本轮，避免重复拉取。
			refreshModelCatalogIfDue()
			modelMu.RLock()
			n := len(modelsCache)
			modelMu.RUnlock()
			slog.Info("model catalog auto-refreshed", "count", n, "warm", warm)
		}
	}()
}
