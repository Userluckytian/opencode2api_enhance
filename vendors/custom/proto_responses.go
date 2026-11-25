// OpenAI Responses 协议出站适配：OpenAI Chat 形态 ⇄ Responses API（POST {base}/responses）。
// 请求：system → instructions、消息 → input 项（output_text/input_image/function_call/
// function_call_output）。响应/SSE：转回 OpenAI Chat 形态（usage 与 finish_reason 映射）。
package custom

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/6Kmfi6HP/opencode2api/core/contract"
	"github.com/6Kmfi6HP/opencode2api/core/protocol"
)

// emptyFunctionCallOutput 空工具结果的占位文本：部分 Responses 上游
// （如 tokenrouter）拒绝空 output，会用 400 拒绝整个请求。
const emptyFunctionCallOutput = "(empty)"

// responsesProto 复用 openai 协议的模型目录拉取（Responses 与 Chat 共用 /models）与认证头。
type responsesProto struct {
	openaiProto
}

// ---------------------------------------------------------------------------
// 请求：OpenAI Chat → Responses
// ---------------------------------------------------------------------------

// openAIToResponsesRequest 把 OpenAI Chat 请求体转为 Responses API 请求。
func openAIToResponsesRequest(raw []byte) ([]byte, error) {
	var req map[string]any
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("bad openai body: %w", err)
	}
	out := map[string]any{"stream": false}
	if s, ok := req["stream"].(bool); ok {
		out["stream"] = s
	}
	if m, _ := req["model"].(string); m != "" {
		out["model"] = m
	}
	for _, k := range []string{"temperature", "top_p", "reasoning", "metadata"} {
		if v, ok := req[k]; ok {
			out[k] = v
		}
	}
	if n, ok := protocol.NumberAsFloat(req["max_tokens"]); ok && n > 0 {
		out["max_output_tokens"] = int(n)
	}

	var instructions []string
	msgs, _ := req["messages"].([]any)
	input := make([]any, 0, len(msgs))
	for _, rawMsg := range msgs {
		m, ok := rawMsg.(map[string]any)
		if !ok {
			continue
		}
		role, _ := m["role"].(string)
		switch role {
		case "system", "developer":
			if s := contentToText(m["content"]); s != "" {
				instructions = append(instructions, s)
			}
		case "user":
			input = append(input, map[string]any{
				"role":    "user",
				"content": openAIContentToResponsesParts(m["content"], "input_text", "input_image"),
			})
		case "assistant":
			if c := openAIContentToResponsesParts(m["content"], "output_text", "input_image"); len(c) > 0 {
				input = append(input, map[string]any{"role": "assistant", "content": c})
			}
			if tcs, ok := m["tool_calls"].([]any); ok {
				for _, rawTC := range tcs {
					tc, ok := rawTC.(map[string]any)
					if !ok {
						continue
					}
					fn, _ := tc["function"].(map[string]any)
					if fn == nil {
						continue
					}
					name, _ := fn["name"].(string)
					args, _ := fn["arguments"].(string)
					callID, _ := tc["id"].(string)
					if callID == "" {
						callID = "call_" + name
					}
					// 部分 Responses 上游（如 tokenrouter）要求历史 function_call 项必须带
					// 独立的 id；缺失时会以 json_parse_error/400 拒绝整个请求。
					itemID := callID
					if !strings.HasPrefix(itemID, "fc_") {
						itemID = "fc_" + itemID
					}
					input = append(input, map[string]any{
						"type": "function_call", "id": itemID, "call_id": callID,
						"name": name, "arguments": args,
					})
				}
			}
		case "tool":
			callID, _ := m["tool_call_id"].(string)
			output := contentToText(m["content"])
			if output == "" {
				// 用占位符保留 tool_call 配对关系，避免上游拒绝空 output。
				output = emptyFunctionCallOutput
			}
			input = append(input, map[string]any{
				"type": "function_call_output", "call_id": callID,
				"output": output,
			})
		}
	}
	if len(instructions) > 0 {
		out["instructions"] = strings.Join(instructions, "\n\n")
	}
	out["input"] = input

	// tools：Responses 扁平函数声明。
	if tools, ok := req["tools"].([]any); ok && len(tools) > 0 {
		rsTools := make([]map[string]any, 0, len(tools))
		for _, rawTool := range tools {
			t, ok := rawTool.(map[string]any)
			if !ok {
				continue
			}
			fn, _ := t["function"].(map[string]any)
			if fn == nil {
				continue
			}
			rt := map[string]any{"type": "function", "name": fn["name"]}
			if d, ok := fn["description"].(string); ok {
				rt["description"] = d
			}
			if p, ok := fn["parameters"]; ok {
				rt["parameters"] = protocol.CleanJSONSchema(p)
			}
			rsTools = append(rsTools, rt)
		}
		if len(rsTools) > 0 {
			out["tools"] = rsTools
			switch tc := req["tool_choice"].(type) {
			case string:
				if tc == "auto" || tc == "none" || tc == "required" {
					out["tool_choice"] = tc
				}
			case map[string]any:
				if fn, ok := tc["function"].(map[string]any); ok {
					if n, ok := fn["name"].(string); ok {
						out["tool_choice"] = map[string]any{"type": "function", "name": n}
					}
				}
			}
		}
	}
	return json.Marshal(out)
}

// openAIContentToResponsesParts OpenAI content → Responses content parts。
// textType 用户消息为 input_text、助手消息为 output_text。
func openAIContentToResponsesParts(content any, textType, imageType string) []map[string]any {
	switch c := content.(type) {
	case string:
		if c == "" {
			return nil
		}
		return []map[string]any{{"type": textType, "text": c}}
	case []any:
		out := make([]map[string]any, 0, len(c))
		for _, rawPart := range c {
			p, ok := rawPart.(map[string]any)
			if !ok {
				continue
			}
			switch t, _ := p["type"].(string); t {
			case "text", "input_text", "output_text":
				s, _ := p["text"].(string)
				out = append(out, map[string]any{"type": textType, "text": s})
			case "image_url", "input_image":
				var u string
				switch iu := p["image_url"].(type) {
				case string:
					u = iu
				case map[string]any:
					u, _ = iu["url"].(string)
				}
				if u != "" {
					out = append(out, map[string]any{"type": imageType, "image_url": u})
				}
			}
		}
		return out
	}
	return nil
}

// ---------------------------------------------------------------------------
// 响应：Responses → OpenAI
// ---------------------------------------------------------------------------

// responsesToOpenAIResponse 把 Responses 响应转为 OpenAI Chat 响应体。
// 解析失败返回 nil（调用方原样透传由上层报错）。
func responsesToOpenAIResponse(body []byte) []byte {
	var resp struct {
		ID     string `json:"id"`
		Model  string `json:"model"`
		Status string `json:"status"`
		Output []struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			CallID    string `json:"call_id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"output"`
		Usage             map[string]any `json:"usage"`
		IncompleteDetails *struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil
	}
	var text strings.Builder
	var toolCalls []map[string]any
	for _, item := range resp.Output {
		switch item.Type {
		case "message":
			for _, c := range item.Content {
				if c.Type == "output_text" || c.Type == "text" {
					text.WriteString(c.Text)
				}
			}
		case "function_call":
			toolCalls = append(toolCalls, map[string]any{
				"id":       item.CallID,
				"type":     "function",
				"function": map[string]any{"name": item.Name, "arguments": item.Arguments},
			})
		}
	}
	msg := map[string]any{"role": "assistant"}
	if text.Len() > 0 {
		msg["content"] = text.String()
	} else {
		msg["content"] = nil
	}
	if len(toolCalls) > 0 {
		msg["tool_calls"] = toolCalls
	}
	finish := "stop"
	if len(toolCalls) > 0 {
		finish = "tool_calls"
	} else if resp.Status == "incomplete" {
		finish = "length"
	}
	pt, _ := protocol.NumberAsFloat(resp.Usage["input_tokens"])
	ct, _ := protocol.NumberAsFloat(resp.Usage["output_tokens"])
	tt, _ := protocol.NumberAsFloat(resp.Usage["total_tokens"])
	if tt == 0 {
		tt = pt + ct
	}
	openai := map[string]any{
		"id":      resp.ID,
		"object":  "chat.completion",
		"created": nowUnix(),
		"model":   resp.Model,
		"choices": []map[string]any{{
			"index":         0,
			"message":       msg,
			"finish_reason": finish,
		}},
		"usage": map[string]any{
			"prompt_tokens":     int64(pt),
			"completion_tokens": int64(ct),
			"total_tokens":      int64(tt),
		},
	}
	out, err := json.Marshal(openai)
	if err != nil {
		return nil
	}
	return out
}

// ---------------------------------------------------------------------------
// Chat / ChatStream
// ---------------------------------------------------------------------------

func (p responsesProto) chat(ctx context.Context, v *Vendor, model, key string, rawBody []byte) (*contract.Reply, error) {
	rsBody, err := openAIToResponsesRequest(rawBody)
	if err != nil {
		return nil, fmt.Errorf("custom %s: %w", v.cfg.ID, err)
	}
	resp, addr, err := v.do(ctx, http.MethodPost, v.cfg.BaseURL+"/responses", p.headers(key, false), rsBody, false)
	if err != nil {
		return nil, err
	}
	body := readBody(resp)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		v.markErr(fmt.Sprintf("chat: HTTP %d: %s", resp.StatusCode, truncateErr(body)))
		return &contract.Reply{Body: body, Status: resp.StatusCode, NodeAddr: addr, Headers: resp.Header}, nil
	}
	converted := responsesToOpenAIResponse(body)
	if converted == nil {
		v.markErr("chat: bad responses body")
		return &contract.Reply{Body: body, Status: http.StatusBadGateway, NodeAddr: addr}, nil
	}
	v.markOK()
	return &contract.Reply{Body: converted, Status: resp.StatusCode, NodeAddr: addr}, nil
}

func (p responsesProto) chatStream(ctx context.Context, v *Vendor, model, key string, rawBody []byte) (*contract.Stream, error) {
	rsBody, err := openAIToResponsesRequest(rawBody)
	if err != nil {
		return nil, fmt.Errorf("custom %s: %w", v.cfg.ID, err)
	}
	resp, addr, err := v.do(ctx, http.MethodPost, v.cfg.BaseURL+"/responses", p.headers(key, true), rsBody, true)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body := readBody(resp)
		v.markErr(fmt.Sprintf("chat stream: HTTP %d: %s", resp.StatusCode, truncateErr(body)))
		return &contract.Stream{ReadCloser: nopCloser{bytes.NewReader(body)}, Status: resp.StatusCode, NodeAddr: addr}, nil
	}
	v.markOK()
	return &contract.Stream{ReadCloser: newResponsesStreamConverter(resp.Body), Status: resp.StatusCode, NodeAddr: addr}, nil
}

// ---------------------------------------------------------------------------
// 流：Responses SSE → OpenAI Chat SSE
// ---------------------------------------------------------------------------

// responsesToolState 一个 function_call 项的流内累积。
type responsesToolState struct {
	tcIndex int
	callID  string
	name    string
	args    strings.Builder
}

// responsesStreamState 流转换状态。
type responsesStreamState struct {
	cc        chunkCtx
	promptTok float64
	outputTok float64
	status    string
	hasTools  bool
	failed    string
	tools     map[string]*responsesToolState // output_index → 项
	toolOrder []string
}

// newResponsesStreamConverter 构造 Responses SSE → OpenAI chunk 转换器。
func newResponsesStreamConverter(rc io.ReadCloser) *sseConverter {
	st := &responsesStreamState{tools: map[string]*responsesToolState{}}
	transform := func(ev *sseEvent, w *bytes.Buffer) { st.transform(ev, w) }
	finish := func(w *bytes.Buffer) { st.finish(w) }
	return newSSEConverter(rc, transform, finish)
}

func (st *responsesStreamState) transform(ev *sseEvent, w *bytes.Buffer) {
	// Responses 事件 data 均为 {"type":"response.xxx",...}；优先按 data.type 分发
	// （兼容不带 event: 行的兼容端点）。
	var head struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(ev.data, &head)
	kind := head.Type
	if kind == "" {
		kind = ev.event
	}
	switch kind {
	case "response.created", "response.in_progress":
		var d struct {
			Response struct {
				ID    string         `json:"id"`
				Model string         `json:"model"`
				Usage map[string]any `json:"usage"`
			} `json:"response"`
		}
		if json.Unmarshal(ev.data, &d) == nil {
			if d.Response.ID != "" {
				st.cc = chunkCtx{id: d.Response.ID, model: d.Response.Model}
			}
			if n, ok := protocol.NumberAsFloat(d.Response.Usage["input_tokens"]); ok {
				st.promptTok = n
			}
		}
	case "response.output_item.added":
		var d struct {
			OutputIndex int `json:"output_index"`
			Item        struct {
				Type   string `json:"type"`
				CallID string `json:"call_id"`
				Name   string `json:"name"`
			} `json:"item"`
		}
		if json.Unmarshal(ev.data, &d) != nil || d.Item.Type != "function_call" {
			return
		}
		t := &responsesToolState{
			tcIndex: len(st.toolOrder),
			callID:  d.Item.CallID,
			name:    d.Item.Name,
		}
		st.tools[fmt.Sprintf("%d", d.OutputIndex)] = t
		st.toolOrder = append(st.toolOrder, fmt.Sprintf("%d", d.OutputIndex))
		st.hasTools = true
		writeChunk(w, &st.cc, map[string]any{
			"tool_calls": []map[string]any{{
				"index": t.tcIndex, "id": t.callID, "type": "function",
				"function": map[string]any{"name": t.name, "arguments": ""},
			}},
		}, nil)
	case "response.function_call_arguments.delta":
		var d struct {
			OutputIndex int    `json:"output_index"`
			Delta       string `json:"delta"`
		}
		if json.Unmarshal(ev.data, &d) != nil {
			return
		}
		if t, ok := st.tools[fmt.Sprintf("%d", d.OutputIndex)]; ok {
			t.args.WriteString(d.Delta)
		}
	case "response.output_text.delta":
		var d struct {
			Delta string `json:"delta"`
		}
		if json.Unmarshal(ev.data, &d) == nil && d.Delta != "" {
			writeChunk(w, &st.cc, map[string]any{"content": d.Delta}, nil)
		}
	case "response.reasoning_text.delta", "response.reasoning_summary_text.delta":
		var d struct {
			Delta string `json:"delta"`
		}
		if json.Unmarshal(ev.data, &d) == nil && d.Delta != "" {
			writeChunk(w, &st.cc, map[string]any{"reasoning_content": d.Delta}, nil)
		}
	case "response.completed":
		var d struct {
			Response struct {
				Status string         `json:"status"`
				Usage  map[string]any `json:"usage"`
			} `json:"response"`
		}
		if json.Unmarshal(ev.data, &d) == nil {
			st.status = d.Response.Status
			if n, ok := protocol.NumberAsFloat(d.Response.Usage["input_tokens"]); ok {
				st.promptTok = n
			}
			if n, ok := protocol.NumberAsFloat(d.Response.Usage["output_tokens"]); ok {
				st.outputTok = n
			}
		}
	case "response.failed":
		st.failed = "上游返回失败（response.failed）"
	}
}

// finish 流结束：补 tool 参数、finish chunk、usage chunk 与 [DONE]。
func (st *responsesStreamState) finish(w *bytes.Buffer) {
	if st.failed != "" {
		writeChunk(w, &st.cc, map[string]any{"content": st.failed}, nil)
	}
	for _, key := range st.toolOrder {
		t := st.tools[key]
		if t.args.Len() == 0 {
			continue
		}
		writeChunk(w, &st.cc, map[string]any{
			"tool_calls": []map[string]any{{
				"index": t.tcIndex, "type": "function",
				"function": map[string]any{"arguments": t.args.String()},
			}},
		}, nil)
	}
	finish := "stop"
	if st.hasTools {
		finish = "tool_calls"
	} else if st.status == "incomplete" {
		finish = "length"
	}
	writeChunk(w, &st.cc, map[string]any{}, &finish)
	writeUsageDone(w, &st.cc, st.promptTok, st.outputTok)
}
