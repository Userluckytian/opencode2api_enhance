// 聊天实现：迁移自 package main 的 upstream.go / convert.go（Anthropic 兼容部分）。
// 适配点：会话由 Vendor 实例持有；HTTP 客户端经 contract.Transport 注入；
// 认证路由（public/auto/zen/go）与最大重试次数从 contract.Message.Options 读取。
package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/6Kmfi6HP/opencode2api/core/contract"
)

// Options 键（补充）：最大上游重试次数。
const KeyMaxRetries = "_oc_max_retries"

const (
	maxUpstreamRetries = 3
	max401Retries      = 3
)

// authMode 与 authT 是 opencode 上游的认证路由语义（public / auto / zen / go）。
type authMode int

const (
	authPublic authMode = iota
	authAuto
	authZen
	authGo
)

type authT struct {
	token string
	mode  authMode
}

func (a authT) tier() contract.Tier {
	if a.mode == authPublic {
		return contract.TierFree
	}
	return contract.TierPaid
}

func (a authT) authHeader() string {
	if a.mode == authPublic {
		return "Bearer public"
	}
	return "Bearer " + a.token
}

// parseAuth 从 Message.Extra 还原上游认证路由。
func parseAuth(msg *contract.Message) authT {
	mode := authAuto
	switch s, _ := msg.Extra[KeyAuthMode].(string); s {
	case "public":
		mode = authPublic
	case "auto":
		mode = authAuto
	case "zen":
		mode = authZen
	case "go":
		mode = authGo
	}
	token, _ := msg.Extra[KeyAuthToken].(string)
	if mode == authAuto && token == "" {
		mode = authPublic
	}
	return authT{token: token, mode: mode}
}

func maxRetriesOf(msg *contract.Message) int {
	if n, ok := msg.Extra[KeyMaxRetries].(int); ok && n > 0 {
		return n
	}
	return maxUpstreamRetries
}

// ---------------------------------------------------------------- 模型集合

// modelIDsOnSurface 返回指定 surface 的模型 ID 列表（来自厂商目录缓存）。
func (v *Vendor) modelIDsOnSurface(surface string) []string {
	v.modelMu.RLock()
	defer v.modelMu.RUnlock()
	var out []string
	for _, m := range v.cacheAll {
		if m.Meta != nil && m.Meta["surface"] == surface {
			out = append(out, m.ID)
		}
	}
	return out
}

func (v *Vendor) hasModelOnSurface(modelID, surface string) bool {
	v.modelMu.RLock()
	defer v.modelMu.RUnlock()
	for _, m := range v.cacheAll {
		if m.ID == modelID && m.Meta != nil && m.Meta["surface"] == surface {
			return true
		}
	}
	return false
}

func (v *Vendor) goOnlyModel(modelID string) bool {
	return v.hasModelOnSurface(modelID, surfaceGo) && !v.hasModelOnSurface(modelID, surfaceZen)
}

func (a authT) useGoEndpoint(v *Vendor, modelID string) bool {
	switch a.mode {
	case authGo:
		return v.hasModelOnSurface(modelID, surfaceGo)
	case authAuto:
		return v.goOnlyModel(modelID)
	default:
		return false
	}
}

// ---------------------------------------------------------------- 请求构造

func (v *Vendor) buildRequest(modelID string, bodyMap map[string]any, a authT) (*http.Request, error) {
	bodyMap["model"] = modelID
	delete(bodyMap, "reasoning_effort")
	tryBody, err := json.Marshal(bodyMap)
	if err != nil {
		return nil, err
	}
	upstreamURL := "https://opencode.ai/zen/v1/chat/completions"
	if a.useGoEndpoint(v, modelID) {
		upstreamURL = "https://opencode.ai/zen/go/v1/chat/completions"
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
	req.Header.Set("Accept", "application/json")
	return req, nil
}

// isRetryable 判定某状态码是否应在本厂商内重试。
// 以 ErrSemantics().Retryable 为唯一状态码来源（契约驱动），另附通用 5xx 兜底
// （历史行为：任意 5xx 一律可重试）。401 走独立计数上限（见 call）。
func (v *Vendor) isRetryable(status int) bool {
	for _, s := range v.ErrSemantics().Retryable {
		if s == status {
			return true
		}
	}
	return status >= 500 && status < 600
}

// ---------------------------------------------------------------- 聊天

// callResult 是一次上游调用的内部结果：非流式成功填 body，流式成功填 stream。
type callResult struct {
	body     []byte        // 非流式：完整响应体（成功或最后一次错误体）
	stream   io.ReadCloser // 流式：SSE 响应流
	status   int           // 最后一次 HTTP 状态（0 = 未获得响应）
	headers  http.Header   // 最后一次响应头（仅非流式路径保留）
	nodeAddr string        // 实际出口节点地址（直连为空）
}

// call 是 Chat / ChatStream 的公共实现：同一套会话/认证/重试/401/端点语义。
// 行为与历史逐项对齐（含 401 独立重试上限、可重试时 CloseIdleConnections、
// 非流式错误带体返回、流式错误体包装为流返回）。
func (v *Vendor) call(ctx context.Context, msg *contract.Message, streaming bool) (*callResult, error) {
	v.sessionID()

	raw, ok := msg.Extra[KeyRawBody].([]byte)
	if !ok {
		return nil, fmt.Errorf("opencode: missing %s in message extra", KeyRawBody)
	}
	var bodyMap map[string]any
	if err := json.Unmarshal(raw, &bodyMap); err != nil {
		// 与历史一致：请求体不可解析 → 400 响应，不视为传输失败。
		if streaming {
			return &callResult{status: http.StatusBadRequest, stream: io.NopCloser(bytes.NewReader(nil))}, nil
		}
		return &callResult{status: http.StatusBadRequest}, nil
	}
	a := parseAuth(msg)
	modelID := msg.Model
	maxRetries := maxRetriesOf(msg)

	// Responses-only 模型（muse-spark 系列）走 /zen/v1/responses（付费 go 面走 /zen/go/v1/responses）。
	// 命中时请求构造与 2xx 响应都在厂商内完成 Responses ↔ chat 翻译，下游契约不变。
	respURL, useResponses := v.responsesEndpoint(modelID, a)

	// 免费通道 body 级门禁（2026-09-18）：强制 stream:true + 补齐 bash/glob/grep/read 工具名。
	// 仅匿名 public 通道生效；付费通道（zen/go 带 key）请求形态保持不变。
	if a.mode == authPublic {
		ensureAgentGate(bodyMap, !useResponses)
	}

	var lastBody []byte
	var lastStatus int
	var lastHeader http.Header
	var lastProxyAddr string
	var lastErr error
	var retryCount, retry401Count, retry429Count int

	for retryCount <= maxRetries {
		var up *http.Request
		var err error
		if useResponses {
			up, err = v.buildResponsesRequest(respURL, modelID, bodyMap, streaming, a)
		} else {
			up, err = v.buildRequest(modelID, bodyMap, a)
		}
		if err != nil {
			lastErr = err
			break
		}
		// 请求携带调用方 ctx：单发/重试链的 client.Do 同样感知客户端断开
		// （竞速候选由 raceDo 内部按候选派生 ctx，不在此列）。
		up = up.WithContext(ctx)
		tr := v.transport()
		// P2b 请求级竞速：首轮并行扇出 N 个候选，首个 2xx（流式 = 首个 chunk 到达）胜出。
		// G1：付费层（TierPaid）跳过竞速直接走单发（与单发路径一致，付费 token 不走代理池扇出）。
		racer, hasRacer := tr.(contract.Racer)
		var client *http.Client
		var resp *http.Response
		var proxyAddr string
		if retryCount == 0 && a.tier() == contract.TierFree && hasRacer && v.raceCopies() > 1 && !v.inRateLimitCooldown() {
			resp, proxyAddr, err = v.raceDo(ctx, racer, up, streaming, a.tier(), v.raceCopies(), tr.Mark)
			if err == nil && resp == nil {
				// 竞速无候选：退化普通单发。
				client, proxyAddr = tr.Client(a.tier(), streaming)
				resp, err = client.Do(up)
			}
		} else {
			client, proxyAddr = tr.Client(a.tier(), streaming)
			resp, err = client.Do(up)
		}
		if err != nil {
			// G32：客户端断开（ctx.Canceled）不是节点链路失败——all-fail 竞速返回的
			// Canceled 连带真实 addr 在此会被误标冷却/熔断，统一排除；真实网络错误仍标记。
			if !errors.Is(err, context.Canceled) {
				tr.Mark(proxyAddr, 0, err)
			}
			lastErr = err
			retryCount++
			continue
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			tr.Mark(proxyAddr, resp.StatusCode, nil)
			// 诊断：记录上游响应 Content-Type，便于排查非 JSON/SSE 响应。
			ct := resp.Header.Get("Content-Type")
			if !strings.HasPrefix(ct, "application/json") && !strings.HasPrefix(ct, "text/event-stream") && ct != "" {
				slog.Warn("upstream 2xx with unexpected Content-Type",
					"model", modelID, "status", resp.StatusCode, "content_type", ct, "node", proxyAddr)
			}
			if streaming {
				if useResponses {
					return &callResult{stream: v.wrapResponsesSSE(resp.Body, modelID), status: resp.StatusCode, nodeAddr: proxyAddr}, nil
				}
				return &callResult{stream: resp.Body, status: resp.StatusCode, nodeAddr: proxyAddr}, nil
			}
			b, readErr := io.ReadAll(resp.Body)
			resp.Body.Close()
			if readErr != nil {
				return nil, readErr
			}
			if useResponses {
				if isSSEBody(b) {
					// 免费通道强制 stream 后，非流式下游也会拿到 Responses SSE：
					// 先走既有 responses→chat SSE 翻译，再聚合为非流式 chat.completion。
					b = aggregateChatSSE(readAllCloser(v.wrapResponsesSSE(io.NopCloser(bytes.NewReader(b)), modelID)), modelID)
				} else {
					b = translateResponsesJSON(b, modelID)
				}
			} else if isAnthropicFormat(b) {
				b = convertAnthropicToOpenAI(b, modelID)
			} else if isSSEBody(b) {
				b = aggregateChatSSE(b, modelID)
			}
			return &callResult{body: b, status: resp.StatusCode, headers: resp.Header, nodeAddr: proxyAddr}, nil
		}
		errBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		tr.Mark(proxyAddr, resp.StatusCode, nil)
		slog.Error("opencode upstream error", "model", modelID, "status", resp.StatusCode, "body", string(errBody))
		lastBody = errBody
		lastStatus = resp.StatusCode
		lastHeader = resp.Header
		lastProxyAddr = proxyAddr
		lastErr = fmt.Errorf("upstream error")
		if v.isRetryable(resp.StatusCode) {
			if client != nil {
				client.CloseIdleConnections()
			}
			if resp.StatusCode == http.StatusTooManyRequests {
				// S2 429 感知：记录最近 429 时间戳（下个请求冷却内跳过竞速），
				// 重试前指数退避 sleep(min(base*2^n, cap))，不放大限流。
				// L3：退避感知 ctx——客户端断开立即返回取消错误，不硬睡最长 30s。
				v.last429.Store(time.Now().UnixNano())
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(v.rateLimitBackoff(retry429Count)):
				}
				retry429Count++
			}
			if resp.StatusCode == http.StatusUnauthorized {
				retry401Count++
				if retry401Count >= max401Retries {
					break
				}
			} else {
				retryCount++
				if retryCount >= maxRetries {
					break
				}
			}
			continue
		}
		if streaming {
			// 非可重试状态：把错误体包装成流返回（上游错误透传，不重试）。
			return &callResult{stream: io.NopCloser(bytes.NewReader(lastBody)), status: lastStatus, nodeAddr: lastProxyAddr}, nil
		}
		break
	}
	// S2 可见报错：429 最终失败用中文文案替换上游原始错误体，status 保持 429 透传
	// （客户端按状态码识别限流，正文明确"免费额度已用尽"）。
	if lastStatus == http.StatusTooManyRequests {
		lastBody = rateLimitErrorBody()
	}
	if streaming {
		if lastStatus != 0 {
			return &callResult{stream: io.NopCloser(bytes.NewReader(lastBody)), status: lastStatus, nodeAddr: lastProxyAddr}, nil
		}
		return nil, fmt.Errorf("all models failed")
	}
	return &callResult{body: lastBody, status: lastStatus, headers: lastHeader, nodeAddr: lastProxyAddr}, lastErr
}

// raceCopies 返回竞速并行数（配置上限 >0 生效，否则 1 = 关闭竞速）。
// S5 压力系数动态副本：pressure = 当前活跃请求数 / 健康节点数，
//
//	pressure <  low（默认 0.5）            → 上限（全速竞速）
//	low ≤ pressure < high（默认 1.0）      → 2（温和竞速）
//	pressure ≥ high                        → 1（退化单发，等效分散路由）
//
// 健康节点数来自 transport 的 contract.RaceTracker.HealthyNodeCount
// （与 raceCandidates 可拿到的候选口径一致）；除数 0 / 无 tracker 统计
// → 按高压力处理（=1）。上限即 pool_race_copies 语义（S5 起）。
func (v *Vendor) raceCopies() int {
	upper := v.cfg.RaceCopies
	if upper <= 0 {
		return 1
	}
	low, high := v.cfg.RacePressureLow, v.cfg.RacePressureHigh
	if low <= 0 {
		low = 0.5
	}
	if high <= 0 {
		high = 1.0
	}
	active := activeRequests.Load()
	healthy := 0
	if tr, ok := v.transport().(contract.RaceTracker); ok {
		healthy = tr.HealthyNodeCount()
	}
	pressure := 2.0 // 除数 0 / 无统计 → 按高压力处理
	if healthy > 0 {
		pressure = float64(active) / float64(healthy)
	}
	switch {
	case pressure < low:
		return upper
	case pressure < high:
		if upper < 2 {
			return upper
		}
		return 2
	default:
		return 1
	}
}

// raceBudget 返回竞速整体预算（race_budget_ms 配置；0 回退 10s 默认）。
func (v *Vendor) raceBudget() time.Duration {
	if v.cfg.RaceBudgetMS > 0 {
		return time.Duration(v.cfg.RaceBudgetMS) * time.Millisecond
	}
	return 10 * time.Second
}

// ---------------------------------------------------------------- 429 感知（S2）

// rateLimitExceededMsg 429 最终失败的可见文案（S2）：替换上游原始错误体，status 保持 429。
const rateLimitExceededMsg = "免费额度已用尽（Rate limit exceeded），请稍后重试"

// rateLimitErrorBody 返回 429 最终失败的内层错误体（OpenAI 兼容形态，客户端按状态码识别限流）。
func rateLimitErrorBody() []byte {
	b, _ := json.Marshal(map[string]any{
		"error": map[string]any{
			"message": rateLimitExceededMsg,
			"type":    "rate_limit_error",
			"code":    "rate_limit_exceeded",
		},
	})
	return b
}

// inRateLimitCooldown 距最近一次 429 是否仍在冷却期（rate_limit_cooldown_sec，默认 30s）。
// 冷却期内跳过竞速走单发：限流时不放大请求量。从未 429 返回 false。
func (v *Vendor) inRateLimitCooldown() bool {
	last := v.last429.Load()
	if last == 0 {
		return false
	}
	sec := v.cfg.RateLimitCooldownSec
	if sec <= 0 {
		sec = 30
	}
	return time.Since(time.Unix(0, last)) < time.Duration(sec)*time.Second
}

// rateLimitBackoff 返回第 attempt 次（0 基）429 重试前的退避时长：min(base*2^attempt, cap)，
// base/cap 可配（默认 1s / 30s；<=0 回退默认）。
func (v *Vendor) rateLimitBackoff(attempt int) time.Duration {
	base := time.Duration(v.cfg.RateLimitBackoffBaseMS) * time.Millisecond
	if base <= 0 {
		base = time.Second
	}
	capD := time.Duration(v.cfg.RateLimitBackoffCapMS) * time.Millisecond
	if capD <= 0 {
		capD = 30 * time.Second
	}
	if capD < base {
		capD = base
	}
	d := base
	for i := 0; i < attempt; i++ {
		if d >= capD {
			return capD
		}
		d *= 2
	}
	if d > capD {
		return capD
	}
	return d
}

// prefixReadCloser 在流式响应前拼回竞速阶段已读出的首字节。
type prefixReadCloser struct {
	io.Reader
	io.Closer
}

// raceOutcome 是竞速中单个候选的结果。
type raceOutcome struct {
	resp *http.Response
	addr string
	err  error
	idx  int // 候选下标：赢家锁流后只取消其它候选，保留赢家流
}

// raceMarkOutcome 上报单个落选候选的最终结果（G6）：真实失败（非 2xx / 传输错误）才记；
// 赢家与全败时返回给调用方的候选不上报（由 call 统一标记，避免重复）；
// 被本竞速取消（ctx 已取消）的候选不算失败——它可能只是较慢的健康节点，误记会污染池健康。
func raceMarkOutcome(mark func(string, int, error), o raceOutcome) {
	if o.err == nil && o.resp != nil && o.resp.StatusCode >= 200 && o.resp.StatusCode < 300 {
		return
	}
	if o.err != nil && errors.Is(o.err, context.Canceled) {
		return
	}
	status := 0
	if o.resp != nil {
		status = o.resp.StatusCode
	}
	mark(o.addr, status, o.err)
}

// raceDo 请求级竞速：并行扇出至多 copies 个候选出口，首个 2xx（流式 = 首个 chunk 到达）胜出，其余取消。
// 整体受 raceBudget 约束：到期（如候选全部挂起）返回非 nil 错误，调用方走单发续写。
// 返回（nil, "", nil）表示无候选——调用方应退化普通单发。
// tier 透传给 CandidateClients（G1：付费层直连，不进入代理池竞速）。
// mark 上报落选候选的失败结果（G6：池健康/冷却可见；赢家与全败返回的候选由 call 统一标记，避免重复）。
func (v *Vendor) raceDo(ctx context.Context, racer contract.Racer, req *http.Request, streaming bool, tier contract.Tier, copies int, mark func(string, int, error)) (*http.Response, string, error) {
	clients, addrs := racer.CandidateClients(tier, streaming, copies)
	// G14：契约未强制 clients/addrs 等长——按下标访问（addrs[i]）前按短者截断，
	// 防止某 transport 实现返回不等长列表时越界 panic 拖崩整个网关进程。
	if len(addrs) < len(clients) {
		clients = clients[:len(addrs)]
	} else if len(clients) < len(addrs) {
		addrs = addrs[:len(clients)]
	}
	if len(clients) == 0 {
		return nil, "", nil
	}
	// S5 每节点 in-flight：候选确定后对每个 addr +1，本函数所有返回路径经
	// defer -1（含预算到期/全败/单候选），与 RaceStarted 严格成对不泄漏。
	// 赢家也在返回时 -1：竞速窗口结束后赢家退化为普通单发，单发不占竞速 in-flight。
	if tracker, ok := racer.(contract.RaceTracker); ok {
		tracker.RaceStarted(addrs)
		defer tracker.RaceFinished(addrs)
	}
	if len(clients) == 1 {
		resp, err := clients[0].Do(req)
		return resp, addrs[0], err
	}
	// 请求体需要多副本（每个候选一份）。
	var bodyBytes []byte
	if req.Body != nil {
		bodyBytes, _ = io.ReadAll(req.Body)
		req.Body.Close()
	}
	// 每候选独立 ctx：赢家锁流后只取消其它候选——取消赢家自身的请求上下文会
	// 中断其已建立的响应流（实测：cancel 后 body Read 立即返回 context canceled）。
	rctx := make([]context.Context, len(clients))
	cancels := make([]context.CancelFunc, len(clients))
	for i := range clients {
		c, cf := context.WithCancel(ctx)
		rctx[i], cancels[i] = c, cf
	}

	firstByteBudget := v.raceBudget()
	results := make(chan raceOutcome, len(clients))
	var wg sync.WaitGroup
	for i := range clients {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := req.Clone(rctx[i])
			if bodyBytes != nil {
				r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			}
			resp, err := clients[i].Do(r)
			if err != nil {
				results <- raceOutcome{err: err, addr: addrs[i], idx: i}
				return
			}
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				results <- raceOutcome{resp: resp, addr: addrs[i], idx: i}
				return
			}
			if streaming {
				// 流式锁流：首字节等待受预算约束（与整体预算同值），超时关流上报错误；
				// 锁流后赢家流本身不设限（延续原连接继续读，不受 Client.Timeout 影响）。
				buf := make([]byte, 1)
				fbDone := make(chan struct{})
				var n int
				var rerr error
				go func() {
					n, rerr = resp.Body.Read(buf)
					close(fbDone)
				}()
				fbTimer := time.NewTimer(firstByteBudget)
				select {
				case <-fbDone:
					fbTimer.Stop()
					if rerr != nil && n == 0 {
						resp.Body.Close()
						results <- raceOutcome{err: rerr, addr: addrs[i], idx: i}
						return
					}
				case <-rctx[i].Done():
					// 本候选被取消（他候选已胜出 / 预算到期）：关流收尾，等读 goroutine 退出。
					fbTimer.Stop()
					resp.Body.Close()
					<-fbDone
					results <- raceOutcome{err: rctx[i].Err(), addr: addrs[i], idx: i}
					return
				case <-fbTimer.C:
					// 候选首字节超时：关流解除挂起的读取，等读 goroutine 退出后上报。
					resp.Body.Close()
					<-fbDone
					results <- raceOutcome{err: fmt.Errorf("stream first byte timeout"), addr: addrs[i], idx: i}
					return
				}
				resp.Body = &prefixReadCloser{
					Reader: io.MultiReader(bytes.NewReader(buf[:n]), resp.Body),
					Closer: resp.Body,
				}
			}
			results <- raceOutcome{resp: resp, addr: addrs[i], idx: i}
		}(i)
	}

	// S1 整体预算：竞速在任何情况下有界——候选全部挂起时预算到期取消并返回错误，
	// 上层 retryCount 循环据此走单发路径（streamWithResume 可接手）。
	budget := v.raceBudget()
	timer := time.NewTimer(budget)
	defer timer.Stop()

	// cancelAll 终止全部候选（预算到期 / 全败收尾）。
	cancelAll := func() {
		for _, cf := range cancels {
			cf()
		}
	}

	var firstFail *raceOutcome
	var done int32
	for {
		select {
		case o := <-results:
			if o.err == nil && o.resp != nil && o.resp.StatusCode >= 200 && o.resp.StatusCode < 300 {
				// 赢家锁流：只取消其余候选，保留赢家的请求上下文（取消会中断赢家流）。
				for j := range cancels {
					if j != o.idx {
						cancels[j]()
					}
				}
				// G6：赢家锁流前已落定的首个失败此时确认落选，补报（赢家由 call 统一标记）。
				if firstFail != nil {
					raceMarkOutcome(mark, *firstFail)
				}
				go raceDrain(&wg, results, mark)
				return o.resp, o.addr, nil
			}
			f := o
			if firstFail == nil {
				// 首个失败暂缓上报：全败时它是返回给调用方的候选（由 call 标记）。
				firstFail = &f
			} else {
				// G6：后续失败候选必为落选，立即上报池健康。
				raceMarkOutcome(mark, o)
			}
			if atomic.AddInt32(&done, 1) == int32(len(clients)) {
				cancelAll()
				// 全败逐个上报：首个失败返回给 call()（由其统一标记），
				// 其余候选已在循环中上报；raceDrain 收尾通道余量。
				go raceDrain(&wg, results, mark)
				if firstFail.err != nil {
					return nil, firstFail.addr, firstFail.err
				}
				return firstFail.resp, firstFail.addr, nil
			}
		case <-timer.C:
			// 预算到期：即使候选全挂也快速失败返回错误（不无限悬着）。
			cancelAll()
			// G6：预算前已落定的失败补报（无返回候选，全部算落选）。
			if firstFail != nil {
				raceMarkOutcome(mark, *firstFail)
			}
			go raceDrain(&wg, results, mark)
			return nil, "", fmt.Errorf("race budget exceeded")
		}
	}
}

// raceDrain 竞速收尾：等所有候选 goroutine 退出后关闭落选响应的 Body（防连接泄漏），
// 并把余下落选候选的失败结果上报池健康（G6；主循环已上报的不在此列）。
func raceDrain(wg *sync.WaitGroup, results chan raceOutcome, mark func(string, int, error)) {
	wg.Wait()
	for {
		select {
		case o := <-results:
			if o.resp != nil {
				o.resp.Body.Close()
			}
			raceMarkOutcome(mark, o)
		default:
			return
		}
	}
}

// activeRequests 当前活跃请求数（S5 压力系数分子）：Chat/ChatStream 入口 +1、
// 返回时 -1（流式在返回流时即释放，不计流消费时长）——所有返回路径成对。
// G18：流式消费期不计入是已声明的设计取舍——客户端慢消费下压力被低估、
// 可能开出偏高竞速副本；若要精确需在流关闭时再 -1，取舍而非缺陷，保持现状。
var activeRequests atomic.Int64

// ActiveRequests 返回当前活跃请求数（供代理池计算压力系数）。
func ActiveRequests() int64 { return activeRequests.Load() }

// Chat 实现 contract.Vendor（非流式）。
// 非 2xx / 传输失败时同时返回（含错误体的 Reply, error），供上层做厂商级 failover。
func (v *Vendor) Chat(ctx context.Context, msg *contract.Message) (*contract.Reply, error) {
	activeRequests.Add(1)
	defer activeRequests.Add(-1)
	res, err := v.call(ctx, msg, false)
	if res == nil {
		return nil, err
	}
	return &contract.Reply{Body: res.body, Status: res.status, Headers: res.headers, NodeAddr: res.nodeAddr}, err
}

// ChatStream 实现 contract.Vendor（流式 SSE）。
// 错误体包装为可读流返回（error=nil，与历史行为一致）；仅完全无响应时返回 error。
func (v *Vendor) ChatStream(ctx context.Context, msg *contract.Message) (*contract.Stream, error) {
	activeRequests.Add(1)
	defer activeRequests.Add(-1)
	res, err := v.call(ctx, msg, true)
	if res == nil {
		return nil, err
	}
	return &contract.Stream{ReadCloser: res.stream, Status: res.status, NodeAddr: res.nodeAddr}, nil
}

// ---------------------------------------------------------------- Anthropic 兼容

func isAnthropicFormat(body []byte) bool {
	var obj map[string]any
	if json.Unmarshal(body, &obj) == nil {
		if typ, _ := obj["type"].(string); typ == "message" {
			return true
		}
	}
	lines := bytes.Split(body, []byte("\n"))
	for _, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal(line, &event); err != nil {
			continue
		}
		typ, _ := event["type"].(string)
		switch typ {
		case "message_start", "content_block_start", "content_block_delta",
			"content_block_stop", "message_delta", "message_stop", "ping":
			return true
		}
		return false
	}
	return false
}

func parseAnthropicSSE(body []byte) (map[string]any, string, []map[string]any) {
	lines := bytes.Split(body, []byte("\n"))
	var anthropicMsg map[string]any
	var textBuilder, currentToolInputBuilder strings.Builder
	var currentToolUse map[string]any
	var toolUseBlocks []map[string]any
	for _, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal(line, &event); err != nil {
			continue
		}
		typ, _ := event["type"].(string)
		switch typ {
		case "message_start":
			if m, ok := event["message"].(map[string]any); ok {
				anthropicMsg = m
			}
		case "content_block_start":
			if cb, ok := event["content_block"].(map[string]any); ok {
				if cbType, _ := cb["type"].(string); cbType == "tool_use" {
					currentToolUse = cb
					currentToolInputBuilder.Reset()
				}
			}
		case "content_block_delta":
			if delta, ok := event["delta"].(map[string]any); ok {
				if t, ok := delta["text"].(string); ok {
					textBuilder.WriteString(t)
				}
				if dt, _ := delta["type"].(string); dt == "input_json_delta" {
					if partial, ok := delta["partial_json"].(string); ok {
						currentToolInputBuilder.WriteString(partial)
					}
				}
			}
		case "content_block_stop":
			if currentToolUse != nil {
				inputStr := currentToolInputBuilder.String()
				var input any = inputStr
				var parsed any
				if json.Unmarshal([]byte(inputStr), &parsed) == nil {
					input = parsed
				}
				currentToolUse["input"] = input
				toolUseBlocks = append(toolUseBlocks, currentToolUse)
				currentToolUse = nil
			}
		case "message_delta":
			if delta, ok := event["delta"].(map[string]any); ok {
				if anthropicMsg == nil {
					anthropicMsg = map[string]any{}
				}
				if stop, ok := delta["stop_reason"].(string); ok {
					anthropicMsg["stop_reason"] = stop
				}
				if usage, ok := delta["usage"].(map[string]any); ok {
					anthropicMsg["usage"] = usage
				}
			}
		case "message_stop":
		case "error":
			return nil, "", nil
		}
	}
	return anthropicMsg, textBuilder.String(), toolUseBlocks
}

func buildOpenAIResponse(anthropicMsg map[string]any, text string, toolUseBlocks []map[string]any, modelID string) []byte {
	if anthropicMsg == nil {
		return nil
	}
	now := time.Now().Unix()
	role, _ := anthropicMsg["role"].(string)
	if role == "" {
		role = "assistant"
	}
	finishReason, _ := anthropicMsg["stop_reason"].(string)
	finishReason = normalizeFinishReason(finishReason)
	choice := map[string]any{
		"index":         0,
		"message":       map[string]any{"role": role, "content": text},
		"finish_reason": finishReason,
	}
	if len(toolUseBlocks) > 0 {
		var toolCalls []map[string]any
		for _, tb := range toolUseBlocks {
			toolInput := tb["input"]
			argsJSON, _ := json.Marshal(toolInput)
			toolCalls = append(toolCalls, map[string]any{
				"id":   tb["id"],
				"type": "function",
				"function": map[string]any{
					"name":      tb["name"],
					"arguments": string(argsJSON),
				},
			})
		}
		choice["message"].(map[string]any)["tool_calls"] = toolCalls
		if text == "" {
			choice["message"].(map[string]any)["content"] = nil
		}
	}
	resp := map[string]any{
		"id":      anthropicMsg["id"],
		"object":  "chat.completion",
		"created": now,
		"model":   modelID,
		"choices": []map[string]any{choice},
	}
	if usage, ok := anthropicMsg["usage"].(map[string]any); ok {
		resp["usage"] = anthropicUsageToChat(usage)
	}
	result, _ := json.Marshal(resp)
	return result
}

func convertAnthropicMessageToOpenAI(msg map[string]any, modelID string) []byte {
	if msg["model"] == nil {
		msg["model"] = modelID
	}
	var textBuilder strings.Builder
	var toolUses []map[string]any
	if content, ok := msg["content"].([]any); ok {
		for _, c := range content {
			if block, ok := c.(map[string]any); ok {
				switch block["type"] {
				case "text":
					if t, ok := block["text"].(string); ok {
						textBuilder.WriteString(t)
					}
				case "tool_use":
					toolUses = append(toolUses, block)
				}
			}
		}
	}
	return buildOpenAIResponse(msg, textBuilder.String(), toolUses, modelID)
}

func convertAnthropicToOpenAI(body []byte, modelID string) []byte {
	var singleMsg map[string]any
	if json.Unmarshal(body, &singleMsg) == nil {
		if typ, _ := singleMsg["type"].(string); typ == "message" {
			return convertAnthropicMessageToOpenAI(singleMsg, modelID)
		}
	}
	msg, text, toolUses := parseAnthropicSSE(body)
	if msg == nil {
		return body
	}
	if msg["model"] == nil {
		msg["model"] = modelID
	}
	return buildOpenAIResponse(msg, text, toolUses, modelID)
}

func normalizeFinishReason(reason string) string {
	switch reason {
	case "end_turn", "stop_sequence", "stop":
		return "stop"
	case "max_tokens", "length":
		return "length"
	case "tool_use", "tool_calls", "function_call":
		return "tool_calls"
	case "refusal", "content_filter":
		return "content_filter"
	default:
		return reason
	}
}

func anthropicUsageToChat(usage map[string]any) map[string]any {
	if usage == nil {
		return nil
	}
	out := make(map[string]any, len(usage)+3)
	for k, v := range usage {
		out[k] = v
	}
	if v, ok := usage["input_tokens"]; ok {
		out["prompt_tokens"] = v
	}
	if v, ok := usage["output_tokens"]; ok {
		out["completion_tokens"] = v
	}
	if p, pok := numberAsFloat(out["prompt_tokens"]); pok {
		if c, cok := numberAsFloat(out["completion_tokens"]); cok {
			out["total_tokens"] = p + c
		}
	}
	delete(out, "input_tokens")
	delete(out, "output_tokens")
	return out
}

func numberAsFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	default:
		return 0, false
	}
}
