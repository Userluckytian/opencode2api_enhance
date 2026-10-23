// Part of the P1 (core split) refactor: code moved out of main.go.
// Same package (main) - do not change package clause manually.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/rand/v2"
	"net"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

type Socks5Proxy struct {
	Addr     string `json:"addr"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	Name     string `json:"name,omitempty"`
}

func socks5Dial(proxy Socks5Proxy) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, target string) (net.Conn, error) {
		conn, err := net.DialTimeout("tcp", proxy.Addr, 10*time.Second)
		if err != nil {
			return nil, fmt.Errorf("socks5 connect to %s: %w", proxy.Addr, err)
		}
		deadline := time.Now().Add(15 * time.Second)
		conn.SetDeadline(deadline)

		// 认证方法协商
		auth := byte(0x00) // no auth
		if proxy.Username != "" {
			auth = 0x02 // username/password
		}
		if _, err := conn.Write([]byte{0x05, 0x01, auth}); err != nil {
			conn.Close()
			return nil, fmt.Errorf("socks5 handshake write: %w", err)
		}
		buf := make([]byte, 2)
		if _, err := io.ReadFull(conn, buf); err != nil {
			conn.Close()
			return nil, fmt.Errorf("socks5 handshake read: %w", err)
		}
		if buf[0] != 0x05 {
			conn.Close()
			return nil, fmt.Errorf("socks5: not socks5 protocol")
		}

		// 用户名/密码认证
		if buf[1] == 0x02 {
			if proxy.Username == "" {
				conn.Close()
				return nil, fmt.Errorf("socks5: server requires auth but no credentials")
			}
			ulen := len(proxy.Username)
			plen := len(proxy.Password)
			authBuf := make([]byte, 3+ulen+plen)
			authBuf[0] = 0x01
			authBuf[1] = byte(ulen)
			copy(authBuf[2:], proxy.Username)
			authBuf[2+ulen] = byte(plen)
			copy(authBuf[3+ulen:], proxy.Password)
			if _, err := conn.Write(authBuf); err != nil {
				conn.Close()
				return nil, fmt.Errorf("socks5 auth write: %w", err)
			}
			authResp := make([]byte, 2)
			if _, err := io.ReadFull(conn, authResp); err != nil {
				conn.Close()
				return nil, fmt.Errorf("socks5 auth read: %w", err)
			}
			if authResp[1] != 0x00 {
				conn.Close()
				return nil, fmt.Errorf("socks5: auth failed")
			}
		} else if buf[1] != 0x00 {
			conn.Close()
			return nil, fmt.Errorf("socks5: unsupported auth method 0x%02x", buf[1])
		}

		// CONNECT 请求
		host, portStr, err := net.SplitHostPort(target)
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("socks5: invalid target %s: %w", target, err)
		}
		port := 0
		fmt.Sscanf(portStr, "%d", &port)

		req := []byte{0x05, 0x01, 0x00} // VER, CMD=CONNECT, RSV
		ip := net.ParseIP(host)
		if ip != nil {
			if ip4 := ip.To4(); ip4 != nil {
				req = append(req, 0x01) // IPv4
				req = append(req, ip4...)
			} else {
				req = append(req, 0x04) // IPv6
				req = append(req, ip.To16()...)
			}
		} else {
			if len(host) > 255 {
				conn.Close()
				return nil, fmt.Errorf("socks5: hostname too long")
			}
			req = append(req, 0x03) // Domain
			req = append(req, byte(len(host)))
			req = append(req, []byte(host)...)
		}
		req = append(req, byte(port>>8), byte(port))

		if _, err := conn.Write(req); err != nil {
			conn.Close()
			return nil, fmt.Errorf("socks5 connect write: %w", err)
		}

		// 读取响应
		resp := make([]byte, 4)
		if _, err := io.ReadFull(conn, resp); err != nil {
			conn.Close()
			return nil, fmt.Errorf("socks5 connect read: %w", err)
		}
		if resp[1] != 0x00 {
			conn.Close()
			return nil, fmt.Errorf("socks5: connect failed, status 0x%02x", resp[1])
		}

		// 读取绑定地址
		switch resp[3] {
		case 0x01: // IPv4
			if _, err := io.ReadFull(conn, make([]byte, 4+2)); err != nil {
				conn.Close()
				return nil, fmt.Errorf("socks5: read bind ipv4: %w", err)
			}
		case 0x03: // Domain
			dlen := make([]byte, 1)
			if _, err := io.ReadFull(conn, dlen); err != nil {
				conn.Close()
				return nil, fmt.Errorf("socks5: read bind domain len: %w", err)
			}
			if _, err := io.ReadFull(conn, make([]byte, int(dlen[0])+2)); err != nil {
				conn.Close()
				return nil, fmt.Errorf("socks5: read bind domain: %w", err)
			}
		case 0x04: // IPv6
			if _, err := io.ReadFull(conn, make([]byte, 16+2)); err != nil {
				conn.Close()
				return nil, fmt.Errorf("socks5: read bind ipv6: %w", err)
			}
		default:
			conn.Close()
			return nil, fmt.Errorf("socks5: unknown address type 0x%02x", resp[3])
		}

		conn.SetDeadline(time.Time{})
		return conn, nil
	}
}

var (
	socks5Proxies []Socks5Proxy
	activeSocks5  string // 启用的代理 Addr，空表示直连，__round_robin__ 表示轮询
	socks5Mu      sync.RWMutex
)

const socks5RR = "__round_robin__"

var socks5RRIndex uint32

// routeMode 代理池/网关路由模式：
//   - smart（默认）：failover 游标逻辑 + 健康计数/坏池/超时切换完整容错
//   - failover：成功不动游标、失败才切换（无健康计数附加层）
//   - round_robin：轮询分发
//
// G5：配置热重载原子写（applyConfig Store），请求路径无锁 Load；默认 "smart" 写于 init。
var routeMode atomic.Value

// proxyCacheKey 客户端缓存键：代理地址 + 凭据（同 addr 异凭据不串池）。
type proxyCacheKey struct {
	addr     string
	username string
	password string
}

var (
	// socks5ClientCache 按代理缓存的 SOCKS5 客户端（连接池复用）。
	socks5ClientCache map[proxyCacheKey]*http.Client
	// G23：客户端缓存的读写专用小锁——请求路径在 socks5Mu.RLock 内并发
	// 读写缓存有写-写竞争（值等价但 -race 必报），独立锁串行化；applyConfig
	// 的整体失效也在本锁下完成（锁序 socks5Mu → socks5CacheMu，与请求路径
	// 同向，无反向嵌套，无死锁环）。
	socks5CacheMu sync.Mutex
)

// ======================== 代理健康池 ========================

// badStatusCodes 坏状态码组：遇到这些状态码 → 立即切换节点并计数，
// 连续 badThreshold 次后标记该节点为"坏"（badReason），不再选用。
// 可配置（config.json 的 bad_status_codes），默认覆盖 401/402/429/503 等。
var badStatusCodes = map[int]string{
	http.StatusUnauthorized:       "401：认证失败",
	http.StatusPaymentRequired:    "402：额度受限",
	http.StatusTooManyRequests:    "429：最大额度上限",
	http.StatusServiceUnavailable: "503：服务不可用",
}

// badThreshold 连续坏状态码次数阈值，达到后节点进入"坏池"（禁用）
const badThreshold = 3

// transportBadThreshold 连续传输层失败（status=0，典型是 SOCKS 连接失败 0x01）次数阈值。
// 达到后升级为可恢复的 link 坏池：否则坏链路节点只做 20s/2min 冷却，会在兜底位被
// 反复选中（线上 42105 累计 2374 次失败），既拖慢请求也掩盖真实错误。
const transportBadThreshold = 3

// badPoolResetSec 链路类坏池自动恢复间隔（秒，默认 300；S3）。
// 链路类坏池（如 503 服务不可用）到期后放行 1 次探测，成功清状态 / 失败重新坏池；
// 账号类（401）不自动恢复（badUntil 零值永久禁用）；额度类（402/429）走 quotaBadPoolResetSec。
// G5：配置热重载原子写（applyConfig Store），请求路径无锁 Load；默认值写于 init。
var badPoolResetSec atomic.Int64

// quotaBadPoolResetSec 额度类坏池（402/429）首次冷却秒数（默认 60，允许 10~600）。
// 额度类属于可恢复坏池：到期后放行 1 个恢复探测；再次 429 按 base×2^(badCount-阈值)
// 递增，上限 600s，并附加 0~15s 随机抖动避免全池同时探测。
// 依据 2026-10-18 生产日志：429 恢复中位数约 14s、86% 在 60s 内，单次/首次不宜隔离过久。
// G5：配置热重载原子写（applyConfig Store），请求路径无锁 Load；默认值写于 init。
var quotaBadPoolResetSec atomic.Int64

// quotaBadPoolResetMaxSec 额度类坏池单次冷却上限（秒）。
const quotaBadPoolResetMaxSec = 600

// badPoolMaxExtensions 坏池节点再次失败时允许「续期」的最大次数。
// 达到上限后不再把 badUntil 往后推，避免持续流量下冷却窗口无限滑动：
// 单次冷却有 600s 上限，总续期次数也有硬上限，节点不会永久被锁在坏池。
// 额度类（429）与链路类（503）共用；认证类（401）本来就不自动恢复。
const badPoolMaxExtensions = 10

// badCountMax 坏池计数展示/退避指数的全局封顶（429 阈值 + 最大续期次数）。
// 用全局值而不是按当前状态的 threshold 计算：节点从 quota 转为 link 时
// 阈值会从 5 变 3，按局部阈值封顶会把已有计数反向压低，破坏历史语义。
const badCountMax = rateLimitBadThreshold + badPoolMaxExtensions

// rateLimitNodeCooldownSec 单次 429 的节点临时冷却（默认 15s），早于进坏池。
var rateLimitNodeCooldownSec atomic.Int64

// rateLimitBadThreshold 429 连续几次才进额度坏池（比通用 badThreshold 更宽容，
// 避免 1~9 条的短突发就把节点隔离很久）。
const rateLimitBadThreshold = 5

// rateLimitRetryAfterCapSec 上游 Retry-After 驱动的节点冷却封顶（Duration，600s）：
// 尊重上游退避指示，但异常大的头不至于把节点锁死过久（比坏池 reset 上限同量级）。
const rateLimitRetryAfterCapSec = 600 * time.Second

func init() {
	routeMode.Store("smart")
	badPoolResetSec.Store(300)
	quotaBadPoolResetSec.Store(60)
	rateLimitNodeCooldownSec.Store(15)
}

// badPoolProbeLease 探针配额租约：探针放行后若在该时长内没有结果回写
// （请求被取消、或某条内部路径未 Mark），视为配额丢失并自动回收。
// 取值需大于单个请求的最长生命周期（非流式 client.Timeout=300s；流式在
// 2xx 响应头到达时即已回写），600s 留足余量。
const badPoolProbeLease = 10 * time.Minute

// badPoolReliefRatio 全池坏池时的减压比例：按「进入坏池时间」从早到晚排序，
// 取前 60% 的节点继续尝试（不改变其坏池状态，仅绕过整池 503 快失败）。
// 这是最后的可用性兜底：宁可让请求在坏节点上真打一次上游并如实报错，
// 也不要直接合成 no_healthy_node。认证类（401 永久禁用）不参与。
const badPoolReliefRatio = 0.6

// reliefLogInterval 减压日志节流：整池坏池是持续状态，避免每请求刷一条告警。
const reliefLogInterval = 30 * time.Second

type socks5HealthState struct {
	failures  int
	until     time.Time
	badReason string // 非空 = 已进坏池（如 "429：最大额度上限"），不再选用
	badClass  string // auth / quota / link（坏池分类）
	badCount  int    // 连续坏状态码计数
	// badUntil 链路类/额度类坏池到期时间（到期放行 1 次探测）；零值 = 认证类，永久禁用。
	badUntil time.Time
	// badProbeUsed 链路类/额度类坏池到期后的半开放行配额是否已消费（1 次/quota）。
	badProbeUsed bool
	// badProbeAt 探针放行时刻；badProbeUsed 为 true 时用于租约兜底回收。
	badProbeAt time.Time
	// badSince 首次进入坏池的时刻；全池坏池减压按此从早到晚取前 60%。
	badSince time.Time
	// badExtendCount 进入坏池后已续期次数；达到 badPoolMaxExtensions 后冻结 badUntil。
	badExtendCount int
}

// badPoolClass 坏池分类：
//   - auth：认证类（401）→ 永久禁用（badUntil 零值）；
//   - quota：额度类（402/429）→ 长冷却，到期放行 1 个恢复探测；
//   - link：链路类（503 等）→ 短冷却，保持现状。
func badPoolClass(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return "auth"
	case http.StatusPaymentRequired, http.StatusTooManyRequests:
		return "quota"
	}
	return "link"
}

// badPoolAccountClass 兼容旧语义：认证类 + 额度类均视为账号类（永久/长冷却，不走链路短冷却）。
func badPoolAccountClass(status int) bool {
	c := badPoolClass(status)
	return c == "auth" || c == "quota"
}

// badPoolResetDuration 链路类坏池恢复间隔生效时长。
func badPoolResetDuration() time.Duration {
	if badPoolResetSec.Load() <= 0 {
		return 300 * time.Second
	}
	return time.Duration(badPoolResetSec.Load()) * time.Second
}

// quotaBadPoolResetDuration 额度类坏池冷却时长：base × 2^(badCount-阈值)，上限 600s，附 0~15s 抖动。
func quotaBadPoolResetDuration(badCount int) time.Duration {
	base := quotaBadPoolResetSec.Load()
	if base <= 0 {
		base = 60
	}
	exp := badCount - rateLimitBadThreshold
	if exp < 0 {
		exp = 0
	}
	// 防止溢出：badCount 极大时直接用上限对应的倍数。
	mult := 1 << minInt(exp, 10)
	secs := base * int64(mult)
	if secs > quotaBadPoolResetMaxSec {
		secs = quotaBadPoolResetMaxSec
	}
	jitter := int64(rand.IntN(16)) // 0~15s
	total := secs + jitter
	if total > quotaBadPoolResetMaxSec+15 {
		total = quotaBadPoolResetMaxSec + 15
	}
	return time.Duration(total) * time.Second
}

// applyBadUntil 按坏池分类设置到期时间（auth 永久、quota 指数退避、link 固定冷却）。
func applyBadUntil(state *socks5HealthState, status int, now time.Time) {
	switch state.badClass {
	case "auth":
		state.badUntil = time.Time{}
	case "quota":
		state.badUntil = now.Add(quotaBadPoolResetDuration(state.badCount))
	default:
		state.badUntil = now.Add(badPoolResetDuration())
	}
}

// minInt 返回较小值。
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// nodeInBadPool 报告 addr 当前是否处于坏池（含探针/减压选中的节点）。
func nodeInBadPool(addr string) bool {
	if addr == "" {
		return false
	}
	socks5HealthMu.Lock()
	st := socks5Health[addr]
	socks5HealthMu.Unlock()
	return st.badReason != ""
}

// badPoolProbeReady 非消费式检查：链路类/额度类坏池已过期且探测配额可用。
// 配额已消费但超过 badPoolProbeLease 仍无结果 → 视为丢失，重新放行。
// 零值 badUntil（认证类）永不放行；调用方需持有 socks5HealthMu。
func badPoolProbeReady(state socks5HealthState, now time.Time) bool {
	if state.badReason == "" || state.badUntil.IsZero() {
		return false
	}
	if now.Before(state.badUntil) {
		return false
	}
	if !state.badProbeUsed {
		return true
	}
	// badProbeAt 为零值属异常/旧状态：直接放行，避免永久锁死。
	if state.badProbeAt.IsZero() {
		return true
	}
	return now.Sub(state.badProbeAt) >= badPoolProbeLease
}

// badPoolProbeRelease 放行链路类坏池 1 次探测（消费配额，复用熔断 half-open 语义）。
// 放行后写回健康表；调用方需持有 socks5HealthMu，且仅对一个节点调用。
func badPoolProbeRelease(addr string, state *socks5HealthState, now time.Time) bool {
	if !badPoolProbeReady(*state, now) {
		return false
	}
	state.badProbeUsed = true
	state.badProbeAt = now
	socks5Health[addr] = *state
	return true
}

// releaseSocks5Probe 归还 addr 的恢复探针配额（坏池探针 + 熔断半开探针）。
// 用于请求在产生任何上游结果前被取消的场景：厂商侧不 Mark（取消不算节点失败），
// 但探针配额必须归还，否则节点会永久 excluded。
// 未持有配额时为无操作；不重置 badUntil / openUntil，节点仍需等到期才能再探测。
func releaseSocks5Probe(addr string) {
	if addr == "" {
		return
	}
	socks5HealthMu.Lock()
	if st, ok := socks5Health[addr]; ok && st.badProbeUsed {
		st.badProbeUsed = false
		st.badProbeAt = time.Time{}
		socks5Health[addr] = st
	}
	socks5HealthMu.Unlock()

	poolBreakerMu.Lock()
	if b := poolBreakers[addr]; b != nil && b.probeUsed {
		b.probeUsed = false
		b.probeAt = time.Time{}
	}
	poolBreakerMu.Unlock()
}

// badPoolReliefCandidatesLocked 构造「全池坏池减压候选」：只纳入可恢复坏池
// （quota/link，排除 auth），按首次进入坏池时间从早到晚排序，取前 60%（向上取整）。
// 调用方需持有 socks5HealthMu；不修改任何健康状态。
func badPoolReliefCandidatesLocked(proxies []Socks5Proxy, now time.Time) []Socks5Proxy {
	type entry struct {
		proxy Socks5Proxy
		since time.Time
	}
	bad := make([]entry, 0, len(proxies))
	for _, p := range proxies {
		st := socks5Health[p.Addr]
		if st.badReason == "" || st.badClass == "auth" {
			continue
		}
		since := st.badSince
		if since.IsZero() {
			// 兼容旧状态：没有进入时刻时用恢复时间近似（仍是越早越优先）。
			since = st.badUntil
		}
		bad = append(bad, entry{proxy: p, since: since})
	}
	if len(bad) == 0 {
		return nil
	}
	sort.SliceStable(bad, func(i, j int) bool {
		if bad[i].since.Equal(bad[j].since) {
			return bad[i].proxy.Addr < bad[j].proxy.Addr
		}
		return bad[i].since.Before(bad[j].since)
	})
	n := int(math.Ceil(float64(len(bad)) * badPoolReliefRatio))
	if n < 1 {
		n = 1
	}
	if n > len(bad) {
		n = len(bad)
	}
	out := make([]Socks5Proxy, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, bad[i].proxy)
	}
	return out
}

// badPoolReliefCandidates 加锁版本，供不持锁的调用方（加权选路）使用。
func badPoolReliefCandidates(proxies []Socks5Proxy, now time.Time) []Socks5Proxy {
	socks5HealthMu.Lock()
	defer socks5HealthMu.Unlock()
	return badPoolReliefCandidatesLocked(proxies, now)
}

// pickRelief 在减压候选集合内轮转，避免所有请求砸向同一个最老节点。
func pickRelief(relief []Socks5Proxy, start int) Socks5Proxy {
	if len(relief) == 0 {
		return Socks5Proxy{}
	}
	i := start % len(relief)
	if i < 0 {
		i += len(relief)
	}
	return relief[i]
}

var reliefLogTS atomic.Int64

// logReliefUse 节流记录减压选路（整池坏池期间最多 30s 一条）。
func logReliefUse(relief []Socks5Proxy, chosen Socks5Proxy) {
	now := time.Now().UnixNano()
	last := reliefLogTS.Load()
	if now-last < int64(reliefLogInterval) {
		return
	}
	if reliefLogTS.CompareAndSwap(last, now) {
		slog.Warn("all proxies in bad pool, using relief candidates",
			"relief", len(relief), "node", chosen.Addr)
	}
}

var (
	socks5HealthMu sync.Mutex
	socks5Health   = map[string]socks5HealthState{}
)

// pickHealthyProxy 从 start 位置起轮询，跳过冷却中代理；全冷→返回冷却最早结束的兜底；
// 连冷却节点都没有（整池坏池）→ 取最早进入坏池的前 60% 作为减压候选继续尝试。
func pickHealthyProxy(proxies []Socks5Proxy, start int) Socks5Proxy {
	// S5: 单出口退化——池中仅 1 个可用出口时不叠加质量加权/熔断选择，
	// 直接走默认请求（请求层的健康计数/超时切换兜底），避免聪明逻辑只服务同一个节点。
	if len(proxies) <= 1 {
		if len(proxies) == 1 {
			return proxies[0]
		}
		return Socks5Proxy{}
	}
	// P2 性能模式：质量加权路由 + 熔断/半开（关闭时走基线逻辑）。
	if poolPerfMode.Load() {
		return pickWeightedProxy(proxies, start)
	}
	now := time.Now()
	var fallback Socks5Proxy
	var fallbackUntil time.Time
	socks5HealthMu.Lock()
	defer socks5HealthMu.Unlock()
	for offset := 0; offset < len(proxies); offset++ {
		proxy := proxies[(start+offset)%len(proxies)]
		state := socks5Health[proxy.Addr]
		// 坏池节点：账号类永久跳过；链路类到期放行即返回（半开放行语义，
		// 放行即消费配额，避免配额耗尽后节点被饿死）。
		if state.badReason != "" {
			if badPoolProbeRelease(proxy.Addr, &state, now) {
				return proxy
			}
			continue
		}
		if state.until.IsZero() || !now.Before(state.until) {
			return proxy
		}
		if fallback.Addr == "" || state.until.Before(fallbackUntil) {
			fallback = proxy
			fallbackUntil = state.until
		}
	}
	if fallback.Addr != "" {
		return fallback
	}
	// 全池坏池：按进入坏池时间取最早 60% 减压候选继续尝试，绝不回退直连/503。
	if relief := badPoolReliefCandidatesLocked(proxies, now); len(relief) > 0 {
		chosen := pickRelief(relief, start)
		logReliefUse(relief, chosen)
		return chosen
	}
	return Socks5Proxy{}
}

// markSocks5Result 记录代理健康/冷却状态。
// 失败分类：
//   - 坏状态码（401/402/429/503，见 badStatusCodes）：立即切换节点并计数，
//     连续 badThreshold 次 → 标记 badReason 进坏池。S3 分类型：
//     账号类（401/402/429）永久禁用；链路类（如 503）带 badUntil 到期自动恢复。
//   - 其他失败：临时冷却（连接错→20s、429→45s、连续 3 次→2min）；
//     链路类坏池节点探测失败 → 重新坏池（重置计时）。
func markSocks5Result(addr string, status int, requestErr error) {
	markSocks5ResultWithRetry(addr, status, requestErr, 0)
}

// markSocks5ResultWithRetry 与 markSocks5Result 同，但 429 时尊重上游 Retry-After：
// retryAfterMS>0 时节点冷却取 max(默认 15s, Retry-After)，封顶 600s，避免被上游
// 长时间锁死的同时优先按上游指示退避（比盲目 15s 后再踩雷更省额度）。
func markSocks5ResultWithRetry(addr string, status int, requestErr error, retryAfterMS int64) {
	if addr == "" {
		return
	}
	socks5HealthMu.Lock()

	// 坏状态码：计数 + 达到阈值标记坏池。
	// S3 分类型：账号类（401/402/429）永久禁用（badUntil 零值）；
	// 链路类（如 503）可自动恢复——badUntil = now + bad_pool_reset_sec，到期放行 1 次探测。
	if reason, ok := badStatusCodes[status]; ok {
		state := socks5Health[addr]
		wasBad := state.badReason != ""
		now := time.Now()
		state.badCount++
		threshold := badThreshold
		if status == http.StatusTooManyRequests {
			threshold = rateLimitBadThreshold
		}
		// badCount 只用于展示与退避指数；达到全局上限后封顶，
		// 避免持续 429 时计数无限增长（历史上出现过 56）。
		if state.badCount > badCountMax {
			state.badCount = badCountMax
		}
		if state.badCount >= threshold {
			state.badReason = reason
			state.badClass = badPoolClass(status)
			if !wasBad {
				// 首次进入坏池：记录 since、清零续期计数、设置初始冷却。
				state.badSince = now
				state.badExtendCount = 0
				applyBadUntil(&state, status, now)
				state.badProbeUsed = false
				state.badProbeAt = time.Time{}
				slog.Warn("proxy entered bad pool", "addr", addr, "reason", reason, "count", state.badCount, "class", state.badClass)
			} else if state.badClass == "auth" {
				// 分类变为认证类：永久禁用。
				state.badUntil = time.Time{}
				state.badProbeUsed = false
				state.badProbeAt = time.Time{}
			} else if state.badExtendCount < badPoolMaxExtensions {
				// 到期后再次失败：允许续期，但有硬上限。
				state.badExtendCount++
				applyBadUntil(&state, status, now)
				state.badProbeUsed = false
				state.badProbeAt = time.Time{}
			}
			// 已达 max：不再后移 badUntil（冻结在最后一次续期的值），
			// 只保留下方的短时 until 冷却，避免冷却窗口无限滑动。
		}
		// 坏状态码也标记临时冷却（后续请求跳过，避免连续踩雷）。
		// 429 用更短的 rateLimitNodeCooldownSec（默认 15s）：实测 429 多在十几秒内恢复，
		// 单次/短突发不该被按 45s 隔离。
		state.failures++
		cooldown := 45 * time.Second
		if status == http.StatusTooManyRequests {
			if s := rateLimitNodeCooldownSec.Load(); s > 0 {
				cooldown = time.Duration(s) * time.Second
			}
			// 上游给了明确的 Retry-After：按其时长冷却（封顶 rateLimitRetryAfterCapSec，
			// 防止一个异常大的头把节点锁死过久），取二者较大值。
			if retryAfterMS > 0 {
				ra := time.Duration(retryAfterMS) * time.Millisecond
				if ra > rateLimitRetryAfterCapSec {
					ra = rateLimitRetryAfterCapSec
				}
				if ra > cooldown {
					cooldown = ra
				}
			}
		}
		state.until = time.Now().Add(cooldown)
		socks5Health[addr] = state
	} else {
		failed := requestErr != nil || status == http.StatusBadGateway ||
			status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout
		if !failed {
			if status >= 200 && status < 300 {
				delete(socks5Health, addr)
			}
		} else {
			state := socks5Health[addr]
			state.failures++
			cooldown := 20 * time.Second
			if state.failures >= 3 {
				cooldown = 2 * time.Minute
			}
			state.until = time.Now().Add(cooldown)
			// 传输层失败（status=0，典型是 SOCKS 连接失败 0x01）说明链路本身不可用，
			// 连续达到阈值后升级为可恢复的 link 坏池，按 bad_pool_reset_sec 放行探针；
			// 否则它只会以 20s/2min 冷却长期占据兜底位，反复空转。
			enteredBad := false
			if requestErr != nil && state.badReason == "" && state.failures >= transportBadThreshold {
				now := time.Now()
				state.badReason = "链路：连接失败"
				state.badClass = "link"
				state.badSince = now
				state.badExtendCount = 0
				applyBadUntil(&state, 0, now)
				state.badProbeUsed = false
				state.badProbeAt = time.Time{}
				enteredBad = true
				slog.Warn("proxy entered bad pool", "addr", addr, "reason", state.badReason, "count", state.failures, "class", state.badClass)
			}
			// 链路类/额度类坏池节点的探测失败 → 重新坏池（重置计时），避免探针配额耗尽后饿死；
			// 续期同样受 badPoolMaxExtensions 限制，达到上限后不再后移 badUntil。
			// 刚进入坏池的这次失败不重复续期（同一次失败只计一次）。
			if !enteredBad && state.badReason != "" && !state.badUntil.IsZero() {
				if state.badExtendCount < badPoolMaxExtensions {
					state.badExtendCount++
					applyBadUntil(&state, status, time.Now())
				}
				state.badProbeUsed = false
				state.badProbeAt = time.Time{}
			}
			socks5Health[addr] = state
		}
	}
	socks5HealthMu.Unlock()

	// P2：请求结果回填（熔断 + 反馈窗口），所有路径统一收口——
	// 成功（2xx）清除熔断（半开恢复），失败累计计数触发熔断。
	applyPoolResult(addr, status, requestErr)
}

// getHTTPClientWithProxy 返回 HTTP 客户端及所用代理地址。
// round_robin 模式下通过 pickHealthyProxy 跳过冷却中代理。
func getHTTPClientWithProxy() (*http.Client, string) {
	socks5Mu.RLock()
	defer socks5Mu.RUnlock()

	if activeSocks5 == "" {
		return httpClient, ""
	}

	var proxy Socks5Proxy

	if activeSocks5 == socks5RR {
		if len(socks5Proxies) == 0 {
			return httpClient, ""
		}
		mode := routeMode.Load().(string)
		if mode == "round_robin" {
			// round_robin：每次请求推进游标
			start := int(atomic.AddUint32(&socks5RRIndex, 1) % uint32(len(socks5Proxies)))
			proxy = pickHealthyProxy(socks5Proxies, start)
		} else if mode == "smart" {
			// smart（默认）：每请求推进游标轮询健康节点——多实例并发时不扎堆单一节点，
			// 保留健康跳过/坏池/熔断/超时切换完整逻辑（pickHealthyProxy 附加层）。
			// 单节点健康时退化恒命中该节点（pickHealthyProxy 的 len<=1 前置）。
			start := int(atomic.AddUint32(&socks5RRIndex, 1) % uint32(len(socks5Proxies)))
			proxy = pickHealthyProxy(socks5Proxies, start)
		} else {
			// failover：成功不动游标，失败（冷却）才切下一个健康代理
			start := int(atomic.LoadUint32(&socks5RRIndex) % uint32(len(socks5Proxies)))
			proxy = pickHealthyProxy(socks5Proxies, start)
			// 游标推进到实际选中的代理（若起始代理冷却被跳过则切换）
			for i := range socks5Proxies {
				if socks5Proxies[i].Addr == proxy.Addr {
					atomic.StoreUint32(&socks5RRIndex, uint32(i))
					break
				}
			}
		}
	} else {
		var found bool
		for i := range socks5Proxies {
			if socks5Proxies[i].Addr == activeSocks5 {
				proxy = socks5Proxies[i]
				found = true
				break
			}
		}
		if !found {
			return httpClient, ""
		}
	}

	// RR 与单发共用同一缓存：未命中构建后存入，后续请求复用连接池。
	return clientForProxy(proxy), proxy.Addr
}

// clientForProxy 返回缓存的代理 HTTP 客户端：按地址+凭据命中直接返回，
// 未命中构建后存入（连接池复用，避免每请求新建 Transport）。
// 竞速（CandidateClients）与单发路径共用，保证行为一致。
// 调用方不得原地修改返回的 client（流式去 Timeout 用浅拷贝）。
func clientForProxy(proxy Socks5Proxy) *http.Client {
	key := proxyCacheKey{addr: proxy.Addr, username: proxy.Username, password: proxy.Password}
	socks5CacheMu.Lock()
	defer socks5CacheMu.Unlock()
	if socks5ClientCache == nil {
		socks5ClientCache = map[proxyCacheKey]*http.Client{}
	}
	if c, ok := socks5ClientCache[key]; ok {
		return c
	}
	dial := socks5Dial(proxy)
	client := &http.Client{
		Timeout: 300 * time.Second,
		Transport: &http.Transport{
			DialContext:         dial,
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 20,
			IdleConnTimeout:     90 * time.Second,
		},
	}
	socks5ClientCache[key] = client
	return client
}

func getHTTPClient() *http.Client {
	client, _ := getHTTPClientWithProxy()
	return client
}

// getHTTPClientForTier 根据层级返回 HTTP 客户端
// 付费层走直连，免费层（默认）走 SOCKS5 代理（如配置）
func getHTTPClientForTier(tier TierType) *http.Client {
	client, _ := getHTTPClientForTierWithProxy(tier)
	return client
}

func getHTTPClientForTierWithProxy(tier TierType) (*http.Client, string) {
	if tier == TierPaid {
		return httpClient, ""
	}
	return getHTTPClientWithProxy()
}

// getStreamingHTTPClientForTierWithProxy keeps the connection/setup limits of
// the normal client but removes http.Client.Timeout. That field is a total
// request lifetime limit, so using it for SSE would terminate healthy long
// reasoning streams after exactly five minutes.
func getStreamingHTTPClientForTierWithProxy(tier TierType) (*http.Client, string) {
	client, proxyAddr := getHTTPClientForTierWithProxy(tier)
	if client == nil || client.Timeout == 0 {
		return client, proxyAddr
	}
	streamClient := *client
	streamClient.Timeout = 0
	return &streamClient, proxyAddr
}

// ======================== 随机 ID ========================
