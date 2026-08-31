// Package custom 自定义模型源：用户自带 key 的第三方供应商适配器。
//
// 形态：一个实例 = 一个用户自定义源（config providers[] 里一条 type:"custom" 条目，
// id 由用户命名），装配参数经 ProviderSpec.Params 注入：
//
//	base_url  上游根地址（如 https://open.bigmodel.cn/api/paas/v4、
//	          https://api.anthropic.com/v1、https://generativelanguage.googleapis.com/v1beta）
//	api_key   上游密钥（由网关持有，客户端无需携带）
//	protocol  出站协议："openai"（默认，OpenAI 兼容）| "anthropic" | "gemini"
//	via_proxy 出站是否走代理池（默认 false 直连；true 时复用节点池出口）
//
// 模型命名：目录恒带 "{id}/" 前缀（如 "myglm/glm-4.7"），与其它厂商同名模型
// 天然隔离、路由与展示稳定；调用上游前剥掉前缀。请求经统一网关发出（Transport 注入），
// 统计/调用日志/失败切换由 core 既有链路自动覆盖。
package custom

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

	"github.com/6Kmfi6HP/opencode2api/core/contract"
)

// 协议取值。
const (
	ProtoOpenAI    = "openai"
	ProtoAnthropic = "anthropic"
	ProtoGemini    = "gemini"
	ProtoResponses = "responses"
)

// keyRawBody 与 core 适配层（upstream.go chatViaVendor）注入的原始 OpenAI 请求体
// Extra 键同值。原始体保留 tools/options 等完整字段，优于归一化 Messages 重建。
const keyRawBody = "_oc_raw_body"

// KeyPreferredIndex 续写/会话粘性透传的 key 池下标（int）：core 在流式中断续写
// 重连时把首次选中的 key 下标经本键回写，withKeys/withKeysStream 优先命中同一
// key（同请求续写不换 key，避免重复输出/串对话）。-1/缺失 = 无偏好走正常调度。
const KeyPreferredIndex = "_oc_custom_key_preferred"

// Config 构造参数。
type Config struct {
	ID        string // 实例标识（用户自定义，模型前缀即它）
	Name      string // 展示名
	BaseURL   string // 上游根地址（尾斜杠容忍）
	Protocol  string // openai | anthropic | gemini | responses
	ViaProxy  bool   // 出站走代理池（TierFree）；默认直连（TierPaid）
	Transport contract.Transport
	// APIKeys 多 key（轮询/错误转移调度）；APIKey 为单 key 兼容字段，两者合并去重。
	APIKeys     []string
	APIKey      string
	KeyStrategy string // round_robin（默认）| failover
	// AllowedModels 暴露白名单（上游模型 ID；空 = 全部暴露）。目录/缓存保存全量，
	// 仅在 ListModels 返回时过滤——编辑界面始终能拿到全量清单。
	AllowedModels []string
	// NoModelCache 禁用目录磁盘缓存（读与写）：连通测试等"必须真连"的场景使用。
	NoModelCache bool
}

// Vendor 自定义模型源厂商。
type Vendor struct {
	cfg         Config
	proto       chatProto
	pool        *keyPool
	mu          sync.Mutex
	models      []contract.Model // 最近一次成功目录（失败时兜底返回）
	affinity    map[string][]int // 原始模型名 → 提供它的 key 下标（ListModels 并集时记录，请求按模型亲和路由）
	lastErr     string
	lastSuccess time.Time
}

// New 构造自定义模型源厂商。
func New(cfg Config) (*Vendor, error) {
	if cfg.ID == "" {
		return nil, fmt.Errorf("custom: id is required")
	}
	if cfg.Name == "" {
		cfg.Name = cfg.ID
	}
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("custom %s: base_url is required", cfg.ID)
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if cfg.Protocol == "" {
		cfg.Protocol = ProtoOpenAI
	}
	var p chatProto
	switch cfg.Protocol {
	case ProtoOpenAI:
		p = &openaiProto{}
	case ProtoAnthropic:
		p = &anthropicProto{}
	case ProtoGemini:
		p = &geminiProto{}
	case ProtoResponses:
		p = &responsesProto{}
	default:
		return nil, fmt.Errorf("custom %s: unknown protocol %q (want openai|anthropic|gemini|responses)", cfg.ID, cfg.Protocol)
	}
	if cfg.Transport == nil {
		cfg.Transport = contract.DirectTransport{}
	}
	keys := append([]string(nil), cfg.APIKeys...)
	if k := strings.TrimSpace(cfg.APIKey); k != "" {
		keys = append(keys, k)
	}
	v := &Vendor{cfg: cfg, proto: p, pool: newKeyPool(keys, cfg.KeyStrategy)}
	// 预热磁盘缓存：启动首拉失败时 ListModels 也能立即给出上次目录（stale-while-revalidate）。
	if !cfg.NoModelCache {
		v.models = v.loadModelsCache()
	}
	return v, nil
}

// ---------------------------------------------------------------------------
// contract.Vendor
// ---------------------------------------------------------------------------

func (v *Vendor) ID() string   { return v.cfg.ID }
func (v *Vendor) Name() string { return v.cfg.Name }

func (v *Vendor) tier() contract.Tier {
	if v.cfg.ViaProxy {
		return contract.TierFree
	}
	return contract.TierPaid
}

// prefix 模型目录前缀（"{id}/"）。
func (v *Vendor) prefix() string { return v.cfg.ID + "/" }

// upstreamModel 把对外模型名（带前缀）还原为上游真实模型名。
// 无前缀时原样返回（经 routing.model_provider_map 强制映射的裸名场景）。
func (v *Vendor) upstreamModel(model string) string {
	return strings.TrimPrefix(model, v.prefix())
}

// prefixedModel 给上游模型名加本源前缀。
func (v *Vendor) prefixedModel(upstream string) string { return v.prefix() + upstream }

// PoolStatus key 健康计数（可用/冷却/禁用；状态快照，非用量统计）。
func (v *Vendor) PoolStatus() KeyPoolStatus { return v.pool.status() }

// ConfiguredKeys 配置内的全部 key 明文（测试端点逐 key 连通验证用）。
func (v *Vendor) ConfiguredKeys() []string { return v.pool.keysSnapshot() }

// ListModels 拉取上游目录并加前缀。失败（含空列表，防上游抖动清空）时回退
// 内存缓存 → 磁盘缓存；成功则更新两级缓存并写盘（stale-while-revalidate：
// 进程重启后无需等上游，首个 /v1/models 即含自定义模型）。
//
// 多 key 目录取**并集**：逐个可用 key 各自拉取合并去重——不同 key 的模型子集
// 不一致时（如同一供应商下不同订阅档），任一 key 独有的模型都进目录；同时记录
// 每个模型由哪些 key 提供（affinity），供请求侧按模型亲和路由（见 preferredKeys）。
func (v *Vendor) ListModels(ctx context.Context) ([]contract.Model, error) {
	tried := map[int]bool{}
	seen := map[string]bool{}
	affinity := map[string][]int{}
	var ids []string
	var lastErr error
	someOK := false
	for {
		key, idx, ok := v.pool.tryAcquire(tried)
		if !ok {
			break
		}
		tried[idx] = true
		perKey, err := v.proto.listModels(ctx, v, key)
		if err != nil {
			lastErr = err
			if se, ok := err.(*keyStatusError); ok {
				switch se.status {
				case http.StatusUnauthorized, http.StatusForbidden:
					v.pool.disable(idx)
				case http.StatusTooManyRequests:
					v.pool.cool(idx, se.retryAfter)
				}
			}
			continue // 单 key 失败不阻断并集；全部失败走缓存兜底
		}
		someOK = true
		for _, id := range perKey {
			if id == "" {
				continue
			}
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
			affinity[id] = appendKeyIdx(affinity[id], idx)
		}
	}
	var err error
	if !someOK {
		err = lastErr
		if err == nil {
			err = fmt.Errorf("custom %s: no key returned model list", v.cfg.ID)
		}
	} else if len(ids) == 0 {
		// 成功但空列表：多为上游异常，按失败处理以保留既有目录。
		err = fmt.Errorf("custom %s: empty model list", v.cfg.ID)
	}
	if err != nil {
		v.mu.Lock()
		cached := append([]contract.Model(nil), v.models...)
		v.mu.Unlock()
		if len(cached) > 0 {
			return v.filterAllowed(cached), nil
		}
		if disk := v.loadModelsCache(); !v.cfg.NoModelCache && len(disk) > 0 {
			v.mu.Lock()
			v.models = disk
			v.mu.Unlock()
			return v.filterAllowed(disk), nil
		}
		return nil, err
	}
	out := make([]contract.Model, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		out = append(out, contract.Model{
			ID:       v.prefixedModel(id),
			Provider: v.cfg.ID,
			// key 由网关持有，客户端不带 key 也可调用 → 对外即"免费可用"目录。
			Free: true,
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("custom %s: empty model list", v.cfg.ID)
	}
	// 内存/磁盘缓存保存全量（白名单变更无需重新拉取），返回时过滤；
	// affinity 与目录同代更新，请求按模型直达可服务的 key。
	v.mu.Lock()
	v.models = out
	v.affinity = affinity
	v.mu.Unlock()
	if !v.cfg.NoModelCache {
		v.saveModelsCache(out)
	}
	return v.filterAllowed(out), nil
}

// appendKeyIdx 去重追加 key 下标（同一模型被多个 key 提供时各记一次）。
func appendKeyIdx(list []int, idx int) []int {
	for _, i := range list {
		if i == idx {
			return list
		}
	}
	return append(list, idx)
}

// preferredKeys 返回能提供该模型（上游名）的 key 下标（affinity 命中时），
// 目录未刷新/无记录返回 nil → 请求走全池普通调度。与 models 同锁保护。
func (v *Vendor) preferredKeys(model string) []int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.affinity[model]
}

// hasModel 目标模型（上游名）是否在本源并集目录中（缓存兜底态也计入）。
// 仅目录中存在时才值得为 404 换 key 重试——真不存在的模型各 key 404 一致，
// 不放大请求；目录未刷新（models 为空）时返回 false 保持旧行为。
func (v *Vendor) hasModel(upstreamModel string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, m := range v.models {
		if strings.TrimPrefix(m.ID, v.prefix()) == upstreamModel {
			return true
		}
	}
	return false
}

// filterAllowed 按暴露白名单过滤目录（空白名单 = 全部暴露）。
func (v *Vendor) filterAllowed(models []contract.Model) []contract.Model {
	if len(v.cfg.AllowedModels) == 0 {
		return models
	}
	allow := make(map[string]bool, len(v.cfg.AllowedModels))
	for _, id := range v.cfg.AllowedModels {
		allow[id] = true
	}
	out := make([]contract.Model, 0, len(models))
	for _, m := range models {
		if allow[strings.TrimPrefix(m.ID, v.prefix())] {
			out = append(out, m)
		}
	}
	return out
}

// FullModelIDs 全量模型清单（上游 ID，不含前缀、不经白名单过滤）——编辑界面勾选用。
func (v *Vendor) FullModelIDs() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	out := make([]string, 0, len(v.models))
	for _, m := range v.models {
		out = append(out, strings.TrimPrefix(m.ID, v.prefix()))
	}
	return out
}

// Probe 活性探测：真实拉一次上游目录（无缓存语义），刷新健康状态。
// 返回（是否成功，耗时毫秒，错误描述）。逐 key 细节探测见管理端 test 端点。
func (v *Vendor) Probe(ctx context.Context) (bool, int64, string) {
	key, _, ok := v.pool.tryAcquire(map[int]bool{})
	if !ok {
		v.markErr("probe: 无可用 key（全部冷却或禁用）")
		return false, 0, "无可用 key（全部冷却或禁用）"
	}
	start := time.Now()
	_, err := v.proto.listModels(ctx, v, key)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		v.markErr(fmt.Sprintf("probe: %v", err))
		return false, latency, err.Error()
	}
	v.markOK()
	return true, latency, ""
}

// IsFree 自定义源模型恒可用（key 在网关侧），返回 true。
func (v *Vendor) IsFree(string) bool { return true }

// ErrSemantics 通用语义：瞬时错误可重试/可切换厂商；401/403（key 失效）可切换
// （同名模型或存在其它候选时接手），不进坏池（与代理池健康无关）。
func (v *Vendor) ErrSemantics() contract.ErrRules {
	return contract.ErrRules{
		Retryable:  []int{http.StatusRequestTimeout, http.StatusTooManyRequests, 500, 502, 503, 504},
		Switchable: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusRequestTimeout, http.StatusTooManyRequests, 500, 502, 503, 504},
	}
}

// Auth 自定义源用配置 key，认证头在协议层构造；入站 key 不透传。
func (v *Vendor) Auth(*http.Request) string { return "Bearer " + v.cfg.APIKey }

// Health 实现 contract.Vendor。
func (v *Vendor) Health() contract.VendorHealth {
	v.mu.Lock()
	defer v.mu.Unlock()
	h := contract.VendorHealth{Available: true}
	if !v.lastSuccess.IsZero() {
		h.LastSuccess = v.lastSuccess.Format(time.RFC3339)
	}
	if v.lastErr != "" {
		h.LastError = v.lastErr
	}
	return h
}

func (v *Vendor) markErr(err string) {
	v.mu.Lock()
	v.lastErr = err
	v.mu.Unlock()
}

func (v *Vendor) markOK() {
	v.mu.Lock()
	v.lastErr = ""
	v.lastSuccess = time.Now()
	v.mu.Unlock()
}

// Chat 非流式：原始 OpenAI 请求体 → 上游协议适配 → OpenAI 形态响应。
// key 池调度：优先挑「并集目录中能提供该模型」的 key（多 key 模型子集不一致时
// 直达正确 key），失败按状态码标记并换 key 重试（见 withKeys）。
// 续写/会话粘性：msg.Extra[KeyPreferredIndex] 存在（>=0）时优先命中该 key，
// 选中后把实际 key 下标写回 Extra，供 core 续写重连透传（同请求不换 key）。
func (v *Vendor) Chat(ctx context.Context, msg *contract.Message) (*contract.Reply, error) {
	body, err := v.buildBody(msg, false)
	if err != nil {
		return nil, err
	}
	upstream := v.upstreamModel(msg.Model)
	stickyIdx := preferredKeyFromExtra(msg)
	reply, idx, err := v.withKeys(upstream, stickyIdx, func(key string) (*contract.Reply, error) {
		return v.proto.chat(ctx, v, upstream, key, body)
	})
	if idx >= 0 {
		if msg.Extra == nil {
			msg.Extra = map[string]any{}
		}
		msg.Extra[KeyPreferredIndex] = idx
	}
	return reply, err
}

// ChatStream 流式：返回 OpenAI Chat 形态 SSE（协议层负责原生流转换），同样走 key 池。
// 粘性语义同 Chat：流式中断续写重连时 core 会把首次选中的 key 下标放回 Extra，
// 这里优先命中同一 key，避免续写换 key 导致重复输出/串对话。
func (v *Vendor) ChatStream(ctx context.Context, msg *contract.Message) (*contract.Stream, error) {
	body, err := v.buildBody(msg, true)
	if err != nil {
		return nil, err
	}
	upstream := v.upstreamModel(msg.Model)
	stickyIdx := preferredKeyFromExtra(msg)
	stream, idx, err := v.withKeysStream(upstream, stickyIdx, func(key string) (*contract.Stream, error) {
		return v.proto.chatStream(ctx, v, upstream, key, body)
	})
	if idx >= 0 {
		if msg.Extra == nil {
			msg.Extra = map[string]any{}
		}
		msg.Extra[KeyPreferredIndex] = idx
	}
	return stream, err
}

// preferredKeyFromExtra 读 Extra 里的会话/续写粘性 key 下标（-1 = 无偏好）。
func preferredKeyFromExtra(msg *contract.Message) int {
	if msg == nil || msg.Extra == nil {
		return -1
	}
	idx, ok := msg.Extra[KeyPreferredIndex].(int)
	if !ok {
		return -1
	}
	return idx
}

// withKeys 非流式调用的 key 池编排：按模型亲和 + 调度挑可用 key，429 冷却
// （Retry-After）、401/403 禁用并换下一个 key 重试，404（目标模型在并集目录中
// 但当前 key 不提供）同样换 key 重试；同请求每 key 至多一次；400 等请求级错误
// 与 key 无关立即返回；全部耗尽返回最后一次结果（交由外层厂商级 failover 接管）。
// stickyIdx >= 0 时优先命中该 key（会话粘性/续写同 key）。
func (v *Vendor) withKeys(model string, stickyIdx int, call func(key string) (*contract.Reply, error)) (*contract.Reply, int, error) {
	tried := map[int]bool{}
	var lastReply *contract.Reply
	var lastErr error
	lastIdx := -1
	for {
		key, idx, ok := v.pool.tryAcquirePreferSticky(tried, v.preferredKeys(model), stickyIdx)
		if !ok {
			break
		}
		tried[idx] = true
		reply, err := call(key)
		lastReply, lastErr, lastIdx = reply, err, idx
		if err != nil || reply == nil {
			continue // 传输错误：不标记，换下一个 key
		}
		if reply.Status >= 200 && reply.Status < 300 {
			return reply, idx, nil
		}
		switch {
		case reply.Status == http.StatusUnauthorized || reply.Status == http.StatusForbidden:
			v.pool.disable(idx)
		case reply.Status == http.StatusTooManyRequests:
			v.pool.cool(idx, parseRetryAfter(reply.Headers.Get("Retry-After")))
		case reply.Status == http.StatusNotFound && v.hasModel(model):
			// 多 key 模型子集不一致：当前 key 不提供该模型但并集目录中有它，
			// 换下一个 key 再试（affinity 缺失/过期时的兜底）。
		case reply.Status >= 500 || reply.Status == http.StatusRequestTimeout:
			// 上游侧问题：不标记 key，仅换 key 重试
		default:
			return reply, idx, nil // 请求级错误：换 key 也一样
		}
	}
	return lastReply, lastIdx, lastErr
}

// withKeysStream 流式版（Stream 无响应头可读 Retry-After，429 用缺省冷却）。
// stickyIdx >= 0 时优先命中该 key（续写同 key）。
func (v *Vendor) withKeysStream(model string, stickyIdx int, call func(key string) (*contract.Stream, error)) (*contract.Stream, int, error) {
	tried := map[int]bool{}
	var lastStream *contract.Stream
	var lastErr error
	lastIdx := -1
	for {
		key, idx, ok := v.pool.tryAcquirePreferSticky(tried, v.preferredKeys(model), stickyIdx)
		if !ok {
			break
		}
		tried[idx] = true
		stream, err := call(key)
		lastStream, lastErr, lastIdx = stream, err, idx
		if err != nil || stream == nil {
			continue
		}
		if stream.Status >= 200 && stream.Status < 300 {
			return stream, idx, nil
		}
		switch {
		case stream.Status == http.StatusUnauthorized || stream.Status == http.StatusForbidden:
			v.pool.disable(idx)
		case stream.Status == http.StatusTooManyRequests:
			v.pool.cool(idx, 0)
		case stream.Status == http.StatusNotFound && v.hasModel(model):
			// 同 withKeys：目标在并集目录中，当前 key 不提供 → 换 key 再试。
		case stream.Status >= 500 || stream.Status == http.StatusRequestTimeout:
		default:
			return stream, idx, nil
		}
	}
	return lastStream, lastIdx, lastErr
}

// buildBody 取 Extra 里的原始 OpenAI 请求体，改写 model/stream 后交协议层。
// Extra 缺失（独立调用/测试）时从归一化 Messages 重建最小请求体。
func (v *Vendor) buildBody(msg *contract.Message, stream bool) ([]byte, error) {
	var m map[string]any
	if raw, _ := msg.Extra[keyRawBody].([]byte); len(raw) > 0 {
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, fmt.Errorf("custom %s: bad raw body: %w", v.cfg.ID, err)
		}
	}
	if m == nil {
		m = map[string]any{}
		if len(msg.Messages) > 0 {
			msgs := make([]any, 0, len(msg.Messages))
			for _, mm := range msg.Messages {
				msgs = append(msgs, map[string]any{"role": mm.Role, "content": mm.Content})
			}
			m["messages"] = msgs
		}
	}
	m["model"] = v.upstreamModel(msg.Model)
	m["stream"] = stream
	out, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("custom %s: marshal body: %w", v.cfg.ID, err)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// 协议层共享 HTTP
// ---------------------------------------------------------------------------

// chatProto 单个出站协议的适配器：统一 OpenAI 形态 ⇄ 厂商原生。
type chatProto interface {
	// listModels 拉取上游模型目录（原始 ID，不带本源前缀）；key 由池调度传入。
	listModels(ctx context.Context, v *Vendor, key string) ([]string, error)
	// chat 非流式调用；rawBody 为 OpenAI Chat 请求体，返回 OpenAI Chat 响应体。
	chat(ctx context.Context, v *Vendor, model, key string, rawBody []byte) (*contract.Reply, error)
	// chatStream 流式调用；返回 OpenAI Chat 形态 SSE。
	chatStream(ctx context.Context, v *Vendor, model, key string, rawBody []byte) (*contract.Stream, error)
}

// keyStatusError 协议层携带上游状态码的错误（供 ListModels 的 key 池标记）。
type keyStatusError struct {
	status     int
	retryAfter time.Duration
}

func (e *keyStatusError) Error() string {
	return fmt.Sprintf("upstream status %d", e.status)
}

// do 经统一网关 Transport 发出请求（直连或代理池由配置决定），回传出口节点地址。
// streaming=true 时用无总超时客户端（长推理流不被切断）。
func (v *Vendor) do(ctx context.Context, method, url string, headers map[string]string, body []byte, streaming bool) (*http.Response, string, error) {
	var rd io.Reader
	if body != nil {
		rd = strings.NewReader(string(body))
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return nil, "", err
	}
	for k, val := range headers {
		req.Header.Set(k, val)
	}
	client, addr := v.cfg.Transport.Client(v.tier(), streaming)
	resp, err := client.Do(req)
	if err != nil {
		if addr != "" {
			v.cfg.Transport.Mark(addr, 0, err)
		}
		v.markErr(err.Error())
		return nil, addr, err
	}
	if addr != "" {
		v.cfg.Transport.Mark(addr, resp.StatusCode, nil)
	}
	return resp, addr, nil
}

// readBody 读完并关闭响应体。
func readBody(resp *http.Response) []byte {
	if resp == nil || resp.Body == nil {
		return nil
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 32*1024*1024))
	return b
}

// nopCloser 把已读出的字节包成 ReadCloser（错误体透传用）。
type nopCloser struct{ *bytes.Reader }

func (nopCloser) Close() error { return nil }

var _ io.ReadCloser = nopCloser{}
