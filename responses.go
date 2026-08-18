// Part of the P1 (core split) refactor: code moved out of main.go.
// Same package (main) - do not change package clause manually.
package main

import (
	"bufio"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/6Kmfi6HP/opencode2api/core/protocol"
)

func responsesInputToMessages(input any, instructions string) []Message {
	var messages []Message
	if instructions != "" {
		messages = append(messages, Message{Role: "system", Content: instructions})
	}
	switch v := input.(type) {
	case string:
		messages = append(messages, Message{Role: "user", Content: v})
	case []any:
		functionOutputs := collectFunctionOutputs(v)
		for _, item := range v {
			switch elem := item.(type) {
			case string:
				messages = append(messages, Message{Role: "user", Content: elem})
			case map[string]any:
				itemType, _ := elem["type"].(string)
				switch itemType {
				case "function_call", "tool_call", "apply_patch_call", "shell_call":
					callID, _ := elem["call_id"].(string)
					if callID == "" {
						callID, _ = elem["id"].(string)
					}
					name, _ := elem["name"].(string)
					if name == "" {
						switch itemType {
						case "apply_patch_call":
							name = "apply_patch"
						case "shell_call":
							name = "shell"
						}
					}
					args, _ := elem["arguments"].(string)
					if name == "" {
						if tu, ok := elem["tool_use"].(map[string]any); ok {
							name, _ = tu["name"].(string)
							callID, _ = tu["id"].(string)
							if a, ok := tu["arguments"].(string); ok {
								args = a
							} else if inp, ok := tu["input"]; ok {
								b, _ := json.Marshal(inp)
								args = string(b)
							}
						}
					}
					if args == "" {
						args = buildBuiltInToolCallArguments(itemType, elem)
					}
					if args == "" {
						args = "{}"
					}
					messages = append(messages, Message{
						Role:    "assistant",
						Content: "",
						ToolCalls: []ToolCall{{
							ID:   callID,
							Type: "function",
							Function: FunctionCall{
								Name:      name,
								Arguments: args,
							},
						}},
					})
					if callID != "" {
						output := functionOutputs[callID]
						if output == "" {
							output = "[tool output missing]"
						}
						messages = append(messages, Message{Role: "tool", ToolCallID: callID, Content: output})
					}
				case "function_call_output", "tool_result", "apply_patch_call_output", "shell_call_output":
					callID, _ := elem["call_id"].(string)
					if callID == "" {
						callID, _ = elem["tool_use_id"].(string)
					}
					if callID != "" {
						output := functionOutputs[callID]
						if output == "" {
							switch o := elem["output"].(type) {
							case string:
								output = o
							default:
								if o != nil {
									b, _ := json.Marshal(o)
									output = string(b)
								}
							}
						}
						if output == "" {
							b, err := json.Marshal(elem)
							if err == nil {
								output = string(b)
							}
						}
						if output == "" {
							output = "[tool output missing]"
						}
						messages = append(messages, Message{Role: "tool", ToolCallID: callID, Content: output})
					}
					continue
				case "reasoning":
					if text := extractTextFromContentParts(elem["summary"]); text != "" {
						messages = append(messages, Message{Role: "assistant", Content: "", ReasoningContent: &text})
					}
					continue
				case "message", "":
					role := "user"
					if r, ok := elem["role"].(string); ok && r != "" {
						role = r
					}
					if role == "developer" {
						role = "system"
					}
					content := responsesContentToMessageContent(elem["content"])
					messages = append(messages, Message{Role: role, Content: content})
				default:
					role := "user"
					if r, ok := elem["role"].(string); ok && r != "" {
						role = r
					}
					content := responsesContentToMessageContent(elem["content"])
					emptyContent := false
					switch v := content.(type) {
					case nil:
						emptyContent = true
					case string:
						emptyContent = v == ""
					case []any:
						emptyContent = len(v) == 0
					}
					if emptyContent {
						b, err := json.Marshal(elem)
						if err != nil {
							continue
						}
						content = string(b)
					}
					messages = append(messages, Message{Role: role, Content: content})
				}
			default:
				b, _ := json.Marshal(elem)
				messages = append(messages, Message{Role: "user", Content: string(b)})
			}
		}
	default:
		b, _ := json.Marshal(v)
		messages = append(messages, Message{Role: "user", Content: string(b)})
	}
	return messages
}

func convertResponsesTools(tools []ResponsesTool) []Tool {
	converted := make([]Tool, 0, len(tools))
	for _, tool := range tools {
		fn, ok := responsesToolFunction(tool)
		if !ok {
			continue
		}
		converted = append(converted, Tool{Type: "function", Function: fn})
	}
	return converted
}

func responsesToolFunction(tool ResponsesTool) (ToolFunction, bool) {
	switch tool.Type {
	case "function":
		fn := ToolFunction{
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  tool.Parameters,
		}
		if tool.Function != nil {
			fn = *tool.Function
		}
		if fn.Parameters == nil {
			fn.Parameters = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		return fn, true
	case "apply_patch":
		return ToolFunction{
			Name:        "apply_patch",
			Description: "Create, update, or delete files using a structured patch operation or unified diff.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"input": map[string]any{
						"type":        "string",
						"description": "Patch diff or patch instructions to apply.",
					},
					"operation": map[string]any{
						"type":        "object",
						"description": "Structured patch operation, including file action and diff payload.",
					},
				},
			},
		}, true
	case "shell":
		return ToolFunction{
			Name:        "shell",
			Description: "Run a shell command in the local workspace and return stdout, stderr, and exit details.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command": map[string]any{
						"type":        "string",
						"description": "Shell command to execute.",
					},
					"timeout_ms": map[string]any{
						"type":        "integer",
						"description": "Optional timeout in milliseconds.",
					},
					"working_directory": map[string]any{
						"type":        "string",
						"description": "Optional working directory for the command.",
					},
					"max_output_tokens": map[string]any{
						"type":        "integer",
						"description": "Optional output budget hint.",
					},
				},
				"required": []string{"command"},
			},
		}, true
	default:
		return ToolFunction{}, false
	}
}

func responsesToolName(tool ResponsesTool) string {
	switch tool.Type {
	case "function":
		if tool.Function != nil && tool.Function.Name != "" {
			return tool.Function.Name
		}
		return tool.Name
	case "apply_patch":
		return "apply_patch"
	case "shell":
		return "shell"
	default:
		return ""
	}
}

func responsesToolKindMap(tools []ResponsesTool) map[string]string {
	kinds := make(map[string]string, len(tools))
	for _, tool := range tools {
		name := responsesToolName(tool)
		if name == "" {
			continue
		}
		kinds[name] = tool.Type
	}
	return kinds
}

func toolCallOutputType(name string, kinds map[string]string) string {
	switch kinds[name] {
	case "apply_patch":
		return "apply_patch_call"
	case "shell":
		return "shell_call"
	default:
		return "function_call"
	}
}

func convertResponsesToolChoice(choice any) any {
	if choice == nil {
		return nil
	}
	choiceMap, ok := choice.(map[string]any)
	if !ok {
		return choice
	}
	if choiceMap["type"] == "function" {
		if name, ok := choiceMap["name"].(string); ok && name != "" {
			return map[string]any{
				"type":     "function",
				"function": map[string]any{"name": name},
			}
		}
	}
	if choiceType, ok := choiceMap["type"].(string); ok {
		switch choiceType {
		case "apply_patch", "shell":
			return map[string]any{
				"type":     "function",
				"function": map[string]any{"name": choiceType},
			}
		}
	}
	return choice
}

func collectFunctionOutputs(items []any) map[string]string {
	outputs := map[string]string{}
	for _, item := range items {
		elem, ok := item.(map[string]any)
		if !ok {
			continue
		}
		itemType, _ := elem["type"].(string)
		switch itemType {
		case "function_call_output", "apply_patch_call_output", "shell_call_output":
		default:
			continue
		}
		callID, _ := elem["call_id"].(string)
		if callID == "" {
			continue
		}
		switch v := elem["output"].(type) {
		case string:
			outputs[callID] = v
		default:
			b, _ := json.Marshal(v)
			outputs[callID] = string(b)
		}
	}
	return outputs
}

func parseJSONString(input string) any {
	var parsed any
	if input == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(input), &parsed); err != nil {
		return nil
	}
	return parsed
}

func buildBuiltInToolCallArguments(itemType string, elem map[string]any) string {
	if arguments, ok := elem["arguments"].(string); ok && arguments != "" {
		return arguments
	}

	payload := map[string]any{}
	switch itemType {
	case "apply_patch_call":
		if input, ok := elem["input"].(string); ok && input != "" {
			payload["input"] = input
		}
		if operation, ok := elem["operation"]; ok && operation != nil {
			payload["operation"] = operation
		}
	case "shell_call":
		for _, key := range []string{"command", "timeout_ms", "working_directory", "max_output_tokens"} {
			if value, ok := elem[key]; ok && value != nil {
				payload[key] = value
			}
		}
	}
	if len(payload) == 0 {
		payload = elem
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "{}"
	}
	return string(encoded)
}

func buildResponseToolCallItem(tc ToolCall, outputType string) map[string]any {
	switch outputType {
	case "apply_patch_call":
		item := map[string]any{
			"id":      "apc_" + tc.ID,
			"type":    outputType,
			"status":  "completed",
			"call_id": tc.ID,
		}
		if parsed, ok := parseJSONString(tc.Function.Arguments).(map[string]any); ok {
			for key, value := range parsed {
				item[key] = value
			}
		} else if tc.Function.Arguments != "" {
			item["arguments"] = tc.Function.Arguments
		}
		return item
	case "shell_call":
		item := map[string]any{
			"id":      "shc_" + tc.ID,
			"type":    outputType,
			"status":  "completed",
			"call_id": tc.ID,
		}
		if parsed, ok := parseJSONString(tc.Function.Arguments).(map[string]any); ok {
			for key, value := range parsed {
				item[key] = value
			}
		} else if tc.Function.Arguments != "" {
			item["arguments"] = tc.Function.Arguments
		}
		return item
	default:
		return map[string]any{
			"id":        "fc_" + tc.ID,
			"type":      "function_call",
			"status":    "completed",
			"arguments": tc.Function.Arguments,
			"call_id":   tc.ID,
			"name":      tc.Function.Name,
		}
	}
}

// L5（CONC-10）：storedResponses 有界化——常数上限 + TTL + 插入序淘汰。
const (
	maxStoredResponses = 2000              // 最大缓存条数（超限淘汰最旧）
	storedResponseTTL  = time.Hour         // 条目有效期（超期惰性清理）
	storedPurgeInterval = time.Minute      // 批量清理检查节流（避免每次写都全表扫）
)

// storedResponseEntry 缓存条目：状态 + 插入序（淘汰最旧用）+ 过期时间。
type storedResponseEntry struct {
	state     StoredResponseState
	seq       uint64
	expiresAt time.Time
}

func storeResponseState(response map[string]any, req ResponsesAPIRequest) {
	if req.Store != nil && !*req.Store {
		return
	}
	responseID, _ := response["id"].(string)
	if responseID == "" {
		return
	}
	output, _ := response["output"].([]any)
	storedResponsesMu.Lock()
	defer storedResponsesMu.Unlock()
	now := time.Now()
	// L5：写入前惰性清理过期条目（节流），控制内存有界。
	storedPurgeIfDueLocked(now)
	storedSeq++
	storedResponses[responseID] = storedResponseEntry{
		state: StoredResponseState{
			Model:        req.Model,
			Instructions: req.Instructions,
			Tools:        protocol.CloneJSONValue(req.Tools),
			ToolChoice:   protocol.CloneJSONValue(req.ToolChoice),
			Output:       protocol.CloneJSONValue(output),
		},
		seq:       storedSeq,
		expiresAt: now.Add(storedResponseTTL),
	}
	// L5：超限淘汰插入序最旧的条目。
	if len(storedResponses) > maxStoredResponses {
		storedEvictOldestLocked()
	}
}

// storedPurgeIfDueLocked 惰性清理过期条目（节流；调用方持写锁）。
func storedPurgeIfDueLocked(now time.Time) {
	if now.Sub(storedLastPurge) < storedPurgeInterval {
		return
	}
	storedLastPurge = now
	for id, e := range storedResponses {
		if now.After(e.expiresAt) {
			delete(storedResponses, id)
		}
	}
}

// storedEvictOldestLocked 淘汰插入序最旧的条目（调用方持写锁）。
func storedEvictOldestLocked() {
	var oldestID string
	var oldestSeq uint64
	for id, e := range storedResponses {
		if oldestID == "" || e.seq < oldestSeq {
			oldestID = id
			oldestSeq = e.seq
		}
	}
	if oldestID != "" {
		delete(storedResponses, oldestID)
	}
}

func loadResponseState(responseID string) (StoredResponseState, bool) {
	storedResponsesMu.Lock()
	defer storedResponsesMu.Unlock()
	now := time.Now()
	// L5：读取路径惰性清理过期条目（节流）。
	storedPurgeIfDueLocked(now)
	entry, ok := storedResponses[responseID]
	if !ok {
		return StoredResponseState{}, false
	}
	if now.After(entry.expiresAt) {
		// 单条确定性过期：命中但超期 → 删除并返回 miss。
		delete(storedResponses, responseID)
		return StoredResponseState{}, false
	}
	return protocol.CloneJSONValue(entry.state), true
}

func extractTextFromContentParts(content any) string {
	parts, ok := content.([]any)
	if !ok {
		if s, ok := content.(string); ok {
			return s
		}
		return ""
	}
	var texts []string
	for _, p := range parts {
		if part, ok := p.(map[string]any); ok {
			if part["type"] == "input_text" || part["type"] == "output_text" {
				if t, ok := part["text"].(string); ok {
					texts = append(texts, t)
				}
			}
		}
	}
	return strings.Join(texts, "\n")
}

func convertResponsesContentPart(part map[string]any) (map[string]any, bool) {
	partType, _ := part["type"].(string)
	switch partType {
	case "input_text", "output_text", "text":
		text, _ := part["text"].(string)
		if text == "" {
			return nil, false
		}
		return map[string]any{
			"type": "text",
			"text": text,
		}, true
	case "input_image":
		imageURL, _ := part["image_url"].(string)
		if imageURL == "" {
			return nil, false
		}
		imageURLValue := map[string]any{
			"url": imageURL,
		}
		if detail, ok := part["detail"].(string); ok && detail != "" {
			imageURLValue["detail"] = detail
		}
		return map[string]any{
			"type":      "image_url",
			"image_url": imageURLValue,
		}, true
	default:
		return nil, false
	}
}

func responsesContentToMessageContent(content any) any {
	if content == nil {
		return nil
	}
	if s, ok := content.(string); ok {
		return s
	}

	parts, ok := content.([]any)
	if !ok {
		b, err := json.Marshal(content)
		if err != nil {
			return nil
		}
		return string(b)
	}

	convertedParts := make([]any, 0, len(parts))
	texts := make([]string, 0, len(parts))
	onlyTextParts := true

	for _, rawPart := range parts {
		part, ok := rawPart.(map[string]any)
		if !ok {
			continue
		}
		convertedPart, ok := convertResponsesContentPart(part)
		if !ok {
			text := extractTextFromContentParts([]any{part})
			if text == "" {
				b, err := json.Marshal(part)
				if err != nil {
					continue
				}
				text = string(b)
			}
			convertedParts = append(convertedParts, map[string]any{
				"type": "text",
				"text": text,
			})
			texts = append(texts, text)
			continue
		}

		if convertedPart["type"] != "text" {
			onlyTextParts = false
		}
		if text, ok := convertedPart["text"].(string); ok && text != "" {
			texts = append(texts, text)
		}
		convertedParts = append(convertedParts, convertedPart)
	}

	if len(convertedParts) == 0 {
		return ""
	}
	if onlyTextParts {
		return strings.Join(texts, "\n")
	}
	return convertedParts
}

func chatContentToResponsesContent(content any) ([]any, string) {
	switch v := content.(type) {
	case nil:
		return nil, ""
	case string:
		if v == "" {
			return nil, ""
		}
		return []any{map[string]any{
			"type":        "output_text",
			"text":        v,
			"annotations": []any{},
			"logprobs":    []any{},
		}}, v
	case []any:
		parts := make([]any, 0, len(v))
		texts := make([]string, 0, len(v))
		for _, rawPart := range v {
			part, ok := rawPart.(map[string]any)
			if !ok {
				continue
			}
			partType, _ := part["type"].(string)
			switch partType {
			case "text", "input_text", "output_text":
				text, _ := part["text"].(string)
				if text == "" {
					continue
				}
				annotations, ok := part["annotations"]
				if !ok {
					annotations = []any{}
				}
				logprobs, ok := part["logprobs"]
				if !ok {
					logprobs = []any{}
				}
				texts = append(texts, text)
				parts = append(parts, map[string]any{
					"type":        "output_text",
					"text":        text,
					"annotations": annotations,
					"logprobs":    logprobs,
				})
			}
		}
		return parts, strings.Join(texts, "\n")
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return nil, ""
		}
		text := string(b)
		return []any{map[string]any{
			"type":        "output_text",
			"text":        text,
			"annotations": []any{},
			"logprobs":    []any{},
		}}, text
	}
}

func responsesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	defer r.Body.Close()
	auth := extractUpstreamAuth(r)
	body, err := io.ReadAll(io.LimitReader(r.Body, 10*1024*1024))
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}

	cnt := requestCount.Add(1)
	slog.Debug("responses request body", "count", cnt, "body", string(body))

	var respReq ResponsesAPIRequest
	if err := json.Unmarshal(body, &respReq); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	respReq.Model = resolveModel(respReq.Model, true) // opencode 恒无 key 免费档 → 恒优先 -free
	previousState, hasPreviousState := StoredResponseState{}, false
	if respReq.PreviousResponseID != "" {
		previousState, hasPreviousState = loadResponseState(respReq.PreviousResponseID)
		if respReq.Model == "" && previousState.Model != "" {
			respReq.Model = previousState.Model
		}
		if len(respReq.Tools) == 0 && len(previousState.Tools) > 0 {
			respReq.Tools = previousState.Tools
		}
		if respReq.ToolChoice == nil && previousState.ToolChoice != nil {
			respReq.ToolChoice = previousState.ToolChoice
		}
	}
	if respReq.Model == "" {
		http.Error(w, "model is required", http.StatusBadRequest)
		return
	}

	// 全流程调用日志：记录每个请求的决策链（网关模式下；对齐 chat_handler 三态）
	startTime := time.Now()
	callRec := CallRecord{
		ReqID:     getReqID(r.Context()),
		TS:        time.Now().Format(time.RFC3339),
		Path:      r.URL.Path,
		Model:     respReq.Model,
		Stream:    respReq.Stream,
		RouteMode: routeMode.Load().(string),
		Status:    "ok",
	}
	if callRec.ReqID == "" {
		callRec.ReqID = "req_" + randomString(12)
	}

	// 多模态路由

	messages := respReq.Messages
	if len(messages) == 0 {
		if hasPreviousState && len(previousState.Output) > 0 {
			messages = append(messages, responsesInputToMessages(previousState.Output, "")...)
		}
		messages = append(messages, responsesInputToMessages(respReq.Input, respReq.Instructions)...)
	} else if respReq.Instructions != "" {
		messages = append([]Message{{Role: "system", Content: respReq.Instructions}}, messages...)
	}

	chatReq := OpenAIRequest{
		Model:    respReq.Model,
		Messages: messages,
		Stream:   respReq.Stream,
	}
	if respReq.Stream {
		chatReq.ExtraBody = map[string]any{
			"stream_options": map[string]any{"include_usage": true},
		}
	}
	if respReq.Temperature != nil {
		chatReq.Temperature = respReq.Temperature
	}
	if respReq.MaxTokens != nil {
		chatReq.MaxTokens = respReq.MaxTokens
	}
	if respReq.TopP != nil {
		chatReq.TopP = respReq.TopP
	}
	if len(respReq.Tools) > 0 {
		chatReq.Tools = convertResponsesTools(respReq.Tools)
	}
	if respReq.ToolChoice != nil {
		chatReq.ToolChoice = convertResponsesToolChoice(respReq.ToolChoice)
	}
	if respReq.ParallelToolCalls != nil {
		if chatReq.ExtraBody == nil {
			chatReq.ExtraBody = map[string]any{}
		}
		chatReq.ExtraBody["parallel_tool_calls"] = *respReq.ParallelToolCalls
	}
	if respReq.Stop != nil {
		if chatReq.ExtraBody == nil {
			chatReq.ExtraBody = map[string]any{}
		}
		chatReq.ExtraBody["stop"] = respReq.Stop
	}
	if respReq.FrequencyPenalty != nil {
		if chatReq.ExtraBody == nil {
			chatReq.ExtraBody = map[string]any{}
		}
		chatReq.ExtraBody["frequency_penalty"] = *respReq.FrequencyPenalty
	}
	if respReq.PresencePenalty != nil {
		if chatReq.ExtraBody == nil {
			chatReq.ExtraBody = map[string]any{}
		}
		chatReq.ExtraBody["presence_penalty"] = *respReq.PresencePenalty
	}
	if respReq.User != "" {
		if chatReq.ExtraBody == nil {
			chatReq.ExtraBody = map[string]any{}
		}
		chatReq.ExtraBody["user"] = respReq.User
	}
	if respReq.StreamOptions != nil {
		if chatReq.ExtraBody == nil {
			chatReq.ExtraBody = map[string]any{}
		}
		streamOptions, ok := respReq.StreamOptions.(map[string]any)
		if !ok {
			streamOptions = map[string]any{}
		}
		if _, exists := streamOptions["include_usage"]; !exists && respReq.Stream {
			streamOptions["include_usage"] = true
		}
		chatReq.ExtraBody["stream_options"] = streamOptions
	}
	// 将 Responses API reasoning.effort 映射到 Chat Completions
	if !getForceDisableThinking() && respReq.Reasoning.Effort != "" {
		if respReq.Reasoning.Effort != "none" {
			chatReq.ReasoningEffort = respReq.Reasoning.Effort
		}
	}

	wantReasoning := !getForceDisableThinking()
	chatReq.Messages = fixToolCallGaps(chatReq.Messages)
	keepReasoning := wantsReasoning(&chatReq)
	chatReq.Messages = ensureReasoningContent(chatReq.Messages, keepReasoning)

	upstreamBody := buildUpstreamBody(&chatReq)

	// auto 虚拟模型：选主选 + 降级链挂 ctx（respReq.Model 保持 "auto" 用于状态存储，
	// previous_response_id 续聊时无 model 也会再次走 auto）。
	callCtx := r.Context()
	var autoDec *autoDecision
	if isAutoModelName(chatReq.Model) {
		callCtx, chatReq.Model, autoDec = prepareAuto(r.Context(), chatReq.Model, upstreamBody)
		if autoDec != nil {
			callRec.Events = append(callRec.Events, autoDec.pickEvent())
		}
	}

	if respReq.Stream {
		// L6：进程级流并发上限——超限直接 503（defer 覆盖所有返回路径释放名额）。
		if !tryAcquireStream() {
			callRec.Status = "fail"
			callRec.ErrMsg = "并发流已达上限"
			callRec.DurationMS = time.Since(startTime).Milliseconds()
			callRec.Events = append(callRec.Events, CallEvent{Type: "capacity", Node: "", Detail: callRec.ErrMsg, At: time.Now()})
			recordCall(callRec)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "并发流已达上限，请稍后重试", "type": "stream_capacity_exceeded"}})
			return
		}
		defer releaseStream()
		upResp, status, _, proxyAddr, err := callOpenCodeAPIStream(callCtx, upstreamBody, chatReq.Model, auth)
		callRec.Nodes = append(callRec.Nodes, proxyAddr)
		if err != nil || status < 200 || status >= 300 {
			callRec.Status = "fail"
			// 非 2xx 时上游错误体随流返回：读出来入日志（截断）并透传客户端。
			var errBody []byte
			if upResp != nil {
				errBody, _ = io.ReadAll(upResp)
				upResp.Close()
			}
			callRec.ErrMsg = upstreamErrMsg(status, err, errBody)
			callRec.DurationMS = time.Since(startTime).Milliseconds()
			callRec.Events = append(callRec.Events, CallEvent{Type: "upstream_error", Node: proxyAddr, Detail: callRec.ErrMsg, At: time.Now()})
			if autoDec == nil {
				recordModelFeedback(chatReq.Model, proxyAddr, false, callRec.DurationMS)
			}
			recordCall(callRec)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(httpStatusOr(status))
			if len(errBody) > 0 {
				if autoDec != nil && isContextLimitError(errBody) {
					learnContextFailure(displayModelName(autoDec.FinalModel), autoDec.EstTokens)
				}
				w.Write(errBody)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "upstream error"}})
			return
		}
		callRec.Events = append(callRec.Events, CallEvent{Type: "connect_ok", Node: proxyAddr, Detail: "connected", At: time.Now()})
		defer upResp.Close()

		resp := &http.Response{
			StatusCode: status,
			Body:       upResp,
			Header:     make(http.Header),
		}
		responsesStreamHandler(w, r, resp, chatReq.Model, chatReq.Model, wantReasoning, respReq.Tools, respReq.ToolChoice, respReq, proxyAddr)
		callRec.DurationMS = time.Since(startTime).Milliseconds()
		callRec.Events = append(callRec.Events, CallEvent{Type: "complete", Node: proxyAddr, Detail: "done", At: time.Now()})
		if autoDec == nil {
			recordModelFeedback(chatReq.Model, proxyAddr, true, callRec.DurationMS)
		}
		recordCall(callRec)
		return
	}

	respBody, status, _, proxyAddr, err := callOpenCodeAPI(callCtx, upstreamBody, chatReq.Model, auth)
	callRec.Nodes = append(callRec.Nodes, proxyAddr)
	if err != nil || status < 200 || status >= 300 {
		callRec.Status = "fail"
		callRec.ErrMsg = upstreamErrMsg(status, err, respBody)
		callRec.DurationMS = time.Since(startTime).Milliseconds()
		callRec.Events = append(callRec.Events, CallEvent{Type: "upstream_error", Node: proxyAddr, Detail: callRec.ErrMsg, At: time.Now()})
		if autoDec != nil && len(respBody) > 0 && isContextLimitError(respBody) {
			learnContextFailure(displayModelName(autoDec.FinalModel), autoDec.EstTokens)
		}
		if autoDec == nil {
			recordModelFeedback(chatReq.Model, proxyAddr, false, callRec.DurationMS)
		}
		recordCall(callRec)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(httpStatusOr(status))
		if len(respBody) > 0 {
			w.Write(respBody)
		} else {
			json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "upstream error"}})
		}
		return
	}

	responsesBody := convertChatToResponses(respBody, chatReq.Model, wantReasoning, respReq.Tools, respReq.ToolChoice)
	var responseMap map[string]any
	if json.Unmarshal(responsesBody, &responseMap) == nil {
		protocol.ApplyResponsesRequestEcho(responseMap, respReq)
		if enriched, marshalErr := json.Marshal(responseMap); marshalErr == nil {
			responsesBody = enriched
		}
		storeResponseState(responseMap, respReq)
	}

	var usageResp map[string]any
	if json.Unmarshal(respBody, &usageResp) == nil {
		if u, ok := usageResp["usage"].(map[string]any); ok {
			pt, _ := u["prompt_tokens"].(float64)
			ct, _ := u["completion_tokens"].(float64)
			tt, _ := u["total_tokens"].(float64)
			if tt > 0 {
				recordTokenUsage(chatReq.Model, int64(pt), int64(ct), int64(tt), proxyAddr)
			}
			callRec.PromptTok = int64(pt)
			callRec.CompletionTok = int64(ct)
		}
	}
	callRec.DurationMS = time.Since(startTime).Milliseconds()
	callRec.Events = append(callRec.Events, CallEvent{Type: "complete", Node: proxyAddr, Detail: "done", At: time.Now()})
	if autoDec == nil {
		recordModelFeedback(chatReq.Model, proxyAddr, true, callRec.DurationMS)
	}
	recordCall(callRec)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	slog.Debug("responses response body", "body", string(responsesBody))
	w.Write(responsesBody)
}

// ======================== Responses Stream Handler ========================

func responsesStreamHandler(w http.ResponseWriter, _ *http.Request, resp *http.Response, model string, _ string, wantReasoning bool, tools []ResponsesTool, toolChoice any, originalReq ResponsesAPIRequest, proxyAddr string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	flusher, _ := w.(http.Flusher)
	reader := bufio.NewReader(resp.Body)

	responseID := "resp_" + time.Now().Format("20060102150405") + "_" + randomString(8)
	reasoningID := "rs_" + responseID
	msgID := "msg_" + responseID + "_0"
	createdAt := time.Now().Unix()
	seq := 0

	reasoningStarted := false
	reasoningDone := false
	messageStarted := false
	messageDone := false
	fullReasoning := ""
	fullText := ""
	totalUsage := map[string]any{}
	createdSent := false
	terminalStatus := "completed"
	terminalEvent := "response.completed"
	itemStatus := "completed"
	toolCalls := map[int]map[string]any{}
	toolOrder := []int{}
	toolKinds := responsesToolKindMap(tools)
	indexAllocator := protocol.OutputIndexAllocator{}
	reasoningOutputIndex := -1
	messageIndex := -1

	messageOutputIndex := func() int {
		if messageIndex < 0 {
			messageIndex = indexAllocator.Allocate()
		}
		return messageIndex
	}

	reasoningItem := func(status string) map[string]any {
		item := map[string]any{
			"id":      reasoningID,
			"type":    "reasoning",
			"summary": []any{},
		}
		if status != "" {
			item["status"] = status
		}
		if status == "completed" {
			item["encrypted_content"] = ""
		}
		if fullReasoning != "" {
			item["summary"] = []any{map[string]any{"type": "summary_text", "text": fullReasoning}}
		}
		return item
	}

	messageItem := func(status string) map[string]any {
		content := []any{map[string]any{
			"type":        "output_text",
			"annotations": []any{},
			"logprobs":    []any{},
			"text":        fullText,
		}}
		return map[string]any{
			"id":      msgID,
			"type":    "message",
			"status":  status,
			"content": content,
			"role":    "assistant",
		}
	}

	emitReasoningDone := func() {
		if !reasoningStarted || reasoningDone {
			return
		}
		seq++
		emitSSEEvent(w, flusher, "response.reasoning_summary_text.done", map[string]any{
			"type":            "response.reasoning_summary_text.done",
			"sequence_number": seq,
			"item_id":         reasoningID,
			"output_index":    reasoningOutputIndex,
			"summary_index":   0,
			"text":            fullReasoning,
		})
		seq++
		emitSSEEvent(w, flusher, "response.reasoning_summary_part.done", map[string]any{
			"type":            "response.reasoning_summary_part.done",
			"sequence_number": seq,
			"item_id":         reasoningID,
			"output_index":    reasoningOutputIndex,
			"summary_index":   0,
			"part":            map[string]any{"type": "summary_text", "text": fullReasoning},
		})
		seq++
		emitSSEEvent(w, flusher, "response.output_item.done", map[string]any{
			"type":            "response.output_item.done",
			"sequence_number": seq,
			"output_index":    reasoningOutputIndex,
			"item":            reasoningItem(itemStatus),
		})
		reasoningDone = true
	}

	emitMessageDone := func() {
		if !messageStarted || messageDone {
			return
		}
		idx := messageOutputIndex()
		seq++
		emitSSEEvent(w, flusher, "response.output_text.done", map[string]any{
			"type":            "response.output_text.done",
			"sequence_number": seq,
			"item_id":         msgID,
			"output_index":    idx,
			"content_index":   0,
			"text":            fullText,
			"logprobs":        []any{},
		})
		seq++
		emitSSEEvent(w, flusher, "response.content_part.done", map[string]any{
			"type":            "response.content_part.done",
			"sequence_number": seq,
			"item_id":         msgID,
			"output_index":    idx,
			"content_index":   0,
			"part":            map[string]any{"type": "output_text", "annotations": []any{}, "logprobs": []any{}, "text": fullText},
		})
		seq++
		emitSSEEvent(w, flusher, "response.output_item.done", map[string]any{
			"type":            "response.output_item.done",
			"sequence_number": seq,
			"output_index":    idx,
			"item":            messageItem(itemStatus),
		})
		messageDone = true
	}

	emitToolCallDone := func(idx int, call map[string]any) {
		if done, _ := call["done"].(bool); done {
			return
		}
		call["done"] = true
		itemID, _ := call["item_id"].(string)
		callID, _ := call["call_id"].(string)
		name, _ := call["name"].(string)
		args, _ := call["arguments"].(string)
		seq++
		emitSSEEvent(w, flusher, "response.function_call_arguments.done", map[string]any{
			"type":            "response.function_call_arguments.done",
			"sequence_number": seq,
			"item_id":         itemID,
			"output_index":    idx,
			"name":            name,
			"arguments":       args,
		})
		seq++
		itemType, _ := call["item_type"].(string)
		if itemType == "" {
			itemType = "function_call"
		}
		item := buildResponseToolCallItem(ToolCall{ID: callID, Function: FunctionCall{Name: name, Arguments: args}}, itemType)
		item["status"] = itemStatus
		emitSSEEvent(w, flusher, "response.output_item.done", map[string]any{
			"type":            "response.output_item.done",
			"sequence_number": seq,
			"output_index":    idx,
			"item":            item,
		})
	}

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			slog.Error("stream read error", "error", err)
			return
		}
		if strings.HasPrefix(line, "data: ") {
			slog.Debug("upstream raw chunk", "data", strings.TrimSpace(line[6:]))
		}

		trimmed := strings.TrimSpace(line)
		if trimmed == "data: [DONE]" || trimmed == "[DONE]" {
			break
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		var chunk map[string]any
		if err := json.Unmarshal([]byte(line[6:]), &chunk); err != nil {
			continue
		}
		if !createdSent {
			if id, ok := chunk["id"].(string); ok && id != "" {
				responseID = id
				reasoningID = "rs_" + responseID + "_0"
				msgID = "msg_" + responseID + "_0"
			}
			if created, ok := chunk["created"].(float64); ok {
				createdAt = int64(created)
			}
			seq++
			emitSSEEvent(w, flusher, "response.created", map[string]any{
				"type":            "response.created",
				"sequence_number": seq,
				"response":        map[string]any{"id": responseID, "object": "response", "created_at": createdAt, "status": "in_progress", "background": false, "error": nil, "output": []any{}},
			})
			seq++
			emitSSEEvent(w, flusher, "response.in_progress", map[string]any{
				"type":            "response.in_progress",
				"sequence_number": seq,
				"response":        map[string]any{"id": responseID, "object": "response", "created_at": createdAt, "status": "in_progress"},
			})
			createdSent = true
		}
		choices, ok := chunk["choices"].([]any)
		if !ok || len(choices) == 0 {
			if usage, ok := chunk["usage"].(map[string]any); ok {
				totalUsage = usage
			}
			continue
		}

		choice, _ := choices[0].(map[string]any)
		delta, _ := choice["delta"].(map[string]any)
		finishReason, _ := choice["finish_reason"].(string)

		if rc, ok := delta["reasoning_content"]; ok && wantReasoning {
			rcStr, _ := rc.(string)
			if rcStr != "" {
				if !reasoningStarted {
					reasoningOutputIndex = indexAllocator.Allocate()
					seq++
					emitSSEEvent(w, flusher, "response.output_item.added", map[string]any{
						"type":            "response.output_item.added",
						"sequence_number": seq,
						"output_index":    reasoningOutputIndex,
						"item":            reasoningItem("in_progress"),
					})
					seq++
					emitSSEEvent(w, flusher, "response.reasoning_summary_part.added", map[string]any{
						"type":            "response.reasoning_summary_part.added",
						"sequence_number": seq,
						"item_id":         reasoningID,
						"output_index":    reasoningOutputIndex,
						"summary_index":   0,
						"part":            map[string]any{"type": "summary_text", "text": ""},
					})
					reasoningStarted = true
				}
				fullReasoning += rcStr
				seq++
				emitSSEEvent(w, flusher, "response.reasoning_summary_text.delta", map[string]any{
					"type":            "response.reasoning_summary_text.delta",
					"sequence_number": seq,
					"item_id":         reasoningID,
					"output_index":    reasoningOutputIndex,
					"summary_index":   0,
					"delta":           rcStr,
				})
			}
		}

		contentStr := ""
		if c, ok := delta["content"]; ok && c != nil {
			contentStr, _ = c.(string)
		}
		if contentStr != "" {
			// The terminal finish reason determines the item's final status. Keep the
			// reasoning item open until that reason is known so a truncation cannot
			// first announce it as completed.
			if !messageStarted {
				idx := messageOutputIndex()
				seq++
				emitSSEEvent(w, flusher, "response.output_item.added", map[string]any{
					"type":            "response.output_item.added",
					"sequence_number": seq,
					"output_index":    idx,
					"item":            map[string]any{"id": msgID, "type": "message", "status": "in_progress", "content": []any{}, "role": "assistant"},
				})
				seq++
				emitSSEEvent(w, flusher, "response.content_part.added", map[string]any{
					"type":            "response.content_part.added",
					"sequence_number": seq,
					"item_id":         msgID,
					"output_index":    idx,
					"content_index":   0,
					"part":            map[string]any{"type": "output_text", "annotations": []any{}, "logprobs": []any{}, "text": ""},
				})
				messageStarted = true
			}
			fullText += contentStr
			seq++
			emitSSEEvent(w, flusher, "response.output_text.delta", map[string]any{
				"type":            "response.output_text.delta",
				"sequence_number": seq,
				"item_id":         msgID,
				"output_index":    messageOutputIndex(),
				"content_index":   0,
				"delta":           contentStr,
				"logprobs":        []any{},
			})
		}

		rawToolCalls, _ := delta["tool_calls"].([]any)
		for _, rawToolCall := range rawToolCalls {
			tc, ok := rawToolCall.(map[string]any)
			if !ok {
				continue
			}
			idxFloat, _ := tc["index"].(float64)
			upstreamIndex := int(idxFloat)
			call, exists := toolCalls[upstreamIndex]
			if !exists {
				outputIndex := indexAllocator.Allocate()
				callID, _ := tc["id"].(string)
				if callID == "" {
					callID = "call_" + randomString(12)
				}
				fn, _ := tc["function"].(map[string]any)
				name, _ := fn["name"].(string)
				itemType := toolCallOutputType(name, toolKinds)
				call = map[string]any{
					"output_index": outputIndex,
					"item_id":      "fc_" + callID,
					"call_id":      callID,
					"name":         name,
					"arguments":    "",
					"done":         false,
					"item_type":    itemType,
				}
				toolCalls[upstreamIndex] = call
				toolOrder = append(toolOrder, upstreamIndex)
				seq++
				emitSSEEvent(w, flusher, "response.output_item.added", map[string]any{
					"type":            "response.output_item.added",
					"sequence_number": seq,
					"output_index":    outputIndex,
					"item": map[string]any{
						"id":        call["item_id"],
						"type":      itemType,
						"status":    "in_progress",
						"arguments": "",
						"call_id":   callID,
						"name":      name,
					},
				})
			}
			fn, _ := tc["function"].(map[string]any)
			if name, _ := fn["name"].(string); name != "" {
				call["name"] = name
				if call["item_type"] == "function_call" {
					call["item_type"] = toolCallOutputType(name, toolKinds)
				}
			}
			if argDelta, _ := fn["arguments"].(string); argDelta != "" {
				call["arguments"] = call["arguments"].(string) + argDelta
				seq++
				emitSSEEvent(w, flusher, "response.function_call_arguments.delta", map[string]any{
					"type":            "response.function_call_arguments.delta",
					"sequence_number": seq,
					"item_id":         call["item_id"],
					"output_index":    call["output_index"],
					"delta":           argDelta,
				})
			}
		}

		if usage, ok := chunk["usage"].(map[string]any); ok {
			totalUsage = usage
		}
		if finishReason == "stop" || finishReason == "length" || finishReason == "content_filter" {
			if finishReason == "length" {
				terminalStatus = "incomplete"
				terminalEvent = "response.incomplete"
				itemStatus = "incomplete"
			}
			emitReasoningDone()
			if !messageStarted && len(toolCalls) == 0 {
				idx := messageOutputIndex()
				seq++
				emitSSEEvent(w, flusher, "response.output_item.added", map[string]any{
					"type":            "response.output_item.added",
					"sequence_number": seq,
					"output_index":    idx,
					"item":            map[string]any{"id": msgID, "type": "message", "status": "in_progress", "content": []any{}, "role": "assistant"},
				})
				seq++
				emitSSEEvent(w, flusher, "response.content_part.added", map[string]any{
					"type":            "response.content_part.added",
					"sequence_number": seq,
					"item_id":         msgID,
					"output_index":    idx,
					"content_index":   0,
					"part":            map[string]any{"type": "output_text", "annotations": []any{}, "logprobs": []any{}, "text": ""},
				})
				messageStarted = true
			}
			emitMessageDone()
			for _, idx := range toolOrder {
				emitToolCallDone(toolCalls[idx]["output_index"].(int), toolCalls[idx])
			}
		}
	}

	emitReasoningDone()
	emitMessageDone()
	for _, idx := range toolOrder {
		emitToolCallDone(toolCalls[idx]["output_index"].(int), toolCalls[idx])
	}

	output := make([]any, indexAllocator.Len())
	if reasoningStarted {
		output[reasoningOutputIndex] = reasoningItem(itemStatus)
	}
	if messageStarted {
		output[messageIndex] = messageItem(itemStatus)
	}
	for _, idx := range toolOrder {
		call := toolCalls[idx]
		itemType, _ := call["item_type"].(string)
		if itemType == "" {
			itemType = "function_call"
		}
		item := buildResponseToolCallItem(ToolCall{
			ID: call["call_id"].(string),
			Function: FunctionCall{
				Name:      call["name"].(string),
				Arguments: call["arguments"].(string),
			},
		}, itemType)
		item["status"] = itemStatus
		output[call["output_index"].(int)] = item
	}

	completedResponse := map[string]any{
		"id":                 responseID,
		"object":             "response",
		"created_at":         createdAt,
		"status":             terminalStatus,
		"background":         false,
		"error":              nil,
		"incomplete_details": nil,
		"model":              model,
		"output":             output,
	}
	if terminalStatus == "incomplete" {
		completedResponse["incomplete_details"] = map[string]any{"reason": "max_output_tokens"}
	}
	protocol.ApplyResponsesRequestEcho(completedResponse, originalReq)
	if len(tools) > 0 {
		completedResponse["tools"] = tools
	}
	if toolChoice != nil {
		completedResponse["tool_choice"] = toolChoice
	}

	if len(totalUsage) > 0 {
		usage := map[string]any{}
		if v, ok := totalUsage["prompt_tokens"]; ok {
			usage["input_tokens"] = v
		}
		if v, ok := totalUsage["prompt_tokens_details"]; ok {
			usage["input_tokens_details"] = v
		} else {
			usage["input_tokens_details"] = map[string]any{"cached_tokens": 0}
		}
		if v, ok := totalUsage["completion_tokens"]; ok {
			usage["output_tokens"] = v
		}
		if v, ok := totalUsage["completion_tokens_details"]; ok {
			usage["output_tokens_details"] = v
		}
		if v, ok := totalUsage["total_tokens"]; ok {
			usage["total_tokens"] = v
		}
		if v, ok := totalUsage["input_tokens"]; ok && usage["input_tokens"] == nil {
			usage["input_tokens"] = v
		}
		if v, ok := totalUsage["output_tokens"]; ok && usage["output_tokens"] == nil {
			usage["output_tokens"] = v
		}
		completedResponse["usage"] = usage
	}

	if totalUsage != nil {
		pt, _ := totalUsage["prompt_tokens"].(float64)
		ct, _ := totalUsage["completion_tokens"].(float64)
		tt, _ := totalUsage["total_tokens"].(float64)
		if tt > 0 {
			recordTokenUsage(model, int64(pt), int64(ct), int64(tt), proxyAddr)
		}
	}

	seq++
	emitSSEEvent(w, flusher, terminalEvent, map[string]any{
		"type":            terminalEvent,
		"sequence_number": seq,
		"response":        completedResponse,
	})

	if flusher != nil {
		flusher.Flush()
	}
	storeResponseState(completedResponse, originalReq)
}

func convertChatToResponses(chatBody []byte, model string, wantReasoning bool, tools []ResponsesTool, toolChoice any) []byte {
	var chat struct {
		ID      string `json:"id"`
		Created int64  `json:"created"`
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content          any        `json:"content"`
				Refusal          string     `json:"refusal"`
				ReasoningContent string     `json:"reasoning_content"`
				ToolCalls        []ToolCall `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage map[string]any `json:"usage"`
	}
	if err := json.Unmarshal(chatBody, &chat); err != nil {
		slog.Warn("convertChatToResponses unmarshal failed", "error", err)
	}

	reasoning := ""
	finishReason := ""
	var toolCalls []ToolCall
	messageContent := []any(nil)
	toolKinds := responsesToolKindMap(tools)
	if len(chat.Choices) > 0 {
		messageContent, _ = chatContentToResponsesContent(chat.Choices[0].Message.Content)
		if refusal := chat.Choices[0].Message.Refusal; refusal != "" {
			messageContent = []any{map[string]any{"type": "refusal", "refusal": refusal}}
		}
		if wantReasoning {
			reasoning = chat.Choices[0].Message.ReasoningContent
		}
		toolCalls = chat.Choices[0].Message.ToolCalls
		finishReason = chat.Choices[0].FinishReason
	}

	outcome := protocol.ResponsesOutcome(finishReason)
	status := outcome.Status
	responses := map[string]any{
		"id":                 chat.ID,
		"object":             "response",
		"status":             status,
		"background":         false,
		"error":              nil,
		"incomplete_details": outcome.IncompleteDetails,
		"model":              model,
		"created_at":         chat.Created,
	}
	if len(tools) > 0 {
		responses["tools"] = tools
	}
	if toolChoice != nil {
		responses["tool_choice"] = toolChoice
	}
	outputID := "msg_" + chat.ID + "_0"
	output := []any{}
	if reasoning != "" {
		output = append(output, map[string]any{
			"id":                "rs_" + chat.ID,
			"type":              "reasoning",
			"encrypted_content": "",
			"summary":           []any{map[string]any{"type": "summary_text", "text": reasoning}},
		})
	}
	if len(messageContent) > 0 {
		output = append(output, map[string]any{
			"id":      outputID,
			"type":    "message",
			"status":  status,
			"role":    "assistant",
			"content": messageContent,
		})
	}
	for _, tc := range toolCalls {
		item := buildResponseToolCallItem(tc, toolCallOutputType(tc.Function.Name, toolKinds))
		item["status"] = status
		output = append(output, item)
	}
	responses["output"] = output
	if chat.Usage != nil {
		usage := map[string]any{}
		if v, ok := chat.Usage["prompt_tokens"]; ok {
			usage["input_tokens"] = v
		}
		if v, ok := chat.Usage["prompt_tokens_details"]; ok {
			usage["input_tokens_details"] = v
		} else {
			usage["input_tokens_details"] = map[string]any{"cached_tokens": 0}
		}
		if v, ok := chat.Usage["completion_tokens"]; ok {
			usage["output_tokens"] = v
		}
		if v, ok := chat.Usage["completion_tokens_details"]; ok {
			usage["output_tokens_details"] = v
		}
		if v, ok := chat.Usage["total_tokens"]; ok {
			usage["total_tokens"] = v
		}
		if v, ok := chat.Usage["input_tokens"]; ok && usage["input_tokens"] == nil {
			usage["input_tokens"] = v
		}
		if v, ok := chat.Usage["output_tokens"]; ok && usage["output_tokens"] == nil {
			usage["output_tokens"] = v
		}
		responses["usage"] = usage
	}

	result, _ := json.Marshal(responses)
	return result
}

func emitSSEEvent(w http.ResponseWriter, flusher http.Flusher, event string, data map[string]any) {
	jsonData, err := json.Marshal(data)
	if err != nil {
		slog.Error("marshal SSE event failed", "error", err)
		return
	}
	w.Write([]byte("event: " + event + "\n"))
	w.Write([]byte("data: " + string(jsonData) + "\n\n"))
	if flusher != nil {
		flusher.Flush()
	}
}

// ======================== Admin 管理页面 ========================
