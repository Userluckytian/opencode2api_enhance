// Part of the P1 (core split) refactor: code moved out of main.go.
// Same package (main) - do not change package clause manually.
package main

import "strings"

// resolveModel 解析请求模型名 → 上游真实模型名。
// preferFree 表示请求处于免费档（public 认证）：此时若缓存中存在 "<名>-free" 变体，
// 一律优先映射到免费变体——即使上游同时存在同名付费模型（如 deepseek-v4-flash），
// 避免免费调用被同名付费模型抢走而上游 401 need key。
func resolveModel(model string, preferFree bool) string {
	m := strings.TrimSpace(model)
	configMu.RLock()
	alias, ok := modelAlias[m]
	configMu.RUnlock()
	if ok {
		return alias
	}
	// 免费档：优先 -free 变体（覆盖"同名付费模型已在缓存"的场景）。
	if preferFree && modelInCaches(m+"-free") {
		return m + "-free"
	}
	// 自动兜底：新 -free 模型无需手动加别名。
	// 若请求名本身已在缓存（含 -free）则原样使用；否则若「请求名+-free」存在，
	// 说明客户发的是去 -free 的展示名，映射回真实免费模型名。
	if modelInCaches(m) {
		return m
	}
	if strings.HasSuffix(m, "-free") {
		return m
	}
	if modelInCaches(m + "-free") {
		return m + "-free"
	}
	return m
}

// modelInCaches 判断模型名是否存在于免费模型或 Go 目录缓存中（含 -free 原名）。
func modelInCaches(id string) bool {
	modelMu.RLock()
	defer modelMu.RUnlock()
	return containsModelWithID(modelsCache, id)
}

// displayModelName 返回模型名的展示形式（用于前缀/UI 展示，不含 -free 标记）：
//  1. 若 model 是某别名的真实上游名（含 -free），返回该别名（显式配置优先）；
//  2. 否则若以 -free 结尾，去掉后缀；
//  3. 否则原样返回。
func displayModelName(model string) string {
	m := strings.TrimSpace(model)
	configMu.RLock()
	defer configMu.RUnlock()
	for alias, upstream := range modelAlias {
		if upstream == m {
			return alias
		}
	}
	if strings.HasSuffix(m, "-free") {
		return strings.TrimSuffix(m, "-free")
	}
	return m
}

func getForceDisableThinking() bool {
	configMu.RLock()
	defer configMu.RUnlock()
	return forceDisableThinking
}

// getShowNodePrefix 恒返回 false：节点池移除后没有「节点前缀」可展示。
func getShowNodePrefix() bool { return false }

func getReasoningEffortMap() map[string]string {
	configMu.RLock()
	defer configMu.RUnlock()
	cp := make(map[string]string, len(reasoningEffortMap))
	for k, v := range reasoningEffortMap {
		cp[k] = v
	}
	return cp
}

// ======================== Token 统计 ========================
