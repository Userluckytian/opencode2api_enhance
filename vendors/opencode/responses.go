// Responses 端点支持：opencode 上游把 Muse Spark（contributor-free / contributor）只挂在
// OpenAI Responses 协议（/zen/v1/responses 或 /zen/go/v1/responses）下，chat/completions 对其恒 500。
// 契约层下游统一消费「OpenAI chat.completion」（JSON/SSE），故此处把 Responses 请求/响应在厂商内
// 翻译回 chat 形态，下游 handler / 竞速 / 断点续写零改动（详见 docs/issue-log/2026-09-09.md #1）。
package opencode

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	zenResponsesURL   = "https://opencode.ai/zen/v1/responses"
	zenGoResponsesURL = "https://opencode.ai/zen/go/v1/responses"
)

// isResponsesModelID 判定模型是否只能走 Responses API。
// 名单照 9router open-sse/executors/opencode.js：muse-spark 系列（名字前缀匹配即可覆盖上游后续变体）。
func isResponsesModelID(modelID string) bool {
	id := strings.ToLower(strings.TrimSpace(modelID))
	return strings.HasPrefix(id, "muse-spark") || strings.HasPrefix(id, "muse_spark")
}

// responsesEndpoint 返回模型应使用的 responses 端点。仅对 muse-spark 系列生效；
// 其它模型返回 ok=false，走原 chat/completions 路径（逐字节不变）。
func (v *Vendor) responsesEndpoint(modelID string, a authT) (string, bool) {
	if !isResponsesModelID(modelID) {
		return "", false
	}
	if a.useGoEndpoint(v, modelID) {
		return zenGoResponsesURL, true
	}
	return zenResponsesURL, true
}

// responsesInputFromMessages 把 OpenAI chat messages 压成 Responses input items。
// 文本消息按角色选 part 类型（assistant→output_text，其余→input_text）；
// assistant 的 tool_calls 拆成 function_call items，tool 结果消息映射为 function_call_output。
// 纯图片/空文本消息丢弃（muse 无视觉）。
func responsesInputFromMessages(messages any) []any {
	msgs, ok := messages.([]any)
	if !ok {
		return nil
	}
	out := make([]any, 0, len(msgs))
	for _, m := range msgs {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		role, _ := mm["role"].(string)
		switch role {
		case "user", "system", "developer":
			text := messageText(mm["content"])
			if text == "" {
				continue
			}
			out = append(out, map[string]any{
				"role":    role,
				"content": []any{map[string]any{"type": "input_text", "text": text}},
			})
		case "assistant":
			text := messageText(mm["content"])
			if text != "" {
				out = append(out, map[string]any{
					"role":    "assistant",
					"content": []any{map[string]any{"type": "output_text", "text": text}},
				})
			}
			// 上一轮 assistant 的工具调用：拆成 function_call items（Responses 输入用）。
			if tcs, ok := mm["tool_calls"].([]any); ok {
				for _, tc := range tcs {
					tcm, ok := tc.(map[string]any)
					if !ok {
						continue
					}
					fn, _ := tcm["function"].(map[string]any)
					if fn == nil {
						continue
					}
					name, _ := fn["name"].(string)
					if name == "" {
						continue
					}
					out = append(out, map[string]any{
						"type":      "function_call",
						"call_id":   strVal(tcm["id"]),
						"name":      name,
						"arguments": anyToJSONString(fn["arguments"]),
					})
				}
			}
		case "tool":
			// 工具执行结果：Responses 用 function_call_output item。
			output := messageText(mm["content"])
			if output == "" {
				output = "(tool returned empty)"
			}
			out = append(out, map[string]any{
				"type":    "function_call_output",
				"call_id": strVal(mm["tool_call_id"]),
				"output":  output,
			})
		default:
			continue
		}
	}
	return out
}

// strVal 容错取 string（nil/non-string 返回空）。
func strVal(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// anyToJSONString 把 tool_call.arguments（OpenAI chat 用 JSON 字符串；个别客户端可能给对象）归一成 JSON 字符串。
func anyToJSONString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if v == nil {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// mapChatTools 把 OpenAI chat 工具定义（{"type":"function","function":{...}}）转成
// Responses 工具（{"type":"function","name","description","parameters"}）。
func mapChatTools(tools any) []any {
	arr, ok := tools.([]any)
	if !ok {
		return nil
	}
	out := make([]any, 0, len(arr))
	for _, t := range arr {
		tm, ok := t.(map[string]any)
		if !ok {
			continue
		}
		fn, _ := tm["function"].(map[string]any)
		if fn == nil {
			// 已经是 Responses 形态（name 顶层）则直接透传
			if name, _ := tm["name"].(string); name != "" {
				out = append(out, tm)
			}
			continue
		}
		rt := map[string]any{"type": "function"}
		if name, _ := fn["name"].(string); name != "" {
			rt["name"] = name
		}
		if desc, _ := fn["description"].(string); desc != "" {
			rt["description"] = desc
		}
		if p, ok := fn["parameters"]; ok && p != nil {
			rt["parameters"] = p
		}
		out = append(out, rt)
	}
	return out
}

// messageText 从 content（string 或 text/input_text/output_text part 数组）提取纯文本。
func messageText(content any) string {
	switch c := content.(type) {
	case string:
		return c
	case []any:
		var b strings.Builder
		for _, part := range c {
			pm, ok := part.(map[string]any)
			if !ok {
				continue
			}
			switch pm["type"] {
			case "text", "input_text", "output_text":
				if t, ok := pm["text"].(string); ok {
					b.WriteString(t)
				}
			}
		}
		return b.String()
	case map[string]any:
		if t, ok := c["text"].(string); ok {
			return t
		}
	}
	return ""
}

// responsesEffort 把 chat 侧 reasoning_effort 收敛到 Responses 支持的值；未知值不传（保持上游默认）。
func responsesEffort(effort string) string {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "low", "medium", "high":
		return strings.ToLower(strings.TrimSpace(effort))
	default:
		return ""
	}
}

// buildResponsesRequest 构造 Responses API 请求（chat 请求体 → Responses 协议）。
// 与 buildRequest 共用会话/认证头；Accept 按流式切换为 text/event-stream。
func (v *Vendor) buildResponsesRequest(upstreamURL, modelID string, bodyMap map[string]any, streaming bool, a authT) (*http.Request, error) {
	rb := map[string]any{
		"model":  modelID,
		"stream": streaming || a.mode == authPublic, // 免费通道 body 门禁强制流式
		"input":  responsesInputFromMessages(bodyMap["messages"]),
	}
	for _, k := range []string{"temperature", "top_p"} {
		if val, ok := bodyMap[k]; ok {
			rb[k] = val
		}
	}
	if max, ok := bodyMap["max_tokens"]; ok {
		rb["max_output_tokens"] = max
	} else if max, ok := bodyMap["max_completion_tokens"]; ok {
		rb["max_output_tokens"] = max
	}
	if effort, _ := bodyMap["reasoning_effort"].(string); effort != "" {
		if e := responsesEffort(effort); e != "" {
			rb["reasoning"] = map[string]any{"effort": e, "summary": "auto"}
		}
	}
	// 工具（agent loop 关键）：chat 工具/工具选择转成 Responses 形态透传，
	// muse 免费模型在上游真实支持 function calling，漏传会导致模型只口头应答不调用。
	// 上游限制：muse 的 responses 端点仅支持 tool_choice:"auto"——"none"/"required"/
	// 指定函数名都会 400。auto 透传；none 近似为"本轮不调工具"（干脆不下发工具定义）；
	// required/指定函数无法表达 → 省略 tool_choice，让上游按 auto 决策（多数场景仍会调用）。
	var responsesTools []any
	if tools, ok := bodyMap["tools"]; ok {
		responsesTools = mapChatTools(tools)
	}
	if tc, ok := bodyMap["tool_choice"]; ok {
		if s, isStr := tc.(string); isStr {
			switch s {
			case "auto":
				rb["tool_choice"] = "auto"
			case "none":
				// 上游 muse 不支持 tool_choice:none，近似为「本轮不调工具」：
				// 只保留门禁占位工具（否则免费通道 403），其余客户端工具不下发。
				responsesTools = filterGateTools(responsesTools)
			}
		}
		// 对象形态（指定函数）或其它值：不进 switch，走默认（有工具+无 tool_choice=auto）
	}
	if len(responsesTools) > 0 {
		rb["tools"] = responsesTools
	}
	tryBody, err := json.Marshal(rb)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest("POST", upstreamURL, bytes.NewReader(tryBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", a.authHeader())
	req.Header.Set("User-Agent", fmt.Sprintf("opencode/%s", v.ocClientVer))
	req.Header.Set("x-opencode-client", "cli")
	req.Header.Set("x-opencode-project", v.ocProjectID)
	req.Header.Set("x-opencode-session", v.ocSessionID)
	req.Header.Set("x-opencode-request", "msg_"+canonicalID())
	if streaming {
		req.Header.Set("Accept", "text/event-stream")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	return req, nil
}

// ---------------------------------------------------------------- 非流式翻译

// translateResponsesJSON 把 Responses 非流式 JSON 翻译成 OpenAI chat.completion JSON。
// 非 Responses 结构（object != "response"）原样返回，由调用方走既有转换链。
func translateResponsesJSON(body []byte, modelID string) []byte {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return body
	}
	if obj, _ := raw["object"].(string); obj != "response" {
		return body
	}

	var textB strings.Builder
	var toolCalls []any
	if out, ok := raw["output"].([]any); ok {
		for _, o := range out {
			om, ok := o.(map[string]any)
			if !ok {
				continue
			}
			typ, _ := om["type"].(string)
			switch typ {
			case "message":
				if parts, ok := om["content"].([]any); ok {
					for _, p := range parts {
						pm, ok := p.(map[string]any)
						if !ok {
							continue
						}
						if pt, _ := pm["type"].(string); pt != "output_text" && pt != "text" {
							continue
						}
						if t, ok := pm["text"].(string); ok {
							textB.WriteString(t)
						}
					}
				}
			case "function_call":
				fn := map[string]any{
					"name":      strVal(om["name"]),
					"arguments": anyToJSONString(om["arguments"]),
				}
				toolCalls = append(toolCalls, map[string]any{
					"id":       strVal(om["call_id"]),
					"type":     "function",
					"function": fn,
				})
			}
		}
	}

	// 有工具调用时按 chat 惯例给 tool_calls 终结（模型要调工具，不是要结束回合）。
	finish := "stop"
	if len(toolCalls) > 0 {
		finish = "tool_calls"
	} else if inc, ok := raw["incomplete_details"].(map[string]any); ok {
		if reason, _ := inc["reason"].(string); reason != "" {
			finish = responsesFinishReason(reason)
		}
	}
	created := time.Now().Unix()
	if ca, ok := raw["created_at"].(float64); ok && ca > 0 {
		created = int64(ca)
	}
	id, _ := raw["id"].(string)
	if id == "" {
		id = "chatcmpl-" + randomString(20)
	}

	msg := map[string]any{"role": "assistant"}
	if textB.Len() > 0 {
		msg["content"] = textB.String()
	}
	if len(toolCalls) > 0 {
		msg["tool_calls"] = toolCalls
	}
	chat := map[string]any{
		"id":      id,
		"object":  "chat.completion",
		"created": created,
		"model":   modelID,
		"choices": []any{
			map[string]any{
				"index":         0,
				"message":       msg,
				"finish_reason": finish,
			},
		},
	}
	if u, ok := raw["usage"].(map[string]any); ok {
		if cu := responsesUsageToChat(u); len(cu) > 0 {
			chat["usage"] = cu
		}
	}
	out, _ := json.Marshal(chat)
	return out
}

func responsesFinishReason(reason string) string {
	switch reason {
	case "max_output_tokens", "length":
		return "length"
	case "content_filter":
		return "content_filter"
	case "stop", "completed":
		return "stop"
	default:
		return reason
	}
}

// responsesUsageToChat 把 Responses usage 映射成 chat usage（含 reasoning_tokens 明细）。
func responsesUsageToChat(u map[string]any) map[string]any {
	num := func(k string) float64 {
		switch v := u[k].(type) {
		case float64:
			return v
		case int:
			return float64(v)
		case int64:
			return float64(v)
		}
		return 0
	}
	in := num("input_tokens")
	out := num("output_tokens")
	// output_tokens 可能是对象（分项），此时取 text 子项为完成数。
	if _, isObj := u["output_tokens"].(map[string]any); isObj {
		if m := u["output_tokens"].(map[string]any); m != nil {
			if t, ok := m["text"].(float64); ok {
				out = t
			}
		}
	}
	total := num("total_tokens")
	if total == 0 {
		total = in + out
	}
	cu := map[string]any{
		"prompt_tokens":     int64(in),
		"completion_tokens": int64(out),
		"total_tokens":      int64(total),
	}
	if det, ok := u["output_tokens_details"].(map[string]any); ok {
		if r, ok := det["reasoning_tokens"].(float64); ok && r > 0 {
			cu["completion_tokens_details"] = map[string]any{"reasoning_tokens": int64(r)}
		}
	}
	if det, ok := u["input_tokens_details"].(map[string]any); ok {
		if c, ok := det["cached_tokens"].(float64); ok && c > 0 {
			cu["prompt_tokens_details"] = map[string]any{"cached_tokens": int64(c)}
		}
	}
	return cu
}

// ---------------------------------------------------------------- 流式翻译

// wrapResponsesSSE 把 Responses 流式 SSE 实时翻译成 OpenAI chat.completion.chunk SSE。
// 内部用 io.Pipe + goroutine：读上游事件 → 写下游 chunk（逐条写入保证首字/续写时序可见）。
// Close 幂等：关闭上游体并终止 goroutine。
func (v *Vendor) wrapResponsesSSE(raw io.ReadCloser, modelID string) io.ReadCloser {
	pr, pw := io.Pipe()
	t := &responsesSSEToChatSSE{pr: pr, pw: pw, raw: raw, modelID: modelID}
	go t.run()
	return t
}

type responsesSSEToChatSSE struct {
	pr   *io.PipeReader
	pw   *io.PipeWriter
	raw  io.ReadCloser
	once sync.Once
	// 由 run 持有，供 Close 唤醒等待写入的 goroutine
	modelID string
}

func (t *responsesSSEToChatSSE) Read(p []byte) (int, error) { return t.pr.Read(p) }

func (t *responsesSSEToChatSSE) Close() error {
	t.once.Do(func() {
		t.pr.Close()
		t.raw.Close()
	})
	return nil
}

// run 消费上游 Responses SSE，向 pw 写 OpenAI chat chunk SSE。
func (t *responsesSSEToChatSSE) run() {
	defer t.pw.Close()
	defer t.raw.Close()
	scanner := bufio.NewScanner(t.raw)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	var event string
	id := ""
	var created int64
	contentStarted := false
	finished := false
	// 工具调用状态：按上游 output_index 记录我们分配的 tool_calls 下标，
	// function_call_arguments.delta 用同一 output_index 续传参数增量。
	toolIdxByOut := map[float64]int{}
	toolCount := 0

	emit := func(obj map[string]any) bool {
		b, err := json.Marshal(obj)
		if err != nil {
			return false
		}
		if _, err := t.pw.Write(append(append([]byte("data: "), b...), '\n', '\n')); err != nil {
			return false
		}
		return true
	}
	chunk := func(delta map[string]any, finish string) map[string]any {
		choice := map[string]any{"index": 0, "delta": delta}
		if finish != "" {
			choice["finish_reason"] = finish
		}
		return map[string]any{
			"id":      id,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   modelIDForChunk(t.modelID),
			"choices": []any{choice},
		}
	}

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "event:") {
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		var obj map[string]any
		if json.Unmarshal([]byte(data), &obj) != nil {
			continue
		}
		ev := event
		if ev == "" {
			if typ, ok := obj["type"].(string); ok {
				ev = typ
			}
		}
		if finished {
			// completed 之后的残余事件（ping 等）直接忽略
			continue
		}
		switch ev {
		case "response.created":
			if r, ok := obj["response"].(map[string]any); ok {
				id, _ = r["id"].(string)
				if id == "" {
					id = "chatcmpl-" + randomString(20)
				}
				if ca, ok := r["created_at"].(float64); ok {
					created = int64(ca)
				}
			}
			if created == 0 {
				created = time.Now().Unix()
			}
		case "response.output_text.delta":
			txt, _ := obj["delta"].(string)
			if txt == "" {
				continue
			}
			delta := map[string]any{"content": txt}
			if !contentStarted {
				contentStarted = true
				delta["role"] = "assistant"
			}
			if !emit(chunk(delta, "")) {
				return
			}
		case "response.output_item.added":
			// function_call 输出项：发出 chat tool_calls 头 chunk（index/name/arguments 空壳），
			// 后续 function_call_arguments.delta 续传参数。
			item, _ := obj["item"].(map[string]any)
			if item == nil {
				continue
			}
			if typ, _ := item["type"].(string); typ == "function_call" {
				name := strVal(item["name"])
				if name == "" {
					continue
				}
				idx := toolCount
				toolCount++
				if oi, ok := obj["output_index"].(float64); ok {
					toolIdxByOut[oi] = idx
				}
				tc := []any{map[string]any{
					"index":    idx,
					"id":       strVal(item["call_id"]),
					"type":     "function",
					"function": map[string]any{"name": name, "arguments": ""},
				}}
				if !emit(chunk(map[string]any{"tool_calls": tc}, "")) {
					return
				}
			}
		case "response.function_call_arguments.delta":
			idx := toolCount - 1
			if oi, ok := obj["output_index"].(float64); ok {
				if v, ok := toolIdxByOut[oi]; ok {
					idx = v
				}
			}
			arg, _ := obj["delta"].(string)
			if arg == "" {
				continue
			}
			tc := []any{map[string]any{"index": idx, "function": map[string]any{"arguments": arg}}}
			if !emit(chunk(map[string]any{"tool_calls": tc}, "")) {
				return
			}
		case "response.completed":
			finish := "stop"
			var usage map[string]any
			if r, ok := obj["response"].(map[string]any); ok {
				if status, _ := r["status"].(string); status == "incomplete" {
					finish = "length"
				}
				if inc, ok := r["incomplete_details"].(map[string]any); ok {
					if reason, _ := inc["reason"].(string); reason != "" {
						finish = responsesFinishReason(reason)
					}
				}
				usage, _ = r["usage"].(map[string]any)
			}
			if toolCount > 0 {
				finish = "tool_calls"
			}
			if !emit(chunk(map[string]any{}, finish)) {
				return
			}
			if len(usage) > 0 {
				if cu := responsesUsageToChat(usage); len(cu) > 0 {
					uObj := map[string]any{
						"id":      id,
						"object":  "chat.completion.chunk",
						"created": created,
						"model":   modelIDForChunk(t.modelID),
						"choices": []any{},
						"usage":   cu,
					}
					if !emit(uObj) {
						return
					}
				}
			}
			if _, err := t.pw.Write([]byte("data: [DONE]\n\n")); err != nil {
				return
			}
			finished = true
			return
		case "error":
			// 上游错误事件：原样透传 data 行，交由下游既有错误判定逻辑处理，随后 EOF。
			if _, err := t.pw.Write([]byte("data: " + data + "\n\n")); err != nil {
				return
			}
		default:
			// reasoning / ping / response.in_progress / output_item.* / content_part.* 均忽略：
			// muse 的 reasoning 正文为 encrypted_content，无明文可透。
		}
	}
	// 上游 EOF 且未收到 response.completed：不伪造 [DONE]，下游按「EOF without DONE」走既有续写/中断逻辑。
}

// modelIDForChunk 兜底 modelID 为空时也保证 chunk 含 model 字段。
func modelIDForChunk(m string) string {
	if m == "" {
		return "opencode"
	}
	return m
}
