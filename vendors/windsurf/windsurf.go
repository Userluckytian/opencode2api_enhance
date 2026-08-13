// Package windsurf 实现 Devin/Windsurf 账号池型厂商（contract.Vendor + contract.PoolVendor）。
//
// 形态：账号池自动注册、健康度挑选、24h 冷却复用、额度阈值预注册——用户无感。
// 上游聊天（server.codeium.com 的 Connect-RPC）通过 Chatter 接口注入；protocol 移植在
// chatter.go（当前仅接口 + 说明，P3-B 填充），以此包先打通"池运维 + 契约 + 换号"。
package windsurf

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/6Kmfi6HP/opencode2api/core/contract"
)

// freeModel 免费实测模型（源自 windsurf-account-manager）。
const freeModel = "swe-1-6-slow"

var (
	ErrNoAccount    = errors.New("windsurf: 无可用账号")
	ErrUnavailable  = errors.New("windsurf: 未装配注册/聊天实现")
	ErrNotWiredChat = errors.New("windsurf: Chatter 未接线（P3-B）")
)

// Config 是池型厂商装配配置。
type Config struct {
	ID   string // 厂商标识（默认 "windsurf"）
	Name string // 展示名（默认 "Devin/Windsurf"）

	// HTTPClient 上游 HTTP 复用（nil → http.DefaultClient）。
	HTTPClient *http.Client

	// MinAvailable EnsureReady 保持的最小可用账号数（默认 1）。
	MinAvailable int
	// QuotaThreshold 预注册阈值（全池最低剩余额度%≤此值触发后台注册；默认 20）。
	QuotaThreshold float64
	// Cooldown 账号冷却时长（默认 24h）。
	Cooldown time.Duration
	// StoreFile 账号库 JSON 路径（空 = 仅内存）。
	StoreFile string

	// Mailbox 临时邮箱提供者（nil 则无法自动注册）。
	Mailbox MailboxProvider
	// Registrar 注册链路（nil 则无法自动注册）。
	Registrar Registrar
	// Chatter 上游聊天协议实现（Connect-RPC；nil 时 Chat 报未接线）。
	Chatter Chatter
}

// Vendor 实现 contract.Vendor 与 contract.PoolVendor。
type Vendor struct {
	cfg  Config
	pool *Pool

	mu          sync.Mutex
	registering bool // 防并发重复注册
	lastErr     string
	lastSuccess time.Time
}

// New 构造 windsurf 厂商。
func New(cfg Config) *Vendor {
	if cfg.ID == "" {
		cfg.ID = "windsurf"
	}
	if cfg.Name == "" {
		cfg.Name = "Devin/Windsurf"
	}
	// MinAvailable 默认 3：同时支持 1 路在用 + 备用换号余量。
	// 可用号 < MinAvailable 时由 EnsureReady 后台并行补齐（不阻塞用户请求）。
	// 可经 providers[].params.min_available 按需调整（流量大时调高）。
	if cfg.MinAvailable <= 0 {
		cfg.MinAvailable = 3
	}
	if cfg.QuotaThreshold <= 0 || cfg.QuotaThreshold > 100 {
		cfg.QuotaThreshold = 20
	}
	if cfg.Cooldown <= 0 {
		cfg.Cooldown = 24 * time.Hour
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	v := &Vendor{cfg: cfg}
	v.pool = newPool(cfg.Cooldown, cfg.StoreFile)
	if cfg.StoreFile != "" {
		if err := v.pool.loadFile(cfg.StoreFile); err != nil {
			slog.Warn("windsurf: load store", "error", err)
		}
	}
	return v
}

// ---------------------------------------------------------------------------
// contract.Vendor 基础接口
// ---------------------------------------------------------------------------

func (v *Vendor) ID() string   { return v.cfg.ID }
func (v *Vendor) Name() string { return v.cfg.Name }

// ListModels 实现 contract.Vendor：账号池厂商免费模型为固定实测列表。
func (v *Vendor) ListModels(_ context.Context) ([]contract.Model, error) {
	return []contract.Model{{
		ID:       freeModel,
		Provider: v.cfg.ID,
		Free:     true,
		Caps:     contract.Capabilities{SupportsTools: true},
	}}, nil
}

// IsFree 实现 contract.Vendor。
func (v *Vendor) IsFree(modelID string) bool { return modelID == freeModel }

// ErrSemantics 实现 contract.Vendor：capacity/429/401 等按可切账号处理。
func (v *Vendor) ErrSemantics() contract.ErrRules {
	return contract.ErrRules{
		Retryable:  []int{http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout},
		Switchable: []int{http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusTooManyRequests, http.StatusServiceUnavailable},
		BadPool:    []int{http.StatusUnauthorized, http.StatusPaymentRequired},
	}
}

// Auth 实现 contract.Vendor：池型厂商按账号内部认证，入站不携带上游 key。
func (v *Vendor) Auth(_ *http.Request) string { return "" }

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

// ---------------------------------------------------------------------------
// contact.PoolVendor 能力（账号运维）
// ---------------------------------------------------------------------------

// EnsureReady 保证最小可用账号数，且尽量不阻塞用户请求：
//   - 可用 ≥ MinAvailable：直接返回。
//   - 可用 ≥1 但 < MinAvailable：立即返回（用户请求不受影响），后台并行补齐差额。
//   - 可用 =0：同步注册 1 个以恢复服务（无号可借只能等待），其余差额交给后台补齐。
func (v *Vendor) EnsureReady(ctx context.Context) error {
	now := time.Now()
	avail := len(v.pool.available(now))
	if avail >= v.cfg.MinAvailable {
		return nil
	}
	if avail >= 1 {
		// 有余量：本次请求直接放行，补齐交给后台（fire-and-forget）。
		v.topUpAsync(v.cfg.MinAvailable - avail)
		return nil
	}
	// 池空：注册 1 个恢复服务；其余差额交给后台。
	if err := v.registerNew(ctx, 1); err != nil {
		return err
	}
	if v.cfg.MinAvailable > 1 {
		v.topUpAsync(v.cfg.MinAvailable - 1)
	}
	return nil
}

// topUpAsync 后台并行补齐 need 个可用账号（fire-and-forget，绝不阻塞用户请求）。
// 与 registerNew 的 single-flight（registering 标志）配合：
// 已有注册进行中则本轮跳过（由进行中的那轮补齐覆盖），避免并发注册风暴。
func (v *Vendor) topUpAsync(need int) {
	if v.cfg.Registrar == nil || v.cfg.Mailbox == nil || need <= 0 {
		return
	}
	v.mu.Lock()
	busy := v.registering
	v.mu.Unlock()
	if busy {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		if err := v.registerNew(ctx, need); err != nil {
			slog.Warn("windsurf: 后台补齐账号失败", "error", err)
		}
	}()
}

// registerNew 注册 need 个新账号（串行，防并发重复注册；无 Registrar 时报错）。
func (v *Vendor) registerNew(ctx context.Context, need int) error {
	if v.cfg.Registrar == nil || v.cfg.Mailbox == nil {
		return ErrUnavailable
	}
	if need <= 0 {
		return nil
	}
	v.mu.Lock()
	if v.registering {
		v.mu.Unlock()
		return errors.New("windsurf: 正在注册中")
	}
	v.registering = true
	v.mu.Unlock()
	defer func() {
		v.mu.Lock()
		v.registering = false
		v.mu.Unlock()
	}()

	for i := 0; i < need; i++ {
		res, err := v.cfg.Registrar.Register(ctx, v.cfg.Mailbox)
		if err != nil {
			v.mu.Lock()
			v.lastErr = err.Error()
			v.mu.Unlock()
			return err
		}
		v.pool.add(&Account{
			Email:                res.Email,
			WindsurfSessionToken: res.SessionToken,
			QuotaDaily:           100, QuotaWeekly: 100, // 未知额度，乐观按 100 计
			CreatedAt: time.Now(),
		})
		slog.Info("windsurf: account registered", "email_masked", maskEmail(res.Email))
	}
	return nil
}

// preRegisterIfLow 额度≤阈值时后台预注册 1 个新号（防抖：同一时刻只一次）。
func (v *Vendor) preRegisterIfLow() {
	if v.cfg.Registrar == nil || v.cfg.Mailbox == nil {
		return
	}
	if v.pool.quotaMin() > v.cfg.QuotaThreshold {
		return
	}
	ready := make(chan struct{}, 1)
	select {
	case ready <- struct{}{}:
	default:
		return // 已有一次进行中
	}
	go func() {
		defer func() { <-ready }()
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		if err := v.registerNew(ctx, 1); err != nil {
			slog.Warn("windsurf: pre-register failed", "error", err)
		}
	}()
}

// PoolStatus 实现 contract.PoolVendor。
func (v *Vendor) PoolStatus() contract.PoolStatus {
	return v.pool.status(time.Now())
}

// Acquire 实现 contract.PoolVendor：借出一个健康账号。
func (v *Vendor) Acquire() (contract.AcctID, error) {
	a, err := v.pool.acquire(time.Now())
	if err != nil {
		return "", err
	}
	return contract.AcctID(a.Email), nil
}

// Release 实现 contract.PoolVendor：归还账号（可在 issue 中标记耗尽）。
func (v *Vendor) Release(id contract.AcctID) {
	v.pool.release(string(id), time.Now(), false)
}

// markExhausted 额度耗尽/故障换号路径内部使用。
func (v *Vendor) markExhausted(id contract.AcctID) {
	v.pool.release(string(id), time.Now(), true)
	v.preRegisterIfLow()
}

// ---------------------------------------------------------------------------
// 聊天（借号 → 上游 → 还号/换号）
// ---------------------------------------------------------------------------

// Chat 实现 contract.Vendor（非流式）。
// 流程：EnsureReady → 借号 → 上游请求 → 成功记账 / 可切换错误换下一账号（至多 2 次）。
func (v *Vendor) Chat(ctx context.Context, msg *contract.Message) (*contract.Reply, error) {
	v.pool.mu.Lock()
	chatter := v.cfg.Chatter
	v.pool.mu.Unlock()
	if chatter == nil {
		return nil, ErrNotWiredChat
	}

	acct, err := v.Acquire()
	if err != nil {
		return nil, err
	}
	for attempt := 0; attempt < 2; attempt++ {
		token := v.pool.tokenOf(string(acct))
		if token == "" {
			v.pool.release(string(acct), time.Now(), true)
			return nil, errors.New("windsurf: account 无会话令牌")
		}
		reply, err := chatter.DoChat(ctx, token, msg)
		if err == nil && reply != nil && reply.Status >= 200 && reply.Status < 300 {
			v.pool.touch(string(acct), time.Now())
			v.mu.Lock()
			v.lastSuccess = time.Now()
			v.lastErr = ""
			v.mu.Unlock()
			v.preUsageRefresh(string(acct))
			return reply, nil
		}
		// 失败：可切换（429/quota/capacity/传输错）→ 换号重试一次；否则记录冷却返回。
		v.pool.release(string(acct), time.Now(), true)
		status := 0
		if reply != nil {
			status = reply.Status
		}
		if attempt == 0 && shouldSwitch(status, err) {
			acct, err = v.Acquire()
			if err != nil {
				return reply, err
			}
			continue
		}
		v.mu.Lock()
		if err != nil {
			v.lastErr = err.Error()
		}
		v.mu.Unlock()
		if reply != nil {
			if err == nil {
				err = fmt.Errorf("windsurf: upstream error (status %d)", status)
			}
			return reply, err
		}
		return nil, err
	}
	return nil, ErrNoAccount
}

// ChatStream 实现 contract.Vendor（流式）。Connect-RPC 流式接缝同 Chat。
//
// P3-B7：返回流经 midStreamSwitch 包装——流内错误/中断自动换号续写（用户无感）。
// 借号失败/连接失败仍按老口径（标号冷却 + 上报错误），由 core 层厂商级 failover 兜底。
func (v *Vendor) ChatStream(ctx context.Context, msg *contract.Message) (*contract.Stream, error) {
	v.pool.mu.Lock()
	chatter := v.cfg.Chatter
	v.pool.mu.Unlock()
	if chatter == nil {
		return nil, ErrNotWiredChat
	}
	acct, err := v.Acquire()
	if err != nil {
		return nil, err
	}
	token := v.pool.tokenOf(string(acct))
	if token == "" {
		v.pool.release(string(acct), time.Now(), true)
		return nil, errors.New("windsurf: account 无会话令牌")
	}
	stream, err := chatter.DoChatStream(ctx, token, msg)
	if err != nil || stream == nil {
		v.pool.release(string(acct), time.Now(), true)
		if stream != nil {
			stream.Close()
		}
		if err == nil {
			err = errors.New("windsurf: 上游未返回流")
		}
		return nil, err
	}
	v.pool.touch(string(acct), time.Now())
	v.preRegisterIfLow()
	// 流中无感换号（P3-B7）：接管流与账号生命周期，流内错误自动换号续写。
	sw := newMidStreamSwitch(v, ctx, msg, stream.ReadCloser, string(acct))
	return &contract.Stream{ReadCloser: sw, Status: stream.Status, NodeAddr: stream.NodeAddr}, nil
}

// SetPoolUsage 供上游用量刷新回写（P3-B 经 GetUserStatus 调用）。
func (v *Vendor) SetPoolUsage(email string, daily, weekly float64) {
	v.pool.updateUsage(email, daily, weekly)
}

// preUsageThrottle 用量刷新节流：同一账号 60s 内最多刷新一次，
// 避免每个成功请求都打一次外部 GetUserStatus（后台 goroutine + 外部请求去重）。
var preUsageThrottle = struct {
	sync.Mutex
	last map[string]time.Time
}{last: map[string]time.Time{}}

func (v *Vendor) preUsageRefresh(email string) {
	token := v.pool.tokenOf(email)
	if token == "" {
		return
	}
	preUsageThrottle.Lock()
	if t, ok := preUsageThrottle.last[email]; ok && time.Since(t) < 60*time.Second {
		preUsageThrottle.Unlock()
		return
	}
	preUsageThrottle.last[email] = time.Now()
	preUsageThrottle.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		daily, weekly, err := fetchUsage(ctx, token, v.cfg.HTTPClient)
		if err != nil {
			slog.Debug("windsurf: usage refresh failed", "err", err)
			return
		}
		v.SetPoolUsage(email, daily, weekly)
		v.preRegisterIfLow()
	}()
}

// shouldSwitch 判定账号级失败是否值得换号（传输错误 / 可切换状态码）。
func shouldSwitch(status int, err error) bool {
	if err != nil {
		return true
	}
	switch status {
	case http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusTooManyRequests, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return status >= 500 && status < 600
}
