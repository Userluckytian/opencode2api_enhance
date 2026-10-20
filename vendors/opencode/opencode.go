// Package opencode 实现 OpenCode 厂商的"数据线束"（contract.Vendor）。
//
// 迁移自 package main 中的 opencode 硬编码逻辑（session / 模型目录 / 上游调用语义）。
// 本包不含代理池与 HTTP 客户端——通过 contract.Transport 由 core（网关）注入，
// 代理池/健康维护在 core/gateway 侧；厂商只负责"构造请求 + 解释响应 + 上游语义"。
package opencode

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/6Kmfi6HP/opencode2api/core/contract"
)

// minFreeTierVersion 是上游免费通道（Authorization: Bearer public）要求的最低客户端版本。
// 上游按 User-Agent "opencode/<版本>" 识别客户端：低于该版本返回 426 UpgradeRequired
// （"OpenCode 1.18.0 or newer is required to use the free tier"），
// 无版本号或第三方 UA 返回 403 FreeTierError。
//
// 下限由上游单方面抬升且会继续往上走（1.17.0 → 1.18.0，2026-10-07 再次上调，
// 免费模型全量 426）。历史排查见 docs/issue-log/2026-09-17.md。
const minFreeTierVersion = "1.18.0"

// minV2Version 是 v2 身份契约的版本线。上游 2026-10-14 把 keyless 免费推理迁到
// /inference/v1 并收紧身份校验：UA 必须形如 opencode/<channel>/<version>/<client>
// （官方 App.useragent），版本段低于 2.x 会被判为非官方客户端（403 FreeTierError）。
// npm 上 opencode-ai 的稳定线仍停在 1.19.0，故探测值达不到该线时一律上报 versionDef。
const minV2Version = "2.0.0"

// freeTierUAChannel / freeTierUAClient 是 UA 的 channel 与 client 段。
// 官方打包时用 OPENCODE_CHANNEL 注入 channel（发布流水线取 prod，beta 线取 beta），
// client 段取 App name（CLI 为 cli）。实测 latest / prod 均可放行，这里取官方发布线的 prod。
const (
	freeTierUAChannel = "prod"
	freeTierUAClient  = "cli"

	// freeTierProject 是 x-opencode-project 的取值：官方 CLI 在无项目上下文时用 global。
	freeTierProject = "global"
)

// 上游端点（OpenCode 专属）。
const (
	zenModelsURL = "https://opencode.ai/zen/v1/models"
	goModelsURL  = "https://opencode.ai/zen/go/v1/models"
	versionURL   = "https://registry.npmjs.org/opencode-ai/latest"

	// 免费档（Bearer public / keyless）推理端点。上游 2026-10-14 起把 keyless 免费推理
	// 从 /zen/v1/* 迁到 /inference/v1/*，旧路径对免费档**任何**形态的请求一律回
	// 403 FreeTierError（实测：同 body 仅换端点 → 403；换到 /inference/v1 且身份契约完整 → 200）。
	inferenceChatURL      = "https://opencode.ai/inference/v1/chat/completions"
	inferenceResponsesURL = "https://opencode.ai/inference/v1/responses"

	// 带 key 的 zen / go 通道端点（上游未下线，保持原样）。
	zenChatURL   = "https://opencode.ai/zen/v1/chat/completions"
	zenGoChatURL = "https://opencode.ai/zen/go/v1/chat/completions"

	// versionDef 是版本探测失败（npm 经 SOCKS 不可达 / 超时 / 非 2xx）或探测值低于
	// minV2Version 时的兜底上报版本。上游按「版本 ≥ 下限」单向放行，且免费档已进入
	// v2 身份契约，故兜底取社区实测可用的 v2 版本（opencode/latest/2.0.29/cli）。
	versionDef = "2.0.29"

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
	// ClientVersion 覆盖免费通道上报给上游的客户端版本（User-Agent: opencode/<值>）。
	// 空 = 默认行为：启动时探测 npm 最新版本，探测失败回落 versionDef。
	// 用户可配（providers[].params.client_version）：上游抬高免费通道下限时，
	// 改配置即可跟进，无需重编译；置空/删除则回到自动探测。
	ClientVersion string
	// PreferResponses 出站协议偏好（providers[].params.prefer_responses，阶段 W）：
	//   "chat"（含空/非法值）= 现状：仅 muse 前缀走 Responses；
	//   "responses" = 全部模型优先走上游原生 /responses（上游行为矩阵实测见
	//   docs/ai-framework/plans/probes/RESULTS.md），带图片请求自动回落 chat。
	PreferResponses string
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
		v.ocSessionID = "ses_" + sessionIDString()
		v.ocProjectID = randomHex(40)
		slog.Info("opencode session initialized", "version", v.ocClientVer, "session_id", v.ocSessionID)
	})
	return v.ocSessionID
}

// refreshOCSession 强制刷新会话（供管理端/401 恢复调用）。
func (v *Vendor) refreshOCSession() {
	v.ocClientVer = v.fetchOCVersion()
	v.ocSessionID = "ses_" + sessionIDString()
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

// freeTierUserAgent 组装免费档上报的 v2 客户端 UA：opencode/<channel>/<version>/<client>。
// 上游只接受这一形态（旧版 "opencode/1.19.0" 或缺少 channel/client 段的串均 403）；
// 探测值达不到 v2 线时回落 versionDef。
func (v *Vendor) freeTierUserAgent() string {
	ver := v.ocClientVer
	if ver == "" || !versionAtLeast(ver, minV2Version) {
		ver = versionDef
	}
	return fmt.Sprintf("opencode/%s/%s/%s", freeTierUAChannel, ver, freeTierUAClient)
}

// applyFreeTierIdentity 写免费档（keyless, Authorization: Bearer public）身份头。
//
// v2 契约（2026-10-14 上游收紧，见 docs/issue-log/2026-10-15.md）：
//   - 四个会话头必须同值：x-opencode-session / x-opencode-session-id /
//     x-session-affinity / x-session-id（缺任一或不同值 → 403）；
//   - 旧的 x-opencode-request 已不再发送 —— 上游见到它就回 403（实测：同一请求带该头
//     403、去掉即 200），官方 2.0.29 客户端也不再发这个头；
//   - x-opencode-project 用官方 CLI 的默认值 global；
//   - UA 必须是 v2 形态（见 freeTierUserAgent）。
//
// 头集合与官方 packages/core/src/session/model-request.ts 的 http.headers 逐头对齐；
// 实测证明官方不发的 x-opencode-request-id 同样非必需（带与不带均 200），故不再多带非官方头。
func (v *Vendor) applyFreeTierIdentity(req *http.Request) {
	ses := v.sessionID()
	req.Header.Set("User-Agent", v.freeTierUserAgent())
	req.Header.Set("x-opencode-client", freeTierUAClient)
	req.Header.Set("x-opencode-project", freeTierProject)
	req.Header.Set("x-opencode-session", ses)
	req.Header.Set("x-opencode-session-id", ses)
	req.Header.Set("x-session-affinity", ses)
	req.Header.Set("x-session-id", ses)
}

func (v *Vendor) fetchOCVersion() string {
	// 配置显式指定时直接采用，跳过 npm 探测：探测走代理池出口本就不可靠，
	// 而用户显式配置是确定值，不该被探测失败覆盖。
	if v.cfg.ClientVersion != "" {
		return v.cfg.ClientVersion
	}
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
		// 免费档已进入 v2 身份契约：低于 minV2Version 的发布线（npm 稳定线 1.19.0）
		// 会被上游判为非官方客户端，回落到 v2 兜底值而不是把它原样上报。
		if versionAtLeast(info.Version, minV2Version) {
			return info.Version
		}
		slog.Warn("opencode version below v2 identity line, using floor",
			"got", info.Version, "floor", versionDef, "min", minV2Version)
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

// freeModelsWithoutSuffix 集中维护上游明确免费、但 ID 不带 `-free` 后缀的模型。
// OpenCode 偶尔会用无后缀 ID 发布免费模型；新增例外时只改此处，避免目录与展示规则漂移。
var freeModelsWithoutSuffix = [...]string{
	"big-pickle",
	"union-alpha",
}

// IsFreeModel 判断 OpenCode 模型是否属于免费通道。
func IsFreeModel(modelID string) bool {
	if strings.Contains(strings.ToLower(modelID), "-free") {
		return true
	}
	for _, freeModelID := range freeModelsWithoutSuffix {
		if strings.EqualFold(modelID, freeModelID) {
			return true
		}
	}
	return false
}

// IsFree 实现 contract.Vendor。
func (v *Vendor) IsFree(modelID string) bool {
	return IsFreeModel(modelID)
}

func (v *Vendor) isFree(modelID string) bool {
	return IsFreeModel(modelID)
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

// randomBase62 生成 n 位 base62 随机串（0-9A-Za-z），与官方客户端身份串字符集一致。
func randomBase62(n int) string {
	const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	b := make([]byte, n)
	rand.Read(b)
	for i := range b {
		b[i] = alphabet[b[i]%byte(len(alphabet))]
	}
	return string(b)
}

// idTimeShift 是标识符时间戳的左移位数：低 12 位留给同毫秒内的自增计数器。
const idTimeShift = 12

// encodeIDPrefix 把毫秒时间戳编码为 12 位小写十六进制前缀（低 48 位，大端）。
// descending=true 取反（会话/降序语义），false 保持升序（请求/升序语义）。
//
// 背景（2026-10-13 修复 403 FreeTierError）：9router（decolua/9router，第三方网关，
// **不是** OpenCode 官方仓库）的 issue #4780 / PR #4786 报告，上游 Zen 自 opencode
// 1.19.0 起不再只做正则校验，而是把身份串前 12 位十六进制**解码**为标识符时间戳，
// 语义等价于官方 Identifier：
//
//	ses_（会话，降序）：~(BigInt(ms) * 0x1000n + counter)  取低 48 位
//	msg_（请求，升序）： (BigInt(ms) * 0x1000n + counter)  取低 48 位
//
// 只满足正则但不含时间语义的前缀（本项目的历史实现是 crypto/rand 随机十六进制）会被判为
// 第三方客户端并回 403 FreeTierError："can only be used from within OpenCode"。
// 编码公式已用官方 CLI 的真实标识符 + 其日志时间戳交叉验证（见 identity_time_test.go）。
//
// 可观测指纹（counter=1 时）：ses_ 前缀以 "ffe" 结尾、msg_ 前缀以 "001" 结尾；counter 变化时
// 前缀尾三位随之变化（counter=0xFFF → "fff"/"000"），故指纹只在 counter=1 时成立。
//
// 会话保持每进程稳定（ocOnce），不随请求重建：据 9router 报告，上游免费档配额按会话说账，
// 频繁新建会话会打成 429。该报告同时称「会话年龄不被校验」，本仓库**未独立验证**该点，
// 仅作为「会话可长期复用」的依据记录在此。
func encodeIDPrefix(ms int64, counter uint64, descending bool) string {
	v := uint64(ms)<<idTimeShift + counter
	if descending {
		v = ^v
	}
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	return hex.EncodeToString(b[2:8]) // 仅取低 48 位（6 字节）
}

// idCounter 复刻官方「同毫秒自增计数器」语义：毫秒变化归 1，同毫秒内递增。
// 上游用它在同一毫秒内区分标识符；若恒定取 1，同毫秒发出的多个 x-opencode-request 会共享
// 前缀（后缀随机故不会重复，但形态与官方不一致）。会话与请求各持一个计数器，对应官方的
// Identifier.descending("session") 与 Identifier.ascending("message") 两个独立生成器。
type idCounter struct {
	mu sync.Mutex
	ms int64
	n  uint64
}

func (c *idCounter) next(ms int64) uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ms != c.ms {
		c.ms = ms
		c.n = 1
	} else {
		c.n++
	}
	return c.n
}

var (
	sessionIDCounter idCounter
	requestIDCounter idCounter
)

// sessionIDString 返回会话身份串主体（不含 "ses_" 前缀）。
func sessionIDString() string {
	ms := time.Now().UnixMilli()
	return encodeIDPrefix(ms, sessionIDCounter.next(ms), true) + randomBase62(14)
}

// requestIDString 返回请求身份串主体（不含 "msg_" 前缀）。
func requestIDString() string {
	ms := time.Now().UnixMilli()
	return encodeIDPrefix(ms, requestIDCounter.next(ms), false) + randomBase62(14)
}

// versionAtLeast 判断 x.y.z 版本串 a 是否 >= b（缺失段按 0，解析失败按 0）。
func versionAtLeast(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < 3; i++ {
		av, bv := 0, 0
		if i < len(as) {
			av, _ = strconv.Atoi(strings.TrimSpace(as[i]))
		}
		if i < len(bs) {
			bv, _ = strconv.Atoi(strings.TrimSpace(bs[i]))
		}
		if av != bv {
			return av > bv
		}
	}
	return true
}
