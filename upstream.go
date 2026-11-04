// 上游调用适配层：把入站请求体交给「模型所属厂商」执行。
//
// v2.0.0 起本包只剩自定义源（vendors/custom）与插件供应商（vendors/remote）：
// OpenCode 免费层已移出本进程（改由 40080 的 opencode2api-win 引擎承担），
// 因此这里不再有 opencode 专属分支、目录代际同步、SOCKS5 池桥接。
//
// 职责：
//   - 按 model 选出候选厂商（chatRouterVar，未装配时回退唯一已注册厂商）；
//   - 组装 contract.Message（含 raw body 与 auth 上下文）；
//   - 厂商级 failover（shouldSwitchVendor）；
//   - auto 虚拟模型的降级链（autoNextModel）。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/6Kmfi6HP/opencode2api/core/contract"
	"github.com/6Kmfi6HP/opencode2api/vendors/custom"
)

// upstreamErrBodyMax 上游错误体在日志/错误文案里的截断长度。
const upstreamErrBodyMax = 2048

// probeCtxKey 标记探针请求：厂商侧跳过重试/退避，让探测快速拿到真实状态码。
type probeCtxKey struct{}

func withProbeContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, probeCtxKey{}, true)
}

func isProbeContext(ctx context.Context) bool {
	v, _ := ctx.Value(probeCtxKey{}).(bool)
	return v
}

// isLoopbackRequest 判断请求是否来自本机回环。
// 探针头只允许本机使用，避免外部客户端伪造 X-Oc2a-Probe 绕过重试/退避。
func isLoopbackRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// modeName 把本包认证路由模式映射为厂商侧字符串。
func modeName(mode AuthRouteMode) string {
	if mode == AuthRoutePublic {
		return "public"
	}
	return "auto"
}

// seedVendorCatalog 把聚合目录中属于该厂商的模型推给它（SetCatalog 可选实现）。
// 自定义源与插件供应商的目录由厂商自身维护（磁盘缓存 + Refresh），此函数仅
// 服务于实现了 SetCatalog 的厂商，当前无实现者——保留接口以便后续厂商接入。
func seedVendorCatalog(v contract.Vendor) {
	if globalAgg == nil {
		return
	}
	seeder, ok := v.(interface{ SetCatalog([]contract.Model) })
	if !ok {
		return
	}
	var mine []contract.Model
	for _, m := range globalAgg.Catalog() {
		if m.Provider == v.ID() {
			mine = append(mine, m)
		}
	}
	seeder.SetCatalog(mine)
}

// chatCandidates 返回可服务 modelID 的厂商（failover 顺序）。
func chatCandidates(modelID string) []contract.Vendor {
	if chatRouterVar != nil {
		if cs := chatRouterVar.Candidates(modelID); len(cs) > 0 {
			return cs
		}
	}
	if globalAgg == nil {
		return nil
	}
	return globalAgg.Vendors()
}

// vendorOfModel 返回模型的首选厂商 ID（供调用日志归类/错误分析过滤）。
func vendorOfModel(modelID string) string {
	if modelID == "" {
		return ""
	}
	if chatRouterVar != nil {
		if cs := chatRouterVar.Candidates(modelID); len(cs) > 0 {
			return cs[0].ID()
		}
	}
	if prefix, _, ok := strings.Cut(modelID, "/"); ok && prefix != "" {
		return prefix
	}
	return ""
}

// chatViaVendor 经单个厂商发起非流式上游调用。
func chatViaVendor(ctx context.Context, v contract.Vendor, upstreamBody []byte, modelID string, auth UpstreamAuth) (*contract.Reply, error) {
	seedVendorCatalog(v)
	msg := &contract.Message{
		Model: modelID,
		// 归一化厂商读 Messages 字段；原始体经 Extra 透传保留 tools/options 等完整字段。
		Messages: rawBodyToContractMessages(upstreamBody),
		Extra: map[string]any{
			contract.ParamRawBody:   upstreamBody,
			contract.ParamAuthMode:  modeName(auth.Mode),
			contract.ParamAuthToken: auth.Token,
			contract.ParamMaxRetries: maxRouteRetries(),
			contract.ParamProbe:     isProbeContext(ctx),
		},
	}
	return v.Chat(ctx, msg)
}

// chatViaVendorStream 构造单个厂商的流式上游调用。
// auth 指针透传：续写/会话粘性场景，把外部带进来的 PreferredKeyIdx 写入
// msg.Extra[custom.KeyPreferredIndex]，custom 源选中 key 后写回，供下次重连
// 继续命中同一 key（同请求续写不换 key）。
func chatViaVendorStream(ctx context.Context, v contract.Vendor, upstreamBody []byte, modelID string, auth *UpstreamAuth) (*contract.Stream, error) {
	seedVendorCatalog(v)
	msg := &contract.Message{
		Model:    modelID,
		Messages: rawBodyToContractMessages(upstreamBody),
		Extra: map[string]any{
			contract.ParamRawBody:    upstreamBody,
			contract.ParamAuthMode:   modeName(auth.Mode),
			contract.ParamAuthToken:  auth.Token,
			contract.ParamMaxRetries: maxRouteRetries(),
			contract.ParamProbe:      isProbeContext(ctx),
		},
	}
	if auth.PreferredKeyIdx != nil {
		msg.Extra[custom.KeyPreferredIndex] = *auth.PreferredKeyIdx
	}
	// 仅多 key 厂商打日志（避免单 key 厂商刷屏）；ConfiguredKeys 经可选接口断言取。
	keysTotal := 0
	if kc, ok := v.(interface{ ConfiguredKeys() []string }); ok {
		keysTotal = len(kc.ConfiguredKeys())
	}
	if keysTotal > 1 {
		slog.Debug("stream upstream via vendor", "vendor", v.ID(), "model", modelID, "keys", keysTotal)
	}
	return v.ChatStream(ctx, msg)
}

// rawBodyToContractMessages 从原始 OpenAI 请求体解析出归一化消息（厂商不支持
// raw body 时使用；custom/remote 均优先读 Extra 原始体）。
func rawBodyToContractMessages(body []byte) []contract.Msg {
	var req struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return nil
	}
	out := make([]contract.Msg, 0, len(req.Messages))
	for _, m := range req.Messages {
		out = append(out, contract.Msg{Role: m.Role, Content: string(m.Content)})
	}
	return out
}

// shouldSwitchVendor 判断是否需要换下一个候选厂商。
func shouldSwitchVendor(v contract.Vendor, status int, _ error) bool {
	if v == nil {
		return false
	}
	rules := v.ErrSemantics()
	for _, s := range rules.Switchable {
		if s == status {
			return true
		}
	}
	return false
}

// upstreamErrMsg 汇总上游错误文案（含状态码与截断后的响应体）。
func upstreamErrMsg(status int, err error, body []byte) string {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	if status != 0 {
		if msg != "" {
			msg = fmt.Sprintf("status=%d %s", status, msg)
		} else {
			msg = fmt.Sprintf("status=%d", status)
		}
	}
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

// callOpenCodeAPI 非流式上游调用（auto 降级链 + 厂商 failover）。
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

// callOpenCodeAPIOnce 非流式单模型尝试（厂商级 failover 循环）。
func callOpenCodeAPIOnce(ctx context.Context, upstreamBody []byte, modelID string, auth UpstreamAuth) ([]byte, int, http.Header, string, error) {
	cands := chatCandidates(modelID)
	if len(cands) == 0 {
		return nil, http.StatusServiceUnavailable, nil, "",
			fmt.Errorf("没有可服务模型 %s 的供应商（其厂商未注册或已停用）", modelID)
	}

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

// callOpenCodeAPIStream 流式上游调用（auto 降级链 + 厂商级 failover）。
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

// callOpenCodeAPIStreamOnce 流式单模型尝试（厂商级 failover 循环）。
func callOpenCodeAPIStreamOnce(ctx context.Context, upstreamBody []byte, modelID string, auth *UpstreamAuth) (io.ReadCloser, int, http.Header, string, error) {
	cands := chatCandidates(modelID)
	if len(cands) == 0 {
		return nil, http.StatusServiceUnavailable, nil, "",
			fmt.Errorf("没有可服务模型 %s 的供应商（其厂商未注册或已停用）", modelID)
	}

	var lastStream *contract.Stream
	var lastErr error
	for i, v := range cands {
		stream, err := chatViaVendorStream(ctx, v, upstreamBody, modelID, auth)
		if err == nil && stream != nil && stream.Status >= 200 && stream.Status < 300 {
			return stream.ReadCloser, stream.Status, stream.Headers, stream.NodeAddr, nil
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
	return lastStream.ReadCloser, lastStream.Status, lastStream.Headers, lastStream.NodeAddr, lastErr
}

// safeResponseHeaders 允许透传给客户端的响应头白名单。
var safeResponseHeaders = map[string]bool{
	"Content-Type":      true,
	"Content-Encoding":  true,
	"Cache-Control":     true,
	"Retry-After":       true,
	"X-Request-Id":      true,
	"Openai-Processing-Ms": true,
	"Openai-Version":    true,
}

// filterResponseHeaders 只保留白名单内的上游响应头。
func filterResponseHeaders(h http.Header) http.Header {
	if h == nil {
		return nil
	}
	out := make(http.Header, len(safeResponseHeaders))
	for k := range h {
		if safeResponseHeaders[http.CanonicalHeaderKey(k)] {
			out.Set(k, h.Get(k))
		}
	}
	return out
}

// httpStatusOr 把 0 状态码归一为 502（上游连不通时给客户端一个明确状态）。
func httpStatusOr(status int) int {
	if status == 0 {
		return http.StatusBadGateway
	}
	return status
}
