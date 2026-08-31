// Package opencode 实现 OpenCode 厂商的"数据线束"（contract.Vendor）。
//
// 迁移自 package main 中的 opencode 硬编码逻辑（session / 模型目录 / 上游调用语义）。
// 本包不含代理池与 HTTP 客户端——通过 contract.Transport 由 core（网关）注入，
// 代理池/健康维护在 core/gateway 侧；厂商只负责"构造请求 + 解释响应 + 上游语义"。
package opencode

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/6Kmfi6HP/opencode2api/core/contract"
)

// 上游端点（OpenCode 专属）。
const (
	zenModelsURL = "https://opencode.ai/zen/v1/models"
	goModelsURL  = "https://opencode.ai/zen/go/v1/models"
	versionURL   = "https://registry.npmjs.org/opencode-ai/latest"
	versionDef   = "1.15.3"

	// surfaceZen / surfaceGo 是 contract.Model.Meta 中 "surface" 键的取值，
	// 用于保留 zen 目录与 go 目录的区分（路由/目录过滤需要）。
	surfaceZen = "zen"
	surfaceGo  = "go"
)

// versionFetchTimeout 版本探测独立预算：npm 仓库经 SOCKS 不可达时，
// 不得被 HTTP client 总超时（约 300s）拖住目录刷新与首包聊天。
// 测试可缩短该值；生产默认 5s。
var versionFetchTimeout = 5 * time.Second

// Extra 键（厂商私有区，见 contract.Message.Extra）：core 只搬运不解释，
// 通用透传选项（temperature/max_tokens/tools 等）仍走 contract.Message.Options。
const (
	// KeyRawBody 存放已由 core/protocol 归一化的 OpenAI Chat 请求体（[]byte），Chat 阶段使用。
	KeyRawBody = "_oc_raw_body"
	// KeyAuthMode 存放认证路由模式："public"|"auto"|"zen"|"go"。
	KeyAuthMode = "_oc_auth_mode"
	// KeyAuthToken 存放透传密钥（不含前缀；public 为空）。
	KeyAuthToken = "_oc_auth_token"
)

// Config 是厂商装配配置（由 core 提供）。
type Config struct {
	ID   string // 厂商标识（默认 "opencode"）
	Name string // 展示名（默认 "OpenCode"）
	// Transport 由 core（网关）注入的 HTTP 传输（含代理池）。nil 时用 contract.DirectTransport。
	Transport contract.Transport
	// AdminPassword 本地门禁密钥：客户端用它修复应视为 public（免费）而非透传付费 key。
	AdminPassword string
	// RaceCopies 请求级竞速并行数上限（P2b）：>1 且 Transport 支持 contract.Racer 时，
	// 一次请求并行扇出该数量的候选出口，首个成功者胜（默认 1 = 关闭竞速）。
	// S5 起语义为上限：实际副本由压力系数（活跃请求数/健康节点数）动态分段决定。
	RaceCopies int
	// RaceBudgetMS 竞速整体预算（毫秒，S1）：raceDo 等待首个成功候选的上限，
	// 到期返回错误走单发续写（0 = 默认 10s）。
	RaceBudgetMS int
	// RacePressureLow / RacePressureHigh 压力系数分段阈值（S5，默认 0.5 / 1.0）：
	// pressure < low → 用满 RaceCopies；low ≤ pressure < high → 2；≥ high → 1。
	RacePressureLow  float64
	RacePressureHigh float64
	// RateLimitCooldownSec 429 冷却（秒，S2）：距最近一次 429 小于该值时跳过竞速走单发，
	// 避免限流时扇出放大请求量（0 = 默认 30）。
	RateLimitCooldownSec int
	// RateLimitBackoffBaseMS / RateLimitBackoffCapMS 429 指数退避（毫秒，S2）：
	// 429 重试前 sleep min(base*2^n, cap)（0 = 默认 1000 / 30000）。
	RateLimitBackoffBaseMS int
	RateLimitBackoffCapMS  int
}

// Vendor 实现 contract.Vendor，代表 OpenCode 上游。
type Vendor struct {
	cfg Config
	tr  contract.Transport

	// last429 最近一次 429 的 UnixNano 时间戳（S2）：冷却期内跳过竞速。0 = 从未 429。
	last429 atomic.Int64

	// 会话状态（原全局 ocSession* 收敛为实例字段）
	ocSessionID string
	ocProjectID string
	ocClientVer string
	ocOnce      sync.Once

	// 模型目录缓存
	modelMu  sync.RWMutex
	cacheAll []contract.Model // ListModels 合并结果
}

// New 构造 OpenCode 厂商。
func New(cfg Config) *Vendor {
	if cfg.ID == "" {
		cfg.ID = "opencode"
	}
	if cfg.Name == "" {
		cfg.Name = "OpenCode"
	}
	tr := cfg.Transport
	if tr == nil {
		tr = contract.DirectTransport{}
	}
	// 传输在 New 阶段固化，transport() 仅只读返回——去掉懒初始化对 v.tr 的无锁写
	// （G4：并发首访时数据竞争）。
	v := &Vendor{cfg: cfg, tr: tr}
	// 预热磁盘缓存：启动首拉失败时 ListModels 也能立即给出上一代目录。
	v.cacheAll = v.loadModelsCache()
	return v
}

// ---------------------------------------------------------------------------
// contract.Vendor 基础接口
// ---------------------------------------------------------------------------

// ID 实现 contract.Vendor。
func (v *Vendor) ID() string { return v.cfg.ID }

// Name 实现 contract.Vendor。
func (v *Vendor) Name() string { return v.cfg.Name }

// transport 返回注入的传输层，未注入时已由 New 固化直连（只读，无懒初始化）。
func (v *Vendor) transport() contract.Transport { return v.tr }

// sessionID 保证会话已初始化并返回当前 session id。
func (v *Vendor) sessionID() string {
	v.ocOnce.Do(func() {
		v.ocClientVer = v.fetchOCVersion()
		v.ocSessionID = "ses_" + randomString(24)
		v.ocProjectID = randomHex(40)
		slog.Info("opencode session initialized", "version", v.ocClientVer, "session_id", v.ocSessionID)
	})
	return v.ocSessionID
}

// refreshOCSession 强制刷新会话（供管理端/401 恢复调用）。
func (v *Vendor) refreshOCSession() {
	v.ocClientVer = v.fetchOCVersion()
	v.ocSessionID = "ses_" + randomString(24)
	v.ocProjectID = randomHex(40)
	slog.Info("opencode session refreshed", "version", v.ocClientVer, "session_id", v.ocSessionID)
	v.ocOnce = sync.Once{}
}

// RefreshSession 强制刷新会话（管理端 reload 等外部入口调用）。
func (v *Vendor) RefreshSession() {
	v.refreshOCSession()
}

// SessionID 返回当前会话 ID（未初始化则按需初始化）。
func (v *Vendor) SessionID() string {
	return v.sessionID()
}

func (v *Vendor) fetchOCVersion() string {
	ctx, cancel := context.WithTimeout(context.Background(), versionFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, versionURL, nil)
	if err != nil {
		return versionDef
	}
	req.Header.Set("Accept", "application/json")
	client, _ := v.transport().Client(contract.TierFree, false)
	resp, err := client.Do(req)
	if err != nil {
		return versionDef
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return versionDef
	}
	body, _ := io.ReadAll(resp.Body)
	var info struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(body, &info) == nil && info.Version != "" {
		return info.Version
	}
	return versionDef
}

// ListModels 实现 contract.Vendor：拉取 zen + go 两个目录，返回合并列表。
// 成功则更新内存/磁盘缓存；失败或空目录回退上一代（stale-while-revalidate），
// 保证进程重启后聚合器首次 Refresh 即可带上 OpenCode 模型。
func (v *Vendor) ListModels(ctx context.Context) ([]contract.Model, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	client, _ := v.transport().Client(contract.TierFree, false)
	var out []contract.Model
	complete := true
	for _, ep := range []struct{ url, surface string }{
		{zenModelsURL, surfaceZen},
		{goModelsURL, surfaceGo},
	} {
		req, err := http.NewRequestWithContext(ctx, "GET", ep.url, nil)
		if err != nil {
			complete = false
			continue
		}
		req.Header.Set("Authorization", "Bearer public")
		req.Header.Set("x-opencode-session", v.sessionID())
		resp, err := client.Do(req)
		if err != nil {
			complete = false
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			complete = false
			continue
		}
		var cat struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if json.Unmarshal(body, &cat) != nil {
			complete = false
			continue
		}
		for _, m := range cat.Data {
			out = append(out, contract.Model{
				ID:       m.ID,
				Provider: v.cfg.ID,
				Free:     v.isFree(m.ID),
				Meta:     map[string]string{"surface": ep.surface},
			})
		}
	}
	// 任一端点失败或合并结果为空：优先保留上一代完整缓存，避免 zen 成功、go
	// 失败时把 go 模型从目录里冲掉。无上一代时，部分目录也比空列表有用。
	if !complete || len(out) == 0 {
		if cached := v.fallbackModels(); len(cached) > 0 {
			return cached, nil
		}
		if len(out) == 0 {
			return out, nil
		}
	}
	v.modelMu.Lock()
	v.cacheAll = append([]contract.Model(nil), out...)
	v.modelMu.Unlock()
	v.saveModelsCache(out)
	return out, nil
}

// Cache 返回最近一次 ListModels 的结果（core 聚合用；未拉取则自动拉一次）。
func (v *Vendor) Cache(ctx context.Context) []contract.Model {
	v.modelMu.RLock()
	cached := v.cacheAll
	v.modelMu.RUnlock()
	if len(cached) > 0 {
		return cached
	}
	all, err := v.ListModels(ctx)
	if err != nil {
		return cached
	}
	v.modelMu.Lock()
	v.cacheAll = all
	v.modelMu.Unlock()
	return all
}

// IsFree 实现 contract.Vendor：沿用既有规则（-free 后缀 / big-pickle）。
func (v *Vendor) IsFree(modelID string) bool {
	return v.isFree(modelID)
}

func (v *Vendor) isFree(modelID string) bool {
	return strings.Contains(strings.ToLower(modelID), "-free") || strings.EqualFold(modelID, "big-pickle")
}

// ErrSemantics 实现 contract.Vendor：opencode 的可重试/可切换/坏账状态码。
// Retryable 是 chat 重试循环的唯一状态码来源（另附通用 5xx 兜底，见 chat.go isRetryable）。
func (v *Vendor) ErrSemantics() contract.ErrRules {
	return contract.ErrRules{
		Retryable:  []int{http.StatusUnauthorized, http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout},
		Switchable: []int{http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusTooManyRequests, http.StatusServiceUnavailable},
		BadPool:    []int{http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusTooManyRequests, http.StatusServiceUnavailable},
	}
}

// Health 实现 contract.Vendor（前置阶段：常绿；P3 接入真实探测）。
func (v *Vendor) Health() contract.VendorHealth {
	return contract.VendorHealth{Available: true}
}

// Auth 实现 contract.Vendor：根据入站请求构造厂商对上游的认证头。
// 门禁（本层密钥校验）由 core/gateway 负责；这里解析的是客户端想要的"上游模式"：
//   - 无头 / Bearer public / 占位 key → Bearer public
//   - 其它 → Bearer <token>
//
// 更细的 go:/zen: 前缀路由在后续 Chat 切流阶段转移到内部 auth 判定（保持现状行为）。
func (v *Vendor) Auth(r *http.Request) string {
	hdr := r.Header.Get("Authorization")
	if !strings.HasPrefix(hdr, "Bearer ") {
		return "Bearer public"
	}
	token := strings.TrimSpace(strings.TrimPrefix(hdr, "Bearer "))
	if token == "" || token == "public" {
		return "Bearer public"
	}
	return "Bearer " + token
}

// ---------------------------------------------------------------------------
// 会话初始化钩子（测试/管理端用）
// ---------------------------------------------------------------------------

// SetSession 预置会话并消费 once（跳过版本探测）。core 装配/测试时使用。
func (v *Vendor) SetSession(ver, sid, pid string) {
	v.ocClientVer = ver
	v.ocSessionID = sid
	v.ocProjectID = pid
	v.ocOnce.Do(func() {})
}

// SetCatalog 注入模型目录缓存（core/aggregator 刷新后回填；也供测试）。
// 空列表不覆盖既有缓存：聊天若早于首次聚合刷新到达，不得把 New 预热的上一代目录冲掉。
func (v *Vendor) SetCatalog(models []contract.Model) {
	if len(models) == 0 {
		return
	}
	v.modelMu.Lock()
	v.cacheAll = append([]contract.Model(nil), models...)
	v.modelMu.Unlock()
}

// ---------------------------------------------------------------------------
// 工具
// ---------------------------------------------------------------------------

// randomString 生成 n 位小写字母+数字随机串。
func randomString(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	rand.Read(b)
	for i := range b {
		b[i] = letters[b[i]%byte(len(letters))]
	}
	return string(b)
}

// randomHex 生成 n 位十六进制随机串。
func randomHex(n int) string {
	const hex = "0123456789abcdef"
	b := make([]byte, n)
	rand.Read(b)
	for i := range b {
		b[i] = hex[b[i]%byte(len(hex))]
	}
	return string(b)
}
