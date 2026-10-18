// Package contract 定义厂商与 core 之间的"数据线束"契约。
//
// 每个厂商（vendors/ 下的子文件夹）必须实现 Vendor 接口；
// core 只认本包的契约，厂商之间互不感知。
package contract

import (
	"context"
	"errors"
	"io"
	"net/http"
)

// Capabilities 描述一个模型的能力元数据（可选）。
// 供路由器做"门当户对"匹配（如不支持 tools 的模型就不该接 tools 请求），
// 也供上层 UI 展示模型能力。
type Capabilities struct {
	SupportsTools    bool // 是否支持 function calling / tools
	SupportsThinking bool // 是否支持思考模式
	SupportsVision   bool // 是否支持图片输入
	ContextWindow    int  // 上下文窗口大小（token）
	MaxTokens        int  // 单次最大输出 token（0=未知）
}

// Model 是厂商模型目录中的一条记录。
type Model struct {
	ID       string            // 厂商内部模型名（如 opencode 的 deepseek-v4-flash-free）
	Provider string            // 厂商 ID（如 "opencode" / "custom"）
	Free     bool              // 是否免费额度模型
	Caps     Capabilities      // 能力元数据（可选，未知则零值）
	Meta     map[string]string // 平台附加信息（如 opencode 的目录面 "surface":"zen"|"go"）
}

// Msg 是归一化请求中的一条消息。
type Msg struct {
	Role             string
	Content          any // string 或 []Part（多模态）；与现有 Message.Content 语义一致
	ReasoningContent *string
	ToolCalls        []ToolCall
	ToolCallID       string
	Name             string
}

// ToolCall 是一次工具调用的结果/请求。
type ToolCall struct {
	ID       string
	Type     string
	Function FunctionCall
}

// FunctionCall 是工具调用中的 function 部分。
type FunctionCall struct {
	Name      string
	Arguments string
}

// Tool 定义可被模型调用的工具。
type Tool struct {
	Type     string
	Function ToolFunction
}

// ToolFunction 是工具的函数定义。
type ToolFunction struct {
	Name        string
	Description string
	Parameters  map[string]any
}

// Message 是 core 发给厂商的统一归一化请求（OpenAI Chat 形态）。
// 入站协议（OpenAI/Anthropic/Responses）在 core/protocol 统一转换为该形态；
// 厂商实现负责"该形态 → 厂商原生请求"的适配。
type Message struct {
	Model    string         // 厂商内部模型名（已由 core 解析/别名后）
	Messages []Msg          // 对话消息
	Stream   bool           // 是否流式 SSE
	Options  map[string]any // 通用透传选项：temperature / max_tokens / top_p / tools / thinking / extra_body 等（所有厂商共享语义）
	Extra    map[string]any // 厂商私有区：core 只负责搬运，不解释语义。厂商自定义参数一律放这里，
	// 保持 Options 的通用语义不被厂商键污染（新厂商接入时优先使用本区）。
}

// Reply 是厂商返回给 core 的统一结果。
// Body 为 OpenAI Chat 形态（流式为逐 chunk），core 负责再转回各入站协议。
type Reply struct {
	Body     []byte      // 响应体
	Status   int         // HTTP 状态码
	Headers  http.Header // 响应头（透传安全头）
	NodeAddr string      // 实际使用的出口节点地址（供统计/节点前缀；直连为空）
}

// Stream 是流式响应的封装：ReadCloser + 状态码 + 实际出口节点地址。
type Stream struct {
	io.ReadCloser
	Status   int
	NodeAddr string
}

// Tier 描述一个请求所属层：免费层（通常走代理池）或付费层（直连）。
// 由 core 的 Transport 解释（如 opencode 免费层走 SOCKS5 池、付费层直连）。
type Tier int

const (
	TierFree Tier = iota
	TierPaid
)

// Transport 由 core（网关）注入厂商：厂商负责构造请求与上游语义，
// 实际发出 HTTP 请求（含代理池选择/健康维护）交给 Transport。
// 这是"节点路由/代理池"下沉到 core、厂商实现不复用池的位置。
type Transport interface {
	// Client 返回用于 tier 层的 HTTP 客户端与所用出口节点地址（直连则空字符串）。
	// streaming=true 时返回去掉总超时的流式客户端（避免长推理流被切断）。
	Client(tier Tier, streaming bool) (*http.Client, string)

	// Mark 记录一次请求结果（成功/失败），供代理池健康/冷却维护。
	Mark(proxyAddr string, status int, reqErr error)
}

// Racer 可选的竞速传输（性能模式 P2b——请求级竞速）：
// 一次请求并行扇出多个候选出口，第一个成功（流式 = 首个 chunk）胜出，其余取消。
// Transport 实现方可选择性实现；厂商端做类型断言，未实现则退化为串行 Client。
type Racer interface {
	// CandidateClients 返回质量优先的至多 n 个候选（client + 出口节点地址）。
	// 直连/无候选时返回空（调用方回退普通 Client）。
	CandidateClients(tier Tier, streaming bool, n int) ([]*http.Client, []string)
}

// RaceTracker 可选的竞速压力/在途追踪（S5——自适应竞速）：
// Transport 实现方实现时，厂商在竞速扇出/收尾时上报候选出口地址
// （每节点 in-flight 计数，least-in-flight 均衡），并上报健康节点规模
// （压力系数分母）。未实现时 healthy=0 → 压力系数恒为高 → 退化为单发
// （1 副本，竞速实际被关闭），候选均衡仅按质量分。
type RaceTracker interface {
	// HealthyNodeCount 返回当前健康（可参与竞速）节点数；压力系数分母。
	HealthyNodeCount() int
	// RaceStarted 竞速候选已确定（对每个出口地址 in-flight +1）。
	RaceStarted(addrs []string)
	// RaceFinished 竞速结束（对每个出口地址 in-flight -1，与 RaceStarted 成对）。
	RaceFinished(addrs []string)
}

// DirectTransport 是最简传输实现：直连（http.DefaultClient）、无代理、无健康维护。
// 用于未注入真实网关传输的场景（单元测试、厂商独立冒烟）。
type DirectTransport struct{}

// Client 实现 Transport。
func (DirectTransport) Client(_ Tier, _ bool) (*http.Client, string) {
	return http.DefaultClient, ""
}

// Mark 实现 Transport（无操作）。
func (DirectTransport) Mark(string, int, error) {}

// ErrNoHealthyNode 免费层已配置代理但没有可用节点时的哨兵错误。
var ErrNoHealthyNode = errors.New("no healthy proxy node")

// NoCandidateClient 返回一个 HTTP 客户端，其 RoundTripper 恒返回 ErrNoHealthyNode。
// 用于池空场景，避免构造空代理地址或落到本机直连。
func NoCandidateClient() *http.Client {
	return &http.Client{Transport: noHealthyNodeTransport{}}
}

type noHealthyNodeTransport struct{}

func (noHealthyNodeTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, ErrNoHealthyNode
}

// 现状（opencode）的 401/402/429/503 是写死的，这里按厂商差异化。
type ErrRules struct {
	Retryable  []int // 该状态码整体可重试（如 429/502/503/504）
	Switchable []int // 该状态码可切换下一个候选（另一个厂商/另一个账号）
	BadPool    []int // 连续命中进入坏池的状态码（如 401/402/429/503）
}

// VendorHealth 描述厂商当前健康状态（供 failover 与 UI）。
type VendorHealth struct {
	Available   bool   // 是否可用
	LastError   string // 最近一次错误描述
	LastSuccess string // 最近一次成功时间（RFC3339）
}

// Vendor 是每个厂商子文件夹必须实现的"数据线束"接口。
type Vendor interface {
	// ID 返回厂商唯一标识（如 "opencode"），须与配置 providers[].id 一致。
	ID() string
	// Name 返回厂商展示名（如 "OpenCode"）。
	Name() string

	// ListModels 拉取厂商动态模型目录；core 做聚合与缓存。
	// 厂商内部应先返回静态兜底，再尝试动态刷新。
	ListModels(ctx context.Context) ([]Model, error)

	// IsFree 判定模型是否免费额度模型（厂商自有规则）。
	IsFree(modelID string) bool

	// Chat 发起非流式聊天，返回 OpenAI 形态响应体。
	Chat(ctx context.Context, msg *Message) (*Reply, error)

	// ChatStream 发起流式 chat，返回 SSE 流（core 负责读流/续写）。
	ChatStream(ctx context.Context, msg *Message) (*Stream, error)

	// Auth 根据入站请求构造厂商请求的认证头（如 "Bearer public"、"Bearer sk-..."）。
	Auth(r *http.Request) string

	// ErrSemantics 返回厂商的错误状态码语义（core 据此决定重试/切换/坏账）。
	ErrSemantics() ErrRules

	// Health 返回厂商健康状态。
	Health() VendorHealth
}
