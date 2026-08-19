// 插件式供应商管理器（R1，设计定稿 docs/PLUGIN-PROVIDERS.md）。
//
// 职责：扫描 providers/<id>/provider.json → spawn 供应商侧车子进程（就绪行契约
// §四）→ 生命周期管理（就绪/need_config/崩溃指数退避重启/启停/删除/主进程退出回收）
// → 3s 目录 watcher 热发现。厂商桥接（vendors/remote）是 R2，本阶段只把就绪后的
// 子进程端点与令牌记在内存/视图中（Endpoint/View.URL）。
package pluginprovider

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// 插件状态（面板展示）。
const (
	StatusDisabled = "disabled"   // 已停用（enabled=false，文件保留）
	StatusStarting = "starting"   // 拉起中（等就绪行）
	StatusRunning  = "running"    // 已就绪，厂商可用
	StatusNeedCfg  = "need_config" // 待配置（子进程自举后报告，不注册厂商）
	StatusError    = "error"      // 启动失败/崩溃退避中（含最近错误）
)

// 默认生命周期参数（设计文档 §4.3）。
const (
	defaultStartupTimeout = 15 * time.Second // 就绪行等待上限
	defaultBackoffBase    = time.Second      // 崩溃退避起点
	defaultBackoffCap     = 60 * time.Second // 崩溃退避封顶
	defaultRescanInterval = 3 * time.Second  // providers/ 目录扫描间隔
	defaultModelTimeout   = 5 * time.Second  // 模型数查询超时
)

// Config 插件管理器配置。零值全部回退默认（测试可注入短退避/短超时）。
type Config struct {
	// ProvidersDir providers/ 根目录（空 = OPCODE2API_PLUGIN_DIR env > <exe 目录>/providers）。
	ProvidersDir string
	// StartupTimeout 就绪行等待上限（默认 15s；超时按启动失败处理，指数退避重启）。
	StartupTimeout time.Duration
	// BackoffBase / BackoffCap 崩溃指数退避区间（默认 1s→60s）。
	BackoffBase time.Duration
	BackoffCap  time.Duration
	// RescanInterval 目录扫描间隔（默认 3s；<=0 关闭自动扫描，仅手动 Rescan）。
	RescanInterval time.Duration
	// APIVersion 兼容的契约版本（默认 1）。
	APIVersion int
	// ModelTimeout 就绪后查询子进程模型数的超时（默认 5s）。
	ModelTimeout time.Duration
	// OnChange 就绪/状态/增删变化回调（R2 桥接厂商经此触发 rebuildVendors；可为 nil）。
	OnChange func()
}

// Manager 插件管理器。所有插件状态在 mu 保护下读写；子进程 stdout 管道/退出
// 检测在各协程间经 channel 传递，不跨 goroutine 直接写状态。
type Manager struct {
	cfg     Config
	mu      sync.Mutex
	plugins map[string]*plugin
	wg      sync.WaitGroup
	ctx     context.Context
	cancel  context.CancelFunc
	started bool
	closed  bool
}

// New 构造插件管理器（零值配置回退默认）。
func New(cfg Config) *Manager {
	if cfg.ProvidersDir == "" {
		cfg.ProvidersDir = defaultProvidersDir()
	}
	if cfg.StartupTimeout <= 0 {
		cfg.StartupTimeout = defaultStartupTimeout
	}
	if cfg.BackoffBase <= 0 {
		cfg.BackoffBase = defaultBackoffBase
	}
	if cfg.BackoffCap <= 0 {
		cfg.BackoffCap = defaultBackoffCap
	}
	if cfg.RescanInterval <= 0 {
		cfg.RescanInterval = defaultRescanInterval
	}
	if cfg.APIVersion <= 0 {
		cfg.APIVersion = supportedAPIVersion
	}
	if cfg.ModelTimeout <= 0 {
		cfg.ModelTimeout = defaultModelTimeout
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{cfg: cfg, plugins: map[string]*plugin{}, ctx: ctx, cancel: cancel}
}

// defaultProvidersDir 计算 providers/ 根目录：env 优先（自定义部署/测试隔离），
// 默认 <可执行文件目录>/providers（安装目录惯例，设计文档 §二）。
func defaultProvidersDir() string {
	if d := os.Getenv("OPCODE2API_PLUGIN_DIR"); d != "" {
		return d
	}
	if exe, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(exe), "providers")
	}
	return "providers"
}

// Start 初始扫描 + 启动目录 watcher（幂等）。
func (m *Manager) Start() {
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return
	}
	m.started = true
	m.mu.Unlock()
	m.scan()
	if m.cfg.RescanInterval > 0 {
		m.wg.Add(1)
		go m.watch()
	}
}

// Close 停止 watcher 并统一 kill 全部子进程（主进程退出回收；设计文档 §4.3）。
// 有界等待监督协程退出，防子进程 kill 失败导致永久挂起。
func (m *Manager) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	m.cancel()
	ps := make([]*plugin, 0, len(m.plugins))
	for _, p := range m.plugins {
		ps = append(ps, p)
	}
	m.mu.Unlock()
	for _, p := range ps {
		m.stopPlugin(p)
		waitPIDGone(m.killCurrent(p), 3*time.Second)
	}
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		slog.Warn("pluginprovider close: supervisor exit timeout", "count", len(ps))
	}
}

// watch 目录 watcher：仿 startConfigWatcher 的 3s ticker，发现新增/移除供应商。
func (m *Manager) watch() {
	defer m.wg.Done()
	t := time.NewTicker(m.cfg.RescanInterval)
	defer t.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-t.C:
			m.scan()
		}
	}
}

// plugin 单个供应商（生命周期状态机）。
type plugin struct {
	id           string
	dir          string // providers/<id> 绝对路径
	manifestPath string // provider.json 绝对路径
	man          Manifest
	manErr       string // 清单校验错误（非空 = 拒绝加载，面板告警）
	raw          []byte // provider.json 全文（回显/编辑回填）

	enabled  bool
	status   string
	lastError string
	pid      int
	auth     string // 一次性令牌（就绪行校验后保留，供 R2 桥接鉴权；不出现在 API 视图）
	url      string
	startedAt time.Time
	modelCount int
	restartCount int

	supervising bool   // 监督协程是否存活（startSupervisor 去重）
	stopCh      chan struct{} // 删除/关闭时关闭（不可重建，见 stopPlugin）
	stopped     bool
	retryCh     chan struct{} // 唤醒监督协程立即行动（启用/配置保存/清单修复）
	exitCh      chan struct{} // 子进程退出通知（缓冲 1，防泄漏）
}

func newPlugin(m *Manager, id string) *plugin {
	dir := filepath.Join(m.cfg.ProvidersDir, id)
	return &plugin{
		id:           id,
		dir:          dir,
		manifestPath: filepath.Join(dir, "provider.json"),
		enabled:      true,
		status:       StatusStarting,
		stopCh:       make(chan struct{}),
		retryCh:      make(chan struct{}, 1),
		exitCh:       make(chan struct{}, 1),
	}
}

// stopPlugin 关闭 stopCh（幂等）。
func (m *Manager) stopPlugin(p *plugin) {
	m.mu.Lock()
	if p.stopped {
		m.mu.Unlock()
		return
	}
	p.stopped = true
	m.mu.Unlock()
	close(p.stopCh)
}

// scan 扫描 providers/：只认固定文件名 provider.json、目录名 = 供应商 id、
// 忽略非目录条目；发现新增 → 拉起；目录/清单消失 → 停进程并从列表移除（不删文件）。
func (m *Manager) scan() {
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if closed {
		return
	}
	entries, err := os.ReadDir(m.cfg.ProvidersDir)
	if err != nil {
		if os.IsNotExist(err) {
			_ = os.MkdirAll(m.cfg.ProvidersDir, 0o755)
		}
		return
	}
	present := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() {
			continue // 忽略非目录条目
		}
		id := e.Name()
		raw, err := os.ReadFile(filepath.Join(m.cfg.ProvidersDir, id, "provider.json"))
		if err != nil {
			continue // 无 provider.json 的目录不视为供应商
		}
		present[id] = true
		m.ensurePlugin(id, raw)
	}

	m.mu.Lock()
	var gone []*plugin
	for id, p := range m.plugins {
		if present[id] {
			continue
		}
		p.enabled = false
		delete(m.plugins, id)
		gone = append(gone, p)
	}
	m.mu.Unlock()
	for _, p := range gone {
		m.stopPlugin(p)
		m.killCurrent(p)
	}
	if len(gone) > 0 {
		m.notifyChange()
	}
}

// ensurePlugin 按扫描/保存结果装载插件：不存在则建档并拉起，已存在则按文件内容
// 变化决定动作（entry/api_version 变化 → 重启子进程；仅私有配置变化 → 子进程自 watch）。
func (m *Manager) ensurePlugin(id string, raw []byte) {
	man, parseErr := parseManifest(raw)
	var manErr string
	if parseErr != nil {
		manErr = parseErr.Error()
	} else if err := validateManifest(man, id, filepath.Join(m.cfg.ProvidersDir, id)); err != nil {
		manErr = err.Error()
	}

	m.mu.Lock()
	p, ok := m.plugins[id]
	if ok {
		changed := !bytes.Equal(p.raw, raw)
		oldEntry, oldAPI, oldManErr := p.man.Entry, p.man.APIVersion, p.manErr
		if changed {
			p.raw = append(p.raw[:0], raw...)
			p.man = man
			p.manErr = manErr
		}
		restart := changed && manErr == "" && (man.Entry != oldEntry || man.APIVersion != oldAPI)
		retry := changed && manErr != oldManErr
		m.mu.Unlock()
		if restart || retry {
			m.signalRetry(p)
			m.notifyChange()
		}
		return
	}
	p = newPlugin(m, id)
	p.raw = append([]byte(nil), raw...)
	p.man = man
	p.manErr = manErr
	m.plugins[id] = p
	m.mu.Unlock()
	m.startSupervisor(p)
}

// signalRetry 唤醒监督协程立即行动（非阻塞）。
func (m *Manager) signalRetry(p *plugin) {
	select {
	case p.retryCh <- struct{}{}:
	default:
	}
}

// startSupervisor 启动/重启监督协程（幂等：同一插件只保留一个存活监督者）。
// 停用不销毁协程（parked 等启用），删除/关闭才经 stopCh 结束。
func (m *Manager) startSupervisor(p *plugin) {
	m.mu.Lock()
	if p.supervising {
		m.mu.Unlock()
		return
	}
	p.supervising = true
	m.mu.Unlock()
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		m.supervise(p)
		m.mu.Lock()
		p.supervising = false
		m.mu.Unlock()
	}()
}

// supervise 插件生命周期监督循环：等启用 → 等清单合法 → spawn 等就绪行 →
// 稳定态监听退出/重试/停用。启动失败或崩溃按指数退避重启（1s→60s 封顶）。
func (m *Manager) supervise(p *plugin) {
	delay := m.cfg.BackoffBase
	for {
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			return
		}
		enabled := p.enabled
		manErr := p.manErr
		m.mu.Unlock()

		if !enabled {
			m.setStatus(p, StatusDisabled, "")
			select {
			case <-p.retryCh: // 重新启用
				continue
			case <-p.stopCh:
				return
			}
		}

		if manErr != "" {
			// 清单错误：面板告警，不自动重试（重试无意义）；修复后经配置保存/retry 唤醒。
			m.setStatus(p, StatusError, manErr)
			select {
			case <-p.retryCh:
				m.revalidateManifest(p)
				continue
			case <-p.stopCh:
				return
			}
		}

		ok := m.spawnAndReadReady(p)
		if !ok {
			// 启动失败（超时/fatal/spawn 错误/令牌不匹配）：指数退避后重试。
			select {
			case <-time.After(delay):
			case <-p.retryCh:
			case <-p.stopCh:
				return
			}
			delay *= 2
			if delay > m.cfg.BackoffCap {
				delay = m.cfg.BackoffCap
			}
			continue
		}
		delay = m.cfg.BackoffBase // 就绪成功，退避归零

		// 丢弃 spawn 期间到达的重试信号：子进程已按最新清单启动，无需再重启。
		select {
		case <-p.retryCh:
		default:
		}
		select {
		case <-p.exitCh:
			m.setStatus(p, StatusError, "子进程意外退出")
		case <-p.retryCh: // 配置保存（entry/api_version 变化）→ 重启子进程
			m.killCurrent(p)
			m.setStatus(p, StatusStarting, "")
			continue
		case <-p.stopCh:
			m.killCurrent(p)
			return
		}
	}
}

// revalidateManifest 重新读盘校验清单（清单错误修复后重试）。
func (m *Manager) revalidateManifest(p *plugin) {
	raw, err := os.ReadFile(p.manifestPath)
	if err != nil {
		m.mu.Lock()
		p.manErr = "provider.json 读取失败: " + err.Error()
		m.mu.Unlock()
		return
	}
	man, parseErr := parseManifest(raw)
	var manErr string
	if parseErr != nil {
		manErr = parseErr.Error()
	} else if err := validateManifest(man, p.id, p.dir); err != nil {
		manErr = err.Error()
	}
	m.mu.Lock()
	p.raw = append(p.raw[:0], raw...)
	p.man = man
	p.manErr = manErr
	m.mu.Unlock()
	m.notifyChange()
}

// setStatus 更新状态；停用态下忽略非 disabled 状态写（防停用竞态覆盖）。
func (m *Manager) setStatus(p *plugin, status, errMsg string) {
	changed := false
	m.mu.Lock()
	if !p.enabled && status != StatusDisabled {
		m.mu.Unlock()
		return
	}
	if p.status != status || p.lastError != errMsg {
		changed = true
	}
	p.status = status
	p.lastError = errMsg
	if status == StatusError {
		p.url = ""
	}
	m.mu.Unlock()
	if changed {
		m.notifyChange()
	}
}

// notifyChange 状态/增删变化回调（锁外调用）。
func (m *Manager) notifyChange() {
	if m.cfg.OnChange != nil {
		m.cfg.OnChange()
	}
}

// spawnAndReadReady 拉起子进程并等待就绪行（设计文档 §4.1）。
//
// 契约：cwd=供应商目录；env 传 PROVIDER_DIR / PROVIDER_CONFIG / PLUGIN_AUTH_TOKEN；
// argv = <entry> --provider-serve --port 0。stdout 逐行解析 ready/need_config/fatal。
//
// 返回 true = 子进程稳定存活（running 或 need_config），false = 启动失败（已清理）。
func (m *Manager) spawnAndReadReady(p *plugin) bool {
	entryPath, err := safeJoin(p.dir, p.man.Entry)
	if err != nil {
		m.setStatus(p, StatusError, "entry 非法: "+err.Error())
		return false
	}
	token := randomToken()

	// 启动超时 ctx；stopCh 关闭（删除/主进程退出）时中断 spawn。
	spawnCtx, spawnCancel := context.WithCancel(m.ctx)
	defer spawnCancel()
	go func() {
		select {
		case <-p.stopCh:
			spawnCancel()
		case <-spawnCtx.Done():
		}
	}()
	ctx, cancel := context.WithTimeout(spawnCtx, m.cfg.StartupTimeout)
	defer cancel()

	// 每个 spawn 周期重建 exitCh：旧子进程（被 kill/退出）的退出通知留在旧通道上，
	// 防止陈旧通知在稳定态 select 中触发误判「子进程意外退出」重启。
	m.mu.Lock()
	p.exitCh = make(chan struct{}, 1)
	exitCh := p.exitCh
	m.mu.Unlock()

	cmd := exec.Command(entryPath, "--provider-serve", "--port", "0")
	cmd.Dir = p.dir
	cmd.Env = append(os.Environ(),
		"PROVIDER_DIR="+p.dir,
		"PROVIDER_CONFIG="+p.manifestPath,
		"PLUGIN_AUTH_TOKEN="+token,
	)
	applyNoWindow(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		m.setStatus(p, StatusError, "无法接管子进程 stdout: "+err.Error())
		return false
	}
	cmd.Stderr = nil // 丢弃 stderr（错误经就绪行上报）
	if err := cmd.Start(); err != nil {
		m.setStatus(p, StatusError, "子进程启动失败: "+err.Error())
		return false
	}

	m.mu.Lock()
	p.pid = cmd.Process.Pid
	p.auth = token
	p.url = ""
	p.startedAt = time.Time{}
	m.mu.Unlock()

	lines := make(chan string, 16)
	go scanStdout(stdout, lines)
	cleanup := func() {
		m.mu.Lock()
		if p.pid == cmd.Process.Pid {
			p.pid = 0
			p.auth = ""
		}
		m.mu.Unlock()
		killPID(cmd.Process.Pid)
		_ = cmd.Wait()
	}

	for {
		select {
		case <-ctx.Done():
			cleanup()
			m.fail(p, fmt.Sprintf("启动超时：%s 内未收到就绪行", m.cfg.StartupTimeout))
			return false
		case ln, ok := <-lines:
			if !ok {
				// stdout 关闭 = 子进程已退出（未报告状态）。
				cleanup()
				m.fail(p, "子进程在报告就绪前退出")
				return false
			}
			msg, isReady := parseReadyLine(ln)
			if !isReady {
				continue // 非就绪行（日志等），忽略
			}
			switch msg.State {
			case "ready":
				// 一次性令牌回显校验：防本地其它进程伪造就绪行。
				if msg.Auth != token {
					cleanup()
					m.fail(p, "就绪行令牌不匹配（疑似伪造就绪行）")
					return false
				}
				if msg.ID != "" && msg.ID != p.id {
					cleanup()
					m.fail(p, fmt.Sprintf("就绪行 id %q 与目录 %q 不一致", msg.ID, p.id))
					return false
				}
				if msg.Port < 1 || msg.Port > 65535 {
					cleanup()
					m.fail(p, "就绪行 port 非法")
					return false
				}
				m.mu.Lock()
				if m.closed || !p.enabled {
					m.mu.Unlock()
					cleanup()
					return false
				}
				p.url = fmt.Sprintf("http://127.0.0.1:%d", msg.Port)
				p.status = StatusRunning
				p.lastError = ""
				p.startedAt = time.Now()
				p.restartCount++
				m.mu.Unlock()
				go watchExit(cmd, exitCh)
				m.queryModelCount(p) // 尽力而为（模型数展示；失败保持 0）
				m.notifyChange()
				return true
			case "need_config":
				m.mu.Lock()
				if m.closed || !p.enabled {
					m.mu.Unlock()
					cleanup()
					return false
				}
				p.status = StatusNeedCfg
				p.lastError = msg.Hint
				m.mu.Unlock()
				go watchExit(cmd, exitCh)
				m.notifyChange()
				return true
			case "fatal":
				cleanup()
				m.fail(p, "子进程启动失败: "+msg.Error)
				return false
			}
		}
	}
}

// fail 记录启动失败状态；停用/关闭竞态下不覆盖状态。
func (m *Manager) fail(p *plugin, msg string) {
	m.mu.Lock()
	skip := !p.enabled || m.closed
	m.mu.Unlock()
	if skip {
		return
	}
	m.setStatus(p, StatusError, msg)
}

// scanStdout 逐行读取子进程 stdout 并投递（缓冲满丢弃，读取者退出后不阻塞）。
func scanStdout(r io.Reader, lines chan<- string) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		select {
		case lines <- sc.Text():
		default:
		}
	}
	close(lines)
}

// watchExit 等待子进程退出并通知 exitCh（缓冲 1，防发送阻塞）。
func watchExit(cmd *exec.Cmd, exitCh chan<- struct{}) {
	_ = cmd.Wait()
	select {
	case exitCh <- struct{}{}:
	default:
	}
}

// readyMsg 就绪行 JSON（设计文档 §4.1）。
type readyMsg struct {
	State   string `json:"state"` // ready | need_config | fatal
	Port    int    `json:"port"`
	Auth    string `json:"auth"`
	ID      string `json:"id"`
	Version string `json:"version"`
	Hint    string `json:"hint"`
	Error   string `json:"error"`
}

// parseReadyLine 尝试把一行 stdout 解析为就绪消息；非 JSON/非就绪状态返回 false。
func parseReadyLine(line string) (*readyMsg, bool) {
	line = strings.TrimSpace(line)
	if line == "" || line[0] != '{' {
		return nil, false
	}
	var m readyMsg
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		return nil, false
	}
	switch m.State {
	case "ready", "need_config", "fatal":
		return &m, true
	default:
		return nil, false
	}
}

// queryModelCount 就绪后向子进程拉一次模型目录计数（不参与桥接，仅列表展示）。
func (m *Manager) queryModelCount(p *plugin) {
	m.mu.Lock()
	url, auth := p.url, p.auth
	m.mu.Unlock()
	if url == "" {
		return
	}
	ctx, cancel := context.WithTimeout(m.ctx, m.cfg.ModelTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/v1/models", nil)
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+auth)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return
	}
	m.mu.Lock()
	p.modelCount = len(out.Data)
	m.mu.Unlock()
}

// killCurrent 终止当前子进程并清 pid（幂等）；返回被杀的 pid（等待其退出用）。
func (m *Manager) killCurrent(p *plugin) int {
	m.mu.Lock()
	pid := p.pid
	p.pid = 0
	m.mu.Unlock()
	killPID(pid)
	return pid
}

// randomToken 生成一次性鉴权令牌（经 env 传给子进程，就绪行原样回显校验）。
func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err == nil {
		return base64.RawURLEncoding.EncodeToString(b)
	}
	return fmt.Sprintf("fallback-token-%d", time.Now().UnixNano())
}

// ---------------------------------------------------------------- 操作面

// Rescan 手动重扫 providers/ 并返回最新列表。
func (m *Manager) Rescan() []View {
	m.scan()
	return m.Views()
}

// View 插件列表项（管理 API 契约，设计文档 §七）。
type View struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Version      string `json:"version"`
	Status       string `json:"status"`
	Models       int    `json:"models"`
	Path         string `json:"path"`
	ProviderJSON string `json:"provider_json"` // provider.json 全文（面板编辑回填）
	PID          int    `json:"pid,omitempty"`
	URL          string `json:"url,omitempty"`
	LastError    string `json:"last_error,omitempty"`
	StartedAt    string `json:"started_at,omitempty"`
	RestartCount int    `json:"restart_count"`
}

// View 查询单个插件视图（不存在返回零值）。
func (m *Manager) View(id string) View {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.plugins[id]; ok {
		return m.viewOf(p)
	}
	return View{}
}

// Views 全部插件视图（按 id 排序）。
func (m *Manager) Views() []View {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]string, 0, len(m.plugins))
	for id := range m.plugins {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	vs := make([]View, 0, len(ids))
	for _, id := range ids {
		vs = append(vs, m.viewOf(m.plugins[id]))
	}
	return vs
}

func (m *Manager) viewOf(p *plugin) View {
	name := p.man.Name
	if name == "" {
		name = p.id
	}
	ver := p.man.Version
	if ver == "" {
		ver = "-"
	}
	started := ""
	if !p.startedAt.IsZero() {
		started = p.startedAt.Format(time.RFC3339)
	}
	return View{
		ID: p.id, Name: name, Version: ver,
		Status: p.status, Models: p.modelCount,
		Path: p.dir, ProviderJSON: string(p.raw),
		PID: p.pid, URL: p.url, LastError: p.lastError,
		StartedAt: started, RestartCount: p.restartCount,
	}
}

// Endpoint 返回已就绪插件的子进程 HTTP 端点与鉴权令牌（R2 vendors/remote 桥接用）。
func (m *Manager) Endpoint(id string) (url, auth string, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.plugins[id]
	if !ok || p.status != StatusRunning {
		return "", "", false
	}
	return p.url, p.auth, true
}

// SaveConfig 校验并原子写回 provider.json（设计文档 §六编辑保存）：
// JSON 合法、id 与目录名一致、entry 指向存在的文件；写盘后按变化决定重启子进程
// 或由子进程自 watch 热重载（私有配置变更）。
func (m *Manager) SaveConfig(id string, data []byte) error {
	if err := validPluginID(id); err != nil {
		return fmt.Errorf("非法插件 id %q", id)
	}
	m.mu.Lock()
	p, ok := m.plugins[id]
	m.mu.Unlock()
	if !ok {
		return errNotFound
	}
	man, err := parseManifest(data)
	if err != nil {
		return err
	}
	if err := validateSave(man, id, p.dir); err != nil {
		return err
	}
	if err := writeFileAtomic(p.manifestPath, data); err != nil {
		return fmt.Errorf("provider.json 写入失败: %w", err)
	}
	m.ensurePlugin(id, data)
	return nil
}

// Toggle 启停插件（enabled=false 停进程+注销但不删文件；true 拉起+注册）。
func (m *Manager) Toggle(id string, enabled bool) (View, error) {
	m.mu.Lock()
	p, ok := m.plugins[id]
	if !ok {
		m.mu.Unlock()
		return View{}, errNotFound
	}
	same := p.enabled == enabled
	p.enabled = enabled
	m.mu.Unlock()

	if same {
		if enabled && p.status != StatusRunning && p.status != StatusNeedCfg {
			// 已启用但进程不在（异常态）→ 立即尝试拉起。
			m.signalRetry(p)
		}
	} else if enabled {
		m.setStatus(p, StatusStarting, "")
		m.signalRetry(p)
	} else {
		m.killCurrent(p)
		m.setStatus(p, StatusDisabled, "")
	}
	return m.View(id), nil
}

// Delete 停进程 + 整目录删除（设计文档 §4.3；前端二次确认）。先停用并结束监督协程
// （防 kill 后自动重启占住 exe），等进程退出再删目录——Windows 上运行中的 exe 被锁，
// 不等待会删不干净。
func (m *Manager) Delete(id string) error {
	m.mu.Lock()
	p, ok := m.plugins[id]
	if !ok {
		m.mu.Unlock()
		return errNotFound
	}
	pid := p.pid
	p.enabled = false
	m.mu.Unlock()

	m.stopPlugin(p)
	m.killCurrent(p)
	waitPIDGone(pid, 5*time.Second)
	var err error
	for i := 0; i < 5; i++ { // 防御性重试：杀进程后句柄释放 + 扫描协程瞬读 provider.json 的窗口
		err = os.RemoveAll(p.dir)
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	m.mu.Lock()
	if cur, ok := m.plugins[id]; ok && cur == p {
		delete(m.plugins, id)
	}
	m.mu.Unlock()
	m.notifyChange()
	return err
}
