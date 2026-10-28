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
	if model == "" && len(allowed) > 0 {
		model = allowed[0]
	}
	if model == "" {
		writeErr(w, http.StatusBadRequest, "没有可用模型，请先在模型管理中勾选")
		return
	}
	if len(allowed) > 0 && !contains(allowed, model) {
		writeErr(w, http.StatusBadRequest, "模型不在白名单内")
		return
	}
	// 与管理面板一致：model 可能带上游前缀（如 backup/gpt-4o），
	// 按同一套规则解析出目标上游与真实模型名。
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

	ctx := a.buildAskContext(now)
	system := "你是 token 自助餐厅的运营分析助手。只依据下面提供的统计数据回答，不要编造数据；" +
		"回答用中文，先给结论，再给 1-3 条可执行建议，数字用简洁格式。" +
		"输出适合在窄栏阅读：段落不超过 3 行，避免宽表格，枚举项用短句列出。数据不足时直接说明。\n\n" + ctx
	payload := map[string]any{
		"model": forwardModel,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": req.Question},
		},
		"temperature": 0.3,
		"stream":      false,
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
	start := time.Now()
	resp, err := a.client().Do(httpReq)
	if err != nil {
		a.logAsk(r, model, req.Question, "", 0, 0, 0, time.Since(start), false, "上游不可达: "+err.Error())
		writeErr(w, http.StatusBadGateway, "上游不可达: "+err.Error())
		return
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
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
				Content any `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage v1Usage `json:"usage"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		a.logAsk(r, model, req.Question, "", 0, 0, 0, time.Since(start), false, "上游响应无法解析")
		writeErr(w, http.StatusBadGateway, "上游响应无法解析")
		return
	}
	answer := ""
	if len(out.Choices) > 0 {
		switch c := out.Choices[0].Message.Content.(type) {
		case string:
			answer = c
		case []any:
			for _, part := range c {
				if m, ok := part.(map[string]any); ok {
					if s, ok := m["text"].(string); ok {
						answer += s
					}
				}
			}
		}
	}
	if strings.TrimSpace(answer) == "" {
		a.logAsk(r, model, req.Question, "", 0, 0, 0, time.Since(start), false, "上游未返回内容")
		writeErr(w, http.StatusBadGateway, "上游未返回内容")
		return
	}
	promptTokens, completionTokens := out.Usage.hit()
	a.logAsk(r, model, req.Question, answer, promptTokens, completionTokens, promptTokens+completionTokens, time.Since(start), true, "")
	writeJSON(w, http.StatusOK, map[string]any{"answer": answer, "model": model})
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
