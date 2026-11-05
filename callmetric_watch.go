package main

// 调用指标采集常驻协程（v2.0.0 增补）。
//
// 设计要点：
//   - 30s 一轮增量消费引擎 stdout（引擎是追加写文件，记偏移避免重复）；
//   - 每轮顺手做一次旧指标清理（默认保留 30 天），避免磁盘无限增长；
//   - 任何失败只记 warn：指标是旁路，绝不能影响推理。

import (
	"log/slog"
	"time"

	"github.com/6Kmfi6HP/opencode2api/core/manager"
)

// callMetricInterval 采集间隔。
//
// 30s 与引擎看门狗同频：既不会让指标落后太多，也不会频繁扫文件。
const callMetricInterval = 30 * time.Second

// callMetricPruneEvery 每多少轮做一次清理（30s × 40 = 20 分钟）。
const callMetricPruneEvery = 40

// watchCallMetrics 常驻采集循环（进程生命周期内不返回）。
func watchCallMetrics(m *manager.Manager) {
	if m == nil {
		return
	}
	// 启动时先跑一次，让历史 stdout 立刻可见（引擎可能已运行很久）。
	if n, err := m.ConsumeEngineMetrics(); err != nil {
		slog.Warn("call metrics: initial consume failed", "error", err)
	} else if n > 0 {
		slog.Info("call metrics: initial consume done", "written", n)
	}

	ticker := time.NewTicker(callMetricInterval)
	defer ticker.Stop()

	round := 0
	for range ticker.C {
		round++
		n, err := m.ConsumeEngineMetrics()
		if err != nil {
			slog.Warn("call metrics: consume failed", "error", err)
		} else if n > 0 {
			slog.Debug("call metrics: consumed", "written", n)
		}

		if round%callMetricPruneEvery == 0 {
			if removed := m.PruneCallMetrics(0); removed > 0 {
				slog.Info("call metrics: pruned old files", "removed", removed)
			}
		}
	}
}
