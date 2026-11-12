package manager

// Kilo 虚拟模型选路（阶段 M / Task 2）。
//
// 设计取舍：竞速需要「只把胜出者回给客户端」，若多个 goroutine 直接写同一个
// http.ResponseWriter 会撕裂响应。因此每个候选先**完整收进内存**（非流式），
// 第一个 2xx 胜出，其余用 context 取消并排空 body。代价是虚拟模型不支持
// SSE 流式（Kilo 上游单次响应体有限，阶段 K 实测 3~5s、几 KB~几十 KB）；
// 请求里带 stream=true 时网关自动剥离该字段按非流式处理，并在注释/UI 说明。
//
// 独立性：全部在 core/manager 包内，零 main 包 / 50080 / 40080 / auto_model 依赖。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// forwardResult 一次候选转发的完整结果（未写回客户端）。
type forwardResult struct {
	model  string // 上游真实返回的 model（观测账本用）
	status int
	header http.Header
	body   []byte
	err    error
}

// forwardOne 把请求体以指定模型发给上游，收完整响应但不写 w。
// 复用 handleChat 的鉴权头/警示头语义；响应体与状态码原样保留。
func (g *kiloGateway) forwardOne(ctx context.Context, body []byte, modelID string, stream bool) forwardResult {
	res := forwardResult{model: modelID}
	client, err := g.upstreamClient()
	if err != nil {
		res.err = err
		return res
	}
	upBody := rewriteModel(body, modelID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, kiloBaseURL()+"/chat/completions", bytes.NewReader(upBody))
	if err != nil {
		res.err = err
		return res
	}
	req.Header.Set("Authorization", kiloUpstreamAuth)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		res.err = err
		return res
	}
	defer resp.Body.Close()
	res.status = resp.StatusCode
	res.header = resp.Header.Clone()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		res.err = err
		return res
	}
	res.body = raw
	// 从响应体提取上游真实 model（不改写，只观测）
	if m := modelFromJSON(raw); m != "" {
		res.model = m
	} else {
		for _, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "data:") {
				if m := modelFromJSON([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:")))); m != "" {
					res.model = m
					break
				}
			}
		}
	}
	_ = stream // 虚拟路径统一非流式，见文件头
	return res
}

// isModelBlockedByFreeOnlyFor 复用 free_only 边界（虚拟模型的每个候选都要过）。
func (g *kiloGateway) resolveVirtualCandidates(vm VirtualModel) ([]string, error) {
	out := make([]string, 0, len(vm.Models))
	for _, m := range vm.Models {
		id := strings.TrimSpace(m)
		id = strings.TrimPrefix(id, "kilo/")
		if id == "" {
			continue
		}
		// 虚拟名不能指向另一个虚拟名（避免嵌套）
		if _, exists := g.virtual.Get(id); exists {
			continue
		}
		out = append(out, id)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("虚拟模型 %q 没有可用候选", vm.Name)
	}
	return out, nil
}

// selectByFallback 按候选顺序试，2xx 即返回；失败换下一个；全败返回最后一个结果。
func (g *kiloGateway) selectByFallback(ctx context.Context, body []byte, candidates []string) (forwardResult, string) {
	var last forwardResult
	for i, id := range candidates {
		if ctx.Err() != nil {
			break
		}
		if blocked, _ := g.isModelBlockedByFreeOnly(id); blocked {
			last = forwardResult{model: id, status: http.StatusBadRequest,
				body: []byte(fmt.Sprintf(`{"error":{"message":"候选 %s 被 free_only 边界拦截"}}`, id))}
			continue
		}
		r := g.forwardOne(ctx, body, id, false)
		last = r
		if r.err == nil && r.status >= 200 && r.status < 300 {
			return r, fmt.Sprintf("fallback#%d", i+1)
		}
	}
	return last, "all-failed"
}

// selectByRoundRobin 轮换起点后按 fallback。
func (g *kiloGateway) selectByRoundRobin(ctx context.Context, body []byte, candidates []string, name string) (forwardResult, string) {
	start := g.virtual.nextStart(name, len(candidates))
	rotated := make([]string, 0, len(candidates))
	rotated = append(rotated, candidates[start:]...)
	rotated = append(rotated, candidates[:start]...)
	r, tag := g.selectByFallback(ctx, body, rotated)
	return r, fmt.Sprintf("round_robin@%d/%s", start, tag)
}

// selectByRace 并发发全部候选，第一个 2xx 胜出，取消其余。
func (g *kiloGateway) selectByRace(parent context.Context, body []byte, candidates []string) (forwardResult, string) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	type entry struct {
		res forwardResult
		idx int
	}
	ch := make(chan entry, len(candidates))
	var wg sync.WaitGroup
	started := time.Now()

	for i, id := range candidates {
		if blocked, _ := g.isModelBlockedByFreeOnly(id); blocked {
			ch <- entry{res: forwardResult{model: id, status: http.StatusBadRequest,
				body: []byte(fmt.Sprintf(`{"error":{"message":"候选 %s 被 free_only 边界拦截"}}`, id))}, idx: i}
			continue
		}
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			if ctx.Err() != nil {
				return
			}
			res := g.forwardOne(ctx, body, id, false)
			select {
			case ch <- entry{res: res, idx: i}:
			case <-ctx.Done():
			}
		}(i, id)
	}
	// 收集：等所有候选返回（或全部失败），取第一个 2xx 且耗时最短者。
	go func() { wg.Wait(); close(ch) }()

	var best *entry
	var last forwardResult
	for e := range ch {
		last = e.res
		if e.res.err == nil && e.res.status >= 200 && e.res.status < 300 {
			if best == nil {
				cp := e
				best = &cp
				cancel() // 取消其余在途请求
			}
		}
	}
	if best != nil {
		return best.res, fmt.Sprintf("race#winner-%d/%d %dms", best.idx+1, len(candidates), time.Since(started).Milliseconds())
	}
	return last, "race-all-failed"
}

// resolveVirtual 按策略选出一个胜出结果；未命中虚拟名返回 nil。
func (g *kiloGateway) resolveVirtual(ctx context.Context, body []byte, modelName string) (*forwardResult, string, error) {
	if g.virtual == nil {
		return nil, "", nil
	}
	name := strings.TrimPrefix(modelName, "kilo/")
	vm, ok := g.virtual.Get(name)
	if !ok || !vm.Enabled {
		return nil, "", nil
	}
	candidates, err := g.resolveVirtualCandidates(vm)
	if err != nil {
		return nil, "", err
	}
	switch vm.Strategy {
	case KiloVirtualStrategyRoundRobin:
		r, tag := g.selectByRoundRobin(ctx, body, candidates, name)
		return &r, tag, nil
	case KiloVirtualStrategyRace:
		r, tag := g.selectByRace(ctx, body, candidates)
		return &r, tag, nil
	default:
		r, tag := g.selectByFallback(ctx, body, candidates)
		return &r, tag, nil
	}
}

// writeForwardResult 把胜出结果原样写回客户端（状态码/Content-Type/警示头）。
func writeForwardResult(w http.ResponseWriter, r forwardResult) {
	if r.header != nil {
		copyHeadersPassthrough(w, r.header)
	}
	w.Header().Set(kiloDataPolicyHeader, kiloDataPolicyHeaderValue)
	status := r.status
	if status == 0 {
		status = http.StatusBadGateway
	}
	w.WriteHeader(status)
	if len(r.body) > 0 {
		_, _ = w.Write(r.body)
	} else if r.err != nil {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{"message": "kilo upstream unreachable: " + r.err.Error(), "type": "kilo_gateway_error"},
		})
	}
}
