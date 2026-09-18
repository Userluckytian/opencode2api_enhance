package opencode

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"time"
)

// readAllCloser 读尽并关闭流（聚合路径用；读取失败时返回已读部分，由聚合层决定是否透传原体）。
func readAllCloser(rc io.ReadCloser) []byte {
	defer rc.Close()
	b, _ := io.ReadAll(rc)
	return b
}

// isSSEBody 判断上游响应体是否为 SSE 事件流。
// 免费通道强制 stream:true 后，下游非流式请求也会收到 SSE，需要在厂商内聚合。
func isSSEBody(b []byte) bool {
	trimmed := bytes.TrimSpace(b)
	return bytes.HasPrefix(trimmed, []byte("data:")) || bytes.Contains(b, []byte("\ndata:"))
}

// aggregateChatSSE 把 chat.completion.chunk 事件流聚合成非流式 chat.completion 响应体。
// 聚合字段：content / reasoning / reasoning_content（字符串或分段数组）、tool_calls（按 index 合并
// arguments）、finish_reason、usage；任一事件都没解析出来时原样返回（保证错误体透传）。
func aggregateChatSSE(raw []byte, modelID string) []byte {
	var (
		id               string
		created          float64
		model            string
		role             = "assistant"
		content          strings.Builder
		reasoning        strings.Builder
		reasoningContent strings.Builder
		finish           string
		usage            map[string]any
		toolCalls        []map[string]any
		toolIndex        = map[float64]int{}
		sawChunk         bool
	)

	for _, line := range bytes.Split(raw, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(line[len("data:"):])
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}
		var chunk map[string]any
		if json.Unmarshal(payload, &chunk) != nil {
			continue
		}
		sawChunk = true
		if id == "" {
			id, _ = chunk["id"].(string)
		}
		if created == 0 {
			created, _ = chunk["created"].(float64)
		}
		if model == "" {
			model, _ = chunk["model"].(string)
		}
		if u, ok := chunk["usage"].(map[string]any); ok {
			usage = u
		}
		choices, _ := chunk["choices"].([]any)
		for _, c := range choices {
			choice, ok := c.(map[string]any)
			if !ok {
				continue
			}
			if fr, _ := choice["finish_reason"].(string); fr != "" {
				finish = fr
			}
			delta, _ := choice["delta"].(map[string]any)
			if delta == nil {
				// 少数上游不按 delta 给增量而直接给 message（含 Anthropic 风格转换后的形态）
				delta, _ = choice["message"].(map[string]any)
			}
			if delta == nil {
				continue
			}
			if r, _ := delta["role"].(string); r != "" {
				role = r
			}
			appendDeltaText(&content, delta["content"])
			appendDeltaText(&reasoning, delta["reasoning"])
			appendDeltaText(&reasoningContent, delta["reasoning_content"])
			mergeToolCallDeltas(delta["tool_calls"], &toolCalls, toolIndex)
		}
	}
	if !sawChunk {
		return raw
	}
	if id == "" {
		id = "chatcmpl-" + randomString(20)
	}
	if created == 0 {
		created = float64(time.Now().Unix())
	}
	if model == "" {
		model = modelID
	}
	if finish == "" {
		finish = "stop"
	}

	message := map[string]any{"role": role}
	if content.Len() > 0 {
		message["content"] = content.String()
	} else {
		message["content"] = nil
	}
	if reasoning.Len() > 0 {
		message["reasoning"] = reasoning.String()
	}
	if reasoningContent.Len() > 0 {
		message["reasoning_content"] = reasoningContent.String()
	}
	if len(toolCalls) > 0 {
		message["tool_calls"] = toolCalls
	}

	resp := map[string]any{
		"id":      id,
		"object":  "chat.completion",
		"created": created,
		"model":   model,
		"choices": []any{map[string]any{
			"index":         0,
			"message":       message,
			"finish_reason": finish,
		}},
	}
	if usage != nil {
		resp["usage"] = usage
	}
	out, err := json.Marshal(resp)
	if err != nil {
		return raw
	}
	return out
}

// appendDeltaText 累加增量文本：兼容字符串与分段数组（[{"type":"text","text":...}]）两种形态。
func appendDeltaText(b *strings.Builder, v any) {
	switch t := v.(type) {
	case string:
		b.WriteString(t)
	case []any:
		for _, part := range t {
			pm, ok := part.(map[string]any)
			if !ok {
				continue
			}
			if s, ok := pm["text"].(string); ok {
				b.WriteString(s)
			}
		}
	}
}

// mergeToolCallDeltas 按 index 合并 tool_calls 增量（id/name 取首次出现值，arguments 逐段拼接）。
// index 缺失时按出现顺序追加。
func mergeToolCallDeltas(v any, out *[]map[string]any, index map[float64]int) {
	arr, ok := v.([]any)
	if !ok {
		return
	}
	for order, item := range arr {
		tm, ok := item.(map[string]any)
		if !ok {
			continue
		}
		idx := float64(order)
		if n, ok := tm["index"].(float64); ok {
			idx = n
		}
		pos, exists := index[idx]
		if !exists {
			*out = append(*out, map[string]any{"type": "function", "function": map[string]any{}})
			pos = len(*out) - 1
			index[idx] = pos
		}
		call := (*out)[pos]
		if id, ok := tm["id"].(string); ok && id != "" {
			call["id"] = id
		}
		if typ, ok := tm["type"].(string); ok && typ != "" {
			call["type"] = typ
		}
		fn, _ := call["function"].(map[string]any)
		deltaFn, _ := tm["function"].(map[string]any)
		if deltaFn == nil {
			continue
		}
		if name, ok := deltaFn["name"].(string); ok && name != "" {
			fn["name"] = name
		}
		if args, ok := deltaFn["arguments"].(string); ok && args != "" {
			prev, _ := fn["arguments"].(string)
			fn["arguments"] = prev + args
		}
	}
}
