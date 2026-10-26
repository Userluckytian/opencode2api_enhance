// 免费层 Bun sidecar 生命周期。
//
// sidecar 只负责“用 Bun 的 TLS 指纹发免费层请求”（OpenCode 免费层会拒绝
// Go/curl/Node 的 TLS 栈）。节点选择/健康/重试/统计仍由 Go 网关负责。
// sidecar 缺失或启动失败时只告警，不阻断应用启动（回退旧行为）。
package manager

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const freeSidecarDirBase = "_free-sidecar"

// freeSidecarPort 生效端口：env > config > 槽位感知默认（slotBase-50）。
// 槽位感知避免默认 40200 落进正式版实例 API 段 [40100,42100)。
func (m *Manager) freeSidecarPort() int {
	cfg := m.loadConfig()
	if v := strings.TrimSpace(os.Getenv("OPCODE2API_FREE_SIDECAR_PORT")); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p >= 1024 && p <= 65535 {
			return p
		}
	}
	if cfg.FreeSidecarPort > 0 {
		return cfg.FreeSidecarPort
	}
	if base := int(m.instanceBasePort()); base > 1050 {
		if p := base - 50; p >= 1024 {
			return p
		}
	}
	return defaultFreeSidecarPort
}

// freeSidecarEnv 构造 sidecar 子进程环境：只绕过本地回环的系统代理，
// 保留 X-Oc2a-Proxy 显式代理能力（NO_PROXY=* 会让 Bun fetch 忽略 proxy 选项）。
func freeSidecarEnv(port int) []string {
	return []string{
		fmt.Sprintf("FREE_SIDECAR_PORT=%d", port),
		"NO_PROXY=127.0.0.1,localhost",
		"no_proxy=127.0.0.1,localhost",
		"HTTP_PROXY=",
		"HTTPS_PROXY=",
		"http_proxy=",
		"https_proxy=",
	}
}

// FreeSidecarStatus 是设置页展示的 sidecar 状态。
type FreeSidecarStatus struct {
	Enabled bool   `json:"enabled"`
	Port    int    `json:"port"`
	URL     string `json:"url"`
	Running bool   `json:"running"`
	Healthy bool   `json:"healthy"`
	PID     int    `json:"pid"`
	Bin     string `json:"bin,omitempty"`
	Script  string `json:"script,omitempty"`
	Error   string `json:"error,omitempty"`
}

// FreeSidecarCheck 是环境检查的一项。
type FreeSidecarCheck struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// SetFreeSidecarURLChanged 注入 URL 变化回调（main 用于写回根配置）。
func (m *Manager) SetFreeSidecarURLChanged(fn func(string)) {
	m.sidecarMu.Lock()
	m.onURLChanged = fn
	m.sidecarMu.Unlock()
}

// SyncFreeSidecarURL 把当前 sidecar URL 通知给回调（空串 = 未运行）。
func (m *Manager) SyncFreeSidecarURL() {
	m.sidecarMu.Lock()
	url := m.sidecarURL
	fn := m.onURLChanged
	m.sidecarMu.Unlock()
	if fn != nil {
		fn(url)
	}
}

// RestartFreeSidecar 停止并重新启动 sidecar（设置页/配置变更用）。
func (m *Manager) RestartFreeSidecar(runner Runner) error {
	m.StopFreeSidecar(runner)
	err := m.StartFreeSidecar(runner)
	m.SyncFreeSidecarURL()
	return err
}

// FreeSidecarStatus 返回当前 sidecar 状态（含健康检查）。
func (m *Manager) FreeSidecarStatus() FreeSidecarStatus {
	cfg := m.loadConfig()
	port := m.freeSidecarPort()
	st := FreeSidecarStatus{
		Enabled: freeSidecarEnabled(cfg),
		Port:    port,
	}
	m.sidecarMu.Lock()
	st.PID = m.sidecarPID
	st.URL = m.sidecarURL
	m.sidecarMu.Unlock()
	st.Healthy = sidecarHealthy(uint16(port))
	st.Running = st.Healthy || (st.PID > 0 && pidAlive(st.PID))
	if bin, args, err := m.freeSidecarCommand(); err != nil {
		st.Error = err.Error()
	} else {
		st.Bin = bin
		if len(args) > 0 {
			st.Script = args[0]
		}
	}
	return st
}

// FreeSidecarCheck 返回设置页“检查环境”的逐项结果。
func (m *Manager) FreeSidecarCheck() []FreeSidecarCheck {
	cfg := m.loadConfig()
	port := m.freeSidecarPort()
	out := []FreeSidecarCheck{
		{Name: "enabled", OK: freeSidecarEnabled(cfg), Detail: fmt.Sprintf("free_sidecar_enabled=%v", freeSidecarEnabled(cfg))},
	}
	if bin, args, err := m.freeSidecarCommand(); err != nil {
		out = append(out, FreeSidecarCheck{Name: "binary", OK: false, Detail: err.Error()})
	} else {
		detail := bin
		if len(args) > 0 {
			detail += " " + args[0]
		}
		out = append(out, FreeSidecarCheck{Name: "binary", OK: true, Detail: detail})
	}
	healthy := sidecarHealthy(uint16(port))
	out = append(out, FreeSidecarCheck{Name: "health", OK: healthy, Detail: fmt.Sprintf("http://127.0.0.1:%d/health", port)})
	free := isPortFree(uint16(port))
	out = append(out, FreeSidecarCheck{Name: "port", OK: free || healthy, Detail: fmt.Sprintf("port=%d free=%v healthy=%v", port, free, healthy)})
	m.sidecarMu.Lock()
	url := m.sidecarURL
	m.sidecarMu.Unlock()
	out = append(out, FreeSidecarCheck{Name: "url", OK: url != "", Detail: url})
	return out
}

// FreeSidecarStatusHandler GET /api/admin/free-sidecar/status。
func (m *Manager) FreeSidecarStatusHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodOK(w, r, http.MethodGet) {
			return
		}
		writeJSON(w, m.FreeSidecarStatus())
	}
}

// FreeSidecarCheckHandler GET /api/admin/free-sidecar/check。
func (m *Manager) FreeSidecarCheckHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodOK(w, r, http.MethodGet) {
			return
		}
		writeJSON(w, m.FreeSidecarCheck())
	}
}

// FreeSidecarRestartHandler POST /api/admin/free-sidecar/restart。
func (m *Manager) FreeSidecarRestartHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodOK(w, r, http.MethodPost) {
			return
		}
		if err := m.RestartFreeSidecar(m.Run()); err != nil {
			writeErr(w, http.StatusInternalServerError, "重启免费层 sidecar 失败: "+err.Error())
			return
		}
		writeJSON(w, m.FreeSidecarStatus())
	}
}

// FreeSidecarURL 返回已启动 sidecar 的地址（未启动 = 空）。
func (m *Manager) FreeSidecarURL() string {
	m.sidecarMu.Lock()
	defer m.sidecarMu.Unlock()
	return m.sidecarURL
}

// StartFreeSidecar 启动免费层 sidecar（幂等）。
// runner 为 nil 时使用 realRunner。
func (m *Manager) StartFreeSidecar(runner Runner) error {
	m.sidecarMu.Lock()
	defer m.sidecarMu.Unlock()

	if m.sidecarPID > 0 && pidAlive(m.sidecarPID) {
		return nil
	}
	cfg := m.loadConfig()
	if !freeSidecarEnabled(cfg) {
		slog.Info("free sidecar disabled by config")
		return nil
	}
	port := m.freeSidecarPort()
	if port < 1024 || port > 65535 {
		return fmt.Errorf("free_sidecar_port 非法: %d", port)
	}
	// 已有 sidecar 在监听（上一次进程未清理/多实例）：健康检查后复用。
	if sidecarHealthy(uint16(port)) {
		m.sidecarPort = uint16(port)
		m.sidecarURL = fmt.Sprintf("http://127.0.0.1:%d", port)
		slog.Info("free sidecar already running, reusing", "port", port)
		return nil
	}
	if !isPortFree(uint16(port)) {
		return fmt.Errorf("免费层 sidecar 端口 %d 已被其他进程占用", port)
	}
	bin, args, err := m.freeSidecarCommand()
	if err != nil {
		return err
	}
	dir := filepath.Join(m.paths.RuntimeDir, freeSidecarDirBase)
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		return fmt.Errorf("创建 sidecar 目录失败: %w", err)
	}
	if runner == nil {
		runner = &realRunner{}
	}
	pid, err := runner.Start(ExecSpec{
		Bin:  bin,
		Args: args,
		Dir:  dir,
		// 只让本地回环绕过系统代理；节点出口由 X-Oc2a-Proxy 显式传入。
		// 注意：不能写 NO_PROXY=*——Bun fetch 会因此忽略显式 proxy 选项，
		// sidecar 将直连上游、节点池失效（表现为全部 429）。
		Env:      freeSidecarEnv(port),
		LogOut:   filepath.Join(dir, "logs", "free-sidecar.out.log"),
		LogErr:   filepath.Join(dir, "logs", "free-sidecar.err.log"),
		NoWindow: true,
	})
	if err != nil {
		return fmt.Errorf("启动免费层 sidecar 失败: %w", err)
	}
	if err := waitSidecarHealthy(uint16(port), 10*time.Second); err != nil {
		_ = runner.Kill(pid)
		// 并发启动竞态：另一方可能已把 sidecar 拉起；再检查一次，健康则复用。
		if sidecarHealthy(uint16(port)) {
			m.sidecarPort = uint16(port)
			m.sidecarURL = fmt.Sprintf("http://127.0.0.1:%d", port)
			slog.Info("free sidecar became healthy after concurrent start, reusing", "port", port)
			return nil
		}
		return fmt.Errorf("免费层 sidecar 未在 10s 内就绪: %w", err)
	}
	m.sidecarPID = pid
	m.sidecarPort = uint16(port)
	m.sidecarURL = fmt.Sprintf("http://127.0.0.1:%d", port)
	slog.Info("free sidecar started", "port", port, "pid", pid, "bin", bin)
	return nil
}

// StopFreeSidecar 停止 sidecar（幂等）。
func (m *Manager) StopFreeSidecar(runner Runner) {
	m.sidecarMu.Lock()
	pid := m.sidecarPID
	m.sidecarPID = 0
	m.sidecarPort = 0
	m.sidecarURL = ""
	m.sidecarMu.Unlock()
	if pid > 0 {
		if runner == nil {
			runner = &realRunner{}
		}
		_ = runner.Kill(pid)
		slog.Info("free sidecar stopped", "pid", pid)
	}
}

// freeSidecarCommand 解析 sidecar 启动命令：
//  1. bin/free-sidecar(.exe) —— 推荐（bun build --compile 产物）
//  2. bin/bun(.exe) + free-sidecar.mjs
//  3. OPCODE2API_BUN + 脚本
func (m *Manager) freeSidecarCommand() (string, []string, error) {
	if p := m.binPath("free-sidecar"); fileExists(p) {
		return p, nil, nil
	}
	for _, bun := range []string{m.binPath("bun"), strings.TrimSpace(os.Getenv("OPCODE2API_BUN"))} {
		if bun == "" || !fileExists(bun) {
			continue
		}
		for _, script := range m.freeSidecarScriptCandidates() {
			if fileExists(script) {
				return bun, []string{script}, nil
			}
		}
	}
	return "", nil, fmt.Errorf("未找到免费层 sidecar：请构建 bin/free-sidecar.exe，或放置 bin/bun.exe + bin/free-sidecar.mjs")
}

// freeSidecarScriptCandidates 返回脚本候选路径（环境变量优先）。
func (m *Manager) freeSidecarScriptCandidates() []string {
	out := []string{}
	if v := strings.TrimSpace(os.Getenv("OPCODE2API_FREE_SIDECAR_SCRIPT")); v != "" {
		out = append(out, v)
	}
	out = append(out,
		filepath.Join(m.paths.BinDir, "free-sidecar.mjs"),
		// 开发态：bin/ 的父目录是仓库根，脚本在 <repo>/sidecar/free-sidecar.mjs。
		filepath.Join(filepath.Dir(m.paths.BinDir), "sidecar", "free-sidecar.mjs"),
	)
	return out
}

func sidecarHealthy(port uint16) bool {
	c := &http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get(fmt.Sprintf("http://127.0.0.1:%d/health", port))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	// 不只看 200：确认是本 sidecar 的 /health，避免端口被其他本地服务占用时误复用。
	var v struct {
		Ok      bool   `json:"ok"`
		Version string `json:"version"`
	}
	if json.NewDecoder(resp.Body).Decode(&v) != nil {
		return false
	}
	return v.Ok && v.Version != ""
}

func waitSidecarHealthy(port uint16, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if sidecarHealthy(port) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("health check timeout")
		}
		time.Sleep(200 * time.Millisecond)
	}
}
