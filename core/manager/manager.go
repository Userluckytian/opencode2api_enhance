// 管理器核心：实例注册表（instances.json 持久化，Rust instance.rs 移植）。
// P4-1 先落地注册表 + 状态类型；生命周期（spawn/stop）在 P4-2。
package manager

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
)

// InstanceStatus 实例状态（外部标签形态，与 Rust serde 一致）：
// "Stopped" | "Starting" | "Running" | "Stopping" | {"Error":["msg"]}。
type InstanceStatus struct {
	State string   `json:"-"` // Stopped/Starting/Running/Stopping
	Error []string `json:"-"` // 非空表示 Error 态
}

// MarshalJSON 编码为外部标签形态。
func (s InstanceStatus) MarshalJSON() ([]byte, error) {
	if s.State == "" && len(s.Error) == 0 {
		return json.Marshal("Stopped")
	}
	if len(s.Error) > 0 {
		return json.Marshal(map[string][]string{"Error": s.Error})
	}
	return json.Marshal(s.State)
}

// UnmarshalJSON 兼容 "Stopped" 与 {"Error":[...]} 两种形态。
func (s *InstanceStatus) UnmarshalJSON(b []byte) error {
	var str string
	if err := json.Unmarshal(b, &str); err == nil {
		s.State = str
		s.Error = nil
		return nil
	}
	var m map[string][]string
	if err := json.Unmarshal(b, &m); err == nil {
		if errs, ok := m["Error"]; ok {
			s.State = "Error"
			s.Error = errs
			return nil
		}
	}
	return errors.New("invalid instance status")
}

// Status helpers。
func StatusStopped() InstanceStatus  { return InstanceStatus{State: "Stopped"} }
func StatusStarting() InstanceStatus { return InstanceStatus{State: "Starting"} }
func StatusRunning() InstanceStatus  { return InstanceStatus{State: "Running"} }
func StatusStopping() InstanceStatus { return InstanceStatus{State: "Stopping"} }
func StatusError(msg string) InstanceStatus {
	return InstanceStatus{State: "Error", Error: []string{msg}}
}

// Instance 单实例契约（JSON 字段与前端/Rust 一致）。
type Instance struct {
	Name        string         `json:"name"`
	Port        uint16         `json:"port"`
	Node        string         `json:"node"`
	Password    string         `json:"password"`
	IP          string         `json:"ip"`
	SingboxPort uint16         `json:"singbox_port"`
	PID         *int           `json:"pid,omitempty"`
	SingboxPID  *int           `json:"singbox_pid,omitempty"`
	JoinGateway bool           `json:"join_gateway"`
	Status      InstanceStatus `json:"status"`
}

// Manager 持有管理器状态（数据目录 + 运行目录 + 注册表）。
type Manager struct {
	paths Paths
	mu    sync.Mutex

	seamsMu sync.Mutex
	seamsFn *SeamFuncs // 可插拔接缝（P4-3 填充；nil = 未装配）

	// 运行依赖（HTTP 操作层装配，P4-5）
	depMu  sync.Mutex
	runner Runner
	gw     *Gateway
	scan   *ScanController

	// poolProbeMu 串行化链路探活轮（后台循环与手动触发互斥执行），防止并发
	// load+save pool_quality.json；配合 savePoolQuality 临时文件+Rename 原子落盘。
	poolProbeMu sync.Mutex

	// healthMu 串行化健康巡检轮（后台循环与手动触发互斥执行），防止双轮并发
	// 对同一实例重复 stop/start；配合 saveHealthRecords 临时文件+Rename 原子落盘。
	healthMu sync.Mutex

	// localProfileMu 串行化本地代理文件源的读写（load→merge→save 全程持锁）。
	// 与 subscriptionCacheMu 同款理由：两个并发重载（用户点「重载全部」时又点单个源的
	// 「重载」、或两个面板各发一次）会基于同一份旧快照写回，后写者把先写者刚登记的源抹掉。
	localProfileMu sync.Mutex

	// subscriptionCacheMu 串行化订阅缓存读写（load→merge→save 全程持锁，防止后台
	// 自动导入与手动导入并发基于旧快照互相覆盖）；落盘用临时文件+Rename 原子替换。
	subscriptionCacheMu sync.Mutex

	// subscribeLoopOnce 订阅后台循环唯一启动保护：多源调度（RunAllSubscriptionLoop）
	// 与旧单条入口（StartSubscribeLoop）二选一，防迁移期双循环并发拉同一 URL。
	subscribeLoopOnce sync.Once
	// subscribeSched 已启动的订阅调度句柄（Once 执行后非 nil，stop 可停）。
	subscribeSched *subscriptionScheduler
	// subscribeFetchSem 订阅拉取并发门控（≤4），防多源到点同步尖峰。
	subscribeFetchSem chan struct{}
}

// New 创建管理器。
func New(dataDir string) *Manager {
	return &Manager{
		paths:            ResolvePaths(dataDir),
		subscribeFetchSem: make(chan struct{}, subscribeFetchConcurrency),
	}
}

// SetDeps 装配运行依赖（进程执行器 / 网关 / 扫描控制器；任一可为 nil）。
func (m *Manager) SetDeps(runner Runner, gw *Gateway, scan *ScanController) {
	m.depMu.Lock()
	defer m.depMu.Unlock()
	m.runner = runner
	m.gw = gw
	m.scan = scan
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

// Gateway 返回网关管理器（nil → 惰性创建）。
func (m *Manager) Gateway() *Gateway {
	m.depMu.Lock()
	defer m.depMu.Unlock()
	if m.gw == nil {
		m.gw = NewGateway(m, 0)
	}
	return m.gw
}

// Scanner 返回扫描控制器（nil → 惰性创建）。
func (m *Manager) Scanner() *ScanController {
	m.depMu.Lock()
	defer m.depMu.Unlock()
	if m.scan == nil {
		m.scan = NewScanController(m, m.runner)
	}
	return m.scan
}

// Paths 暴露路径集合（供探针/网关等子模块复用）。
func (m *Manager) Paths() Paths { return m.paths }

// ListInstances 返回实例列表（按名称排序）。
func (m *Manager) ListInstances() []Instance {
	m.mu.Lock()
	defer m.mu.Unlock()
	list := m.load()
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return list
}

// FindInstance 按名称查实例。
func (m *Manager) FindInstance(name string) (Instance, bool) {
	for _, inst := range m.ListInstances() {
		if inst.Name == name {
			return inst, true
		}
	}
	return Instance{}, false
}

// AddInstance 添加实例（重名/端口冲突拒绝）。
func (m *Manager) AddInstance(inst Instance) error {
	if inst.Name == "" {
		return errors.New("实例名不能为空")
	}
	if inst.Port < 1024 {
		return errors.New("端口需 >= 1024")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	list := m.load()
	for _, e := range list {
		if e.Name == inst.Name {
			return errors.New("实例 '" + inst.Name + "' 已存在")
		}
		if e.Port == inst.Port {
			return errors.New("端口 " + itoa(inst.Port) + " 已被其他实例占用")
		}
	}
	inst.Status = StatusStopped()
	list = append(list, inst)
	return m.save(list)
}

// RemoveInstance 删除实例（生命周期归零；运行中由 P4-2 先停止）。
func (m *Manager) RemoveInstance(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	list := m.load()
	for i, e := range list {
		if e.Name == name {
			list = append(list[:i], list[i+1:]...)
			return m.save(list)
		}
	}
	return errors.New("实例不存在")
}

// UpdateInstance 就地更新实例并持久化（供状态机流转）。
func (m *Manager) UpdateInstance(inst Instance) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	list := m.load()
	for i, e := range list {
		if e.Name == inst.Name {
			list[i] = inst
			return m.save(list)
		}
	}
	return errors.New("实例不存在")
}

// load 读取注册表（锁内调用）。
func (m *Manager) load() []Instance {
	data, err := readFile(m.paths.Instances)
	if err != nil {
		return []Instance{}
	}
	var list []Instance
	if err := json.Unmarshal(data, &list); err != nil {
		return []Instance{}
	}
	if list == nil {
		return []Instance{}
	}
	return list
}

// save 持久化注册表（锁内调用）。
func (m *Manager) save(list []Instance) error {
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(m.paths.Instances, data)
}

// itoa 小工具。
func itoa(v uint16) string {
	return strconv.FormatUint(uint64(v), 10)
}

// 小工具：文件读写（目录自动创建）。
func readFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

// writeFileAtomic 临时文件+Rename 原子落盘（目录自动创建）：读者要么看到旧文件、
// 要么看到完整新文件，崩溃/断电不会留下半截 JSON——instances.json 等注册表
// 半写入会被 load() 当作损坏丢弃，直接清空整个注册表。
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
