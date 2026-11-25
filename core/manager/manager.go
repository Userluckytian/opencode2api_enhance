// 管理器核心：供应商服务生命周期 + 数据目录/运行依赖装配。
//
// v2.0.0 起本包不再管理「实例池 / 统一网关 / 节点池 / 订阅 / 扫描」——
// 那些能力随 OpenCode 免费层自研路径一并移除。剩余职责：
//   - 配置文件读写与校验（config.go）
//   - 供应商服务（30080）的启停与看门狗（providerservice.go）
//   - 免费层引擎（40080）的启停与看门狗（freetier_engine.go）
//   - 调用日志 / 统计 / 日志聚合（calllog*.go / stats*.go / logs.go）
//   - 平台相关工具（autostart / open_url / port / process / netstat / caps）
package manager

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Manager 持有管理器状态（数据目录 + 运行目录 + 运行依赖）。
type Manager struct {
	paths Paths
	mu    sync.Mutex

	// 运行依赖（HTTP 操作层装配）
	depMu  sync.Mutex
	runner Runner
	// engine 免费层引擎（40080）托管状态。
	engine *freeTierEngine

	// retryAfterMu 保护 retryAfter：模型 ID → 额度重置时刻。
	//
	// 引擎的 availability.json **不记录**上游的 Retry-After（它只写
	// reason=http_429），所以「额度何时重置」这个信息只存在于响应头里。
	// 面板要显示它，唯一的办法是某次真的收到 429 时把响应头接住存下来。
	// 手动探测（diag_api.go）命中 429 时会写这里，面板随模型列表一起读。
	// 进程内存态：重启管理面即清空 —— 它只是「上次看到的提示」，不是事实源。
	retryAfterMu sync.RWMutex
	retryAfter   map[string]retryAfterHint

	// probeMu 保护 probeHints：手动探测的最近结果。
	// 引擎的 availability 只在它自己巡检时更新，面板点「探测」拿到 200 后
	// 状态列仍显示「受限」—— 这里记录手动结果，FreeTierModels 合并时以
	// 「更新的那个」为准；成功时顺手清掉旧的 429 倒计时。
	probeMu    sync.RWMutex
	probeHints map[string]probeHint

	// warpHistoryMu 保护 warpHistory：巡检采样的出口数曲线（最多 60 点）。
	warpHistoryMu sync.Mutex
	warpHistory   []WarpSample

	// warpFallbackMu 保护 warpFallback：WARP 池空时的自动降级判定状态。
	// 见 freetier_warp_fallback.go 顶部的成因说明。
	warpFallbackMu sync.Mutex
	warpFallback   warpFallbackState

	// nodeFarmMu / nodeFarm 节点池农场（订阅节点 → 本地端口）。
	// 见 nodepool.go / nodefarm.go：只服务 OpenCode 免费层注册，不影响出口代理。
	nodeFarmMu          sync.Mutex
	nodeFarm            *nodeFarm
	nodePoolRefreshOnce sync.Once

	// updateManifestMu / updateManifestCache 更新清单镜像加速的改写缓存。
	// 见 updatemirror.go：只服务桌面壳的 updater，按平台缓存 5 分钟。
	updateManifestMu    sync.Mutex
	updateManifestCache map[string]updateManifestEntry
}

// retryAfterHint 某模型 429 时上游给出的额度重置提示。
type retryAfterHint struct {
	// Seconds 上游 Retry-After 原值（秒）。
	Seconds int
	// ResetAt 换算后的重置时刻（UTC）。
	ResetAt time.Time
}

// NoteRetryAfter 记录某模型的 429 重置提示（seconds <= 0 视为未知，忽略）。
// WarpSample 一次出口池采样（每轮巡检记录一个点）。
type WarpSample struct {
	At      string `json:"at"`
	Size    int    `json:"size"`
	Active  int    `json:"active"`
	Cooling int    `json:"cooling"`
}

// appendWarpSample 记录一个采样点（环形，最多 60 个）。
func (m *Manager) appendWarpSample(s WarpSample) {
	m.warpHistoryMu.Lock()
	m.warpHistory = append(m.warpHistory, s)
	if len(m.warpHistory) > 60 {
		m.warpHistory = m.warpHistory[len(m.warpHistory)-60:]
	}
	m.warpHistoryMu.Unlock()
}

// WarpHistory 返回历史采样副本。
func (m *Manager) WarpHistory() []WarpSample {
	m.warpHistoryMu.Lock()
	defer m.warpHistoryMu.Unlock()
	return append([]WarpSample(nil), m.warpHistory...)
}

// probeHint 一次手动探测的结果。
type probeHint struct {
	OK     bool
	Status int
	At     time.Time
}

// NoteProbeResult 记录手动探测结果；成功时清掉该模型的 429 倒计时，
// 避免面板继续显示「额度重置 59 分钟后」。
func (m *Manager) NoteProbeResult(model string, ok bool, status int) {
	if model == "" {
		return
	}
	m.probeMu.Lock()
	if m.probeHints == nil {
		m.probeHints = map[string]probeHint{}
	}
	m.probeHints[model] = probeHint{OK: ok, Status: status, At: time.Now()}
	m.probeMu.Unlock()
	if ok {
		m.retryAfterMu.Lock()
		delete(m.retryAfter, model)
		m.retryAfterMu.Unlock()
	}
}

// ProbeHint 读最近一次手动探测结果。
func (m *Manager) ProbeHint(model string) (probeHint, bool) {
	m.probeMu.RLock()
	h, ok := m.probeHints[model]
	m.probeMu.RUnlock()
	return h, ok
}

func (m *Manager) NoteRetryAfter(model string, seconds int) {
	if model == "" || seconds <= 0 {
		return
	}
	m.retryAfterMu.Lock()
	defer m.retryAfterMu.Unlock()
	if m.retryAfter == nil {
		m.retryAfter = map[string]retryAfterHint{}
	}
	m.retryAfter[model] = retryAfterHint{
		Seconds: seconds,
		ResetAt: time.Now().Add(time.Duration(seconds) * time.Second).UTC(),
	}
}

// RetryAfterHint 读某模型的 429 重置提示；已过期或未记录返回零值。
func (m *Manager) RetryAfterHint(model string) (seconds int, resetAt string) {
	m.retryAfterMu.RLock()
	h, ok := m.retryAfter[model]
	m.retryAfterMu.RUnlock()
	if !ok {
		return 0, ""
	}
	remain := time.Until(h.ResetAt)
	if remain <= 0 {
		return 0, h.ResetAt.Format(time.RFC3339)
	}
	return int(remain.Seconds()), h.ResetAt.Format(time.RFC3339)
}

// New 创建管理器。
func New(dataDir string) *Manager {
	return &Manager{paths: ResolvePaths(dataDir)}
}

// SetRunner 装配进程执行器（nil → realRunner）。
func (m *Manager) SetRunner(runner Runner) {
	m.depMu.Lock()
	defer m.depMu.Unlock()
	m.runner = runner
}

// Run 返回进程执行器（nil → realRunner）。
func (m *Manager) Run() Runner {
	m.depMu.Lock()
	defer m.depMu.Unlock()
	if m.runner == nil {
		return &realRunner{}
	}
	return m.runner
}

// Paths 暴露路径集合（供各子模块复用）。
func (m *Manager) Paths() Paths { return m.paths }

// 小工具：文件读写（目录自动创建）。
func readFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

// writeFileAtomic 临时文件+Rename 原子落盘（目录自动创建）：读者要么看到旧文件、
// 要么看到完整新文件，崩溃/断电不会留下半截 JSON——配置文件半写入会被 load
// 当作损坏丢弃，直接清空整份配置。
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // 失败路径清理；成功 Rename 后目标已不存在，无副作用
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// readJSONFile 读取 JSON 到 out；文件缺失或损坏返回错误（调用方决定回退）。
func readJSONFile(path string, out any) error {
	data, err := readFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}
