package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const askMaxQuestion = 2000

// buildAskContext 生成给模型的统计上下文（只含聚合数据，不含密钥）。
func (a *App) buildAskContext(now time.Time) string {
	keys := a.Store.List()
	active, expired, disabled, exhausted := 0, 0, 0, 0
	for _, k := range keys {
		if k.Disabled {
			disabled++
		}
		if isExpired(k, now) {
			expired++
		}
		if k.TokenUsed >= k.Quota {
			exhausted++
		}
		if !k.Disabled && !isExpired(k, now) && k.TokenUsed < k.Quota {
			active++
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "当前时间：%s\n", now.Format("2006-01-02 15:04"))
	fmt.Fprintf(&b, "Key 总数 %d：可用 %d，已过期 %d，已禁用 %d，额度用尽 %d。\n", len(keys), active, expired, disabled, exhausted)
	// 默认额度是全局唯一配置项（config.token_quota），每日 08:00 重置后各 Key 恢复到该值。
	a.Mu.RLock()
	defQuota := a.Cfg.TokenQuota
	a.Mu.RUnlock()
	if defQuota > 0 {
		fmt.Fprintf(&b, "每个 Key 的默认额度：%d token（每日 08:00 自动重置回该额度）。\n", defQuota)
	}

	today := now.Format("2006-01-02")
	logs, _ := a.Logger.ReadLogs(today, "")
	req, tok, errs, blocked, ips := summarizeLogs(logs)
	fmt.Fprintf(&b, "今日：请求 %d 次，token %d，错误 %d 次，策略拦截 %d 次，活跃 IP %d 个。\n", req, tok, errs, blocked, len(ips))

	b.WriteString("最近 7 天：\n")
	for _, d := range a.dailyTrend(7, now) {
		fmt.Fprintf(&b, "- %s：请求 %d，token %d，错误 %d\n", d.Date, d.Requests, d.Tokens, d.Errors)
	}

	aggs := aggregateByIP(logs)
	if len(aggs) > 10 {
		aggs = aggs[:10]
	}
	b.WriteString("今日 IP 用量前 10：\n")
	for _, g := range aggs {
		fmt.Fprintf(&b, "- %s：请求 %d，token %d，Key %d 个，错误 %d，被拦截 %d\n",
			g.IP, g.Requests, g.TotalTokens, g.Keys, g.Errors, g.Blocked)
	}

	if a.IPs != nil {
		d := a.IPs.Policies().Defaults()
		fmt.Fprintf(&b, "IP 默认策略：每 %d 分钟窗口最多 %d token 或 %d 分钟活跃时长（先到先拦），输出限速 %d token/秒；按 IP 覆盖 %d 条。\n",
			d.WindowMinutes, d.WindowTokens, d.MaxActiveMinutes, d.OutputTPS, len(a.IPs.Policies().ListOverrides()))
	}
	b.WriteString("配额换算参考：周预算 2000 万 token ≈ 日均 257 万（按 90% 利用率），若每天约 6 个活跃 IP，则单 IP 日窗口建议约 43 万。\n")
	return b.String()
}

func (a *App) handleAsk(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Question string `json:"question"`
		Model    string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	req.Question = strings.TrimSpace(req.Question)
	if req.Question == "" {
		writeErr(w, http.StatusBadRequest, "问题不能为空")
		return
	}
	if len([]rune(req.Question)) > askMaxQuestion {
		writeErr(w, http.StatusBadRequest, "问题过长（最多 2000 字）")
		return
	}

	a.Mu.RLock()
	allowed := append([]string(nil), a.Cfg.AllowedModels...)
	a.Mu.RUnlock()
	model := strings.TrimSpace(req.Model)
	// 未指定模型时回退到白名单第一个（保持旧行为）。
	if model == "" && len(allowed) > 0 {
		model = allowed[0]
	}
	if model == "" {
		writeErr(w, http.StatusBadRequest, "没有可用模型：请在下拉框选择模型，或先在模型管理中勾选")
		return
	}
	// AI 问答是管理员自用，模型可来自任意已启用上游（下拉框列出的是上游实际提供的
	// 全部模型），因此不再强制要求在白名单内——白名单只约束发给使用者的 Key。
	// 目标上游是否可用仍由 Resolve 判定。
	up, forwardModel, uerr := a.Resolve(model)
	if uerr != nil {
		writeErr(w, http.StatusServiceUnavailable, "没有可用的上游实例: "+uerr.Error())
		return
	}
	upstream := strings.TrimRight(up.BaseURL, "/")
	apiKey := up.APIKey

	now := time.Now()
	// 管理员自用的 AI 问数不设额度闸门（IP 策略只管 /v1 使用者流量），
	// 但会计量并写入业务日志，便于回看与统计消耗。

	// 提示词（2026-10-28 改为工具驱动）：
	// 不再由服务端把统计数字汇总好塞进 prompt，而是告诉模型「有哪些数据源、
	// 各自在哪、字段是什么」，并给它一组只读查询工具，让它自己决定查什么、
	// 查多细。这样它能下钻到状态码/模型/具体错误，而不是只会复述汇总数字。
	system := "你是 token 自助餐厅的运营分析助手。你可以调用工具查询本服务的运行数据（只读），" +
		"也可以在用户明确询问「该怎么配」时提议配置修改。\n" +
		"规则：\n" +
		"1. 先调用 list_data_sources 了解有哪些数据源、字段含义；不确定就查，不要凭猜测回答。\n" +
		"2. 用最少必要的查询回答：先聚合（aggregate_call_logs）看分布，需要细节再查明细（query_call_logs）。\n" +
		"3. 只依据工具返回的真实数据作答，不要编造数值；工具返回「无数据」就如实说明。\n" +
		"4. 先给结论，再给 1-3 条可执行建议；数字用简洁格式。\n" +
		"5. 输出适合窄栏阅读：段落不超过 3 行，避免宽表格，枚举项用短句。\n" +
		"6. 当用户问「建议设成多少」「帮我改」这类问题时：先用只读工具查当前值与实际用量，" +
		"算出建议值，然后调用 propose_config_change 提议修改（可一次提议多项）。" +
		"该工具只记录建议、不会立即生效，管理员会在界面上确认；所以你可以放心提议，" +
		"但必须在 reason 里写明计算依据（引用你查到的数据）。不要自己在回答里假装已经改好了。"
	userMsg := req.Question

	msgs := []map[string]any{
		{"role": "system", "content": system},
		{"role": "user", "content": userMsg},
	}

	// 工具调用循环：最多 askMaxToolRounds 轮，避免模型无限查下去。
	// 工具调用轮次上限：一轮 = 一次上游请求。模型常见路径是
	// 「列数据源 → 聚合 2~3 个维度 → 查明细 → 给结论」，约 5~7 轮；
	// 留到 10 轮以容纳下钻更深的分析，同时防止失控。
	const askMaxToolRounds = 10
	start := time.Now()
	var totalPrompt, totalCompletion int
	var answer string
	var lastToolLog []string
	var suggestions []askSuggestion

	for round := 0; round < askMaxToolRounds; round++ {
		payload := map[string]any{
			"model":       forwardModel,
			"messages":    msgs,
			"temperature": 0.3,
			"stream":      false,
			"tools":       askToolPayload(),
			"tool_choice": "auto",
		}
		body, _ := json.Marshal(payload)
		httpReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstream+"/chat/completions", bytes.NewReader(body))
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "构造请求失败")
			return
		}
		httpReq.Header.Set("Content-Type", "application/json")
		if apiKey != "" {
			httpReq.Header.Set("Authorization", "Bearer "+apiKey)
		}

		resp, err := a.client().Do(httpReq)
		if err != nil {
			a.logAsk(r, model, req.Question, "", 0, 0, 0, time.Since(start), false, "上游不可达: "+err.Error())
			writeErr(w, http.StatusBadGateway, "上游不可达: "+err.Error())
			return
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			msg := strings.TrimSpace(string(raw))
			if len(msg) > 500 {
				msg = msg[:500]
			}
			a.logAsk(r, model, req.Question, "", 0, 0, 0, time.Since(start), false, fmt.Sprintf("上游返回 %d", resp.StatusCode))
			writeErr(w, http.StatusBadGateway, fmt.Sprintf("上游返回 %d: %s", resp.StatusCode, msg))
			return
		}

		var out struct {
			Choices []struct {
				Message struct {
					Content   any              `json:"content"`
					ToolCalls []askToolCallOut `json:"tool_calls"`
				} `json:"message"`
			} `json:"choices"`
			Usage v1Usage `json:"usage"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			a.logAsk(r, model, req.Question, "", 0, 0, 0, time.Since(start), false, "上游响应无法解析")
			writeErr(w, http.StatusBadGateway, "上游响应无法解析")
			return
		}
		if p, c := out.Usage.hit(); p+c > 0 {
			totalPrompt += p
			totalCompletion += c
		}
		if len(out.Choices) == 0 {
			a.logAsk(r, model, req.Question, "", totalPrompt, totalCompletion, totalPrompt+totalCompletion, time.Since(start), false, "上游未返回 choices")
			writeErr(w, http.StatusBadGateway, "上游未返回内容")
			return
		}
		choice := out.Choices[0].Message
		text := extractAskText(choice.Content)

		// 没有工具调用 = 最终回答，结束循环。
		if len(choice.ToolCalls) == 0 {
			answer = text
			break
		}

		// 把模型这一轮（可能含文本 + 工具调用）追加进对话，再逐个执行工具。
		assistantMsg := map[string]any{"role": "assistant"}
		if text != "" {
			assistantMsg["content"] = text
		}
		calls := make([]map[string]any, 0, len(choice.ToolCalls))
		for _, tc := range choice.ToolCalls {
			calls = append(calls, map[string]any{
				"id":   tc.ID,
				"type": "function",
				"function": map[string]any{
					"name":      tc.Function.Name,
					"arguments": tc.Function.Arguments,
				},
			})
		}
		assistantMsg["tool_calls"] = calls
		msgs = append(msgs, assistantMsg)

		for _, tc := range choice.ToolCalls {
			args := map[string]any{}
			if strings.TrimSpace(tc.Function.Arguments) != "" {
				_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
			}
			var result string
			if tc.Function.Name == "propose_config_change" {
				// 只记录建议，不写配置；写入由管理员点「应用」触发。
				sug, serr := buildAskSuggestion(a, args)
				if serr != nil {
					result = "建议被拒绝: " + serr.Error()
				} else {
					suggestions = append(suggestions, sug)
					result = fmt.Sprintf("已记录建议（尚未生效，需管理员确认）：%s %s → %s",
						sug.Label, sug.Current, sug.Proposed)
				}
			} else {
				var terr error
				result, terr = a.runAskTool(tc.Function.Name, args, now)
				if terr != nil {
					result = "调用失败: " + terr.Error()
				}
			}
			lastToolLog = append(lastToolLog, tc.Function.Name+"("+jsonArgString(args)+")")
			msgs = append(msgs, map[string]any{
				"role":         "tool",
				"tool_call_id": tc.ID,
				"name":         tc.Function.Name,
				"content":      result,
			})
		}
	}

	if strings.TrimSpace(answer) == "" {
		a.logAsk(r, model, req.Question, "", totalPrompt, totalCompletion, totalPrompt+totalCompletion, time.Since(start), false, "工具调用轮次用尽或上游未返回内容")
		writeErr(w, http.StatusBadGateway, "分析未完成：模型在限定轮次内未给出结论，请重试或换个模型")
		return
	}
	a.logAsk(r, model, req.Question, answer, totalPrompt, totalCompletion, totalPrompt+totalCompletion, time.Since(start), true, "")
	writeJSON(w, http.StatusOK, map[string]any{
		"answer":      answer,
		"model":       model,
		"tool_calls":  lastToolLog,
		"suggestions": suggestions,
	})
}

// askToolCallOut 是上游返回的 tool_calls 元素。
type askToolCallOut struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// extractAskText 从 content 字段提取纯文本（兼容字符串与 parts 数组）。
func extractAskText(content any) string {
	switch c := content.(type) {
	case string:
		return c
	case []any:
		var b strings.Builder
		for _, part := range c {
			if m, ok := part.(map[string]any); ok {
				if s, ok := m["text"].(string); ok {
					b.WriteString(s)
				}
			}
		}
		return b.String()
	}
	return ""
}

// logAsk 记录一次 AI 问数：写业务日志并计入每日额度。
func (a *App) logAsk(r *http.Request, model, question, answer string, prompt, completion, total int, dur time.Duration, ok bool, errMsg string) {
	if a.Ask == nil {
		return
	}
	entry := AskEntry{
		Time:             time.Now().Format(time.RFC3339),
		Question:         question,
		Answer:           answer,
		Model:            model,
		PromptTokens:     prompt,
		CompletionTokens: completion,
		Total:            total,
		Ms:               dur.Milliseconds(),
		OK:               ok,
		Error:            errMsg,
	}
	_ = a.Ask.WriteAskLog(entry)
	// 上游没给 usage 时用「字符估算」兜底，保证额度不会被绕过。
	if total <= 0 {
		total = estimateTokens(answer) + estimateTokens(question)
	}
	a.Ask.Record(total, question, time.Now())
}
