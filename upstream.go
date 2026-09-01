// 上游调用适配层（P2-B3 切流后）。
//
// callOpenCodeAPI / callOpenCodeAPIStream 接收请求上下文（ctx），从 handler /
// 网关续写一路透传到厂商 Chat/ChatStream——客户端断开时竞速候选、重试链、
// EnsureReady 立即收到取消，不空跑预算。内部桥接到全局 OpenCode 厂商
// （vendors/opencode，实现 contract.Vendor）。传输层经 rootTransport 复用
// 既有 SOCKS5 池/健康/冷却逻辑。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/6Kmfi6HP/opencode2api/core/contract"
	"github.com/6Kmfi6HP/opencode2api/vendors/custom"
	"github.com/6Kmfi6HP/opencode2api/vendors/opencode"
)

const (
	maxUpstreamRetries = 3
	max401Retries      = 3
)

var (
	ocAdapterOnce   sync.Once
	ocAdapterTarget *opencode.Vendor
)

// catalogGen 模型目录代际：refreshModelCatalog 每次同步后递增。
// syncVendorState / seedVendorCatalog 只在代际变化时执行 SetCatalog——
// 目录 10 分钟才刷新，逐请求 O(catalog) 遍历重建是纯浪费。
var catalogGen atomic.Int64

// vendorCatalogLastGen 各厂商最近一次 SetCatalog 的代际（缺项 = 从未同步）。
var vendorCatalogLastGenMu sync.Mutex
var vendorCatalogLastGen = map[string]int64{}

// catalogGenChanged 返回该厂商是否需要重新 SetCatalog，并原子记录本次代际。
// 首次请求（缺项）视为代际 -1 → 强制同步；同代际并发下仅第一个调用方执行。
func catalogGenChanged(vendorID string) bool {
	gen := catalogGen.Load()
	vendorCatalogLastGenMu.Lock()
	defer vendorCatalogLastGenMu.Unlock()
	last, ok := vendorCatalogLastGen[vendorID]
	if ok && last == gen {
		return false
	}
	vendorCatalogLastGen[vendorID] = gen
	return true
}

// mainCodeVendor 返回全局 OpenCode 厂商。
// 生产：优先复用聚合器（globalAgg）中已注册的 opencode 实例——目录与聊天共享同一
// 会话/缓存，杜绝"双实例各自建会话"的历史隐患；未装配聚合器（单元测试）时惰性创建
// 独立实例（经 rootTransport 桥接 fake httpClient）。
func mainCodeVendor() *opencode.Vendor {
	if globalAgg != nil {
		for _, v := range globalAgg.Vendors() {
			if oc, ok := v.(*opencode.Vendor); ok && oc.ID() == "opencode" {
				return oc
			}
		}
	}
	ocAdapterOnce.Do(func() {
		ocAdapterTarget = opencode.New(opencode.Config{
			ID:                 "opencode",
			Name:               "OpenCode",
			Transport:          rootTransport{},
			AdminPassword:      adminPassword,
			RaceCopies:         int(poolRaceCopies.Load()),
			RaceBudgetMS:       int(raceBudgetMS.Load()),
			RacePressureLow:    poolRacePressureLow.Load().(float64),
			RacePressureHigh:   poolRacePressureHigh.Load().(float64),
			RateLimitCooldownSec:  int(rateLimitCooldownSec.Load()),
			RateLimitBackoffBaseMS: int(rateLimitBackoffBaseMS.Load()),
			RateLimitBackoffCapMS:  int(rateLimitBackoffCapMS.Load()),
		})
	})
	return ocAdapterTarget
}

// modeName 把本包认证路由模式映射为 vendor 侧字符串（public/auto/zen/go）。
func modeName(mode AuthRouteMode) string {
	switch mode {
	case AuthRoutePublic:
		return "public"
	case AuthRouteAuto:
		return "auto"
	case AuthRouteZen:
		return "zen"
	case AuthRouteGo:
		return "go"
	default:
		return "auto"
	}
}

// syncVendorState 把 main 侧的模型目录缓存推给 opencode 厂商（SetCatalog），
// 保证 /v1/models 展示与路由的 go 端点判定一致。
// 会话由厂商自身持有（vendors/opencode 内 lazy 初始化 / 测试经 SetSession 注入），
// 本函数不再读写全局会话。
func syncVendorState(v *opencode.Vendor) {
	if !catalogGenChanged("opencode") {
		return // 目录代际未变：跳过 O(catalog) 重建与 SetCatalog
	}
	modelMu.RLock()
	zen, goM := modelsCache, goModelsCache
	modelMu.RUnlock()
	all := make([]contract.Model, 0, len(zen)+len(goM))
	for _, m := range zen {
		all = append(all, contract.Model{ID: m.ID, Provider: "opencode", Free: isFreeModel(m.ID), Meta: map[string]string{"surface": "zen"}})
	}
	for _, m := range goM {
		all = append(all, contract.Model{ID: m.ID, Provider: "opencode", Free: isFreeModel(m.ID), Meta: map[string]string{"surface": "go"}})
	}
	v.SetCatalog(all)
}

// chatCandidates 返回可服务 modelID 的厂商（failover 顺序）。
// 未装配路由器（测试环境）时退化为单 opencode。
func chatCandidates(modelID string) []contract.Vendor {
	if chatRouterVar != nil {
		if cs := chatRouterVar.Candidates(modelID); len(cs) > 0 {
			return cs
		}
	}
	return []contract.Vendor{mainCodeVendor()}
}

// seedVendorCatalog 把 globalAgg 中属于该厂商的模型推给它（SetCatalog 可选实现）。
func seedVendorCatalog(v contract.Vendor) {
	if globalAgg == nil {
		return
	}
	seeder, ok := v.(interface{ SetCatalog([]contract.Model) })
	if !ok {
		return
	}
	if !catalogGenChanged(v.ID()) {
		return // 目录代际未变：跳过 O(catalog) 遍历与 SetCatalog
	}
	var mine []contract.Model
	for _, m := range globalAgg.Catalog() {
		if m.Provider == v.ID() {
			mine = append(mine, m)
		}
	}
	seeder.SetCatalog(mine)
}

// chatViaVendor 经单个厂商发起非流式上游调用。
func chatViaVendor(ctx context.Context, v contract.Vendor, upstreamBody []byte, modelID string, auth UpstreamAuth) (*contract.Reply, error) {
	if oc, ok := v.(*opencode.Vendor); ok && oc == mainCodeVendor() {
		syncVendorState(oc)
		// opencode 上游一律无 key（免费档）：客户端携带的任何 key 都不转发给 opencode，
		// 避免非 opencode 的 key（如 swe/本地占位密钥）被透传导致上游 401 Invalid API key。
		auth = UpstreamAuth{Mode: AuthRoutePublic}
	} else {
		seedVendorCatalog(v)
	}
	// 池型厂商（PoolVendor）：请求前保证可用账号——池空自动注册，用户无感。
	// 等待受请求 ctx 约束（客户端断开立即中止），不自造 Background + 固定超时。
	if pv, ok := v.(contract.PoolVendor); ok {
		if err := pv.EnsureReady(ctx); err != nil {
			return nil, fmt.Errorf("%s: 无可用账号且自动注册失败: %w", v.ID(), err)
		}
	}
	msg := &contract.Message{
		Model: modelID,
		// 归一化消息：非 opencode 厂商（如 windsurf 账号池）不读 KeyRawBody，
		// 走 Messages 字段；opencode 仍优先读 Extra[KeyRawBody]，此字段对其无副作用。
		Messages: rawBodyToContractMessages(upstreamBody),
		Extra: map[string]any{
			opencode.KeyRawBody:    upstreamBody,
			opencode.KeyAuthMode:   modeName(auth.Mode),
			opencode.KeyAuthToken:  auth.Token,
			opencode.KeyMaxRetries: maxRouteRetries(),
		},
	}
	return v.Chat(ctx, msg)
}

// chatViaVendorStream 构造单个厂商的流式上游调用。
// auth 指针透传：续写/会话粘性场景，把外部带进来的 PreferredKeyIdx 写入
// msg.Extra[custom.KeyPreferredIndex]，custom 源选中 key 后写回，供下次重连
// 继续命中同一 key（同请求续写不换 key）。
func chatViaVendorStream(ctx context.Context, v contract.Vendor, upstreamBody []byte, modelID string, auth *UpstreamAuth) (*contract.Stream, error) {
	if oc, ok := v.(*opencode.Vendor); ok && oc == mainCodeVendor() {
		syncVendorState(oc)
		// opencode 上游一律无 key（免费档）：不转发客户端 key（同 chatViaVendor）。
		auth = &UpstreamAuth{Mode: AuthRoutePublic}
	} else {
		seedVendorCatalog(v)
	}
	// 池型厂商（PoolVendor）：请求前保证可用账号——池空自动注册，用户无感。
	// 等待受请求 ctx 约束（客户端断开立即中止），不自造 Background + 固定超时。
	if pv, ok := v.(contract.PoolVendor); ok {
		if err := pv.EnsureReady(ctx); err != nil {
			return nil, fmt.Errorf("%s: 无可用账号且自动注册失败: %w", v.ID(), err)
		}
	}
	msg := &contract.Message{
		Model: modelID,
		// 归一化厂商（windsurf 池型）读 Messages 字段；opencode 仍走 Extra[KeyRawBody]。
		Messages: rawBodyToContractMessages(upstreamBody),
		Extra: map[string]any{
			opencode.KeyRawBody:    upstreamBody,
			opencode.KeyAuthMode:   modeName(auth.Mode),
			opencode.KeyAuthToken:  auth.Token,
			opencode.KeyMaxRetries: maxRouteRetries(),
		},
	}
	if auth.PreferredKeyIdx != nil {
		msg.Extra[custom.KeyPreferredIndex] = *auth.PreferredKeyIdx
	}
	// 仅多 key 厂商打日志（避免单 key 厂商刷屏）；ConfiguredKeys 是 custom 具体类型方法，经可选接口断言取。
	keysTotal := 0
	if kc, ok := v.(interface{ ConfiguredKeys() []string }); ok {
		keysTotal = len(kc.ConfiguredKeys())
	}
	if keysTotal > 1 {
		sticky := -1
		if auth.PreferredKeyIdx != nil {
			sticky = *auth.PreferredKeyIdx
		}
		slog.Info("custom-key: enter chatViaVendorStream (stream)", "vendor", v.ID(), "model", modelID, "keys_total", keysTotal, "sticky_idx_in", sticky)
	}
	stream, err := v.ChatStream(ctx, msg)
	// custom 源选中 key 后写回 Extra：回填到 auth，供 streamWithResume 续写重连透传。
	// 首次调用（auth 未带偏好）也要回读——首次选中的 key 是续写粘性的起点。
	if idx, ok := msg.Extra[custom.KeyPreferredIndex].(int); ok && idx >= 0 {
		auth.PreferredKeyIdx = &idx
		if keysTotal > 1 {
			slog.Info("custom-key: after ChatStream selected", "vendor", v.ID(), "model", modelID, "key_idx", idx)
		}
	}
	return stream, err
}

// rawBodyToContractMessages 从 OpenAI Chat 形态请求体提取归一化消息。
// content 保持 string / 数组两种形态（与 contract.Msg.Content 语义一致），
// 供非 opencode 厂商（windsurf 等）经 contract.Message.Messages 读取。
func rawBodyToContractMessages(body []byte) []contract.Msg {
	var req struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if json.Unmarshal(body, &req) != nil {
		return nil
	}
	out := make([]contract.Msg, 0, len(req.Messages))
	for _, raw := range req.Messages {
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			continue
		}
		role, _ := m["role"].(string)
		if role == "" {
			continue
		}
		msg := contract.Msg{Role: role}
		switch c := m["content"].(type) {
		case string:
			msg.Content = c
		case []any:
			msg.Content = c
		}
		out = append(out, msg)
	}
	return out
}

// shouldSwitchVendor 判定某厂商的失败是否应切换下一个候选厂商。
// 有上游状态码 → 按厂商 Switchable 语义 + 5xx 判定；无状态码（传输错误）→ 切换。
func shouldSwitchVendor(v contract.Vendor, status int, _ error) bool {
	if status == 0 {
		return true
	}
	rules := v.ErrSemantics()
	for _, s := range rules.Switchable {
		if status == s {
			return true
		}
	}
	return status >= 500 && status < 600
}

// callOpenCodeAPI 非流式上游调用（适配层；路由 + 厂商级 failover + auto 模型降级链）。
// auto：ctx 挂有降级链时，失败（非 2xx）沿链换模型重试（有界，见 maxAutoSwitches）。
// upstreamErrBodyMax 上游失败响应体入日志的最大字节数（防错误体刷屏）。
const upstreamErrBodyMax = 512

// upstreamErrMsg 组装上游失败信息：状态码 + 错误 + 响应体摘要（截断防刷屏）。
// body 为上游返回的错误体（非 2xx 时由厂商透传）；空则退化为原「状态码 + err」格式。
func upstreamErrMsg(status int, err error, body []byte) string {
	msg := fmt.Sprintf("upstream status %d: %v", status, err)
	if len(body) > 0 {
		s := strings.TrimSpace(string(body))
		if s == "" {
			return msg
		}
		if len(s) > upstreamErrBodyMax {
			s = s[:upstreamErrBodyMax] + "…"
		}
		msg += " body=" + s
	}
	return msg
}

func callOpenCodeAPI(ctx context.Context, upstreamBody []byte, modelID string, auth UpstreamAuth) ([]byte, int, http.Header, string, error) {
	dec, _ := ctx.Value(autoCtxKey{}).(*autoDecision)
	for switched := 0; ; switched++ {
		if dec != nil {
			dec.FinalModel = modelID
		}
		start := time.Now()
		body, status, hdr, addr, err := callOpenCodeAPIOnce(ctx, upstreamBody, modelID, auth)
		recordAutoAttempt(ctx, modelID, addr, status, time.Since(start).Milliseconds())
		if err == nil && status >= 200 && status < 300 {
			return body, status, hdr, addr, err
		}
		next, ok := autoNextModel(ctx, switched)
		if !ok {
			return body, status, hdr, addr, err
		}
		modelID = next
	}
}

// callOpenCodeAPIOnce 非流式单模型尝试（原路由 + 厂商级 failover 循环）。
func callOpenCodeAPIOnce(ctx context.Context, upstreamBody []byte, modelID string, auth UpstreamAuth) ([]byte, int, http.Header, string, error) {
	cands := chatCandidates(modelID)

	var lastReply *contract.Reply
	var lastErr error
	for i, v := range cands {
		reply, err := chatViaVendor(ctx, v, upstreamBody, modelID, auth)
		if err == nil && reply != nil && reply.Status >= 200 && reply.Status < 300 {
			return reply.Body, reply.Status, reply.Headers, reply.NodeAddr, nil
		}
		lastReply, lastErr = reply, err
		if i == len(cands)-1 {
			break
		}
		status := 0
		if reply != nil {
			status = reply.Status
		}
		if !shouldSwitchVendor(v, status, err) {
			break
		}
	}
	if lastReply == nil {
		return nil, 0, nil, "", lastErr
	}
	return lastReply.Body, lastReply.Status, lastReply.Headers, lastReply.NodeAddr, lastErr
}

// callOpenCodeAPIStream 流式上游调用（适配层；路由 + 厂商级 failover + auto 模型降级链）。
// auto 降级只发生在返回客户端之前（厂商循环层），流中续写（streamWithResume）沿用
// 同一模型重试——流已对外吐字节后换模型会破坏对话连贯性。
func callOpenCodeAPIStream(ctx context.Context, upstreamBody []byte, modelID string, auth *UpstreamAuth) (io.ReadCloser, int, http.Header, string, error) {
	dec, _ := ctx.Value(autoCtxKey{}).(*autoDecision)
	for switched := 0; ; switched++ {
		if dec != nil {
			dec.FinalModel = modelID
		}
		start := time.Now()
		stream, status, hdr, addr, err := callOpenCodeAPIStreamOnce(ctx, upstreamBody, modelID, auth)
		recordAutoAttempt(ctx, modelID, addr, status, time.Since(start).Milliseconds())
		if err == nil && status >= 200 && status < 300 {
			return stream, status, hdr, addr, err
		}
		next, ok := autoNextModel(ctx, switched)
		if !ok {
			return stream, status, hdr, addr, err
		}
		modelID = next
	}
}

// callOpenCodeAPIStreamOnce 流式单模型尝试（原路由 + 厂商级 failover 循环）。
func callOpenCodeAPIStreamOnce(ctx context.Context, upstreamBody []byte, modelID string, auth *UpstreamAuth) (io.ReadCloser, int, http.Header, string, error) {
	cands := chatCandidates(modelID)

	var lastStream *contract.Stream
	var lastErr error
	for i, v := range cands {
		stream, err := chatViaVendorStream(ctx, v, upstreamBody, modelID, auth)
		if err == nil && stream != nil && stream.Status >= 200 && stream.Status < 300 {
			return stream.ReadCloser, stream.Status, nil, stream.NodeAddr, nil
		}
		lastStream, lastErr = stream, err
		if i == len(cands)-1 {
			break
		}
		status := 0
		if stream != nil {
			status = stream.Status
		}
		if !shouldSwitchVendor(v, status, err) {
			break
		}
	}
	if lastStream == nil {
		return nil, 0, nil, "", lastErr
	}
	return lastStream.ReadCloser, lastStream.Status, nil, lastStream.NodeAddr, lastErr
}

// ======================== 安全响应头过滤 ========================

var safeResponseHeaders = map[string]bool{
	"Content-Type":          true,
	"X-RateLimit-Limit":     true,
	"X-RateLimit-Remaining": true,
	"X-RateLimit-Reset":     true,
}

func filterResponseHeaders(h http.Header) http.Header {
	filtered := make(http.Header)
	for k, v := range h {
		if safeResponseHeaders[k] {
			filtered[k] = v
		}
	}
	return filtered
}

// httpStatusOr 上游调用未获得 HTTP 状态码（传输错误 / 厂商未接线等，status==0）时
// 返回 502 Bad Gateway，避免 handler 侧 WriteHeader(0) 触发
// Go net/http 的 "invalid WriteHeader code 0" panic。
func httpStatusOr(status int) int {
	if status == 0 {
		return http.StatusBadGateway
	}
	return status
}
