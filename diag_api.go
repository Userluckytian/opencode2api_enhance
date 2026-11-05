package main

// 诊断面（v2.1）：三端口自检 + 引擎实时 + 失败聚合 + 手动模型探测。
//
// 为什么单独做一组端点：这些问题此前只能靠翻日志或手工 curl 定位
//（例如「429 是不是我们的 bug」「40080 到底起没起」「某个模型现在能不能用」）。
// 这里把排查动作收口成一次点击。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/6Kmfi6HP/opencode2api/core/manager"
)

// diagHTTPClient 本地探测专用 client：不走系统代理。
//
// 本机 127.0.0.1 的请求若走 HTTP_PROXY 会被转发到外部代理，代理无法连回本机端口，
// 表现为「连接挂住直到超时」。这里显式禁用代理。
var diagHTTPClient = &http.Client{
	Timeout: 65 * time.Second,
	Transport: &http.Transport{
		Proxy:             nil, // 不用任何代理
		MaxIdleConns:      16,
		IdleConnTimeout:   30 * time.Second,
		DisableKeepAlives: false,
	},
}

// ---- 自检 ----

// DiagCheck 单项自检结果。
type DiagCheck struct {
	Name       string `json:"name"`
	Target     string `json:"target"`
	OK         bool   `json:"ok"`
	Detail     string `json:"detail,omitempty"`
	DurationMS int64  `json:"duration_ms"`
}

// DiagReport 自检总报告。
type DiagReport struct {
	Checks      []DiagCheck `json:"checks"`
	GeneratedAt string      `json:"generated_at"`
}

// diagHandler GET /api/admin/diag → 一次性自检（进程 / 引擎 / 供应商 / 配置 / 二进制）。
func diagHandler(m *manager.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSONResp(w, runDiag(r.Context(), m))
	}
}

// runDiag 执行全部自检项。
func runDiag(ctx context.Context, m *manager.Manager) DiagReport {
	rep := DiagReport{GeneratedAt: time.Now().Format(time.RFC3339)}

	// 1) 管理进程自身（能跑到这里就说明 HTTP 正常）
	rep.Checks = append(rep.Checks, DiagCheck{
		Name:   "manager",
		Target: "127.0.0.1:" + port,
		OK:     true,
		Detail: versionString(),
	})

	// 2) 免费层引擎（40080）：从状态里拿端口，避免重复推导
	st := m.FreeTierEngineStatus()
	rep.Checks = append(rep.Checks, probeHealthz(ctx, st.Port))

	// 3) 供应商服务（50080）
	view := m.ConfigViewOf()
	rep.Checks = append(rep.Checks, probeHealth(ctx, view.ProviderPortEffective))

	// 4) 配置文件
	rep.Checks = append(rep.Checks, checkConfigFile(configPath))

	// 5) 引擎二进制
	rep.Checks = append(rep.Checks, DiagCheck{
		Name:   "engine_binary",
		Target: st.BinPath,
		OK:     st.BinExists,
		Detail: map[bool]string{true: "已安装", false: "未安装（把 opencode2api-win 放到 bin/ 目录）"}[st.BinExists],
	})

	return rep
}

// probeHealthz 检查引擎 /healthz（返回 ready + models 数量）。
func probeHealthz(ctx context.Context, port uint16) DiagCheck {
	target := fmt.Sprintf("127.0.0.1:%d", port)
	chk := DiagCheck{Name: "engine", Target: target}
	start := time.Now()
	defer func() { chk.DurationMS = time.Since(start).Milliseconds() }()

	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, "http://"+target+"/healthz", nil)
	if err != nil {
		chk.Detail = err.Error()
		return chk
	}
	resp, err := diagHTTPClient.Do(req)
	if err != nil {
		chk.Detail = "引擎未响应（未启动或端口被占）"
		return chk
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		chk.Detail = fmt.Sprintf("HTTP %d", resp.StatusCode)
		return chk
	}
	var hz struct {
		Ready  bool   `json:"ready"`
		Status string `json:"status"`
		Models struct {
			Total   int `json:"total"`
			Exposed int `json:"exposed"`
		} `json:"models"`
		Warp struct {
			Status string `json:"status"`
			Active int    `json:"active"`
		} `json:"warp"`
	}
	if json.NewDecoder(resp.Body).Decode(&hz) != nil {
		chk.Detail = "响应不是合法 JSON"
		return chk
	}
	chk.OK = hz.Ready
	chk.Detail = fmt.Sprintf("ready=%v status=%s models=%d/%d warp=%s(%d)",
		hz.Ready, hz.Status, hz.Models.Exposed, hz.Models.Total, hz.Warp.Status, hz.Warp.Active)
	return chk
}

// probeHealth 检查供应商服务 /health。
func probeHealth(ctx context.Context, port uint16) DiagCheck {
	target := fmt.Sprintf("127.0.0.1:%d", port)
	chk := DiagCheck{Name: "provider", Target: target}
	start := time.Now()
	defer func() { chk.DurationMS = time.Since(start).Milliseconds() }()

	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, "http://"+target+"/health", nil)
	if err != nil {
		chk.Detail = err.Error()
		return chk
	}
	resp, err := diagHTTPClient.Do(req)
	if err != nil {
		chk.Detail = "供应商服务未响应（未启动）"
		return chk
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()
	chk.OK = resp.StatusCode == http.StatusOK
	chk.Detail = fmt.Sprintf("HTTP %d", resp.StatusCode)
	return chk
}

// checkConfigFile 检查配置文件可解析、providers 可读。
func checkConfigFile(path string) DiagCheck {
	chk := DiagCheck{Name: "config", Target: path}
	start := time.Now()
	defer func() { chk.DurationMS = time.Since(start).Milliseconds() }()

	cfg := loadConfig(path)
	chk.OK = true
	if len(cfg.Providers) == 0 {
		chk.Detail = "可读，providers 为空（纯插件供应商模式）"
	} else {
		chk.Detail = fmt.Sprintf("可读，providers=%d", len(cfg.Providers))
	}
	return chk
}

// ---- 模型手动探测 ----

// ModelProbeRequest 单个/全量探测请求。
type ModelProbeRequest struct {
	Model string `json:"model,omitempty"`
}

// ModelProbeResult 单模型探测结果。
type ModelProbeResult struct {
	Model     string `json:"model"`
	OK        bool   `json:"ok"`
	Status    int    `json:"status"`
	LatencyMS int64  `json:"latency_ms"`
	Error     string `json:"error,omitempty"`
	Preview   string `json:"preview,omitempty"`
}

// modelProbeHandler POST /api/admin/free-tier/probe
//
// 对引擎发起一次最小 chat 请求（max_tokens=8），用真实状态码判定模型是否可用。
// 这是引擎自带 availability 探测之外的**手动复核**：点一下就知道当下的真实结果。
func modelProbeHandler(m *manager.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req ModelProbeRequest
		_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req)

		var targets []string
		if model := strings.TrimSpace(req.Model); model != "" {
			targets = []string{model}
		} else {
			view := m.FreeTierModels(r.Context())
			for _, it := range view.Models {
				if it.Available {
					targets = append(targets, it.ID)
				}
			}
			// 全量探测上限：避免一次点出几十个上游请求
			const maxBatch = 20
			if len(targets) > maxBatch {
				targets = targets[:maxBatch]
			}
		}
		if len(targets) == 0 {
			writeJSONResp(w, map[string]any{"results": []ModelProbeResult{}, "note": "没有可探测的模型（引擎未就绪）"})
			return
		}

		st := m.FreeTierEngineStatus()
		if !st.Running {
			writeJSONResp(w, map[string]any{
				"results": []ModelProbeResult{},
				"note":    "引擎未运行，无法探测",
			})
			return
		}
		// 引擎「不健康」时（典型：WARP 出口全部冷却）推理请求会挂起等待可用出口，
		// 探测会卡住几十秒。这里先做健康预判，不健康就立刻给出明确原因。
		if st.WarpStatus == "unavailable" && st.WarpActive == 0 {
			writeJSONResp(w, map[string]any{
				"results": []ModelProbeResult{},
				"note": fmt.Sprintf(
					"引擎当前无可用出口（WARP 活跃 0 / 冷却 %d），推理请求会挂起，已跳过探测。请等待出口冷却结束或更换出口配置。",
					st.WarpCooling),
			})
			return
		}

		base := fmt.Sprintf("http://127.0.0.1:%d", st.Port)
		results := make([]ModelProbeResult, 0, len(targets))
		for _, id := range targets {
			results = append(results, probeOneModel(r.Context(), base, st.APIKey, id))
		}
		writeJSONResp(w, map[string]any{"results": results})
	}
}

// probeOneModel 对单个模型发一次最小请求。
func probeOneModel(ctx context.Context, base, apiKey, model string) ModelProbeResult {
	res := ModelProbeResult{Model: model}
	body, _ := json.Marshal(map[string]any{
		"model":      model,
		"max_tokens": 8,
		"stream":     false,
		"messages":   []map[string]string{{"role": "user", "content": "hi"}},
	})
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, base+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		res.Error = err.Error()
		return res
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	start := time.Now()
	resp, err := diagHTTPClient.Do(req)
	res.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		res.Error = err.Error()
		return res
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	res.Status = resp.StatusCode
	res.OK = resp.StatusCode >= 200 && resp.StatusCode < 300
	preview := strings.TrimSpace(string(raw))
	if len(preview) > 240 {
		preview = preview[:240] + "…"
	}
	res.Preview = preview
	if !res.OK && res.Error == "" {
		res.Error = http.StatusText(resp.StatusCode)
	}
	return res
}

// ---- 失败聚合（按厂商 × 错误码，从调用日志现算） ----

// FailBucket 一个失败聚合桶。
type FailBucket struct {
	Vendor string `json:"vendor"`
	Reason string `json:"reason"`
	Count  int    `json:"count"`
	LastAt string `json:"last_at,omitempty"`
}

// failAggregateHandler GET /api/admin/fail-aggregate → 从调用日志按「厂商 × 错误码」现算。
//
// 与 /api/admin/fail-stats 的区别：fail-stats 是进程内存态（重启清零、只统计本进程
// 见过的失败）；这里直接读磁盘调用日志，跨重启都有数据。
func failAggregateHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		limit := 5000
		if s := r.URL.Query().Get("limit"); s != "" {
			if n, err := strconv.Atoi(s); err == nil && n > 0 && n <= 20000 {
				limit = n
			}
		}

		var records []CallRecord
		if callLog != nil {
			records = callLog.ReadAll()
		}
		// 只看尾部 limit 条
		if len(records) > limit {
			records = records[len(records)-limit:]
		}

		type key struct{ vendor, reason string }
		agg := map[key]*FailBucket{}
		total := 0
		for _, rec := range records {
			if rec.Status == "ok" || rec.Status == "" {
				continue
			}
			vendor := strings.TrimSpace(rec.Vendor)
			if vendor == "" {
				if prefix, _, ok := strings.Cut(rec.Model, "/"); ok && prefix != "" {
					vendor = prefix
				} else {
					vendor = "(unknown)"
				}
			}
			reason := failReasonForRecord(rec)
			k := key{vendor, reason}
			b := agg[k]
			if b == nil {
				b = &FailBucket{Vendor: vendor, Reason: reason}
				agg[k] = b
			}
			b.Count++
			if rec.TS > b.LastAt {
				b.LastAt = rec.TS
			}
			total++
		}

		out := make([]FailBucket, 0, len(agg))
		for _, b := range agg {
			out = append(out, *b)
		}
		sort.Slice(out, func(i, j int) bool {
			if out[i].Count != out[j].Count {
				return out[i].Count > out[j].Count
			}
			if out[i].Vendor != out[j].Vendor {
				return out[i].Vendor < out[j].Vendor
			}
			return out[i].Reason < out[j].Reason
		})

		writeJSONResp(w, map[string]any{
			"buckets":     out,
			"total_fails": total,
			"scanned":     len(records),
		})
	}
}

// failReasonForRecord 从调用记录判定错误类别（结构化字段优先）。
func failReasonForRecord(rec CallRecord) string {
	if cls := strings.TrimSpace(rec.ErrorClass); cls != "" {
		return cls
	}
	switch rec.UpstreamStatus {
	case 429:
		return "rate_limit"
	case 401, 403:
		return "auth"
	case 408, 504:
		return "timeout"
	}
	blob := strings.ToLower(rec.ErrMsg)
	switch {
	case strings.Contains(blob, "429"), strings.Contains(blob, "rate limit"):
		return "rate_limit"
	case strings.Contains(blob, "quota"), strings.Contains(blob, "额度"):
		return "quota"
	case strings.Contains(blob, "401"), strings.Contains(blob, "403"), strings.Contains(blob, "unauthorized"):
		return "auth"
	case strings.Contains(blob, "timeout"), strings.Contains(blob, "deadline"), strings.Contains(blob, "超时"):
		return "timeout"
	case strings.Contains(blob, "connect"), strings.Contains(blob, "dial"), strings.Contains(blob, "refused"):
		return "transport"
	case strings.Contains(blob, "503"), strings.Contains(blob, "502"), strings.Contains(blob, "upstream"):
		return "upstream"
	default:
		return "other"
	}
}
