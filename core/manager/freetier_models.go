package manager

// 免费层引擎的「可用模型」视图（v2.1）。
//
// 引擎自己在运行目录里维护三份文件，我们只读不写：
//  1. config.json.models.catalog.json  —— 完整模型目录（zen/go 两个通道的全部模型
//     + 每个模型的上下文/输出/推理/工具能力），是「全集」；
//  2. config.json.<hash>.availability.json —— 每个模型的最近一次探测结果
//     （reason: probe_succeeded / http_429 / http_403 …）与下次探测时间；
//  3. GET /v1/models —— 引擎当前**对外暴露**的模型。
//
// 三者合一才能回答用户真正关心的问题：
//   「引擎认识哪些模型」→ catalog
//   「引擎愿意接哪些」→ /v1/models（注意：**不等于能用**）
//   「某个不能用的为什么、什么时候重试」→ availability
//
// v2.1 修正：早先把「在 /v1/models 里」当成「可用」，但引擎对 disabled=false
// 的模型一律照常列出（哪怕探测 429），于是面板显示「可用 + http_429（已恢复）」
// 而用户点探测仍 429。现在拆成 Available（暴露）与 ProbeOK（探测通过）两列。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// FreeTierModel 单个免费模型的展示视图。
type FreeTierModel struct {
	// ID 模型 ID（如 big-pickle）。
	ID string `json:"id"`
	// Free 引擎口径下是否免费（模型 ID 含 "free"，或元数据声明
	// input/output 成本均为 0 且未废弃）。见 pricing.go:Decide。
	Free bool `json:"free"`
	// Deprecated 元数据标记为已废弃/下线（免费但不再推荐）。
	Deprecated bool `json:"deprecated,omitempty"`
	// Available 当前是否对外暴露（在引擎 /v1/models 里 = true）。
	//
	// 注意：这只代表「引擎愿意接这一路请求」，**不代表上游真能给出结果**。
	// 引擎对 disabled=false 的模型一律照常列进 /v1/models（即使它探测 429），
	// 所以面板不能把它当成「可用」。判断真能用请看 ProbeOK。
	Available bool `json:"available"`
	// ProbeOK 最近一次**真实探测**是否成功（availability.reason == probe_succeeded）。
	// 这是唯一能说「现在能用」的依据。
	ProbeOK bool `json:"probe_ok"`
	// ProbeStatus 最近一次探测的 HTTP 状态码（0 = 从未探测）。
	ProbeStatus int `json:"probe_status,omitempty"`
	// Channel 探测通道：anonymous（匿名免费层）/ zen（带 key）/ go（订阅）。
	Channel string `json:"channel,omitempty"`
	// Reason 最近一次探测结果：probe_succeeded / http_429 / http_403 / …
	Reason string `json:"reason,omitempty"`
	// RetryAfterSeconds 上游 429 给的 Retry-After（秒）；0 = 未知。
	//
	// 免费额度耗尽时上游会带这个头（实测约 20 小时），它比「等一会儿再试」
	// 有用得多：用户据此能判断该等还是该换账号。
	RetryAfterSeconds int `json:"retry_after_seconds,omitempty"`
	// RetryAfterAt 换算后的重置时刻（RFC3339，空 = 未知）。
	RetryAfterAt string `json:"retry_after_at,omitempty"`
	// CheckedAt 最近一次探测时间（RFC3339，空 = 从未探测）。
	CheckedAt string `json:"checked_at,omitempty"`
	// NextCheck 下次计划探测时间（RFC3339，空 = unknown）。
	NextCheck string `json:"next_check,omitempty"`
	// ContextWindow 上下文档位（0 = 未提供）。
	ContextWindow int `json:"context_window,omitempty"`
	// MaxOutput 最大输出档位（0 = 未提供）。
	MaxOutput int `json:"max_output,omitempty"`
	// Reasoning / ToolCall / StructuredOutput 能力标记。
	Reasoning        bool `json:"reasoning,omitempty"`
	ToolCall         bool `json:"tool_call,omitempty"`
	StructuredOutput bool `json:"structured_output,omitempty"`
	// NativeProtocol 原生协议（chat / responses / anthropic；空 = 未知）。
	NativeProtocol string `json:"native_protocol,omitempty"`
}

// FreeTierModelsView 模型列表响应。
type FreeTierModelsView struct {
	// Models 全部已知模型（按可用优先、ID 升序排列）。
	Models []FreeTierModel `json:"models"`
	// Total 引擎目录里的模型总数（含不可用）。
	Total int `json:"total"`
	// Shown 对外暴露的数量。
	Shown int `json:"shown"`
	// FreeOnly 面板当前是否开启「只使用免费模型」（对应引擎 models.free_only）。
	FreeOnly bool `json:"free_only"`
	// FreeTotal 判定为免费的模型数（与 FreeOnly 无关，始终统计）。
	FreeTotal int `json:"free_total"`
	// FreeShown 免费且对外暴露的数量。
	FreeShown int `json:"free_shown"`
	// UsableShown 探测通过（ProbeOK）的数量 —— 真正能用的个数。
	//
	// 与 Shown 的区别就是「引擎列出来」vs「上游真给结果」。免费额度耗尽时
	// Shown 仍是 10、UsableShown 会掉到 4，这个差值本身就是最重要的排障信号。
	UsableShown int `json:"usable_shown"`
	// CatalogSource / AvailabilitySource 数据文件路径（排障用；空 = 未找到）。
	CatalogSource      string `json:"catalog_source,omitempty"`
	AvailabilitySource string `json:"availability_source,omitempty"`
	// Error 拉取失败时的说明（部分失败仍返回已拿到的数据）。
	Error string `json:"error,omitempty"`
}

// pricingCache 引擎的定价元数据缓存（`*.models.dev.json`），用于判定免费。
// 引擎自己每小时刷新它；我们只读，拿不到就退回「名字含 free」规则。
type pricingCache struct {
	UpdatedAt string                    `json:"updated_at"`
	Models    map[string]modelPriceInfo `json:"models"`
}

// modelPriceInfo 单个模型的定价（指针 = 元数据未声明该成本）。
type modelPriceInfo struct {
	ID         string   `json:"id"`
	InputCost  *float64 `json:"input_cost,omitempty"`
	OutputCost *float64 `json:"output_cost,omitempty"`
	Deprecated bool     `json:"deprecated"`
}

// availabilityEntry availability.json 里单个模型的记录（只声明我们用到的字段）。
type availabilityEntry struct {
	Model     string `json:"model"`
	Disabled  bool   `json:"disabled"`
	CheckedAt string `json:"checked_at"`
	NextCheck string `json:"next_check"`
	Reason    string `json:"reason"`
	Channel   string `json:"channel"`
	// RetryAfterSeconds 引擎在探测 429 时记下的上游 Retry-After（秒）。
	RetryAfterSeconds int `json:"retry_after_seconds"`
	// RetryAfterAt 引擎若直接写了重置时刻，优先用它。
	RetryAfterAt string `json:"retry_after_at"`
}

// catalogMeta 目录里单个模型的元数据。
type catalogMeta struct {
	ContextWindow    int  `json:"context_window"`
	MaxInput         int  `json:"max_input"`
	MaxOutput        int  `json:"max_output"`
	Reasoning        bool `json:"reasoning"`
	ToolCall         bool `json:"tool_call"`
	StructuredOutput bool `json:"structured_output"`
}

// engineCatalog 引擎的完整模型目录（只声明我们用到的字段）。
type engineCatalog struct {
	SchemaVersion   int                               `json:"schema_version"`
	UpdatedAt       string                            `json:"updated_at"`
	Zen             []string                          `json:"zen"`
	Go              []string                          `json:"go"`
	NativeProtocols map[string]map[string]string      `json:"native_protocols"`
	Metadata        map[string]map[string]catalogMeta `json:"metadata"`
}

// engineModelsPayload 引擎 /v1/models 的最小结构。
type engineModelsPayload struct {
	Data []struct {
		ID            string `json:"id"`
		ContextLength int    `json:"context_length"`
		ContextWindow int    `json:"context_window"`
		MaxOutput     int    `json:"max_output"`
		Reasoning     bool   `json:"reasoning"`
		ToolCall      bool   `json:"tool_call"`
	} `json:"data"`
}

// FreeTierModels 汇总「引擎完整目录」「对外暴露清单」「可用性探测结果」。
//
// 引擎未运行时仍返回 catalog（如果文件存在），只是 Available 全为 false
// 且 Error 说明引擎不可达——面板据此可以显示「引擎未启动，N 个模型待激活」。
func (m *Manager) FreeTierModels(ctx context.Context) FreeTierModelsView {
	cfg := m.loadConfig()
	out := FreeTierModelsView{FreeOnly: engineFreeOnly(cfg)}
	dir := m.Paths().FreeTierEngineDir()

	// 1) 读完整目录（全集来源）。
	catalog, catPath := readEngineCatalog(dir)
	out.CatalogSource = catPath
	if catalog != nil {
		out.Total = len(catalog.Zen) + len(catalog.Go)
	}

	// 1b) 读定价缓存（免费判定的第二来源；缺失时只按名字规则判）。
	pricing, _ := readEnginePricing(dir)

	// 2) 拉 /v1/models（当前暴露 = 可用）。
	shown := map[string]FreeTierModel{}
	base := fmt.Sprintf("http://127.0.0.1:%d", m.managerGatewayPort())
	if data, err := fetchEngineModels(ctx, base, effectiveGatewayKey(cfg)); err != nil {
		out.Error = err.Error()
	} else {
		for _, d := range data.Data {
			if d.ID == "" {
				continue
			}
			w := d.ContextLength
			if w == 0 {
				w = d.ContextWindow
			}
			shown[d.ID] = FreeTierModel{
				ID:            d.ID,
				Available:     true,
				ContextWindow: w,
				MaxOutput:     d.MaxOutput,
				Reasoning:     d.Reasoning,
				ToolCall:      d.ToolCall,
			}
		}
	}

	// 3) 读 availability（探测结果）。
	entries, availPath := readAvailabilityFile(dir)
	out.AvailabilitySource = availPath

	// 4) 合并：catalog 是全集骨架，availability 补探测结果，shown 定可用性。
	merged := map[string]FreeTierModel{}

	// 4a. catalog 里的 zen / go 模型（全集）
	if catalog != nil {
		for _, id := range catalog.Zen {
			merged[id] = FreeTierModel{ID: id, Channel: "zen"}
		}
		for _, id := range catalog.Go {
			merged[id] = FreeTierModel{ID: id, Channel: "go"}
		}
		// 元数据（上下文/输出/能力/原生协议）
		for ch, metas := range catalog.Metadata {
			for id, meta := range metas {
				v, ok := merged[id]
				if !ok {
					continue
				}
				v.ContextWindow = meta.ContextWindow
				if v.ContextWindow == 0 {
					v.ContextWindow = meta.MaxInput
				}
				v.MaxOutput = meta.MaxOutput
				v.Reasoning = meta.Reasoning
				v.ToolCall = meta.ToolCall
				v.StructuredOutput = meta.StructuredOutput
				if protos := catalog.NativeProtocols[ch]; protos != nil {
					v.NativeProtocol = protos[id]
				}
				merged[id] = v
			}
		}
	}

	// 4b. availability 补探测结果（channel 以探测通道为准，更贴近实际）
	//
	// 关键：**不要再把「引擎还列着它」解释成「已恢复」**。
	// v2.0 曾在这里给 reason 追加「（已恢复）」，依据只是 /v1/models 里有这个
	// ID。但引擎对 disabled=false 的模型一律照常列出（哪怕探测 429），于是
	// 面板会同时显示「可用 + http_429（已恢复）」，用户点探测却仍是 429 ——
	// 一个纯误导。现在把两件事分开表示：
	//   Available —— 引擎暴露（引擎愿意接这一路）
	//   ProbeOK   —— 探测真的成功（上游给出结果）
	// reason 原样保留，由前端决定怎么措辞。
	for id, e := range entries {
		v, ok := merged[id]
		if !ok {
			v = FreeTierModel{ID: id}
		}
		if e.Channel != "" {
			v.Channel = e.Channel
		}
		v.CheckedAt = e.CheckedAt
		v.NextCheck = e.NextCheck
		v.Reason = e.Reason
		v.ProbeOK = e.Reason == "probe_succeeded"
		v.ProbeStatus = probeStatusFromReason(e.Reason)
		// 先看引擎文件（它若写了就以它为准），再用管理面缓存兜底：
		// 引擎只写 reason=http_429、不写 Retry-After，所以「何时重置」几乎
		// 只能来自手动探测时接住的响应头。
		v.RetryAfterSeconds, v.RetryAfterAt = resolveRetryAfter(e)
		if v.RetryAfterAt == "" && v.RetryAfterSeconds == 0 {
			if sec, at := m.RetryAfterHint(id); at != "" {
				v.RetryAfterSeconds, v.RetryAfterAt = sec, at
			}
		}
		merged[id] = v
	}

	// 4c. /v1/models 有但 catalog 没有的（引擎新增了目录外的模型）→ 补进来
	for id, s := range shown {
		v, ok := merged[id]
		if !ok {
			v = FreeTierModel{ID: id}
		}
		v.Available = true
		if v.ContextWindow == 0 {
			v.ContextWindow = s.ContextWindow
		}
		if v.MaxOutput == 0 {
			v.MaxOutput = s.MaxOutput
		}
		v.Reasoning = v.Reasoning || s.Reasoning
		v.ToolCall = v.ToolCall || s.ToolCall
		merged[id] = v
	}

	// 4d. 免费标记（与引擎 free_only 判定同口径）
	for id, v := range merged {
		v.Free, v.Deprecated = engineIsFreeModel(id, pricing)
		merged[id] = v
	}

	// 5. 排序：免费优先 → 探测通过优先 → 引擎暴露优先 → ID 升序
	//
	// 为什么免费排最前：面板默认勾了「只使用免费模型」，用户打开卡片想看的就是
	// 那十几个免费模型。把付费的排在前面会让人以为「怎么全是收费的」。
	//
	// 为什么在 Available 之前加 ProbeOK：额度耗尽时 10 个模型全都「暴露」，
	// 按旧的排序真正能用的 4 个会散在中间，用户会以为一个都不能用。
	list := make([]FreeTierModel, 0, len(merged))
	for _, v := range merged {
		list = append(list, v)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Free != list[j].Free {
			return list[i].Free
		}
		if list[i].ProbeOK != list[j].ProbeOK {
			return list[i].ProbeOK
		}
		if list[i].Available != list[j].Available {
			return list[i].Available
		}
		return list[i].ID < list[j].ID
	})

	out.Models = list
	out.Total = len(list)
	for _, v := range list {
		if v.Available {
			out.Shown++
		}
		if v.ProbeOK {
			out.UsableShown++
		}
		if v.Free {
			out.FreeTotal++
			if v.Available {
				out.FreeShown++
			}
		}
	}
	return out
}

// probeStatusFromReason 从 availability 的 reason 反推 HTTP 状态码。
//
// 引擎把探测结果写成 http_429 / http_403 / probe_succeeded 这类字符串，
// 前端要显示「429」而不是「http_429」，所以在这里统一翻译一次。
// 未探测（reason 为空）返回 0。
func probeStatusFromReason(reason string) int {
	if reason == "" {
		return 0
	}
	if reason == "probe_succeeded" {
		return http.StatusOK
	}
	// 形如 http_<code>；也兼容引擎将来直接写数字。
	if i := strings.Index(reason, "_"); i >= 0 && i+1 < len(reason) {
		if n, err := strconv.Atoi(reason[i+1:]); err == nil {
			return n
		}
	}
	if n, err := strconv.Atoi(reason); err == nil {
		return n
	}
	return 0
}

// resolveRetryAfter 计算该模型的「额度重置时刻」。
//
// 两个来源，优先显式时刻：
//  1. availability 里引擎若写了 retry_after_at，直接用；
//  2. 否则用 retry_after_seconds 从 checked_at 起算。
//
// 为什么从 checked_at 而不是 now 起算：引擎每小时探测一次，写盘时上游给的
// 是「从那一刻起还要等 N 秒」。用 now 会把重置时间算得比实际晚越来越久，
// 一小时一次探测下来能漂出好几分钟，用户按这个时间去等就会白等。
func resolveRetryAfter(e availabilityEntry) (seconds int, at string) {
	if e.RetryAfterAt != "" {
		if t, err := time.Parse(time.RFC3339Nano, e.RetryAfterAt); err == nil {
			at = t.UTC().Format(time.RFC3339)
			if d := time.Until(t); d > 0 {
				return int(d.Seconds()), at
			}
			return 0, at
		}
	}
	if e.RetryAfterSeconds <= 0 {
		return 0, ""
	}
	base := time.Now()
	if e.CheckedAt != "" {
		if t, err := time.Parse(time.RFC3339Nano, e.CheckedAt); err == nil {
			base = t
		}
	}
	reset := base.Add(time.Duration(e.RetryAfterSeconds) * time.Second)
	remain := time.Until(reset)
	if remain <= 0 {
		return 0, reset.UTC().Format(time.RFC3339)
	}
	return int(remain.Seconds()), reset.UTC().Format(time.RFC3339)
}

// fetchEngineModels 调引擎 GET /v1/models。
func fetchEngineModels(ctx context.Context, base, apiKey string) (engineModelsPayload, error) {
	var out engineModelsPayload
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, base+"/v1/models", nil)
	if err != nil {
		return out, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return out, fmt.Errorf("引擎未响应（可能未启动）：%w", err)
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return out, fmt.Errorf("引擎 /v1/models 返回 %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return out, fmt.Errorf("解析引擎模型列表失败：%w", err)
	}
	return out, nil
}

// readEnginePricing 读引擎的定价缓存（`*.models.dev.json`）。
//
// 这是「免费」判定的第二来源：引擎 Decide() 的规则是
//   name_free       —— 模型 ID 含 "free"（大小写不敏感）
//   metadata_free   —— 元数据显示 input/output 成本均为 0 且未废弃
// 只靠名字会漏掉 big-pickle 这类「名字没 free 但成本为 0」的模型。
func readEnginePricing(engineDir string) (*pricingCache, string) {
	p := newestMatch(engineDir, "*models.dev.json")
	if p == "" {
		return nil, ""
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, p
	}
	var c pricingCache
	if json.Unmarshal(data, &c) != nil {
		return nil, p
	}
	return &c, p
}

// engineIsFreeModel 复刻引擎的免费判定（internal/models/pricing.go:Decide）。
//
// 与引擎保持同一口径很重要：面板显示「免费」、引擎按 free_only 过滤，两边
// 若判定不一致，用户会看到「勾了只使用免费模型，列表却少了/多了几个」。
func engineIsFreeModel(id string, price *pricingCache) (free, deprecated bool) {
	nameFree := strings.Contains(strings.ToLower(id), "free")
	if price == nil {
		// 定价缓存缺失（引擎首次启动尚未拉取）：只按名字规则判，
		// 与引擎 metadata_pending 时的 fallback 一致。
		return nameFree, false
	}
	info, ok := price.Models[id]
	if !ok {
		return nameFree, false
	}
	metadataFree := !info.Deprecated && info.InputCost != nil && info.OutputCost != nil &&
		*info.InputCost == 0 && *info.OutputCost == 0
	return nameFree || metadataFree, info.Deprecated
}

// readEngineCatalog 读引擎的完整模型目录文件（`*.models.catalog.json`）。
func readEngineCatalog(engineDir string) (*engineCatalog, string) {
	p := newestMatch(engineDir, "*models.catalog.json")
	if p == "" {
		return nil, ""
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, p
	}
	var c engineCatalog
	if json.Unmarshal(data, &c) != nil {
		return nil, p
	}
	return &c, p
}

// readAvailabilityFile 读引擎写的可用性探测结果文件（`*availability.json`）。
func readAvailabilityFile(engineDir string) (map[string]availabilityEntry, string) {
	p := newestMatch(engineDir, "*availability.json")
	if p == "" {
		return nil, ""
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, p
	}
	var raw map[string]availabilityEntry
	if json.Unmarshal(data, &raw) != nil {
		return nil, p
	}
	// 兜底：条目内 Model 字段为空时用 map key 补。
	for id, e := range raw {
		if strings.TrimSpace(e.Model) == "" {
			e.Model = id
			raw[id] = e
		}
	}
	return raw, p
}

// newestMatch 在目录里按 glob 找最新的一个文件（引擎重启可能换文件名 hash）。
func newestMatch(dir, pattern string) string {
	matches, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil || len(matches) == 0 {
		return ""
	}
	var newest string
	var newestMod time.Time
	for _, p := range matches {
		if fi, err := os.Stat(p); err == nil && fi.ModTime().After(newestMod) {
			newest, newestMod = p, fi.ModTime()
		}
	}
	return newest
}
