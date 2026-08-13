// Part of the P1 (core split) refactor: code moved out of main.go.
// Same package (main) - do not change package clause manually.
package main

import (
	"encoding/json"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

type ModelStats struct {
	RequestCount     int64 `json:"request_count"`
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

type TokenStatsData struct {
	TotalRequests int64                  `json:"total_requests"`
	Models        map[string]*ModelStats `json:"models"`
}

var (
	tokenStats     = &TokenStatsData{Models: map[string]*ModelStats{}}
	tokenStatsMu   sync.Mutex
	tokenStatsPath = "stats.json"
)

// ======================== 节点 Token 统计 ========================
// 网关/代理池模式下按实际选中的 SOCKS5 出口（节点）累计 token 统计，
// 供统计界面展示「统一网关总体 + 各节点明细」。

type NodeStat struct {
	RequestCount     int64 `json:"request_count"`
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

type NodeStatsData struct {
	TotalRequests int64                `json:"total_requests"`
	Nodes         map[string]*NodeStat `json:"nodes"`
}

var (
	nodeStats     = &NodeStatsData{Nodes: map[string]*NodeStat{}}
	nodeStatsMu   sync.Mutex
	nodeStatsPath = "node_stats.json"
)

func loadNodeStats() {
	data, err := os.ReadFile(nodeStatsPath)
	if err != nil {
		return
	}
	var st NodeStatsData
	if err := json.Unmarshal(data, &st); err != nil {
		return
	}
	nodeStatsMu.Lock()
	if st.Nodes == nil {
		st.Nodes = map[string]*NodeStat{}
	}
	nodeStats = &st
	nodeStatsMu.Unlock()
}

func saveNodeStats() {
	nodeStatsMu.Lock()
	data, err := json.MarshalIndent(nodeStats, "", "  ")
	nodeStatsMu.Unlock()
	if err != nil {
		return
	}
	os.WriteFile(nodeStatsPath, data, 0644)
}

// ======================== 统计落盘合并（防磁盘 IO 风暴） ========================
// 历史实现每个计费请求都 `go saveTokenStats()` + `go saveNodeStats()`，
// 各自做一次整文件 JSON marshal + 落盘：高并发下 goroutine 与磁盘写堆积。
// 现改为：内存统计即时更新（行为不变），落盘由单个后台协程按 2s 窗口合并执行。

var (
	tokenStatsDirty atomic.Bool
	nodeStatsDirty  atomic.Bool
	statsFlushCh    = make(chan struct{}, 1)
	statsFlusherOn  sync.Once
)

func markTokenStatsDirty() {
	tokenStatsDirty.Store(true)
	signalStatsFlush()
}

func markNodeStatsDirty() {
	nodeStatsDirty.Store(true)
	signalStatsFlush()
}

func signalStatsFlush() {
	select {
	case statsFlushCh <- struct{}{}:
	default:
	}
}

// startStatsFlusher 启动（惰性，首次记账时初始化）后台落盘协程：
// 收到信号后等待 2s 合并窗口，再一次性写 token/node 两份统计。
func startStatsFlusher() {
	statsFlusherOn.Do(func() {
		go func() {
			for range statsFlushCh {
				time.Sleep(2 * time.Second)
				if tokenStatsDirty.Swap(false) {
					saveTokenStats()
				}
				if nodeStatsDirty.Swap(false) {
					saveNodeStats()
				}
			}
		}()
	})
}

func recordNodeUsage(addr string, promptTokens, completionTokens, totalTokens int64) {
	// 节点级统计只对统一网关进程（代理池路由）有意义；
	// 直连实例走自身 sing-box，其记录无人读取，跳过以避免垃圾文件。
	if addr == "" || !gatewayMode {
		return
	}
	nodeStatsMu.Lock()
	nodeStats.TotalRequests++
	ns, ok := nodeStats.Nodes[addr]
	if !ok {
		ns = &NodeStat{}
		nodeStats.Nodes[addr] = ns
	}
	ns.RequestCount++
	ns.PromptTokens += promptTokens
	ns.CompletionTokens += completionTokens
	ns.TotalTokens += totalTokens
	nodeStatsMu.Unlock()
	// 落盘改为后台合并（见 startStatsFlusher）。
	markNodeStatsDirty()
	startStatsFlusher()
}

// ======================== 数据模型 ========================
