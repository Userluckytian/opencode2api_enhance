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
	Version    string `json:"version,omitempty"`
	ModelTotal int    `json:"model_total"`
	ModelShown int    `json:"model_shown"`
	WarpStatus string `json:"warp_status,omitempty"`
	WarpStack  string `json:"warp_stack,omitempty"`
	WarpActive int    `json:"warp_active"`
	WarpCooling int   `json:"warp_cooling"`
	// BinPath / BinExists 引擎二进制位置与是否已安装。
	BinPath   string `json:"bin_path"`
	BinExists bool   `json:"bin_exists"`
	// Error 最近一次失败原因。
	Error string `json:"error,omitempty"`
}

// freeTierEngine 引擎托管状态。
type freeTierEngine struct {
	mu      sync.Mutex
	pid     int
	state   string
	lastErr string
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
	Listen      string             `json:"listen"`
	ServerKeys  []string           `json:"server_keys"`
	ZenKeys     []string           `json:"zen_keys,omitempty"`
	GoKeys      []string           `json:"go_keys,omitempty"`
	Anonymous   bool               `json:"anonymous"`
	Warp        engineWarpConfig   `json:"warp"`
	Proxies     []string           `json:"proxies"`
	ProxyFile   string             `json:"proxyfile"`
	Upstream    engineUpstream     `json:"upstream"`
	Retry       engineRetry        `json:"retry"`
	Models      engineModels       `json:"models"`
	Performance enginePerformance  `json:"performance"`
	Logging     engineLogging      `json:"logging"`
	WebUI       engineWebUI        `json:"webui"`
	Prefer      string             `json:"prefer"`
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
	poolSize := cfg.EngineWarpPoolSize
	if poolSize <= 0 {
		poolSize = 2
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
		Anonymous:  true,
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
			FreeOnly:       false,
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
func (m *Manager) StartFreeTierEngine(runner Runner) error {
	e := m.FreeTierEngine()
	e.mu.Lock()
	defer e.mu.Unlock()

	port := m.managerGatewayPort()
	if probePort(port, 300*time.Millisecond) {
		e.state = "ready"
		e.lastErr = ""
		return nil // 已在运行（可能是上次残留的进程）
	}
	binPath := m.EngineBinPath()
	if _, err := os.Stat(binPath); err != nil {
		e.state = "failed"
		e.lastErr = "引擎未安装: " + binPath
		return fmt.Errorf("%s", e.lastErr)
	}
	if err := m.WriteEngineConfig(); err != nil {
		e.state = "failed"
		e.lastErr = err.Error()
		return err
	}

	e.state = "starting"
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
		e.state = "failed"
		e.lastErr = err.Error()
		return err
	}
	e.pid = pid
	if err := waitForPort(port, 20*time.Second); err != nil {
		e.state = "failed"
		e.lastErr = "引擎启动超时: " + err.Error()
		return err
	}
	e.state = "ready"
	e.lastErr = ""
	return nil
}

// Stop 停止引擎。
func (m *Manager) StopFreeTierEngine(runner Runner) error {
	e := m.FreeTierEngine()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.pid > 0 {
		_ = runner.Kill(e.pid)
		e.pid = 0
	}
	port := m.managerGatewayPort()
	if err := waitPortClosed(port, 5*time.Second); err != nil {
		e.lastErr = err.Error()
	}
	e.state = "stopped"
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
		State:    state,
		Address:  fmt.Sprintf("http://127.0.0.1:%d/v1", port),
		Port:     port,
		PID:      pid,
		APIKey:   effectiveGatewayKey(cfg),
		BinPath:  m.EngineBinPath(),
		Error:    lastErr,
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
			Total  int `json:"total"`
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
