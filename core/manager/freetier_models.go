package manager

// 免费层引擎的「可用模型」视图（v2.1）。
//
// 引擎自己在运行目录里维护三份文件，我们只读不写：
//  1. config.json.models.catalog.json  —— 完整模型目录（zen/go 两个通道的全部模型
//     + 每个模型的上下文/输出/推理/工具能力），是「全集」；
//  2. config.json.<hash>.availability.json —— 每个模型的最近一次探测结果
//     （reason: probe_succeeded / http_429 / http_403 …）与下次探测时间；
//  3. GET /v1/models —— 引擎当前**对外暴露**的模型（已按可用性过滤）。
//
// 三者合一才能回答用户真正关心的问题：
//   「引擎认识哪些模型」→ catalog
//   「现在哪些能用」→ /v1/models
//   「某个不能用的为什么、什么时候重试」→ availability

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// FreeTierModel 单个免费模型的展示视图。
type FreeTierModel struct {
	// ID 模型 ID（如 big-pickle）。
	ID string `json:"id"`
	// Available 当前是否对外暴露（在引擎 /v1/models 里 = true）。
	Available bool `json:"available"`
	// Channel 探测通道：anonymous（匿名免费层）/ zen（带 key）/ go（订阅）。
	Channel string `json:"channel,omitempty"`
	// Reason 最近一次探测结果：probe_succeeded / http_429 / http_403 / …
	Reason string `json:"reason,omitempty"`
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
	// CatalogSource / AvailabilitySource 数据文件路径（排障用；空 = 未找到）。
	CatalogSource      string `json:"catalog_source,omitempty"`
	AvailabilitySource string `json:"availability_source,omitempty"`
	// Error 拉取失败时的说明（部分失败仍返回已拿到的数据）。
	Error string `json:"error,omitempty"`
}

// availabilityEntry availability.json 里单个模型的记录（只声明我们用到的字段）。
type availabilityEntry struct {
	Model     string `json:"model"`
	Disabled  bool   `json:"disabled"`
	CheckedAt string `json:"checked_at"`
	NextCheck string `json:"next_check"`
	Reason    string `json:"reason"`
	Channel   string `json:"channel"`
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
	out := FreeTierModelsView{}
	dir := m.Paths().FreeTierEngineDir()

	// 1) 读完整目录（全集来源）。
	catalog, catPath := readEngineCatalog(dir)
	out.CatalogSource = catPath
	if catalog != nil {
		out.Total = len(catalog.Zen) + len(catalog.Go)
	}

	// 2) 拉 /v1/models（当前暴露 = 可用）。
	shown := map[string]FreeTierModel{}
	base := fmt.Sprintf("http://127.0.0.1:%d", m.managerGatewayPort())
	if data, err := fetchEngineModels(ctx, base, effectiveGatewayKey(m.loadConfig())); err != nil {
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
		reason := e.Reason
		// 若 /v1/models 里有它但探测结果是失败，说明「已恢复」——
		// 引擎的探测是周期性的，目录是实时的，两者短暂不一致属正常。
		if shown[id].Available && reason != "" && reason != "probe_succeeded" {
			reason = reason + "（已恢复）"
		}
		v.Reason = reason
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

	// 5. 排序：可用优先 → ID 升序
	list := make([]FreeTierModel, 0, len(merged))
	for _, v := range merged {
		list = append(list, v)
	}
	sort.Slice(list, func(i, j int) bool {
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
	}
	return out
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
