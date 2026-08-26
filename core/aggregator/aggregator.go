// Package aggregator 负责模型目录聚合：注册厂商 → 拉取各厂商目录 → 合并缓存。
//
// 上层 /v1/models 与分发层都从这里取数；单厂商配置下输出与基线一致。
package aggregator

import (
	"context"
	"sync"
	"time"

	"github.com/6Kmfi6HP/opencode2api/core/contract"
)

// Aggregator 聚合多个厂商的模型目录。
type Aggregator struct {
	mu               sync.RWMutex
	vendors          []contract.Vendor
	catalog          []contract.Model    // 最近一次 Refresh 的合并结果
	providersByModel map[string][]string // 倒排索引：modelID → 提供它的厂商 ID 列表（Refresh 时重建）
	// lastGood 厂商 ID → 最近一次成功拉取的目录。Refresh 失败/空结果时回退沿用
	//（上游抖动不让目录整体落空）；ReplaceAll 时按仍在册厂商剪枝后立即重建 catalog
	//（消除「替换后到刷新完成」的目录空洞窗口——2026-08-26 网关 502 问题修复）。
	lastGood map[string][]contract.Model
}

// New 构造空聚合器。
func New() *Aggregator { return &Aggregator{lastGood: map[string][]contract.Model{}} }

// Register 注册一个厂商（幂等：重复注册同一 ID 会追加，由调用方保证唯一）。
func (a *Aggregator) Register(v contract.Vendor) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.vendors = append(a.vendors, v)
}

// ReplaceAll 原地替换全部厂商（运行时热重建用：配置 providers 变化后不换聚合器实例，
// 全局指针不动、读侧零改动）。合并目录**不整体清空**：按仍在册厂商的 lastGood 立即重建
// （已移除厂商的旧模型随剪枝淘汰、新增厂商首次成功拉取前无目录），避免替换后到下次
// Refresh 完成之间的目录空洞导致请求兜底 502；调用方需随后 Refresh 拉取最新目录。
func (a *Aggregator) ReplaceAll(vendors []contract.Vendor) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.vendors = append([]contract.Vendor(nil), vendors...)
	keep := make(map[string]bool, len(vendors))
	for _, v := range vendors {
		keep[v.ID()] = true
	}
	var all []contract.Model
	for _, v := range vendors {
		if ms, ok := a.lastGood[v.ID()]; ok {
			all = append(all, ms...)
		}
	}
	for id := range a.lastGood {
		if !keep[id] {
			delete(a.lastGood, id)
		}
	}
	a.catalog = all
	a.providersByModel = indexOf(all)
}

// Vendors 返回已注册厂商快照。
func (a *Aggregator) Vendors() []contract.Vendor {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return append([]contract.Vendor(nil), a.vendors...)
}

// Refresh 并行拉取所有已注册厂商目录并合并缓存。
// 每家厂商独立预算：在总 60s 预算内派生各自 ctx，一家慢/挂起只影响自己；
// 单个厂商失败不影响其它厂商（记录到 catalog 之外由调用方决定是否告警）。
// 某厂商本次拉取失败或返回空目录时，回退沿用其 lastGood（上一代成功目录）——
// 单次刷新失败不让该厂商的模型从合并目录中消失（2026-08-26 网关 502 修复；
// 注意 opencode 上游不可达时 ListModels 返回「空列表 + nil 错误」，故空结果同样回退）。
// 合并仍单次写锁重建倒排索引；目录按注册顺序拼接。
func (a *Aggregator) Refresh(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	vendors := a.Vendors()
	// 并行拉取各厂商目录（结果按下标收集，顺序与注册序一致）。
	parts := make([][]contract.Model, len(vendors))
	var wg sync.WaitGroup
	for i, v := range vendors {
		wg.Add(1)
		go func(i int, v contract.Vendor) {
			defer wg.Done()
			vctx, vcancel := context.WithTimeout(ctx, 60*time.Second)
			defer vcancel()
			ms, err := v.ListModels(vctx)
			a.mu.Lock()
			defer a.mu.Unlock()
			if err != nil || len(ms) == 0 {
				parts[i] = append([]contract.Model(nil), a.lastGood[v.ID()]...)
				return
			}
			cp := append([]contract.Model(nil), ms...)
			a.lastGood[v.ID()] = cp
			parts[i] = cp
		}(i, v)
	}
	wg.Wait()
	var all []contract.Model
	for _, ms := range parts {
		all = append(all, ms...)
	}
	a.mu.Lock()
	a.catalog = all
	a.providersByModel = indexOf(all)
	a.mu.Unlock()
	return nil
}

// indexOf 由合并目录重建倒排索引：modelID → 提供厂商（去重、保持目录出现顺序）。
func indexOf(all []contract.Model) map[string][]string {
	by := make(map[string][]string, len(all))
	for _, m := range all {
		seen := false
		for _, p := range by[m.ID] {
			if p == m.Provider {
				seen = true
				break
			}
		}
		if !seen {
			by[m.ID] = append(by[m.ID], m.Provider)
		}
	}
	return by
}

// Catalog 返回合并后的全部模型（含免费与非免费，按出现顺序）。
func (a *Aggregator) Catalog() []contract.Model {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return append([]contract.Model(nil), a.catalog...)
}

// FreeModels 返回全部免费模型（Free==true）。
func (a *Aggregator) FreeModels() []contract.Model {
	a.mu.RLock()
	defer a.mu.RUnlock()
	var out []contract.Model
	for _, m := range a.catalog {
		if m.Free {
			out = append(out, m)
		}
	}
	return out
}

// HasModel 判断某模型是否存在于某厂商的目录（用于"谁提供 X"的路由匹配）。
// 走倒排索引，O(提供该模型的厂商数)，不随目录规模线性放大。
func (a *Aggregator) HasModel(provider, modelID string) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, p := range a.providersByModel[modelID] {
		if p == provider {
			return true
		}
	}
	return false
}

// ProvidersOf 返回提供该模型的所有厂商 ID（按目录出现顺序，去重）。
// 供路由/UI 直接取"谁提供 X"，无需遍历目录。
func (a *Aggregator) ProvidersOf(modelID string) []string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return append([]string(nil), a.providersByModel[modelID]...)
}
