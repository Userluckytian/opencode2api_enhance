// Package remote 插件式供应商桥接厂商（R2，设计定稿 docs/PLUGIN-PROVIDERS.md §四）。
//
// 形态：一个实例 = 一个已就绪的插件子进程。装配参数（子进程端点 url + 一次性令牌）
// 由插件管理器（pluginprovider.Manager.RunningPlugins()）就绪时注入，静态配置无法
// 表达（子进程监听随机端口）。请求带 Authorization: Bearer 令牌直连 127.0.0.1 子进程：
// GET /v1/models + POST /v1/chat/completions（stream:true 返回 SSE），响应原样透传，
// 对齐 vendors/custom 的 openaiProto 语义。上游特殊需求（traceparent、token 头、
// session 续期等）全部由子进程自行处理，本厂商不感知。
//
// 与 custom 的差异：无多 key 池（只有子进程这一个端点，无 key 调度）；出站恒直连
// （子进程仅监听 127.0.0.1，走代理池没有意义）；健康状态反映子进程可达性
// （连接失败 → Available=false + LastError，供 failover 与面板展示）。
package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/6Kmfi6HP/opencode2api/core/contract"
)

// keyRawBody 与 core 适配层（upstream.go chatViaVendor）注入的原始 OpenAI 请求体
// Extra 键同值（与 vendors/custom 同款语义）。原始体保留 tools/options 等完整字段，
// 优于归一化 Messages 重建。
const keyRawBody = "_oc_raw_body"

// Config 构造参数。
type Config struct {
	ID        string             // 插件 id（模型目录前缀；与 pluginprovider 插件 id 一致）
	Name      string             // 展示名（provider.json name；空 = ID）
	BaseURL   string             // 子进程 HTTP 端点（pluginprovider Endpoint 的 url，如 http://127.0.0.1:54321）
	AuthToken string             // 一次性令牌（子进程就绪行回显校验后保留）
	Transport contract.Transport // 由 core（网关）注入；未注入 → DirectTransport 直连
	// AllowedModels 暴露白名单（主进程侧过滤，对齐 vendors/custom 同名语义）：
	// 空 = 全部暴露；非空 = 仅在 ListModels 返回白名单内的模型。目录仍拉全量，
	// 仅返回时过滤——编辑界面始终能拿到全量清单。
	AllowedModels []string
}

// Vendor 插件子进程桥接厂商。
type Vendor struct {
	cfg         Config
	mu          sync.Mutex
	available   bool // 子进程当前可达（请求失败置 false，成功后恢复）
	lastErr     string
	lastSuccess time.Time
}

// New 构造桥接厂商。
func New(cfg Config) (*Vendor, error) {
	if cfg.ID == "" {
		return nil, fmt.Errorf("remote: id is required")
	}
	if cfg.Name == "" {
		cfg.Name = cfg.ID
	}
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("remote %s: base_url (子进程端点) is required", cfg.ID)
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if cfg.Transport == nil {
		cfg.Transport = contract.DirectTransport{}
	}
	return &Vendor{cfg: cfg, available: true}, nil
}

// ---------------------------------------------------------------------------
// contract.Vendor
// ---------------------------------------------------------------------------

func (v *Vendor) ID() string   { return v.cfg.ID }
func (v *Vendor) Name() string { return v.cfg.Name }

// prefix 模型目录前缀（"{id}/"）。
func (v *Vendor) prefix() string { return v.cfg.ID + "/" }

// upstreamModel 把对外模型名（带前缀）还原为子进程侧真实模型名。
func (v *Vendor) upstreamModel(model string) string {
	return strings.TrimPrefix(model, v.prefix())
}

// ListModels 拉取子进程模型目录并加前缀。子进程不可达/非 2xx → 返回错误（上层 failover
// 接管、本次聚合目录不并入）；成功但空列表按失败处理（防上游抖动把目录清空）。
func (v *Vendor) ListModels(ctx context.Context) ([]contract.Model, error) {
	resp, err := v.do(ctx, http.MethodGet, v.cfg.BaseURL+"/v1/models",
		v.headers(false), nil, false)
	if err != nil {
		return nil, err
	}
	body := readBody(resp)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errMsg := fmt.Sprintf("remote %s: list models: HTTP %d: %s", v.cfg.ID, resp.StatusCode, truncateErr(body))
		v.markErr(errMsg)
		return nil, errors.New(errMsg)
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("remote %s: bad models response: %w", v.cfg.ID, err)
	}
	if len(out.Data) == 0 {
		return nil, fmt.Errorf("remote %s: empty model list", v.cfg.ID)
	}
	v.markOK()
	models := make([]contract.Model, 0, len(out.Data))
	for _, m := range out.Data {
		if m.ID == "" {
			continue
		}
		models = append(models, contract.Model{
			ID:       v.prefix() + m.ID,
			Provider: v.cfg.ID,
			// 令牌由网关持有，客户端无需携带 → 对外即「免费可用」目录（与 custom 同款语义）。
			Free: true,
		})
	}
	models = v.filterAllowed(models)
	if len(models) == 0 {
		return nil, fmt.Errorf("remote %s: empty model list", v.cfg.ID)
	}
	return models, nil
}

// filterAllowed 按暴露白名单过滤目录（空白名单 = 全部暴露）。
// 对齐 vendors/custom filterAllowed：目录/缓存保存全量，仅在 ListModels 返回时过滤。
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

// IsFree 插件模型恒可用（令牌藏在网关侧），返回 true。
func (v *Vendor) IsFree(string) bool { return true }

// ErrSemantics 与 custom 同款：瞬时错误可重试/可切换厂商；401/403（令牌失效）可切换
// （同名模型或存在其它候选时接手），不进坏池（与代理池健康无关）。
func (v *Vendor) ErrSemantics() contract.ErrRules {
	return contract.ErrRules{
		Retryable:  []int{http.StatusRequestTimeout, http.StatusTooManyRequests, 500, 502, 503, 504},
		Switchable: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusRequestTimeout, http.StatusTooManyRequests, 500, 502, 503, 504},
	}
}

// Auth 桥接鉴权头：透传一次性令牌（入站 key 不透传，子进程只认此令牌）。
func (v *Vendor) Auth(*http.Request) string { return "Bearer " + v.cfg.AuthToken }

// Health 实现 contract.Vendor：子进程不可达/最近请求失败 → Available=false + LastError。
func (v *Vendor) Health() contract.VendorHealth {
	v.mu.Lock()
	defer v.mu.Unlock()
	h := contract.VendorHealth{Available: v.available}
	if !v.lastSuccess.IsZero() {
		h.LastSuccess = v.lastSuccess.Format(time.RFC3339)
	}
	if v.lastErr != "" {
		h.LastError = v.lastErr
	}
	return h
}

// Chat 非流式：raw body 透传（改写 model 剥前缀 + stream=false）→ 子进程；
// 响应体/状态码原样回传（错误体同样透传，供上层 failover 判定）。
func (v *Vendor) Chat(ctx context.Context, msg *contract.Message) (*contract.Reply, error) {
	body, err := v.buildBody(msg, false)
	if err != nil {
		return nil, err
	}
	resp, err := v.do(ctx, http.MethodPost, v.cfg.BaseURL+"/v1/chat/completions",
		v.headers(false), body, false)
	if err != nil {
		return nil, err
	}
	body = readBody(resp)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		v.markErr(fmt.Sprintf("remote %s: chat: HTTP %d: %s", v.cfg.ID, resp.StatusCode, truncateErr(body)))
	} else {
		v.markOK()
	}
	return &contract.Reply{Body: body, Status: resp.StatusCode, Headers: resp.Header}, nil
}

// ChatStream 流式：同上但 stream:true，2xx 时响应体作为 SSE 流返回（core 负责读流/续写）。
func (v *Vendor) ChatStream(ctx context.Context, msg *contract.Message) (*contract.Stream, error) {
	body, err := v.buildBody(msg, true)
	if err != nil {
		return nil, err
	}
	resp, err := v.do(ctx, http.MethodPost, v.cfg.BaseURL+"/v1/chat/completions",
		v.headers(true), body, true)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b := readBody(resp)
		v.markErr(fmt.Sprintf("remote %s: chat stream: HTTP %d: %s", v.cfg.ID, resp.StatusCode, truncateErr(b)))
		return &contract.Stream{ReadCloser: nopCloser{bytes.NewReader(b)}, Status: resp.StatusCode}, nil
	}
	v.markOK()
	return &contract.Stream{ReadCloser: resp.Body, Status: resp.StatusCode}, nil
}

// ---------------------------------------------------------------------------
// 内部 HTTP
// ---------------------------------------------------------------------------

func (v *Vendor) headers(stream bool) map[string]string {
	h := map[string]string{
		"Content-Type":  "application/json",
		"Authorization": "Bearer " + v.cfg.AuthToken,
	}
	if stream {
		h["Accept"] = "text/event-stream"
	}
	return h
}

// do 经注入 Transport 发出请求，恒走 TierPaid（直连：子进程仅监听 127.0.0.1，
// 走代理池没有意义）。streaming=true 时用无总超时客户端（长推理流不被切断）。
func (v *Vendor) do(ctx context.Context, method, url string, headers map[string]string, body []byte, streaming bool) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return nil, err
	}
	for k, val := range headers {
		req.Header.Set(k, val)
	}
	client, _ := v.cfg.Transport.Client(contract.TierPaid, streaming)
	resp, err := client.Do(req)
	if err != nil {
		// 子进程不可达（连接失败/超时）：健康置灰，上层 failover 接管。
		v.markErr(fmt.Sprintf("remote %s: 子进程不可达: %v", v.cfg.ID, err))
		return nil, err
	}
	return resp, nil
}

// buildBody 取 Extra 里的原始 OpenAI 请求体，改写 model（剥前缀）/ stream 后透传。
// Extra 缺失（独立调用/测试）时从归一化 Messages 重建最小请求体（与 custom 同款）。
func (v *Vendor) buildBody(msg *contract.Message, stream bool) ([]byte, error) {
	var m map[string]any
	if raw, _ := msg.Extra[keyRawBody].([]byte); len(raw) > 0 {
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, fmt.Errorf("remote %s: bad raw body: %w", v.cfg.ID, err)
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
		return nil, fmt.Errorf("remote %s: marshal body: %w", v.cfg.ID, err)
	}
	return out, nil
}

func (v *Vendor) markErr(err string) {
	v.mu.Lock()
	v.available = false
	v.lastErr = err
	v.mu.Unlock()
}

func (v *Vendor) markOK() {
	v.mu.Lock()
	v.available = true
	v.lastErr = ""
	v.lastSuccess = time.Now()
	v.mu.Unlock()
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

// truncateErr 错误体摘要（日志/Health 用，防大段 HTML 刷屏）。
func truncateErr(b []byte) string {
	s := string(b)
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}
