// 统计聚合（前端 /api/admin/stats 契约）。
//
// v2.0.0：实例池 / 统一网关随免费层移除，统计只剩一个数据源——
// 供应商服务（50080）的调用日志与 stats.json。节点维度（SOCKS5 出口）已无意义，
// 因此对外契约收敛为「模型维度 + 总计」。
package manager

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ModelStat 单模型统计（前端契约）。
type ModelStat struct {
	Model            string `json:"model"`
	Requests         int64  `json:"requests"`
	PromptTokens     int64  `json:"prompt_tokens"`
	CompletionTokens int64  `json:"completion_tokens"`
	TotalTokens      int64  `json:"total_tokens"`
}

// SourceStat 单来源统计（"供应商服务"）。
type SourceStat struct {
	Name             string      `json:"name"`
	Exists           bool        `json:"exists"`
	Requests         int64       `json:"requests"`
	PromptTokens     int64       `json:"prompt_tokens"`
	CompletionTokens int64       `json:"completion_tokens"`
	TotalTokens      int64       `json:"total_tokens"`
	Models           []ModelStat `json:"models"`
}

// StatsSummary 汇总视图。
type StatsSummary struct {
	TotalRequests         int64        `json:"total_requests"`
	TotalPromptTokens     int64        `json:"total_prompt_tokens"`
	TotalCompletionTokens int64        `json:"total_completion_tokens"`
	TotalTokens           int64        `json:"total_tokens"`
	Sources               []SourceStat `json:"sources"`
	// ResetAt 最近一次「重置统计」的时间点（RFC3339；空 = 从未重置）。
	// 按天视图只统计该时间点之后的调用日志。
	ResetAt string `json:"reset_at,omitempty"`
}

// providerServiceDisplayName 供应商服务目录的展示名。
const providerServiceDisplayName = "供应商服务"

// statsResetProbeTimeout 重置统计前的端口存活探测超时。
const statsResetProbeTimeout = 300 * time.Millisecond

// GoModelStats / GoStatsData 与 main 包写盘结构一致（子进程 stats.json 格式）。
type GoModelStats struct {
	RequestCount     int64 `json:"request_count"`
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

type GoStatsData struct {
	TotalRequests int64                    `json:"total_requests"`
	Models        map[string]*GoModelStats `json:"models"`
}

// statsWriteRetryAttempts / statsWriteRetryDelay 统计文件直写对瞬时跨进程占用的重试：
// 子进程后台原子写（tmp+Rename）与本管理器直写并发时，Windows 会报
// "being used by another process" / "Access is denied"，短暂重试即可越过。
const (
	statsWriteRetryAttempts = 8
	statsWriteRetryDelay    = 25 * time.Millisecond
)

// AggregateStats 读取供应商服务的 stats.json 并汇总。
func (m *Manager) AggregateStats() StatsSummary {
	dir := filepath.Join(m.paths.RuntimeDir, providerServiceDirBase)
	sum := StatsSummary{Sources: []SourceStat{}}

	stat := SourceStat{Name: providerServiceDisplayName, Exists: true, Models: []ModelStat{}}
	if data, err := os.ReadFile(filepath.Join(dir, "stats.json")); err == nil {
		var goStats GoStatsData
		if json.Unmarshal(data, &goStats) == nil {
			for model, ms := range goStats.Models {
				if ms == nil {
					continue
				}
				stat.Requests += ms.RequestCount
				stat.PromptTokens += ms.PromptTokens
				stat.CompletionTokens += ms.CompletionTokens
				stat.TotalTokens += ms.TotalTokens
				stat.Models = append(stat.Models, ModelStat{
					Model:            model,
					Requests:         ms.RequestCount,
					PromptTokens:     ms.PromptTokens,
					CompletionTokens: ms.CompletionTokens,
					TotalTokens:      ms.TotalTokens,
				})
			}
		}
	}
	sort.Slice(stat.Models, func(i, j int) bool { return stat.Models[i].TotalTokens > stat.Models[j].TotalTokens })

	sum.Sources = append(sum.Sources, stat)
	sum.TotalRequests += stat.Requests
	sum.TotalPromptTokens += stat.PromptTokens
	sum.TotalCompletionTokens += stat.CompletionTokens
	sum.TotalTokens += stat.TotalTokens

	if t := m.statsResetAt(); !t.IsZero() {
		sum.ResetAt = t.Format(time.RFC3339)
	}
	return sum
}

// ResetStatsResult 重置统计的结果汇总（前端契约）。
type ResetStatsResult struct {
	ResetCount   int      `json:"reset_count"`
	DeletedCount int      `json:"deleted_count"`
	Failed       []string `json:"failed"`
	// ResetAt 本次重置时间点（RFC3339）；按天视图从该时刻起算。
	ResetAt string `json:"reset_at,omitempty"`
}

// statsResetAtPath 统计重置时间点文件（runtime/stats_reset_at）。
func (m *Manager) statsResetAtPath() string {
	return filepath.Join(m.paths.RuntimeDir, "stats_reset_at")
}

// statsResetAt 读取最近一次统计重置时间点；缺失/损坏返回零值。
func (m *Manager) statsResetAt() time.Time {
	data, err := os.ReadFile(m.statsResetAtPath())
	if err != nil {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(string(data)))
	if err != nil {
		return time.Time{}
	}
	return t
}

// writeStatsResetAt 原子写入统计重置时间点。
func (m *Manager) writeStatsResetAt(t time.Time) error {
	if err := os.MkdirAll(m.paths.RuntimeDir, 0o755); err != nil {
		return err
	}
	return writeFileAtomic(m.statsResetAtPath(), []byte(t.Format(time.RFC3339)))
}

// writeEmptyStatsFile 覆写为空统计文件。
func writeEmptyStatsFile(path string) error {
	v := map[string]any{"total_requests": 0, "models": map[string]any{}}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeFileRetry(path, data, 0o644)
}

// writeFileRetry 写文件并对瞬时跨进程占用重试。
func writeFileRetry(path string, data []byte, perm os.FileMode) error {
	var err error
	for i := 0; i < statsWriteRetryAttempts; i++ {
		if err = os.WriteFile(path, data, perm); err == nil {
			return nil
		}
		if i+1 < statsWriteRetryAttempts {
			time.Sleep(statsWriteRetryDelay)
		}
	}
	return err
}

// ResetStats 重置供应商服务统计（运行中走 HTTP DELETE，未运行覆写磁盘）。
// clearDeleted 保留参数以兼容既有调用方；当前无「已删除实例」概念，忽略。
func (m *Manager) ResetStats(clearDeleted bool) ResetStatsResult {
	res := ResetStatsResult{Failed: []string{}}
	dir := filepath.Join(m.paths.RuntimeDir, providerServiceDirBase)
	statsPath := filepath.Join(dir, "stats.json")

	if probePort(m.managerProviderPort(), statsResetProbeTimeout) {
		status, _, err := httpDeleteJSON(m.managerProviderPort(), "/api/reset-stats", 6*time.Second, effectiveGatewayKey(m.loadConfig()))
		if err == nil && status >= 200 && status < 300 {
			res.ResetCount++
		} else if err != nil {
			res.Failed = append(res.Failed, providerServiceDisplayName+": "+err.Error())
		} else {
			res.Failed = append(res.Failed, providerServiceDisplayName+": HTTP "+itoa16(status))
		}
	} else if data, err := os.ReadFile(statsPath); err == nil && len(data) > 0 {
		if writeEmptyStatsFile(statsPath) == nil {
			res.ResetCount++
		} else {
			res.Failed = append(res.Failed, providerServiceDisplayName+": 覆写 stats.json 失败")
		}
	}

	// 记录重置时间点：按天视图只统计此刻之后的调用日志，避免「累计已清零、
	// 按天视图仍显示旧数据」的两套数据源不一致。
	now := time.Now()
	if err := m.writeStatsResetAt(now); err != nil {
		res.Failed = append(res.Failed, "统计重置时间点写入失败: "+err.Error())
	} else {
		res.ResetAt = now.Format(time.RFC3339)
	}
	return res
}

// StatsHandler GET 统计聚合。
func (m *Manager) StatsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodOK(w, r, http.MethodGet) {
			return
		}
		writeJSON(w, m.AggregateStats())
	}
}

// ResetStatsHandler POST 重置统计（?clearDeleted=bool，当前忽略）。
func (m *Manager) ResetStatsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodOK(w, r, http.MethodPost) {
			return
		}
		clearDeleted := true
		if q := r.URL.Query().Get("clearDeleted"); q != "" {
			clearDeleted = q == "true" || q == "1"
		}
		writeJSON(w, m.ResetStats(clearDeleted))
	}
}
