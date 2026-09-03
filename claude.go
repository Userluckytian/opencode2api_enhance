// Part of the P1 (core split) refactor: code moved out of main.go.
// Same package (main) - do not change package clause manually.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/6Kmfi6HP/opencode2api/core/protocol"
)

func claudeMessagesHandler(w http.ResponseWriter, r *http.Request) {
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
	slog.Debug("claude messages request body", "count", cnt, "body", string(body))

	var claudeReq ClaudeRequest
	if err := json.Unmarshal(body, &claudeReq); err != nil {
		http.Error(w, `{"type":"error","error":{"type":"invalid_request_error","message":"Invalid JSON"}}`, http.StatusBadRequest)
		return
	}
	claudeReq.Model = resolveModel(claudeReq.Model, true) // opencode 恒无 key 免费档 → 恒优先 -free
	if claudeReq.Model == "" {
		http.Error(w, `{"type":"error","error":{"type":"invalid_request_error","message":"model is required"}}`, http.StatusBadRequest)
		return
	}

	// 全流程调用日志：记录每个请求的决策链（网关模式下；对齐 chat_handler 三态）
	startTime := time.Now()
	callRec := CallRecord{
		ReqID:     getReqID(r.Context()),
		TS:        time.Now().Format(time.RFC3339),
		Path:      r.URL.Path,
		Model:     claudeReq.Model,
		Stream:    claudeReq.Stream,
		RouteMode:  routeMode.Load().(string),
		Tier:       tierOfAuth(auth),
		ServingPort: port,
		Status:     "ok",
	}
	if callRec.ReqID == "" {
		callRec.ReqID = "req_" + randomString(12)
	}

	// 多模态路由

	chatReq := protocol.ConvertClaudeRequest(claudeReq)
	chatReq.Messages = fixToolCallGaps(chatReq.Messages)
	if claudeReq.Stream {
		if chatReq.ExtraBody == nil {
			chatReq.ExtraBody = map[string]any{}
		}
		chatReq.ExtraBody["stream_options"] = map[string]any{"include_usage": true}
	}

	wantReasoning := !getForceDisableThinking()
	if claudeReq.Thinking != nil {
		if isThinkingDisabled(claudeReq.Thinking) {
			wantReasoning = false
		}
	}
	keepReasoning := wantReasoning
	chatReq.Messages = ensureReasoningContent(chatReq.Messages, keepReasoning)

	upstreamBody := buildUpstreamBody(&chatReq)

	// auto 虚拟模型：选主选 + 降级链挂 ctx（claudeReq.Model 保持 "auto" 用于展示）。
	callCtx := r.Context()
	var autoDec *autoDecision
	if isAutoModelName(chatReq.Model) {
		var autoErr error
		callCtx, chatReq.Model, autoDec, autoErr = prepareAuto(r.Context(), chatReq.Model, upstreamBody)
		if autoErr != nil {
			// auto 已启用但无可用候选：直接回客户端明确错误（不落到默认厂商撞 502/404）。
			callRec.Status = "fail"
			callRec.ErrMsg = autoErr.Error()
			callRec.DurationMS = time.Since(startTime).Milliseconds()
			recordCall(callRec)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]any{"type": "error", "error": map[string]string{"type": "invalid_request_error", "message": autoErr.Error()}})
			return
		}
		if autoDec != nil {
			callRec.Events = append(callRec.Events, autoDec.pickEvent())
		}
	}

	if claudeReq.Stream {
		// L6：进程级流并发上限——超限直接 503（defer 覆盖所有返回路径释放名额）。
		if !tryAcquireStream() {
			callRec.Status = "fail"
			callRec.ErrMsg = "并发流已达上限"
			callRec.DurationMS = time.Since(startTime).Milliseconds()
			callRec.Events = append(callRec.Events, CallEvent{Type: "capacity", Node: "", Detail: callRec.ErrMsg, At: time.Now()})
			recordCall(callRec)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]any{"type": "error", "error": map[string]string{"type": "overloaded", "message": "并发流已达上限，请稍后重试"}})
			return
		}
		defer releaseStream()
		upResp, status, _, proxyAddr, err := callOpenCodeAPIStream(callCtx, upstreamBody, chatReq.Model, &auth)
		callRec.Nodes = append(callRec.Nodes, proxyAddr)
		if err != nil || status < 200 || status >= 300 {
			callRec.Status = "fail"
			// 非 2xx 时上游错误体随流返回：读出来入日志（截断），供日志页显示完整原因。
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
			errResp := map[string]any{
				"type":  "error",
				"error": map[string]string{"type": "api_error", "message": "upstream error"},
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(httpStatusOr(status))
			json.NewEncoder(w).Encode(errResp)
			return
		}
		callRec.Events = append(callRec.Events, CallEvent{Type: "connect_ok", Node: proxyAddr, Detail: "connected", At: time.Now()})
		defer upResp.Close()
		claudeStreamHandler(w, upResp, chatReq.Model, keepReasoning, proxyAddr)
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
			json.NewEncoder(w).Encode(map[string]any{"type": "error", "error": map[string]string{"type": "api_error", "message": "upstream error"}})
		}
		return
	}

	claudeRespBody := protocol.OpenAIToClaudeResponse(respBody, claudeReq.Model, wantReasoning)

	// Record token usage
	var usageResp map[string]any
	if json.Unmarshal(respBody, &usageResp) == nil {
		if u, ok := usageResp["usage"].(map[string]any); ok {
			pt, _ := u["prompt_tokens"].(float64)
			ct, _ := u["completion_tokens"].(float64)
			tt, _ := u["total_tokens"].(float64)
			if tt > 0 {
				// 用上游真实模型记账（auto 请求也计入具体模型，统计页不出现虚拟行）。
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
	slog.Debug("claude response body", "body", string(claudeRespBody))
	w.Write(claudeRespBody)
}

func claudeStreamHandler(w http.ResponseWriter, respBody io.ReadCloser, model string, keepReasoning bool, proxyAddr string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	flusher, _ := w.(http.Flusher)
	reader := bufio.NewReader(respBody)

	msgID := fmt.Sprintf("msg_%s", randomString(24))
	blockIndex := 0
	thinkingBlockOpen := false
	textBlockOpen := false
	toolCallAccumulator := map[int]map[string]string{}
	toolBlockIndices := map[int]int{}
	toolCallOrder := []int{}
	messageStartSent := false
	fullUsage := map[string]any{}
	defer func() {
		if len(fullUsage) > 0 {
			pt, _ := fullUsage["prompt_tokens"].(float64)
			ct, _ := fullUsage["completion_tokens"].(float64)
			tt, _ := fullUsage["total_tokens"].(float64)
			if tt > 0 {
				recordTokenUsage(model, int64(pt), int64(ct), int64(tt), proxyAddr)
			}
		}
	}()

	emitClaudeEvent := func(event string, data any) {
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

	closeThinkingBlock := func() {
		if !thinkingBlockOpen {
			return
		}
		emitClaudeEvent("content_block_stop", map[string]any{
			"type":          "content_block_stop",
			"index":         blockIndex - 1,
			"content_block": map[string]any{"type": "thinking"},
		})
		thinkingBlockOpen = false
	}

	closeTextBlock := func() {
		if !textBlockOpen {
			return
		}
		emitClaudeEvent("content_block_stop", map[string]any{
			"type":          "content_block_stop",
			"index":         blockIndex - 1,
			"content_block": map[string]any{"type": "text"},
		})
		textBlockOpen = false
	}

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			slog.Error("stream read error", "error", err)
			break
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
		if usage, ok := chunk["usage"].(map[string]any); ok {
			fullUsage = usage
		}

		choices, ok := chunk["choices"].([]any)
		if !ok || len(choices) == 0 {
			continue
		}

		choice, _ := choices[0].(map[string]any)
		delta, _ := choice["delta"].(map[string]any)
		finishReason, _ := choice["finish_reason"].(string)

		if !messageStartSent {
			messageStartSent = true
			emitClaudeEvent("message_start", map[string]any{
				"type": "message_start",
				"message": map[string]any{
					"id":          msgID,
					"type":        "message",
					"role":        "assistant",
					"content":     []any{},
					"model":       model,
					"stop_reason": nil,
					"usage":       protocol.BuildClaudeMessageUsage(fullUsage),
				},
			})
			emitClaudeEvent("ping", map[string]any{"type": "ping"})
		}

		if rc, ok := delta["reasoning_content"]; ok && keepReasoning {
			rcStr, _ := rc.(string)
			if rcStr != "" {
				closeTextBlock()
				if !thinkingBlockOpen {
					emitClaudeEvent("content_block_start", map[string]any{
						"type":  "content_block_start",
						"index": blockIndex,
						"content_block": map[string]any{
							"type":     "thinking",
							"thinking": "",
						},
					})
					thinkingBlockOpen = true
					blockIndex++
				}
				emitClaudeEvent("content_block_delta", map[string]any{
					"type":  "content_block_delta",
					"index": blockIndex - 1,
					"delta": map[string]any{
						"type":     "thinking_delta",
						"thinking": rcStr,
					},
				})
			}
		}

		if c, ok := delta["content"]; ok && c != nil {
			contentStr, _ := c.(string)
			if contentStr != "" {
				closeThinkingBlock()
				if !textBlockOpen {
					emitClaudeEvent("content_block_start", map[string]any{
						"type":  "content_block_start",
						"index": blockIndex,
						"content_block": map[string]any{
							"type": "text",
							"text": "",
						},
					})
					textBlockOpen = true
					blockIndex++
				}
				emitClaudeEvent("content_block_delta", map[string]any{
					"type":  "content_block_delta",
					"index": blockIndex - 1,
					"delta": map[string]any{
						"type": "text_delta",
						"text": contentStr,
					},
				})
			}
		}

		if rawToolCalls, ok := delta["tool_calls"].([]any); ok {
			for _, rawTC := range rawToolCalls {
				tc, ok := rawTC.(map[string]any)
				if !ok {
					continue
				}
				idxFloat, _ := tc["index"].(float64)
				upstreamIndex := int(idxFloat)

				closeThinkingBlock()
				closeTextBlock()

				if _, exists := toolCallAccumulator[upstreamIndex]; !exists {
					callID, _ := tc["id"].(string)
					if callID == "" {
						callID = "toolu_" + randomString(12)
					}
					fn, _ := tc["function"].(map[string]any)
					name, _ := fn["name"].(string)
					toolCallAccumulator[upstreamIndex] = map[string]string{
						"id":   callID,
						"name": name,
						"args": "",
					}
					toolCallOrder = append(toolCallOrder, upstreamIndex)
					toolBlockIndices[upstreamIndex] = blockIndex
					emitClaudeEvent("content_block_start", map[string]any{
						"type":  "content_block_start",
						"index": blockIndex,
						"content_block": map[string]any{
							"type":  "tool_use",
							"id":    callID,
							"name":  name,
							"input": map[string]any{},
						},
					})
					blockIndex++
				}

				fn, _ := tc["function"].(map[string]any)
				if argDelta, ok := fn["arguments"].(string); ok && argDelta != "" {
					toolCallAccumulator[upstreamIndex]["args"] += argDelta
					emitClaudeEvent("content_block_delta", map[string]any{
						"type":  "content_block_delta",
						"index": toolBlockIndices[upstreamIndex],
						"delta": map[string]any{
							"type":         "input_json_delta",
							"partial_json": argDelta,
						},
					})
				}
			}
		}

		if finishReason == "stop" || finishReason == "length" || finishReason == "tool_calls" || finishReason == "function_call" || finishReason == "content_filter" {
			closeThinkingBlock()
			closeTextBlock()

			for _, idx := range toolCallOrder {
				acc := toolCallAccumulator[idx]
				emitClaudeEvent("content_block_stop", map[string]any{
					"type":  "content_block_stop",
					"index": toolBlockIndices[idx],
					"content_block": map[string]any{
						"type":  "tool_use",
						"id":    acc["id"],
						"name":  acc["name"],
						"input": map[string]any{},
					},
				})
			}

			stopReason := "end_turn"
			switch finishReason {
			case "length":
				stopReason = "max_tokens"
			case "tool_calls", "function_call":
				stopReason = "tool_use"
			case "content_filter":
				stopReason = "refusal"
			}

			emitClaudeEvent("message_delta", map[string]any{
				"type": "message_delta",
				"delta": map[string]any{
					"stop_reason": stopReason,
				},
				"usage": protocol.BuildClaudeDeltaUsage(fullUsage),
			})
			emitClaudeEvent("message_stop", map[string]any{
				"type": "message_stop",
			})
			return
		}
	}

	closeThinkingBlock()
	closeTextBlock()
	emitClaudeEvent("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": "end_turn"},
		"usage": protocol.BuildClaudeDeltaUsage(nil),
	})
	emitClaudeEvent("message_stop", map[string]any{"type": "message_stop"})
}

func indexOfInt(slice []int, val int) int {
	for i, v := range slice {
		if v == val {
			return i
		}
	}
	return 0
}

// ======================== Responses API ========================
