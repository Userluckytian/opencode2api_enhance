// 管理器核心：供应商服务生命周期 + 数据目录/运行依赖装配。
//
// v2.0.0 起本包不再管理「实例池 / 统一网关 / 节点池 / 订阅 / 扫描」——
// 那些能力随 OpenCode 免费层自研路径一并移除。剩余职责：
//   - 配置文件读写与校验（config.go）
//   - 供应商服务（50080）的启停与看门狗（providerservice.go）
//   - 免费层引擎（40080）的启停与看门狗（freetier_engine.go）
//   - 调用日志 / 统计 / 日志聚合（calllog*.go / stats*.go / logs.go）
//   - 平台相关工具（autostart / open_url / port / process / netstat / caps）
package manager

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
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
