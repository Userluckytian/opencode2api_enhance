// Part of the P1 (core split) refactor: code moved out of main.go.
// Same package (main) - do not change package clause manually.
package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"
)

func chatCompletionsHandler(w http.ResponseWriter, r *http.Request) {
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
	slog.Debug("chat completion request body", "count", cnt, "body", string(body))

	var req OpenAIRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	// opencode 上游恒为无 key 免费档（见 chatViaVendor）→ 模型解析恒优先 -free 变体，
	// 客户端带任何 key 都不影响落到免费模型。
	req.Model = resolveModel(req.Model, true)
	if req.Model == "" {
		http.Error(w, "model is required", http.StatusBadRequest)
		return
	}

	// 全流程调用日志：记录每个请求的决策链（网关模式下）
	startTime := time.Now()
	callRec := CallRecord{
		ReqID:     getReqID(r.Context()),
		TS:        time.Now().Format(time.RFC3339),
		Path:      r.URL.Path,
		Model:     req.Model,
		Stream:    req.Stream,
		RouteMode:  routeMode.Load().(string),
		Tier:       tierOfAuth(auth),
		ServingPort: port,
		TraceID:     getTraceID(r.Context()),
		Status:     "ok",
	}
	if callRec.ReqID == "" {
		callRec.ReqID = "req_" + randomString(12)
	}
	if callRec.TraceID == "" {
		callRec.TraceID = callRec.ReqID
	}

	// 多模态路由：检测到图片时转发到配置的上游

	req.Messages = fixToolCallGaps(req.Messages)
	keepReasoning := wantsReasoning(&req)
	req.Messages = ensureReasoningContent(req.Messages, keepReasoning)
	if req.Stream {
		if req.ExtraBody == nil {
			req.ExtraBody = map[string]any{}
		}
		req.ExtraBody["stream_options"] = map[string]any{"include_usage": true}
	}
	upstreamBody := buildUpstreamBody(&req)

	// auto 虚拟模型：选主选 + 降级链挂 ctx（非 auto / 未启用 = 零开销原路径）。
	callCtx := r.Context()
	var autoDec *autoDecision
	if isAutoModelName(req.Model) {
		var autoErr error
		callCtx, req.Model, autoDec, autoErr = prepareAuto(r.Context(), req.Model, upstreamBody)
		if autoErr != nil {
			// auto 已启用但无可用候选：直接回客户端明确错误（不落到默认厂商撞 502/404）。
			callRec.Status = "fail"
			callRec.ErrMsg = autoErr.Error()
			callRec.DurationMS = time.Since(startTime).Milliseconds()
			recordCall(callRec)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": autoErr.Error(), "type": "auto_unconfigured"}})
			return
		}
		if autoDec != nil {
			callRec.Events = append(callRec.Events, autoDec.pickEvent())
		}
	}

	if req.Stream {
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
		upResp, status, _, proxyAddr, err := callOpenCodeAPIStream(callCtx, upstreamBody, req.Model, &auth)
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
			callRec.Events = append(callRec.Events, CallEvent{Type: "upstream_error", Node: proxyAddr, Detail: callRec.ErrMsg, At: time.Now()})
			if autoDec == nil {
				recordModelFeedback(req.Model, proxyAddr, false, time.Since(startTime).Milliseconds())
			}
			recordCall(callRec)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(httpStatusOr(status))
			if len(errBody) > 0 {
				w.Write(errBody)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "upstream error", "type": "upstream_error"}})
			return
		}
		callRec.Events = append(callRec.Events, CallEvent{Type: "connect_ok", Node: proxyAddr, Detail: "connected", At: time.Now()})
		defer upResp.Close()
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)
		// 流内超时 + 断点续写切换（阶段1验证过的核心逻辑）
		res := streamWithResume(w, r, upstreamBody, req.Model, auth, upResp, proxyAddr, keepReasoning, &callRec)
		callRec.DurationMS = time.Since(startTime).Milliseconds()
		if autoDec == nil {
			recordModelFeedback(req.Model, lastNode(callRec), res.OK, callRec.DurationMS)
		}
		if res.PromptTok > 0 || res.Completion > 0 {
			callRec.PromptTok = res.PromptTok
			callRec.CompletionTok = res.Completion
			recordTokenUsage(req.Model, res.PromptTok, res.Completion, res.PromptTok+res.Completion, proxyAddr)
		}
		if !res.OK {
			callRec.Status = "fail"
			if res.ErrMsg != "" {
				callRec.ErrMsg = res.ErrMsg
			}
			// 若未吐过 [DONE]，补错误事件
			w.Write([]byte("data: {\"error\":\"stream interrupted: " + res.ErrMsg + "\"}\n\n"))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		} else {
			callRec.Status = "ok"
		}
		recordCall(callRec)
		return
	}

	respBody, status, _, proxyAddr, err := callOpenCodeAPI(callCtx, upstreamBody, req.Model, auth)
	callRec.Nodes = append(callRec.Nodes, proxyAddr)
	if err != nil || status < 200 || status >= 300 {
		callRec.Status = "fail"
		callRec.ErrMsg = upstreamErrMsg(status, err, respBody)
		callRec.DurationMS = time.Since(startTime).Milliseconds()
		callRec.Events = append(callRec.Events, CallEvent{Type: "upstream_error", Node: proxyAddr, Detail: callRec.ErrMsg, At: time.Now()})
		if autoDec != nil && len(respBody) > 0 && isContextLimitError(respBody) {
			// 上下文护栏学习：最终尝试的模型在此估算量级下确认装不下，收紧其上限。
			learnContextFailure(displayModelName(autoDec.FinalModel), autoDec.EstTokens)
		}
		if autoDec == nil {
			recordModelFeedback(req.Model, proxyAddr, false, callRec.DurationMS)
		}
		recordCall(callRec)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(httpStatusOr(status))
		if len(respBody) > 0 {
			w.Write(respBody)
		} else {
			json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "upstream error", "type": "upstream_error"}})
		}
		return
	}
	outBody := respBody
	// 非流式：解析一次，同时完成 usage 提取与响应转换（避免双重 JSON 解析）。
	var usageResp map[string]any
	if json.Unmarshal(respBody, &usageResp) != nil && len(respBody) > 0 {
		// 上游 2xx 但响应体不是合法 JSON——记录原始内容便于诊断（如 HTML 重定向页、空 body）。
		snippet := string(respBody)
		if len(snippet) > 500 {
			snippet = snippet[:500] + "...(truncated)"
		}
		slog.Warn("upstream 2xx but body is not valid JSON",
			"model", req.Model, "status", status, "node", proxyAddr,
			"body_len", len(respBody), "body_snippet", snippet)
	}
	if json.Unmarshal(respBody, &usageResp) == nil {
		if u, ok := usageResp["usage"].(map[string]any); ok {
			pt, _ := u["prompt_tokens"].(float64)
			ct, _ := u["completion_tokens"].(float64)
			tt, _ := u["total_tokens"].(float64)
			if tt > 0 {
				recordTokenUsage(req.Model, int64(pt), int64(ct), int64(tt), proxyAddr)
			}
			callRec.PromptTok = int64(pt)
			callRec.CompletionTok = int64(ct)
		}
		if conv, err := convertResponseFromObj(usageResp, keepReasoning); err == nil {
			outBody = conv
		}
	}
	callRec.DurationMS = time.Since(startTime).Milliseconds()
	callRec.Events = append(callRec.Events, CallEvent{Type: "complete", Node: proxyAddr, Detail: "done", At: time.Now()})
	if autoDec == nil {
		recordModelFeedback(req.Model, proxyAddr, true, callRec.DurationMS)
	}
	recordCall(callRec)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(outBody)
}

// ======================== Models Handler ========================

func listModelsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	modelMu.RLock()
	loaded, models := modelsLoaded, modelsCache
	modelMu.RUnlock()
	// 目录未就绪（启动后首请求早于首次聚合刷新）→ 走聚合器路径同步拉取一次。
	// 聚合器是唯一数据源：不保留直连上游的兜底（双轨已消灭）。
	// 节流版：上游故障导致目录持续为空时，至多每 10s 重拉一轮（防每请求惊群）。
	if !loaded || len(models) == 0 {
		refreshModelCatalogIfDue()
		modelMu.RLock()
		loaded, models = modelsLoaded, modelsCache
		modelMu.RUnlock()
	}
	_ = loaded
	// 保存别名快照；目录权限仍按真实上游模型判断，最后再替换为客户端可见名称。
	configMu.RLock()
	aliases := make(map[string]string, len(modelAlias))
	for alias, upstream := range modelAlias {
		aliases[alias] = upstream
	}
	configMu.RUnlock()

	// 仅返回免费可用模型（产品定位：免费模型聚合；付费模型不带 key 调用必 401，不展示）。
	// 不做按 key 的鉴权分流——任何客户端拿到的都是同一份"免费目录"。
	modelMu.RLock()
	var combinedModels []ModelInfo
	for _, model := range models {
		if isFreeModel(model.ID) {
			combinedModels = append(combinedModels, model)
		}
	}
	for _, goModel := range goModelsCache {
		if isFreeModel(goModel.ID) && !containsModelWithID(combinedModels, goModel.ID) {
			combinedModels = append(combinedModels, goModel)
		}
	}
	modelMu.RUnlock()

	allModels := replaceModelIDsWithAliases(combinedModels, aliases)
	// 多厂商聚合：把其它厂商（非 opencode）的免费模型并入列表（同名加厂商前缀）。
	allModels = appendOtherFreeModels(allModels, globalAgg)

	// 空判定放在合并自定义源之后：仅有自定义源（基础目录为空）时也应正常返回。
	if len(allModels) == 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{
			"error": "无法获取模型列表，请检查上游服务是否可用",
		})
		return
	}

	// auto 虚拟模型置顶（仅开启且有可用候选后可见；空列表/关闭不出现，
	// 避免客户端缓存无效模型名——2026-08-26 白名单化改造：勾选模型才展示）。
	if autoHasCandidates() {
		allModels = append([]ModelInfo{{
			ID:      autoModelName(),
			Object:  "model",
			Created: time.Now().Unix(),
			OwnedBy: "gateway",
		}}, allModels...)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"object": "list",
		"data":   allModels,
	})
}

func replaceModelIDsWithAliases(models []ModelInfo, aliases map[string]string) []ModelInfo {
	aliasesByUpstream := make(map[string][]string, len(aliases))
	for alias, upstream := range aliases {
		alias = strings.TrimSpace(alias)
		upstream = strings.TrimSpace(upstream)
		if alias == "" || upstream == "" {
			continue
		}
		aliasesByUpstream[upstream] = append(aliasesByUpstream[upstream], alias)
	}
	for upstream := range aliasesByUpstream {
		sort.Strings(aliasesByUpstream[upstream])
	}

	result := make([]ModelInfo, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		visibleIDs := aliasesByUpstream[model.ID]
		if len(visibleIDs) == 0 {
			// 自动兜底：未配置别名的 -free 模型，展示名去掉 -free 后缀
			// （内部请求仍用原名；显式别名优先）。
			if strings.HasSuffix(model.ID, "-free") {
				visibleIDs = []string{strings.TrimSuffix(model.ID, "-free")}
			} else {
				visibleIDs = []string{model.ID}
			}
		}
		for _, visibleID := range visibleIDs {
			if _, exists := seen[visibleID]; exists {
				continue
			}
			visibleModel := model
			visibleModel.ID = visibleID
			if visibleID != model.ID {
				visibleModel.OwnedBy = "alias"
			}
			result = append(result, visibleModel)
			seen[visibleID] = struct{}{}
		}
	}
	return result
}
