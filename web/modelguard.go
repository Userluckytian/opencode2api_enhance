package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

// maxUpstreamBodyBytes 是转发到上游的请求体上限（16 MiB），防止无界读取耗尽内存。
const maxUpstreamBodyBytes int64 = 16 << 20

var (
	// errDuplicateModelKey 表示请求体里出现多个 model 键（含大小写变体）。
	// 这是 finding 546 的绕过手法：网关按一种规则解析、上游按另一种规则解析，
	// 从而「校验用已授权模型、转发用未授权模型」。
	errDuplicateModelKey = errors.New("duplicate model key")
	// errModelKeyCase 表示 model 键出现了大小写变体（Model / MODEL）。
	// Go 的 encoding/json 对字段名大小写不敏感，上游会把 "Model" 当成 model，
	// 而网关只认小写 "model"——两侧解析不一致就是绕过空间，故一律拒绝。
	errModelKeyCase = errors.New(`model key must be exactly "model"`)
	// errModelNotString 表示 model 不是字符串（数字 / 布尔 / 数组 / 对象）。
	errModelNotString = errors.New("model must be a string")
	// errTrailingJSON 表示 JSON 对象之后还有多余数据（拼接第二个 JSON 文档）。
	errTrailingJSON = errors.New("invalid trailing data after json body")
	// errInvalidJSONBody 表示请求体不是合法 JSON。
	errInvalidJSONBody = errors.New("invalid request body: not a valid json document")
	// errBodyNotObject 表示请求体顶层不是 JSON 对象。
	errBodyNotObject = errors.New("invalid request body: top-level value must be a json object")
	// errModelRequired 表示请求体里没有可用的 model（缺字段 / 空串 / null / 纯空白）。
	// 措辞刻意与上游自身的 "model is required" 文本区分开，便于确认 400 来自网关。
	errModelRequired = errors.New(`model parameter is missing or empty: specify a non-empty "model" field in the request body`)
)

// decodeTopLevelModel 严格解析请求体顶层的 model 字段。
//
// 与旧实现（json.Unmarshal 到匿名结构体）的区别：
//  1. 用 json.Decoder 逐 token 扫描，**显式检测重复键**——出现第二个 model
//     （包括 "Model"/"MODEL" 等大小写变体）直接报错，不再让「最后一个值生效」
//     这种解析歧义被利用来绕过白名单；
//  2. 非字符串类型的 model 直接拒绝（旧实现会把数字/对象静默解成空串）；
//  3. 拒绝对象结束后还有多余 JSON 数据（防止拼接第二个 JSON 文档）；
//  4. 【fail-closed 补齐·finding 546 残余缺口】请求体不是合法 JSON、顶层不是
//     对象、model 缺失 / 为 null / 为空串 / 为纯空白时，一律返回错误，由网关
//     400 拒绝，**不再放行转发**。
//
// 返回值 hasModel=true 表示确实读到了一个字符串类型的 model 字段。
func decodeTopLevelModel(raw []byte) (model string, hasModel bool, err error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	first, err := dec.Token()
	if err != nil {
		return "", false, errInvalidJSONBody
	}
	if d, ok := first.(json.Delim); !ok || d != '{' {
		return "", false, errBodyNotObject
	}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return "", false, errInvalidJSONBody
		}
		key, _ := kt.(string)
		var rawVal json.RawMessage
		if err := dec.Decode(&rawVal); err != nil {
			return "", false, errInvalidJSONBody
		}
		if !strings.EqualFold(key, "model") {
			continue
		}
		// 只接受规范的小写键名：OpenAI 协议里就是 model，大小写变体上游多半不认，
		// 留着只会制造「两端解析不一致」的空间。
		if key != "model" {
			return "", false, errModelKeyCase
		}
		if hasModel {
			return "", false, errDuplicateModelKey
		}
		trimmed := bytes.TrimSpace(rawVal)
		if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
			// null 视同「没有指定模型」：fail-closed 直接拒绝，不放行转发。
			// （json.Unmarshal 对 null 是 no-op，会静默得到空串，旧实现因此漏判。）
			return "", false, errModelRequired
		}
		var s string
		if err := json.Unmarshal(rawVal, &s); err != nil {
			return "", false, errModelNotString
		}
		model = s
		hasModel = true
	}
	if _, err := dec.Token(); err != nil { // 结束的 '}'
		return "", false, errInvalidJSONBody
	}
	if _, err := dec.Token(); err != io.EOF {
		return "", false, errTrailingJSON
	}
	return model, hasModel, nil
}

// resolveRequestedModel 严格解析请求体中的 model 字段，并把「解析不出有效 model」
// 收敛成唯一的失败原因（fail-closed）。
//
// 判定规则（任一不满足即返回错误，网关据此 400 拒绝，**不再转发上游**）：
//  1. 请求体必须是合法 JSON 且顶层是对象——否则无法保证「网关看到的 model」
//     与「上游看到的 model」是同一个值；
//  2. 必须存在且仅存在一个 "model" 键（重复键 / 大小写变体一律拒绝）；
//  3. model 必须是字符串，且去掉首尾空白后非空（null / 空串 / 纯空白都拒绝）。
//
// 返回的 model 已去掉首尾空白，可直接用于 per-Key 白名单比对。
func resolveRequestedModel(raw []byte) (string, error) {
	model, hasModel, err := decodeTopLevelModel(raw)
	if err != nil {
		return "", err
	}
	if !hasModel {
		return "", errModelRequired
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return "", errModelRequired
	}
	return model, nil
}

// modelAllowedForKey 判断该 Key 是否可以使用 model。
//
// Key 只绑定一个模型（k.Model）。当 Key 未绑定模型（历史数据为空）时，
// 退化为「必须在全局白名单 allowed_models 内」，且白名单为空时一律不放行
// （fail-closed：宁可拒绝也不要把白名单绕过留成默认路径）。
//
// 【fail-closed 补齐·finding 546 残余缺口】model 为空串（旧实现直接 return true，
// 相当于放行）现在一律返回 false：空模型永远不构成有效授权。
func modelAllowedForKey(k Key, allowed []string, model string) bool {
	if model == "" {
		return false
	}
	if k.Model != "" {
		return model == k.Model
	}
	return contains(allowed, model)
}

// keyModels 返回该 Key 实际可用的模型列表（用于 /v1/models 过滤）。
// 结果同时受 Key 自身绑定与全局白名单约束。
func keyModels(k Key, allowed []string) []string {
	if k.Model != "" {
		if len(allowed) == 0 || contains(allowed, k.Model) {
			return []string{k.Model}
		}
		return nil
	}
	return append([]string(nil), allowed...)
}

// writeModelsFor 是 /v1/models 的本地实现：只返回该 Key 有权使用的模型。
//
// 安全修复（finding 546）：旧实现把 GET /v1/models 直接反代到上游，导致
// 只被授权单个模型的 Key 也能拿到上游全部 19 个模型（含别名与提供方信息），
// 与 /api/models 公布的 4 个白名单模型不一致。现在完全不问上游，只回本地白名单。
func (a *App) writeModelsFor(w http.ResponseWriter, k Key) {
	a.Mu.RLock()
	allowed := append([]string(nil), a.Cfg.AllowedModels...)
	a.Mu.RUnlock()
	models := keyModels(k, allowed)
	data := make([]map[string]any, 0, len(models))
	for _, id := range models {
		data = append(data, map[string]any{
			"id":       id,
			"object":   "model",
			"created":  0,
			"owned_by": "token-buffet",
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

// isModelsPath 判断是否为 OpenAI 兼容的模型列表请求（容忍末尾斜杠与 query）。
func isModelsPath(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	p := strings.TrimRight(r.URL.Path, "/")
	return p == "/v1/models"
}

// isModelsPathAnyMethod 只看路径、不看方法，用于识别「非 GET/HEAD 的 /v1/models」。
//
// 旧实现里这类请求（如 POST /v1/models）会绕过本地白名单应答、被原样反代到
// 上游的 /v1/models，从而把上游全量模型列表泄露给受限 Key（finding 546 的
// 残余旁路），故单独用 405 拦掉。
func isModelsPathAnyMethod(r *http.Request) bool {
	return strings.TrimRight(r.URL.Path, "/") == "/v1/models"
}
