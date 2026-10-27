// 供应商服务（Provider Service）：自定义模型源 + 插件式供应商的独立入口进程。
//
// 背景（2026-10 拆分）：自定义源与插件此前随每个 opencode2api 进程装配（管理器 / 每个
// 实例 / 统一网关），既与 OpenCode 免费池共用 40080，又导致插件子进程重复拉起、账号状态
// 与固定面板端口互相打架。本服务把两类供应商收敛到独立端口（默认 50080）：
//   - 只装配 type=custom 与插件 remote 厂商，不加载 opencode；
//   - 不启动 sidecar / sing-box / 实例池，OpenCode 池故障不影响本端口；
//   - 插件子进程全局只由本进程托管一份。
//
// 进程形态与统一网关一致：管理器以子进程方式拉起、看护、退出回收。
package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// providerServicePortDefault 供应商服务默认端口（与实例段 40100+ / 网关 40080 错开）。
const providerServicePortDefault uint16 = 50080

// providerServiceDirBase 供应商服务运行目录名（runtime/_provider-service）。
const providerServiceDirBase = "_provider-service"

// ProviderServiceStatus 供应商服务状态（前端契约）。
type ProviderServiceStatus struct {
	Running bool   `json:"running"`
	Address string `json:"address"`
	Port    uint16 `json:"port"`
	APIKey  string `json:"api_key"`
	Message string `json:"message"`
	// 供应商服务当前暴露的模型（自定义源 + 插件），与网关卡片的「可用模型」同语义。
	Models          []string `json:"models"`
	ModelsUpdatedAt int64    `json:"models_updated_at,omitempty"`
	ModelsError     string   `json:"models_error,omitempty"`
}

// ProviderService 管理供应商服务子进程。
type ProviderService struct {
	m *Manager
	// configPath 子进程加载的配置文件（根进程 -config；含自定义源）。
	configPath string

	mu      sync.Mutex
	pid     int
	lastErr string
	lastTry time.Time
	// stopped 用户显式停止（看护协程不得自动拉起；Start 清除）。
	stopped bool
	// 拉起时使用的端口/密钥：配置热更新后与当前值不一致则看护重启。
	spawnPort uint16
	spawnKey  string

	// 模型目录缓存（与统一网关同款：10s 节流 + 60s 后台刷新）。
	models          []string
	modelsUpdatedAt int64
	modelsErr       string
	modelsLastFetch int64
	modelsLoading   bool

	ctx    context.Context
	cancel context.CancelFunc
}

// NewProviderService 构造供应商服务管理器。configPath 传根进程的 -config 路径
// （自定义源写在根配置里；空则回退管理器数据目录的 config.json）。
func NewProviderService(m *Manager, configPath string) *ProviderService {
	ctx, cancel := context.WithCancel(context.Background())
	if configPath == "" {
		configPath = m.configPath()
	}
	return &ProviderService{m: m, configPath: configPath, ctx: ctx, cancel: cancel}
}

// managerProviderPort 供应商服务端口：优先环境变量 OPCODE2API_PROVIDER_PORT，
// 其次 config.provider_port，否则默认 50080。
func (m *Manager) managerProviderPort() uint16 {
	if s := os.Getenv("OPCODE2API_PROVIDER_PORT"); s != "" {
		if n := parsePositiveInt(s); n > 0 && n < 65536 {
			return uint16(n)
		}
	}
	if m != nil {
		if p := m.loadConfig().ProviderPort; p > 0 {
			return p
		}
	}
	return providerServicePortDefault
}

// Port 返回当前端口。
func (p *ProviderService) Port() uint16 {
	if s := os.Getenv("OPCODE2API_PROVIDER_PORT"); s != "" {
		if n := parsePositiveInt(s); n > 0 && n < 65536 {
			return uint16(n)
		}
	}
	if n := providerPortFromFile(p.configPath); n > 0 {
		return n
	}
	return p.m.managerProviderPort()
}

// providerPortFromFile 从根配置读取 provider_port（0 = 未设置/读取失败）。
func providerPortFromFile(path string) uint16 {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	var cfg struct {
		ProviderPort int `json:"provider_port"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return 0
	}
	if cfg.ProviderPort <= 0 || cfg.ProviderPort >= 65536 {
		return 0
	}
	return uint16(cfg.ProviderPort)
}

// Address 返回 OpenAI 兼容入口地址。
func (p *ProviderService) Address() string {
	return fmt.Sprintf("http://127.0.0.1:%d/v1", p.Port())
}

// APIKey 返回供应商服务鉴权密钥（与统一网关同源，便于客户端复用）。
func (p *ProviderService) APIKey() string {
	return effectiveGatewayKey(p.m.loadConfig())
}

// providerServiceDir 运行目录。
func (p *ProviderService) dir() string {
	return filepath.Join(p.m.Paths().RuntimeDir, providerServiceDirBase)
}

// Status 查询状态（模型目录走 10s 节流 + 后台刷新，不阻塞 UI）。
func (p *ProviderService) Status() ProviderServiceStatus {
	p.mu.Lock()
	pid := p.pid
	lastErr := p.lastErr
	stopped := p.stopped
	models := append([]string(nil), p.models...)
	modelsUpdatedAt := p.modelsUpdatedAt
	modelsErr := p.modelsErr
	p.mu.Unlock()
	running := pid > 0 && pidAlive(pid)
	msg := "运行中（自定义源 + 插件供应商）"
	if !running {
		msg = "未运行"
		if stopped {
			msg = "已停止（手动）"
		} else if lastErr != "" {
			msg = lastErr
		}
	} else {
		p.refreshModels()
	}
	if models == nil {
		models = []string{}
	}
	return ProviderServiceStatus{
		Running:         running,
		Address:         p.Address(),
		Port:            p.Port(),
		APIKey:          p.APIKey(),
		Message:         msg,
		Models:          models,
		ModelsUpdatedAt: modelsUpdatedAt,
		ModelsError:     modelsErr,
	}
}

// refreshModels 后台刷新供应商服务模型目录（10s 节流；60s 未更新才再拉）。
func (p *ProviderService) refreshModels() {
	p.mu.Lock()
	if p.modelsLoading {
		p.mu.Unlock()
		return
	}
	now := time.Now().Unix()
	if now-p.modelsLastFetch < 10 && p.modelsUpdatedAt != 0 && now-p.modelsUpdatedAt < 60 {
		p.mu.Unlock()
		return
	}
	p.modelsLoading = true
	p.modelsLastFetch = now
	p.mu.Unlock()
	port := p.Port()
	key := p.APIKey()

	go func() {
		models, err := fetchGatewayModels(port, key)
		p.mu.Lock()
		p.modelsLoading = false
		if err != nil {
			p.modelsErr = err.Error()
		} else {
			p.models = models
			p.modelsUpdatedAt = time.Now().Unix()
			p.modelsErr = ""
		}
		p.mu.Unlock()
	}()
}

// ForceRefreshModels 同步刷新模型目录（卡片「刷新模型」按钮用），返回最新状态。
func (p *ProviderService) ForceRefreshModels() ProviderServiceStatus {
	port := p.Port()
	key := p.APIKey()
	models, err := fetchGatewayModels(port, key)
	p.mu.Lock()
	if err != nil {
		p.modelsErr = err.Error()
	} else {
		p.models = models
		p.modelsUpdatedAt = time.Now().Unix()
		p.modelsErr = ""
	}
	p.modelsLastFetch = time.Now().Unix()
	p.mu.Unlock()
	return p.Status()
}

// Start 拉起供应商服务子进程（幂等）。
func (p *ProviderService) Start(runner Runner) error {
	if runner == nil {
		runner = &realRunner{}
	}
	p.mu.Lock()
	if p.pid > 0 && pidAlive(p.pid) {
		p.stopped = false
		p.mu.Unlock()
		return nil
	}
	p.stopped = false
	p.lastTry = time.Now()
	port := p.Port()
	key := p.APIKey()
	dir := p.dir()
	p.mu.Unlock()

	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		return err
	}
	pid, err := runner.Start(ExecSpec{
		Bin: p.m.binPath("opencode2api"),
		Args: []string{
			"-port", itoa(port),
			"-config", p.configPath,
			"-password", key,
			"-provider-service",
			"-listen", "127.0.0.1",
			"-log-level", "info",
		},
		Env:      append([]string{"OPCODE2API_ROLE=" + "provider-service"}, traceEnvKV()...),
		Dir:      dir,
		LogOut:   filepath.Join(dir, "logs", "opencode2api.out.log"),
		LogErr:   filepath.Join(dir, "logs", "opencode2api.err.log"),
		NoWindow: true,
	})
	if err != nil {
		p.setErr(fmt.Sprintf("启动供应商服务失败: %v", err))
		return err
	}
	p.mu.Lock()
	p.pid = pid
	p.lastErr = ""
	p.spawnPort = port
	p.spawnKey = key
	p.modelsLastFetch = 0 // 启动后首次 Status 立即拉模型
	p.mu.Unlock()
	slog.Info("provider service started", "port", port, "pid", pid)
	return nil
}

// Stop 停止供应商服务子进程：先请求优雅退出（让插件子进程随 Close 回收），
// 3s 内未退出再强杀。
func (p *ProviderService) Stop(runner Runner) error {
	if runner == nil {
		runner = &realRunner{}
	}
	p.mu.Lock()
	pid := p.pid
	spawnPort := p.spawnPort
	spawnKey := p.spawnKey
	p.pid = 0
	p.stopped = true
	p.models = nil
	p.modelsErr = ""
	p.modelsUpdatedAt = 0
	p.modelsLastFetch = 0
	p.mu.Unlock()
	if pid <= 0 {
		return nil
	}
	// 优雅退出：插件子进程由服务进程的 pluginMgr.Close 统一 kill，避免残留占端口。
	// 端口/密钥用拉起时的快照（配置刚热更新时，当前值可能已指向新端口）。
	port := spawnPort
	if port == 0 {
		port = p.Port()
	}
	key := spawnKey
	if key == "" {
		key = p.APIKey()
	}
	_, _, _ = httpRequest(http.MethodPost, "/api/admin/shutdown", port, 2*time.Second, key, nil, 2*time.Second)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && pidAlive(pid) {
		time.Sleep(100 * time.Millisecond)
	}
	if pidAlive(pid) {
		_ = runner.Kill(pid)
	}
	slog.Info("provider service stopped", "pid", pid)
	return nil
}

// Close 停止看护协程并回收子进程。
func (p *ProviderService) Close() {
	p.cancel()
	_ = p.Stop(nil)
}

// Watchdog 周期看护：子进程退出后自动重启（10s 一轮，失败至少间隔 15s 再试）。
func (p *ProviderService) Watchdog(runner Runner) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-p.ctx.Done():
			return
		case <-t.C:
			p.mu.Lock()
			alive := p.pid > 0 && pidAlive(p.pid)
			lastTry := p.lastTry
			stopped := p.stopped
			spawnPort := p.spawnPort
			spawnKey := p.spawnKey
			p.mu.Unlock()
			if stopped {
				continue
			}
			if alive {
				// 端口/密钥热更新：子进程启动参数已过时，重启使其生效。
				if curPort, curKey := p.Port(), p.APIKey(); spawnPort != curPort || spawnKey != curKey {
					slog.Info("provider service config changed, restarting",
						"old_port", spawnPort, "new_port", curPort, "key_changed", spawnKey != curKey)
					_ = p.Stop(runner)
					if err := p.Start(runner); err != nil {
						slog.Warn("provider service restart after config change failed", "error", err)
					}
				}
				continue
			}
			if time.Since(lastTry) < 15*time.Second {
				continue
			}
			if err := p.Start(runner); err != nil {
				slog.Warn("provider service watchdog restart failed", "error", err)
			}
		}
	}
}

func (p *ProviderService) setErr(msg string) {
	p.mu.Lock()
	p.lastErr = msg
	p.mu.Unlock()
}

// ProxyHandler 把管理进程收到的插件管理 API 反代到供应商服务。
// 管理进程不再自己托管插件子进程，插件列表/启停/配置/模型刷新统一由 50080 处理。
func (p *ProviderService) ProxyHandler() http.Handler {
	target := &url.URL{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%d", p.Port())}
	proxy := httputil.NewSingleHostReverseProxy(target)
	director := proxy.Director
	key := p.APIKey()
	proxy.Director = func(r *http.Request) {
		director(r)
		r.Host = target.Host
		r.Header.Set("Authorization", "Bearer "+key)
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(fmt.Sprintf(`{"error":"provider service unavailable: %s"}`, err.Error())))
	}
	return proxy
}
