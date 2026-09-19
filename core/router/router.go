// Package router 实现"模型→厂商"分发解析。
//
// 解析优先级：model_provider_map 精确命中 → 厂商目录提供者 → 默认厂商。
// Candidates 返回可服务同一模型的全部厂商（按优先级排序），供厂商级 failover 使用。
package router

import (
	"sort"
	"strings"

	"github.com/6Kmfi6HP/opencode2api/core/aggregator"
	"github.com/6Kmfi6HP/opencode2api/core/contract"
)

// Router 持有厂商注册表与模型→厂商映射。
type Router struct {
	agg       *aggregator.Aggregator
	modelMap  map[string]string // model → provider（强制映射）
	defaultID string            // 兜底厂商 ID
}

// New 构造路由器。
func New(agg *aggregator.Aggregator, modelMap map[string]string, defaultID string) *Router {
	cp := make(map[string]string, len(modelMap))
	for k, v := range modelMap {
		cp[k] = v
	}
	if defaultID == "" {
		defaultID = "opencode"
	}
	return &Router{agg: agg, modelMap: cp, defaultID: defaultID}
}

// vendorByID 返回指定厂商（不存在返回 nil）。
func (r *Router) vendorByID(id string) contract.Vendor {
	for _, v := range r.agg.Vendors() {
		if v.ID() == id {
			return v
		}
	}
	return nil
}

// candidates 返回按优先级排序的可服务 modelID 的厂商：
//  1. modelMap[model]（若存在且已注册）
//  2. 聚合器倒排索引中提供该模型的厂商（按目录出现顺序）
//  3. 带厂商前缀（"x/model"）的模型：前缀 x 命中已注册厂商 → 直连该厂商；
//     x 未注册（停用/未加载/写错）→ 返回空候选，由上层报明确错误
//  4. 无前缀且无人提供 → 兜底 defaultID
//
// 带前缀的模型不兜底默认厂商：默认厂商对该模型必然报 model not supported
// （401），既给客户端一个误导性错误，还会把默认厂商的账号池打进坏池
//（2026-09-19 skywork 插件停用期间 skywork/* 请求全部误落 opencode 的事故）。
// 去重并保持顺序。
func (r *Router) candidates(modelID string) []contract.Vendor {
	var out []contract.Vendor
	seen := map[string]bool{}
	add := func(id string) {
		if id == "" || seen[id] {
			return
		}
		if v := r.vendorByID(id); v != nil {
			seen[id] = true
			out = append(out, v)
		}
	}
	if p, ok := r.modelMap[modelID]; ok {
		add(p)
	}
	for _, pid := range r.agg.ProvidersOf(modelID) {
		add(pid)
	}
	if len(out) == 0 {
		if prefix, _, ok := strings.Cut(modelID, "/"); ok && prefix != "" {
			// 前缀命中已注册厂商（其目录本轮拉取失败 ≠ 厂商不可用）→ 直连该厂商。
			if v := r.vendorByID(prefix); v != nil {
				out = append(out, v)
				return out
			}
			return out // 前缀厂商未注册：空候选，上层报明确错误
		}
		add(r.defaultID) // 无前缀模型：保持默认厂商兜底
	}
	return out
}

// Candidates 返回可服务该模型的厂商（failover 顺序）。
func (r *Router) Candidates(modelID string) []contract.Vendor {
	return r.candidates(modelID)
}

// Resolve 返回首选厂商（nil 表示无任何厂商可服务）。
func (r *Router) Resolve(modelID string) contract.Vendor {
	cs := r.candidates(modelID)
	if len(cs) == 0 {
		return nil
	}
	return cs[0]
}

// ProviderList 返回全部已注册厂商 ID（排序，供 UI/测试）。
func (r *Router) ProviderList() []string {
	var ids []string
	for _, v := range r.agg.Vendors() {
		ids = append(ids, v.ID())
	}
	sort.Strings(ids)
	return ids
}
