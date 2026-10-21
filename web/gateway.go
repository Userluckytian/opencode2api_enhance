package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	}
	return strings.TrimSpace(h)
}

type v1Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
}

// hit 归一化两种 usage 形态（OpenAI prompt_/completion_ 与 Anthropic input_/output_）。
// 判定条件：prompt+completion>0 或 total_tokens>0（至少输入/输出有个非零才算命中）。
func (u v1Usage) hit() (prompt, completion int) {
	p, c := u.PromptTokens, u.CompletionTokens
	if p == 0 {
		p = u.InputTokens
	}
	if c == 0 {
		c = u.OutputTokens
	}
	if p+c > 0 || u.TotalTokens > 0 {
		return p, c
	}
	return 0, 0
}

func (a *App) HandleV1(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	key := bearerToken(r)
	if key == "" {
		writeErr(w, http.StatusUnauthorized, "missing api key")
		return
	}
	now := time.Now()
	// 额度跨日自动重置：先刷新该 Key 的用量快照（跨 08:00 边界则归零）。
	// 读配置要加锁：设置接口会在写 token_quota 时持 a.Mu，无锁读会构成数据竞争。
	a.Mu.RLock()
	defaultQuota := a.Cfg.TokenQuota
	a.Mu.RUnlock()
	a.Store.RollQuota(defaultQuota, now)
	k, ok := a.Store.SnapshotByKey(key)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "invalid api key")
		return
	}
	if k.Disabled {
		writeErr(w, http.StatusForbidden, "key disabled")
		return
	}
	if isExpired(k, now) {
		writeErr(w, http.StatusForbidden, "key expired")
		return
	}
	if k.TokenUsed >= k.Quota {
		writeErr(w, http.StatusTooManyRequests, fmt.Sprintf(
			"token quota exhausted (used %d / %d), resets at %s",
			k.TokenUsed, k.Quota, nextDailyReset(now).Format("2006-01-02 15:04")))
		return
	}

	// /v1/models 由本地按 Key 权限应答，不再反代上游（finding 546：
	// 旧实现会把上游全部模型列表（含别名与提供方信息）泄露给受限 Key）。
	if isModelsPath(r) {
		a.writeModelsFor(w, k)
		return
	}
	// 同一路径但方法不是 GET/HEAD（如 POST /v1/models）：旧实现里会跳过上面的
	// 本地应答、继续往下走，最终被原样反代到上游的 /v1/models，从而绕过
	// writeModelsFor 的按 Key 过滤、把上游全量模型列表泄露给受限 Key。
	// 这里显式 405 拦掉，不给非 GET/HEAD 方法任何反代机会。
	if isModelsPathAnyMethod(r) {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed on /v1/models")
		return
	}

	// 读取 body 用于模型匹配
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxUpstreamBodyBytes+1))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "cannot read body")
		return
	}
	if int64(len(raw)) > maxUpstreamBodyBytes {
		writeErr(w, http.StatusRequestEntityTooLarge, "request body too large")
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	// 模型白名单校验（finding 546 + 残余缺口 fail-closed 补齐）。
	//
	// 旧实现把整个校验包在 `if hasModel { ... }` 里：请求体不带 model / model 为
	// 空串 / model 为 null 时整段跳过，**网关侧没有任何授权控制**，请求被原样
	// 转发上游，只靠上游自己的 "model is required" 兜底（fact 708 实测）。
	// 现在改为无条件 fail-closed：
	//   1. resolveRequestedModel 严格解析出唯一、非空白的字符串 model；
	//      缺 model / 空串 / null / 纯空白 / 非字符串 / 重复键 / 大小写变体 /
	//      请求体非法 JSON / 顶层非对象 —— 一律 400 拒绝，**不再放行转发**；
	//   2. 解析出的 model 与 per-Key 白名单比对，不在授权范围 → 403；
	//   3. 校验用的 model 与转发的 body 同源（同一份 raw），不存在二次解析歧义。
	model, derr := resolveRequestedModel(raw)
	if derr != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body: "+derr.Error())
		return
	}
	a.Mu.RLock()
	allowedModels := append([]string(nil), a.Cfg.AllowedModels...)
	a.Mu.RUnlock()
	if !modelAllowedForKey(k, allowedModels, model) {
		writeErr(w, http.StatusForbidden, "model not allowed for this key")
		return
	}
	if !a.RateLimiter().Allow(k.ID, now) {
		writeErr(w, http.StatusTooManyRequests, "rate limit exceeded")
		return
	}

	// IP 窗口策略：token 或活跃时长先到先拦；被拦请求落日志，便于后台汇总。
	outputTPS := 0
	if a.IPs != nil {
		allowed, reason, resetAt := a.IPs.Check(ip, now)
		if !allowed {
			if !resetAt.IsZero() {
				secs := int(time.Until(resetAt).Seconds())
				if secs < 0 {
					secs = 0
				}
				w.Header().Set("Retry-After", strconv.Itoa(secs))
			}
			a.logBlocked(ip, k.ID, k.Model, reason, http.StatusTooManyRequests)
			writeErr(w, http.StatusTooManyRequests, reason)
			return
		}
		outputTPS = a.IPs.Policies().Effective(ip).OutputTPS
	}

	// 构造上游 URL：UpstreamBaseURL 形如 http://host/v1，请求路径 /v1/xx
	a.Mu.RLock()
	target := a.Cfg.UpstreamBaseURL + strings.TrimPrefix(r.URL.Path, "/v1")
	upstreamKey := a.Cfg.UpstreamAPIKey
	a.Mu.RUnlock()
	if q := r.URL.RawQuery; q != "" {
		target += "?" + q
	}
	outReq, err := http.NewRequestWithContext(r.Context(), r.Method, target, bytes.NewReader(raw))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "upstream request failed")
		return
	}
	outReq.Header = r.Header.Clone()
	outReq.Header.Del("Authorization") // 客户端的 sk- Key 不需要转发给上游
	if upstreamKey != "" {
		outReq.Header.Set("Authorization", "Bearer "+upstreamKey)
	}

	start := time.Now()
	resp, err := a.client().Do(outReq)
	ms := time.Since(start).Milliseconds()
	if err != nil {
		writeErr(w, http.StatusBadGateway, "upstream unreachable")
		return
	}
	defer resp.Body.Close()

	prompt, completion := a.drainUpstream(w, resp, newOutputThrottle(outputTPS, r.Context()))

	// 转发成功后原子扣减额度；并发超额时 Consume 返回 false，
	// 响应已发出无法收回，按现状记录日志（best-effort）。
	a.Store.ConsumeAt(k.ID, prompt+completion, now)
	if a.IPs != nil {
		a.IPs.Record(ip, prompt+completion, time.Since(start), time.Now())
	}
	_ = a.Logger.Write(LogEntry{
		Time:             time.Now().Format(time.RFC3339),
		IP:               ip,
		KeyID:            k.ID,
		Model:            k.Model,
		PromptTokens:     prompt,
		CompletionTokens: completion,
		Total:            prompt + completion,
		Status:           resp.StatusCode,
		Ms:               ms,
	})
}

func (a *App) logBlocked(ip, keyID, model, reason string, status int) {
	_ = a.Logger.Write(LogEntry{
		Time:   time.Now().Format(time.RFC3339),
		IP:     ip,
		KeyID:  keyID,
		Model:  model,
		Status: status,
		Error:  reason,
	})
}

// parseV1Usage 从 JSON 片段中提取 prompt/completion token 数。
// 兼容两种形态：usage 字段直接平铺，或嵌套在顶层 "usage" 对象内；
// 兼容两种命名：OpenAI 的 prompt_tokens/completion_tokens 与 Anthropic 的 input_tokens/output_tokens。
func parseV1Usage(data []byte) (prompt, completion int) {
	var u v1Usage
	if err := json.Unmarshal(data, &u); err == nil {
		if p, c := u.hit(); p+c > 0 {
			return p, c
		}
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err == nil {
		if use, ok := obj["usage"]; ok {
			var uu v1Usage
			if err := json.Unmarshal(use, &uu); err == nil {
				if p, c := uu.hit(); p+c > 0 {
					return p, c
				}
			}
		}
	}
	return 0, 0
}

// drainUpstream 把上游响应写回客户端，并尽最大努力解析 usage，返回 prompt/completion token 数。
func (a *App) drainUpstream(w http.ResponseWriter, resp *http.Response, th *outputThrottle) (prompt, completion int) {
	isStream := strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream")
	// 先复制响应头
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	if isStream {
		return a.drainSSE(w, resp.Body, th)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, 0
	}
	if text := responseText(body); text != "" {
		th.wait(estimateTokens(text))
	} else {
		th.wait(len(body) / 4)
	}
	_, _ = w.Write(body)
	return parseV1Usage(body)
}

func (a *App) drainSSE(w http.ResponseWriter, body io.Reader, th *outputThrottle) (prompt, completion int) {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "data: ") {
			content := strings.TrimPrefix(line, "data: ")
			if strings.TrimSpace(content) != "[DONE]" {
				if text := sseDeltaText([]byte(content)); text != "" {
					th.wait(estimateTokens(text))
				}
				p, c := parseV1Usage([]byte(content))
				if p+c > 0 {
					prompt, completion = p, c
				}
			}
		}
		_, _ = w.Write([]byte(line + "\n"))
		if w2, ok := w.(http.Flusher); ok {
			w2.Flush()
		}
	}
	return prompt, completion
}
