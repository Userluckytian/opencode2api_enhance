// 免费层引擎（40080）托管：opencode2api-win 以独立进程运行，本文件只负责
// 生成配置、启停、健康检查与看门狗。
//
// 为什么是「托管」而不是「内置」：
//   - 引擎是 GPL-3.0 的独立程序（github.com/wbi35772/opencode2api-win），
//     我们以独立进程 + HTTP 通信方式使用（mere aggregation），不链接其代码，
//     因此本项目许可证不受其传染；
//   - 引擎自己实现 OpenCode 免费层的客户端形态伪装、WARP 出口池、会话亲和
//     与两段式 429——这些是本项目**不再自研**的部分。
//
// 运行目录：<dataDir>/runtime/free-tier/
// 引擎会在 config.json 同目录写 models.catalog / rotation / availability 等
// 状态文件，因此必须给它独立可写目录（不能与 dataDir 混用）。
package manager

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// freeTierEngineBinName 引擎可执行文件名（不含平台后缀）。
const freeTierEngineBinName = "opencode2api-win"

// freeTierEnginePortDefault 引擎默认监听端口（统一网关槽位）。
const freeTierEnginePortDefault = 40080

// FreeTierEngineStatus 引擎状态快照（前端契约）。
type FreeTierEngineStatus struct {
	// State: stopped | starting | ready | failed
	State string `json:"state"`
	// Address 引擎 API 基址（如 http://127.0.0.1:40080）。
	Address string `json:"address"`
	// Port 生效端口。
	Port uint16 `json:"port"`
	// Running / Healthy 进程存活 / /healthz 通过。
	Running bool `json:"running"`
	Healthy bool `json:"healthy"`
	// PID 引擎进程 PID（0 = 未运行）。
	PID int `json:"pid"`
	// APIKey 客户端应使用的统一密钥。
	APIKey string `json:"api_key"`
	// Version / Models / Warp 来自引擎 /healthz。
	Version     string `json:"version,omitempty"`
	ModelTotal  int    `json:"model_total"`
	ModelShown  int    `json:"model_shown"`
	WarpStatus  string `json:"warp_status,omitempty"`
	WarpStack   string `json:"warp_stack,omitempty"`
	WarpActive  int    `json:"warp_active"`
	WarpCooling int    `json:"warp_cooling"`
	// BinPath / BinExists 引擎二进制位置与是否已安装。
	BinPath   string `json:"bin_path"`
	BinExists bool   `json:"bin_exists"`
	// Error 最近一次失败原因。
	Error string `json:"error,omitempty"`
}

// freeTierEngine 引擎托管状态。
type freeTierEngine struct {
	mu          sync.Mutex
	pid         int
	state       string
	lastErr     string
	warpCooling int
}

// FreeTierEngine 返回引擎托管句柄。
func (m *Manager) FreeTierEngine() *freeTierEngine {
	m.depMu.Lock()
	defer m.depMu.Unlock()
	if m.engine == nil {
		m.engine = &freeTierEngine{state: "stopped"}
	}
	return m.engine
}

// managerGatewayPort 引擎端口：优先环境变量 OPCODE2API_GATEWAY_PORT，
// 其次 config.gateway_port，否则默认 40080。
func (m *Manager) managerGatewayPort() uint16 {
	if s := os.Getenv("OPCODE2API_GATEWAY_PORT"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 && n < 65536 {
			return uint16(n)
		}
	}
	if m != nil {
		if p := m.loadConfig().GatewayPort; p > 0 {
			return p
		}
	}
	return freeTierEnginePortDefault
}

// EngineBinPath 返回引擎二进制完整路径（含平台后缀）。
func (m *Manager) EngineBinPath() string {
	name := freeTierEngineBinName
	if isWindows() {
		name += ".exe"
	}
	return filepath.Join(m.paths.BinDir, name)
}

// engineConfig 引擎 config.json 的生成结构（只声明我们写入的字段；
// 引擎的 DisallowUnknownFields 要求不能出现它不认识的键）。
type engineConfig struct {
	Listen      string            `json:"listen"`
	ServerKeys  []string          `json:"server_keys"`
	ZenKeys     []string          `json:"zen_keys,omitempty"`
	GoKeys      []string          `json:"go_keys,omitempty"`
	Anonymous   bool              `json:"anonymous"`
	Warp        engineWarpConfig  `json:"warp"`
	Proxies     []string          `json:"proxies"`
	ProxyFile   string            `json:"proxyfile"`
	Upstream    engineUpstream    `json:"upstream"`
	Retry       engineRetry       `json:"retry"`
	Models      engineModels      `json:"models"`
	Performance enginePerformance `json:"performance"`
	Logging     engineLogging     `json:"logging"`
	WebUI       engineWebUI       `json:"webui"`
	Prefer      string            `json:"prefer"`
}

type engineWarpConfig struct {
	Enabled    bool   `json:"enabled"`
	PoolSize   int    `json:"pool_size,omitempty"`
	Stack      string `json:"stack,omitempty"`
	SourceIPv6 string `json:"source_ipv6,omitempty"`
}

type engineUpstream struct {
	Zen       string `json:"zen"`
	Go        string `json:"go"`
	Anonymous string `json:"anonymous"`
}

type engineRetry struct {
	MaxAttempts    int `json:"max_attempts"`
	TimeoutSeconds int `json:"timeout_seconds"`
}

type engineModels struct {
	RefreshSeconds int            `json:"refresh_seconds"`
	Protocols      map[string]any `json:"protocols"`
	FreeOnly       bool           `json:"free_only"`
}

type enginePerformance struct {
	MaxIdleConns          int `json:"max_idle_conns"`
	MaxIdleConnsPerHost   int `json:"max_idle_conns_per_host"`
	MaxConnsPerHost       int `json:"max_conns_per_host"`
	IdleConnTimeoutSecond int `json:"idle_conn_timeout_seconds"`
	ConnectTimeoutSeconds int `json:"connect_timeout_seconds"`
	FailureCooldownSecond int `json:"failure_cooldown_seconds"`
	AttemptTimeoutSeconds int `json:"attempt_timeout_seconds"`
	FirstEventTimeoutSec  int `json:"first_event_timeout_seconds"`
	MaxAnonymousExits     int `json:"max_anonymous_exits"`
	FreeTierBlockSeconds  int `json:"free_tier_block_seconds"`
}

type engineLogging struct {
	Level             string `json:"level"`
	RingSize          int    `json:"ring_size"`
	DumpRequestBodies bool   `json:"dump_request_bodies"`
}

type engineWebUI struct {
	Enabled           bool   `json:"enabled"`
	Listen            string `json:"listen"`
	Username          string `json:"username"`
	Password          string `json:"password"`
	SessionTTLMinutes int    `json:"session_ttl_minutes"`
}

// EngineConfigPath 引擎配置文件路径。
func (m *Manager) EngineConfigPath() string {
	return filepath.Join(m.paths.FreeTierEngineDir(), "config.json")
}

// WriteEngineConfig 按当前面板配置生成引擎 config.json。
func (m *Manager) WriteEngineConfig() error {
	cfg := m.loadConfig()
	dir := m.paths.FreeTierEngineDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建引擎目录失败: %w", err)
	}

	port := m.managerGatewayPort()
	// WARP 出口池大小：未显式设置时按 key 数量自动推导（额度最大化）。
	//
	// 引擎把 key 均匀绑定到出口（proxyIndex = i % len(transports)），而
	// 上游免费额度按「key × 出口 IP」计算：出口数不足时多个 key 共享同一 IP，
	// 会一起 429。因此出口数至少要与 key 数量同量级。
	poolSize := cfg.EngineWarpPoolSize
	if poolSize <= 0 {
		poolSize = autoWarpPoolSize(len(cfg.EngineZenKeys) + len(cfg.EngineGoKeys))
	}
	retryMax := cfg.EngineRetryMaxAttempts
	if retryMax <= 0 {
		retryMax = 3
	}
	refreshSec := cfg.EngineModelsRefreshSec
	if refreshSec <= 0 {
		refreshSec = 300
	}
	proxies := cfg.EngineProxies
	if len(proxies) == 0 {
		proxies = []string{"direct"}
	}

	ec := engineConfig{
		Listen:     fmt.Sprintf("127.0.0.1:%d", port),
		ServerKeys: []string{effectiveGatewayKey(cfg)},
		ZenKeys:    cfg.EngineZenKeys,
		GoKeys:     cfg.EngineGoKeys,
		// Anonymous 只在完全没有付费 key 时开启：实测（同一引擎、同样直连）匿名
		// 模式请求 big-pickle 恒 429；配一个 zen key 后同一模型立刻 200。
		// 配了 key 还留 anonymous=true，引擎仍会优先走匿名通道，继续 429。
		Anonymous: len(cfg.EngineZenKeys) == 0 && len(cfg.EngineGoKeys) == 0,
		Warp: engineWarpConfig{
			Enabled:    engineWarpEnabled(cfg),
			PoolSize:   poolSize,
			Stack:      "ipv6",
			SourceIPv6: cfg.EngineWarpSourceIPv6,
		},
		Proxies:   proxies,
		ProxyFile: "",
		Upstream: engineUpstream{
			Zen:       "https://opencode.ai/zen",
			Go:        "https://opencode.ai/zen/go",
			Anonymous: "https://opencode.ai/inference",
		},
		Retry: engineRetry{MaxAttempts: retryMax, TimeoutSeconds: 300},
		Models: engineModels{
			RefreshSeconds: refreshSec,
			Protocols:      map[string]any{},
			// FreeOnly 只暴露/路由免费模型（面板「只使用免费模型」勾选）。
			//
			// 引擎的 free_only 语义（internal/models/catalog.go:158）：
			// 路由入口一律拒绝非免费模型，/v1/models 与轮换别名由 routes 派生，
			// 因此也一并隐藏 —— 三条路径口径一致。
			//
			// 「免费」判定（pricing.go:256 Decide）：模型 ID 含 "free"，
			// 或元数据显示 input/output 成本均为 0 且未废弃。
			FreeOnly: engineFreeOnly(cfg),
		},
		Performance: enginePerformance{
			MaxIdleConns:          2048,
			MaxIdleConnsPerHost:   256,
			MaxConnsPerHost:       0,
			IdleConnTimeoutSecond: 120,
			ConnectTimeoutSeconds: 5,
			FailureCooldownSecond: 15,
			AttemptTimeoutSeconds: 0,
			FirstEventTimeoutSec:  0,
			MaxAnonymousExits:     8,
			FreeTierBlockSeconds:  900,
		},
		Logging: engineLogging{Level: "info", RingSize: 2000, DumpRequestBodies: false},
		// 引擎自带 WebUI 关闭：管理面板统一在 40000。
		WebUI:  engineWebUI{Enabled: false, Listen: "127.0.0.1:0", SessionTTLMinutes: 60},
		Prefer: "go",
	}

	data, err := json.MarshalIndent(ec, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(m.EngineConfigPath(), data)
}

// Start 启动引擎（幂等：已在运行则直接返回）。
//
// 重要：锁只保护状态字段，**长等待（waitForPort 最长 95s）必须在锁外做**。
// 早先整个函数 defer Unlock，导致重启期间任何 FreeTierEngineStatus() 调用
// 都排队等满 95s，前端表现为「点勾选框后转圈一分半」甚至请求超时。
func (m *Manager) StartFreeTierEngine(runner Runner) error {
	e := m.FreeTierEngine()
	setState := func(state, lastErr string, pid int) {
		e.mu.Lock()
		e.state = state
		e.lastErr = lastErr
		if pid >= 0 {
			e.pid = pid
		}
		e.mu.Unlock()
	}

	port := m.managerGatewayPort()
	if probePort(port, 300*time.Millisecond) {
		setState("ready", "", -1)
		return nil // 已在运行（可能是上次残留的进程）
	}
	binPath := m.EngineBinPath()
	if _, err := os.Stat(binPath); err != nil {
		msg := "引擎未安装: " + binPath
		setState("failed", msg, -1)
		return fmt.Errorf("%s", msg)
	}
	if err := m.WriteEngineConfig(); err != nil {
		setState("failed", err.Error(), -1)
		return err
	}

	setState("starting", "", -1)
	dir := m.paths.FreeTierEngineDir()
	pid, err := runner.Start(ExecSpec{
		Bin:      binPath,
		Args:     []string{"-config", m.EngineConfigPath()},
		Dir:      dir,
		LogOut:   filepath.Join(dir, "stdout.log"),
		LogErr:   filepath.Join(dir, "stderr.log"),
		NoWindow: true,
		Env:      traceEnvKV(),
	})
	if err != nil {
		setState("failed", err.Error(), -1)
		return err
	}
	// pid 先落库：这样等待期间状态接口也能看到进程已在跑
	setState("starting", "", pid)

	// WARP 开启时引擎会先完成首次出口注册再监听端口（上游最长 90s），
	// 因此等待窗口要覆盖这段：否则会误判「启动超时」并把状态打成 failed。
	waitTimeout := 20 * time.Second
	if engineWarpEnabled(m.loadConfig()) {
		waitTimeout = 95 * time.Second
	}
	if err := waitForPort(port, waitTimeout); err != nil {
		// 仍在启动（WARP 注册未完成）：保持 starting，让面板/看门狗继续观察，
		// 不返回错误——引擎稍后会自行监听。
		if engineWarpEnabled(m.loadConfig()) {
			setState("starting", "等待 WARP 首次出口注册", -1)
			return nil
		}
		setState("failed", "引擎启动超时: "+err.Error(), -1)
		return err
	}
	setState("ready", "", -1)
	return nil
}

// Stop 停止引擎。
//
// 锁只保护状态字段的读写，**不在锁内做等待** —— 早先 defer Unlock + 锁内
// waitPortClosed（最长 5s）会让并发的 FreeTierEngineStatus() 一起阻塞，
// 表现为面板状态接口卡住、异步重启也「点了没反应」。
func (m *Manager) StopFreeTierEngine(runner Runner) error {
	e := m.FreeTierEngine()
	e.mu.Lock()
	pid := e.pid
	e.pid = 0
	e.state = "stopping"
	e.mu.Unlock()

	if pid > 0 {
		_ = runner.Kill(pid)
	}
	port := m.managerGatewayPort()
	err := waitPortClosed(port, 5*time.Second)
	e.mu.Lock()
	if err != nil {
		e.lastErr = err.Error()
	}
	e.state = "stopped"
	e.mu.Unlock()
	return nil
}

// Restart 重启引擎。
func (m *Manager) RestartFreeTierEngine(runner Runner) error {
	_ = m.StopFreeTierEngine(runner)
	return m.StartFreeTierEngine(runner)
}

// Watchdog 每 30s 检查引擎存活与健康，异常则重启。
func (m *Manager) WatchdogFreeTierEngine(runner Runner) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		cfg := m.loadConfig()
		if !engineEnabled(cfg) {
			continue
		}
		port := m.managerGatewayPort()
		if probePort(port, 2*time.Second) {
			continue
		}
		_ = m.RestartFreeTierEngine(runner)
	}
}

// Status 返回引擎状态（读进程 + /healthz）。
func (m *Manager) FreeTierEngineStatus() FreeTierEngineStatus {
	e := m.FreeTierEngine()
	e.mu.Lock()
	pid, state, lastErr := e.pid, e.state, e.lastErr
	e.mu.Unlock()

	cfg := m.loadConfig()
	port := m.managerGatewayPort()
	st := FreeTierEngineStatus{
		State:       state,
		Address:     fmt.Sprintf("http://127.0.0.1:%d/v1", port),
		Port:        port,
		PID:         pid,
		APIKey:      effectiveGatewayKey(cfg),
		BinPath:     m.EngineBinPath(),
		Error:       lastErr,
		WarpCooling: e.warpCooling,
	}
	if _, err := os.Stat(st.BinPath); err == nil {
		st.BinExists = true
	}

	// /healthz 探活（引擎公开端点，无需鉴权）。
	status, body, err := httpGetJSON(port, "/healthz", 3*time.Second, "")
	if err != nil || status != 200 {
		st.Running = probePort(port, time.Second)
		if st.State == "ready" && !st.Running {
			st.State = "failed"
		}
		return st
	}
	st.Running = true
	st.Healthy = true
	st.State = "ready"

	var hz struct {
		Status  string `json:"status"`
		Version string `json:"version"`
		Models  struct {
			Total   int `json:"total"`
			Exposed int `json:"exposed"`
		} `json:"models"`
		Warp *struct {
			Status  string `json:"status"`
			Stack   string `json:"stack"`
			Active  int    `json:"active"`
			Cooling int    `json:"cooling"`
		} `json:"warp"`
	}
	if json.Unmarshal(body, &hz) == nil {
		st.Version = hz.Version
		st.ModelTotal = hz.Models.Total
		st.ModelShown = hz.Models.Exposed
		if hz.Warp != nil {
			st.WarpStatus = hz.Warp.Status
			st.WarpStack = hz.Warp.Stack
			st.WarpActive = hz.Warp.Active
			st.WarpCooling = hz.Warp.Cooling
			e.mu.Lock()
			e.warpCooling = hz.Warp.Cooling
			e.mu.Unlock()
		}
	}
	return st
}

// waitPortClosed 等待端口不再监听（或超时）。
func waitPortClosed(port uint16, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !probePort(port, 300*time.Millisecond) {
			return nil
		}
		time.Sleep(150 * time.Millisecond)
	}
	return fmt.Errorf("端口 %d 未在 %v 内释放", port, timeout)
}

func autoWarpPoolSize(keyCount int) int {
	// 额度主要绑 session id，而不是出口 IP（见引擎 sessions.go：同一对话耗尽后
	// 换任何新出口都仍 429，反而把整池烧掉 24h）。所以出口多不等于额度多，
	// 盲目开大只会拖慢启动（每个出口注册 10~90s）并加速冷却消耗。
	//
	// 策略：小起点 + 按需扩容
	//   - 无 key（纯匿名）：2 个（匿名本身按 IP 限流，多出口收益递减）
	//   - 有 key：4 个（key 能开新 session，多出口才有意义）
	//   - 上限 4，不够时由诊断页/面板手动调大
	if keyCount <= 0 {
		return 2
	}
	return 4
}
