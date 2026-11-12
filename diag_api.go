package main

// 诊断面（v2.1）：四服务自检 + 引擎实时 + 失败聚合 + 手动模型探测。
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
	"sync"
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

	// 3b) Kilo 独立网关（30080）：阶段 L。无鉴权 /healthz 只回 4 字段，
	// 按配置停用时算「通过（按配置）」而非失败，避免误报。
	rep.Checks = append(rep.Checks, probeKilo(ctx, view.KiloEnabled, view.KiloPortEffective))

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

// probeKilo 检查 Kilo 独立网关（30080）的 /healthz。
// 停用时不算失败：按配置停用是合法状态；启用才要求 200 且 status=ready。
func probeKilo(ctx context.Context, enabled bool, port uint16) (chk DiagCheck) {
	chk = DiagCheck{Name: "kilo", Target: fmt.Sprintf("127.0.0.1:%d", port)}
	start := time.Now()
	defer func() { chk.DurationMS = time.Since(start).Milliseconds() }()
	if !enabled {
		chk.OK = true
		chk.Detail = "已停用（按配置）"
		return chk
	}

	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, "http://"+chk.Target+"/healthz", nil)
	if err != nil {
		chk.Detail = err.Error()
		return chk
	}
	resp, err := diagHTTPClient.Do(req)
	if err != nil {
		chk.Detail = "Kilo 网关未响应（未启动或端口被占）"
		return chk
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		chk.Detail = fmt.Sprintf("HTTP %d", resp.StatusCode)
		return chk
	}
	var hz struct {
		Status      string `json:"status"`
		ModelsShown int    `json:"models_shown"`
		MayTrain    bool   `json:"may_train"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&hz); err != nil {
		chk.Detail = "响应不是合法 JSON"
		return chk
	}
	chk.OK = hz.Status == "ready"
	chk.Detail = fmt.Sprintf("status=%s models_shown=%d may_train=%v", hz.Status, hz.ModelsShown, hz.MayTrain)
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
	// RetryAfterSeconds 上游 429 带的 Retry-After（秒）；0 = 没有该头。
	//
	// 免费额度耗尽时上游会明确告诉你要等多久（实测 ~20h）。把它抛到界面，
	// 用户才知道「该等」还是「该换账号」，否则只能反复点探测。
	RetryAfterSeconds int `json:"retry_after_seconds,omitempty"`
	// RetryAfterAt 换算出的重置时刻（RFC3339，空 = 未知）。
	RetryAfterAt string `json:"retry_after_at,omitempty"`
}

// modelProbeHandler POST /api/admin/free-tier/probe
//
// 对引擎发起一次最小 chat 请求（max_tokens=32），用真实状态码判定模型是否可用。
//
// 为什么不是更小的值：有些模型（如 crouton-alpha-free）对 max_output_tokens
// 有下限要求（上游报 max_output_tokens must be at least 16），用 8 会被直接
// 400 拒绝 —— 那是**探测参数**的问题、不是模型不可用，会在调用日志里留下
// 一批假的「上游 400」记录，误导排障。32 高于已知下限又不浪费额度。
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
			// 全量探测跟随「只使用免费模型」：开了 free_only 时引擎本来就不会
			// 暴露付费模型，此时挨个去探付费模型既浪费上游配额、又会得到一堆
			// 无意义的失败（引擎按 free_only 直接拒绝），对排障没有帮助。
			for _, it := range view.Models {
				if !it.Available {
					continue
				}
				if view.FreeOnly && !it.Free {
					continue
				}
				targets = append(targets, it.ID)
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
		// 并发探测：每个模型是一次真实 chat 请求，单模型可能耗时数秒；串行
		// 跑 20 个会把「全量探测」变成等两三分钟。这里限并发 6，既不压垮
		// 引擎（免费层对并发敏感）又把总耗时压到最长的那一个。
		results := make([]ModelProbeResult, len(targets))
		const probeConcurrency = 6
		sem := make(chan struct{}, probeConcurrency)
		var wg sync.WaitGroup
		for i, id := range targets {
			wg.Add(1)
			sem <- struct{}{}
			go func(idx int, modelID string) {
				defer wg.Done()
				defer func() { <-sem }()
				results[idx] = probeOneModel(r.Context(), base, st.APIKey, modelID)
				// 把 429 的重置提示记下来，供模型列表显示「额度何时重置」。
				// 引擎只写 reason=http_429，不写 Retry-After，所以这里是唯一
				// 能拿到它的地方。
				if r2 := results[idx]; r2.RetryAfterSeconds > 0 {
					m.NoteRetryAfter(modelID, r2.RetryAfterSeconds)
				}
			}(i, id)
		}
		wg.Wait()
		writeJSONResp(w, map[string]any{"results": results})
	}
}

// probeOneModel 对单个模型发一次最小请求。
func probeOneModel(ctx context.Context, base, apiKey, model string) ModelProbeResult {
	res := ModelProbeResult{Model: model}
	body, _ := json.Marshal(map[string]any{
		"model":      model,
		"max_tokens": 32,
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
	// 429 时把上游的「还要等多久」带出来。引擎会把上游的 Retry-After 原样
	// 透传（实测 /v1/chat/completions 429 响应带 Retry-After: 72815）。
	if ra := strings.TrimSpace(resp.Header.Get("Retry-After")); ra != "" {
		if sec, err := strconv.Atoi(ra); err == nil && sec > 0 {
			res.RetryAfterSeconds = sec
			res.RetryAfterAt = time.Now().Add(time.Duration(sec) * time.Second).UTC().Format(time.RFC3339)
		}
	}
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

// failAggregateHandler GET /api/admin/fail-aggregate → 按「厂商 × 错误类别」现算失败分布。
//
// 与 /api/admin/fail-stats 的区别：fail-stats 是进程内存态（重启清零、只统计本进程
// 见过的失败）；这里直接读磁盘日志，跨重启都有数据。
//
// 两个来源合并（与调用日志页保持同一口径）：
//   - 本进程 callLog（供应商服务/管理面写盘的 call_log.jsonl）；
//   - manager.ReadCallLog：供应商服务 JSONL + OpenCode 引擎 stdout 事件。
//
// 只有合并引擎侧，失败分布才反映真实流量 —— 免费层推理全在引擎进程里，
// 管理面自己几乎不产生失败记录。
func failAggregateHandler(m *manager.Manager) http.HandlerFunc {
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

		type key struct{ vendor, reason string }
		agg := map[key]*FailBucket{}
		total := 0
		scanned := 0
		bump := func(vendor, reason, ts string) {
			vendor = strings.TrimSpace(vendor)
			if vendor == "" {
				vendor = "(unknown)"
			}
			k := key{vendor, reason}
			b := agg[k]
			if b == nil {
				b = &FailBucket{Vendor: vendor, Reason: reason}
				agg[k] = b
			}
			b.Count++
			if ts > b.LastAt {
				b.LastAt = ts
			}
			total++
		}
		// 来源一：本进程内存 callLog（启动时已从磁盘加载历史）。
		var records []CallRecord
		if callLog != nil {
			records = callLog.ReadAll()
		}
		if len(records) > limit {
			records = records[len(records)-limit:]
		}
		scanned += len(records)
		for _, rec := range records {
			if rec.Status == "ok" || rec.Status == "" {
				continue
			}
			bump(failVendorOf(rec), failReasonOf(rec), rec.TS)
		}

		// 来源二：磁盘侧（供应商服务 JSONL + OpenCode 引擎 stdout 事件）。
		// 记录类型是 manager.CallLogRecord，与 main.CallRecord 不同，需用配套的判定函数。
		if m != nil {
			extra := m.ReadCallLog(limit)
			scanned += len(extra)
			for _, rec := range extra {
				if rec.Status == "ok" || rec.Status == "" {
					continue
				}
				bump(failLogVendorOf(rec), failLogReasonOf(rec), rec.TS)
			}
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
			"scanned":     scanned,
		})
	}
}

// failLogVendorOf 取磁盘日志记录的厂商归属（优先 Vendor，回退模型前缀）。
func failLogVendorOf(rec manager.CallLogRecord) string {
	if v := strings.TrimSpace(rec.Vendor); v != "" {
		return v
	}
	if prefix, _, ok := strings.Cut(rec.Model, "/"); ok && prefix != "" {
		return prefix
	}
	if strings.TrimSpace(rec.Model) != "" {
		return "(unknown)"
	}
	return ""
}

// failLogReasonOf 从磁盘日志记录判定错误类别（结构化字段优先，再回退文本匹配）。
//
// 引擎侧记录由 calllog_engine.go 映射，ErrorClass 已归一为同一套取值；
// 供应商服务侧可能只有 err_msg，故保留文本兜底。
func failLogReasonOf(rec manager.CallLogRecord) string {
	switch cls := strings.TrimSpace(rec.ErrorClass); cls {
	case FailReasonRateLimit, FailReasonQuota, FailReasonAuth,
		FailReasonUpstream, FailReasonTransport, FailReasonProtocol,
		FailReasonTimeout, FailReasonOther:
		return cls
	}
	// 状态码兜底（旧记录或上游未给结构化错误时）。
	switch rec.UpstreamStatus {
	case 429:
		return FailReasonRateLimit
	case 402:
		return FailReasonQuota
	case 401, 403:
		return FailReasonAuth
	case 408, 499, 504:
		return FailReasonTimeout
	}
	var sb strings.Builder
	sb.WriteString(strings.ToLower(rec.ErrMsg))
	for _, ev := range rec.Events {
		sb.WriteByte(' ')
		sb.WriteString(strings.ToLower(ev.Type))
		sb.WriteByte(' ')
		sb.WriteString(strings.ToLower(ev.Detail))
	}
	blob := sb.String()
	switch {
	case strings.Contains(blob, "429"), strings.Contains(blob, "rate limit"):
		return FailReasonRateLimit
	case strings.Contains(blob, "quota"), strings.Contains(blob, "额度"):
		return FailReasonQuota
	case strings.Contains(blob, "401"), strings.Contains(blob, "403"), strings.Contains(blob, "unauthorized"):
		return FailReasonAuth
	case strings.Contains(blob, "timeout"), strings.Contains(blob, "deadline"), strings.Contains(blob, "超时"):
		return FailReasonTimeout
	case strings.Contains(blob, "connect"), strings.Contains(blob, "dial"), strings.Contains(blob, "refused"):
		return FailReasonTransport
	case strings.Contains(blob, "503"), strings.Contains(blob, "502"), strings.Contains(blob, "upstream"):
		return FailReasonUpstream
	default:
		return FailReasonOther
	}
}
