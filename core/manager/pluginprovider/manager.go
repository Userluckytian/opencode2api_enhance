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
	StatusDisabled = "disabled"    // 已停用（enabled=false，文件保留）
	StatusStarting = "starting"    // 拉起中（等就绪行）
	StatusRunning  = "running"     // 已就绪，厂商可用
	StatusNeedCfg  = "need_config" // 待配置（子进程自举后报告，不注册厂商）
	StatusError    = "error"       // 启动失败/崩溃退避中（含最近错误）
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
	// StateFile 插件启停状态落盘路径（跨进程共享：主管理器开关 → 实例子进程跟随）。
	// 空 = <ProvidersDir>/.plugin-state.json。文件不存在 = 全部默认启用。
	StateFile string
	// OnChange 就绪/状态/增删变化回调（R2 桥接厂商经此触发 rebuildVendors；可为 nil）。
	OnChange func()
}

// Manager 插件管理器。所有插件状态在 mu 保护下读写；子进程 stdout 管道/退出
// 检测在各协程间经 channel 传递，不跨 goroutine 直接写状态。
type Manager struct {
	cfg     Config
	mu      sync.Mutex
	plugins map[string]*plugin
	state   map[string]bool // 跨进程启停状态（id → enabled；缺失 = 默认启用）
	stateMu sync.Mutex      // 保护 state 读写（updateStateFile 在 mu 外写盘）
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
	m := &Manager{cfg: cfg, plugins: map[string]*plugin{}, ctx: ctx, cancel: cancel}
	if cfg.StateFile == "" {
		m.cfg.StateFile = filepath.Join(cfg.ProvidersDir, ".plugin-state.json")
	}
	m.state = loadPluginState(m.cfg.StateFile)
	return m
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
	m.reapOrphans() // 回收本 providers 目录的插件孤儿（旧主进程强杀/崩溃残留长期占端口）
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

	enabled      bool
	status       string
	lastError    string
	pid          int
	auth         string // 一次性令牌（就绪行校验后保留，供 R2 桥接鉴权；不出现在 API 视图）
	url          string
	startedAt    time.Time
	modelCount   int
	modelsAll    []string // 就绪后拉取的全量模型 ID 清单（暴露勾选弹层用；尽力而为，失败为空）
	restartCount int

	supervising bool          // 监督协程是否存活（startSupervisor 去重）
	stopCh      chan struct{} // 删除/关闭时关闭（不可重建，见 stopPlugin）
	stopped     bool
	retryCh     chan struct{} // 唤醒监督协程立即行动（启用/配置保存/清单修复）
	exitCh      chan struct{} // 子进程退出通知（缓冲 1，防泄漏）
	// stdoutCh 子进程 stdout 行流（当前 spawn 周期有效）：spawnAndReadReady 就该通道
	// 解析就绪行；收到首个 need_config 后把它移交稳定态（supervise 的 select 分支），
	// 从而持续捕获子进程后续补打的 ready/fatal 行（设计文档 §4.1：宿主必须持续
	// 消费 stdout 行流，否则漏掉 need_config → ready 转换，插件永不注册）。由
	// scanStdout 写入、spawnAndReadReady 之后 supervise 继续读，通道不跨周期重建。
	stdoutCh chan string
}

func newPlugin(m *Manager, id string) *plugin {
	dir := filepath.Join(m.cfg.ProvidersDir, id)
	// enabled 按跨进程共享状态初始化（须在 m.mu 锁内调用：读 m.state）。
	// 状态文件缺失 = 默认启用（向后兼容）。
	enabled := true
	if v, ok := m.state[id]; ok {
		enabled = v
	}
	return &plugin{
		id:           id,
		dir:          dir,
		manifestPath: filepath.Join(dir, "provider.json"),
		enabled:      enabled,
		status:       StatusStarting,
		stopCh:       make(chan struct{}),
		retryCh:      make(chan struct{}, 1),
		exitCh:       make(chan struct{}, 1),
		stdoutCh:     make(chan string, 16),
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
	m.applyStateChanges() // 跨进程开关跟随（主管理器写的状态文件，子进程同步）
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
		// 稳定态：持续消费子进程 stdout 行流（need_config 后子进程补打 ready/fatal 行），
		// 同时监听退出/重试/停用。设计文档 §4.1：宿主必须持续消费 stdout 行流。
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
		case ln, ok := <-p.stdoutCh:
			if !ok {
				continue // stdout 关闭由 exitCh 分支处理（子进程退出时触发）
			}
			if m.handleStdoutLine(p, ln) {
				continue // 已重启（fatal）或就绪（ready），回循环顶部统一处理
			}
		}
	}
}

// handleStdoutLine 处理稳定态收到的子进程 stdout 行（need_config 后的后续状态行）。
// 返回 true = 状态已变化需要回循环顶部（就绪 running / fatal 重启），
// false = 忽略（非就绪行、重复 need_config、重复 ready）。
func (m *Manager) handleStdoutLine(p *plugin, ln string) bool {
	msg, isReady := parseReadyLine(ln)
	if !isReady {
		return false
	}
	switch msg.State {
	case "ready":
		m.mu.Lock()
		if m.closed || !p.enabled || p.status == StatusRunning {
			m.mu.Unlock()
			return false // 已就绪：重复行，忽略
		}
		token := p.auth
		m.mu.Unlock()
		// 一次性令牌/port 校验（与 spawnAndReadReady 首次就绪同款）。
		if msg.Auth != token || msg.Port < 1 || msg.Port > 65535 {
			return false
		}
		if msg.ID != "" && msg.ID != p.id {
			return false
		}
		m.mu.Lock()
		p.url = fmt.Sprintf("http://127.0.0.1:%d", msg.Port)
		p.status = StatusRunning
		p.lastError = ""
		p.startedAt = time.Now()
		m.mu.Unlock()
		m.queryModelCount(p) // 尽力而为（模型数展示；失败保持 0）
		m.notifyChange()
		return true
	case "need_config":
		return false // 状态已记录（spawn 时），重复行忽略
	case "fatal":
		m.killCurrent(p)
		m.setStatus(p, StatusError, "子进程运行中报告致命错误: "+msg.Error)
		return true
	}
	return false
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

// procInfo 系统进程枚举结果（孤儿回收用：--provider-serve 插件子进程）。
type procInfo struct {
	PID  int
	PPID int // 父进程 PID（宿主存活判定）
	Cmd  string
}

// reapOrphans 回收本 providers 目录下宿主已退出的插件子进程残留（孤儿）。
// 枚举系统插件子进程（--provider-serve），命令行指向本 ProvidersDir、非本管理器
// 持活、且父进程（宿主 opencode2api/本测试二进制）已不存在的 → 杀之。宿主存活的
// 插件子进程归其宿主进程管理（主管理器与实例子进程共享 providers 目录时互不干扰）。
func (m *Manager) reapOrphans() {
	procs, err := listProviderProcesses()
	if err != nil {
		slog.Debug("plugin orphan scan failed", "error", err)
		return
	}
	dirLow := strings.ToLower(filepath.Clean(m.cfg.ProvidersDir))
	hosts := map[int]bool{}
	for _, pr := range procs {
		if strings.Contains(strings.ToLower(pr.Cmd), "opencode2api") {
			hosts[pr.PID] = true
		}
	}
	m.mu.Lock()
	alive := map[int]bool{}
	for _, p := range m.plugins {
		if p.pid > 0 {
			alive[p.pid] = true
		}
	}
	m.mu.Unlock()
	for _, pr := range procs {
		low := strings.ToLower(pr.Cmd)
		if !strings.Contains(low, "--provider-serve") {
			continue
		}
		if !strings.Contains(low, dirLow) {
			continue // 只处理本 providers 目录的进程，不动别处的插件子进程
		}
		if alive[pr.PID] {
			continue // 当前持活的子进程
		}
		if pr.PPID > 0 && hosts[pr.PPID] {
			continue // 宿主存活（主管理器/其它实例子进程管理，非孤儿）
		}
		slog.Info("plugin orphan reaped", "pid", pr.PID)
		killPID(pr.PID)
	}
}

// loadPluginState 读取跨进程启停状态文件。不存在/非法（含 UTF-8 BOM）→ 空表
// （= 全部默认启用）。主管理器写、实例/网关子进程读，据此跟随开关。
func loadPluginState(path string) map[string]bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]bool{}
	}
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")) // 容忍 UTF-8 BOM
	var st struct {
		Enabled map[string]bool `json:"enabled"`
	}
	if json.Unmarshal(data, &st) != nil || st.Enabled == nil {
		return map[string]bool{}
	}
	return st.Enabled
}

// updateStateFile 更新单个插件启停状态并原子写盘（临时文件 + rename），
// 供其它进程（实例/统一网关子进程）在下一个扫描周期跟随。
func (m *Manager) updateStateFile(id string, enabled bool) {
	m.stateMu.Lock()
	m.state[id] = enabled
	data, err := json.Marshal(map[string]any{"enabled": m.state})
	m.stateMu.Unlock()
	if err != nil {
		return
	}
	tmp := m.cfg.StateFile + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		slog.Debug("plugin state write failed", "path", m.cfg.StateFile, "error", err)
		return
	}
	if err := os.Rename(tmp, m.cfg.StateFile); err != nil {
		slog.Debug("plugin state rename failed", "path", m.cfg.StateFile, "error", err)
	}
}

// applyStateChanges 跨进程启停状态跟随：重读状态文件，凡与当前 enabled 不一致的
// 插件对称执行 Toggle（kill/拉起 + 聚合器变更经 OnChange 传播）。主管理器开关
// 关闭后，实例子进程在此 ≤1 个扫描周期（默认 3s）内停掉自家插件并移除模型。
func (m *Manager) applyStateChanges() {
	st := loadPluginState(m.cfg.StateFile)
	if len(st) == 0 {
		return
	}
	m.mu.Lock()
	var diff []string
	for id, want := range st {
		if p, ok := m.plugins[id]; ok && p.enabled != want {
			diff = append(diff, id)
		}
	}
	m.mu.Unlock()
	for _, id := range diff {
		if _, err := m.Toggle(id, st[id]); err != nil {
			slog.Debug("plugin state follow failed", "id", id, "error", err)
		}
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
	// 重新拉起前先回收上一周期的子进程（反复启停/配置重启/崩溃重试不叠加进程）。
	// 首个周期 p.pid == 0 时 killCurrent 直接返回 0，无副作用。
	if old := m.killCurrent(p); old > 0 {
		waitPIDGone(old, 2*time.Second)
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

	// 每个 spawn 周期重建 exitCh 与 stdoutCh：旧子进程（被 kill/退出）的退出通知与
	// stdout 行留在旧通道上，防止陈旧消息在稳定态 select 中触发误判
	// （「子进程意外退出」重启 / 旧周期行干扰）。scanStdout 结束时 close 的是本周期
	// 局部引用，不影响下一周期的重建通道。
	m.mu.Lock()
	p.exitCh = make(chan struct{}, 1)
	exitCh := p.exitCh
	stdoutCh := make(chan string, 16)
	p.stdoutCh = stdoutCh
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

	go scanStdout(stdout, stdoutCh)
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
		case ln, ok := <-stdoutCh:
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
	ids := make([]string, 0, len(out.Data))
	for _, d := range out.Data {
		if d.ID != "" {
			ids = append(ids, d.ID)
		}
	}
	p.modelsAll = ids
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
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Version      string   `json:"version"`
	Status       string   `json:"status"`
	Models       int      `json:"models"`
	ModelsAll    []string `json:"models_all,omitempty"`    // 全量模型 ID 清单（暴露勾选弹层用）
	ExposeAll    bool     `json:"expose_all"`              // 全部暴露（true 时 ExposedModels 无意义）
	ExposedModels []string `json:"exposed_models,omitempty"` // 暴露白名单（ExposeAll=false 时生效）
	Path         string   `json:"path"`
	ProviderJSON string   `json:"provider_json"` // provider.json 全文（面板编辑回填）
	PID          int      `json:"pid,omitempty"`
	URL          string   `json:"url,omitempty"`
	LastError    string   `json:"last_error,omitempty"`
	StartedAt    string   `json:"started_at,omitempty"`
	RestartCount int      `json:"restart_count"`
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
		ModelsAll: p.modelsAll, ExposeAll: p.man.ExposeAll == nil || *p.man.ExposeAll,
		ExposedModels: p.man.ExposedModels,
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

// RunningPlugin 已就绪插件的桥接信息（R2 装配 vendors/remote 用）。
type RunningPlugin struct {
	ID   string // 插件 id；remote vendor 的模型目录前缀
	Name string // 展示名（provider.json name，缺省 = id）
	URL  string // 子进程 HTTP 端点（http://127.0.0.1:<port>）
	Auth string // 一次性令牌（子进程鉴权）
	// 模型暴露白名单（主进程侧过滤，对齐自定义源 allowed_models）：
	// ExposeAll=true 全量透传；false 时仅暴露 ExposedModels 内的模型。
	ExposeAll     bool
	ExposedModels []string
}

// RunningPlugins 返回全部 running 状态插件的桥接信息（按 id 排序）。
// starting/need_config/error/disabled 的插件不在列——装配方据此重建厂商集合，
// 使聚合器目录与插件真实可用状态保持一致。
func (m *Manager) RunningPlugins() []RunningPlugin {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]string, 0, len(m.plugins))
	for id, p := range m.plugins {
		if p.status == StatusRunning && p.url != "" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	out := make([]RunningPlugin, 0, len(ids))
	for _, id := range ids {
		p := m.plugins[id]
		name := p.man.Name
		if name == "" {
			name = id
		}
		exposeAll := p.man.ExposeAll == nil || *p.man.ExposeAll
		out = append(out, RunningPlugin{
			ID: id, Name: name, URL: p.url, Auth: p.auth,
			ExposeAll: exposeAll, ExposedModels: append([]string(nil), p.man.ExposedModels...),
		})
	}
	return out
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

// SetExposedModels 保存插件的模型暴露白名单（设计文档 §六「获取模型并自定义暴露」）。
// 主进程侧过滤：把 expose_all/exposed_models 合并进 provider.json（保留其余键原样），
// 原子写盘后触发 OnChange 重建桥接厂商，过滤即时生效（无需插件改动）。
// exposeAll=true 时 exposedModels 无意义（清除旧白名单键）。
func (m *Manager) SetExposedModels(id string, exposeAll bool, exposedModels []string) error {
	if err := validPluginID(id); err != nil {
		return fmt.Errorf("非法插件 id %q", id)
	}
	m.mu.Lock()
	p, ok := m.plugins[id]
	m.mu.Unlock()
	if !ok {
		return errNotFound
	}
	// 基于当前 manifest 原文合并（不回写私有配置/其它键，仅改暴露配置两个保留键）。
	var doc map[string]any
	if len(p.raw) > 0 {
		if err := json.Unmarshal(p.raw, &doc); err != nil {
			return fmt.Errorf("provider.json 不是合法 JSON: %w", err)
		}
	} else {
		doc = map[string]any{}
	}
	doc["expose_all"] = exposeAll
	if exposeAll || len(exposedModels) == 0 {
		// 全暴露（或空白名单）→ 清除白名单键，回退「空 = 全部暴露」语义。
		delete(doc, "exposed_models")
		if len(exposedModels) == 0 {
			doc["expose_all"] = true
		}
	} else {
		list := make([]any, 0, len(exposedModels))
		for _, s := range exposedModels {
			if s = strings.TrimSpace(s); s != "" {
				list = append(list, s)
			}
		}
		doc["exposed_models"] = list
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFileAtomic(p.manifestPath, data); err != nil {
		return fmt.Errorf("provider.json 写入失败: %w", err)
	}
	m.ensurePlugin(id, data)
	m.notifyChange() // 白名单变化 → 桥接厂商重建 → 聚合目录/网关过滤即时生效
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
	if !same {
		m.updateStateFile(id, enabled) // 跨进程共享：实例/网关子进程据此跟随
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
