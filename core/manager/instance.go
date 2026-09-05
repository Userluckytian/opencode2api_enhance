// 实例生命周期（Rust instance.rs start_instance_inner 移植）。
// 短锁模式：锁内快照 + 置 Starting；锁外 spawn/等待；锁内写回 Running/Error。
package manager

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// singboxWait sing-box 监听端口就绪等待。
const singboxWait = 10 * time.Second

// openCodeWait opencode2api 监听端口就绪等待。
const openCodeWait = 15 * time.Second

// fileExists 判断文件是否存在。
func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// StartInstance 启动实例（短锁模式）。
// 对齐 Rust start_instance_inner：已 Running 时单独提示"已在运行"（batch 走 markStartingLocked 报"正在忙"）。
func (m *Manager) StartInstance(runner Runner, name string) error {
	if runner == nil {
		runner = &realRunner{}
	}
	// 阶段1：锁内快照 + 置 Starting
	m.mu.Lock()
	for _, e := range m.load() {
		if e.Name == name && e.Status.State == "Running" {
			m.mu.Unlock()
			return fmt.Errorf("实例 '%s' 已在运行", name)
		}
	}
	inst, err := m.markStartingLocked(name)
	m.mu.Unlock()
	if err != nil {
		return err
	}
	// 阶段2：锁外执行（可长时间阻塞）
	runErr := m.startInstanceLockFree(runner, &inst)
	// 阶段3：锁内写回结果
	m.mu.Lock()
	defer m.mu.Unlock()
	list := m.load()
	for i := range list {
		if list[i].Name != name {
			continue
		}
		list[i].PID, list[i].SingboxPID = inst.PID, inst.SingboxPID
		if runErr != nil {
			// 失败路径进程已 kill / 未启动——不持久化死 PID：Stop 对 Error 态实例
			// 按快照 PID 直接 taskkill，PID 被系统复用后会误杀无关进程。
			// 万一 Kill 失败留下活进程，交由孤儿清理（KillOrphans）兜底。
			list[i].PID, list[i].SingboxPID = nil, nil
			list[i].Status = StatusError(runErr.Error())
		} else {
			list[i].Status = StatusRunning()
		}
		_ = m.save(list)
		return runErr
	}
	return runErr
}

// markStartingLocked 标记并校验可启动（锁内）。
func (m *Manager) markStartingLocked(name string) (Instance, error) {
	list := m.load()
	for i := range list {
		if list[i].Name != name {
			continue
		}
		switch list[i].Status.State {
		case "Running", "Starting", "Stopping":
			return Instance{}, fmt.Errorf("实例 '%s' 正在忙", name)
		}
		list[i].Status = StatusStarting()
		_ = m.save(list)
		return list[i], nil
	}
	return Instance{}, errors.New("实例不存在")
}

// startInstanceLockFree 锁外执行：sing-box → 等口 → opencode2api → 等口。
func (m *Manager) startInstanceLockFree(runner Runner, inst *Instance) error {
	sf := m.currentSeams()
	if sf.ResolveNode == nil || sf.BuildSingbox == nil || sf.BuildOpenCfg == nil {
		return errors.New("未装配实例接缝（clash/singbox/opencode 生成器）")
	}
	node, ok := sf.ResolveNode(inst.Node)
	if !ok {
		return fmt.Errorf("未找到节点 '%s'", inst.Node)
	}
	dir := m.paths.RuntimeDirOf(inst.Name)
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		return fmt.Errorf("创建实例目录失败: %w", err)
	}
	sbCfg, err := sf.BuildSingbox(node, inst.SingboxPort)
	if err != nil {
		return fmt.Errorf("生成 sing-box 配置失败: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "singbox.json"), sbCfg, 0o644); err != nil {
		return fmt.Errorf("写入 sing-box 配置失败: %w", err)
	}
	sbPID, err := runner.Start(ExecSpec{
		Bin:      m.binPath("sing-box"),
		Args:     []string{"run", "-c", filepath.Join(dir, "singbox.json")},
		Dir:      dir,
		LogOut:   filepath.Join(dir, "logs", "singbox.out.log"),
		LogErr:   filepath.Join(dir, "logs", "singbox.err.log"),
		NoWindow: true,
	})
	if err != nil {
		// 对齐 Rust：可执行文件缺失时给出专门文案
		if sbBin := m.binPath("sing-box"); !fileExists(sbBin) {
			return fmt.Errorf("未找到 sing-box 可执行文件: %s", sbBin)
		}
		return fmt.Errorf("启动 sing-box 失败: %w", err)
	}
	inst.SingboxPID = &sbPID
	if err := waitForPort(inst.SingboxPort, singboxWait); err != nil {
		_ = runner.Kill(sbPID)
		return fmt.Errorf("sing-box 在 10s 内未能监听 127.0.0.1:%d", inst.SingboxPort)
	}
	ocCfg, err := sf.BuildOpenCfg(inst.SingboxPort)
	if err != nil {
		_ = runner.Kill(sbPID)
		return fmt.Errorf("生成 opencode2api 配置失败: %w", err)
	}
	if err := WriteFileAtomic(filepath.Join(dir, "opencode2api.json"), ocCfg, 0o644); err != nil {
		_ = runner.Kill(sbPID)
		return fmt.Errorf("写入 opencode2api 配置失败: %w", err)
	}
	// -call-log：独享/池成员进程把调用日志写到 cwd/call_log.jsonl（日志页 S4 聚合读取）
	ocPID, err := runner.Start(ExecSpec{
		Bin:      m.binPath("opencode2api"),
		Args:     []string{"-port", itoa(inst.Port), "-config", filepath.Join(dir, "opencode2api.json"), "-password", inst.Password, "-call-log"},
		Env:      append([]string{"OPCODE2API_ROLE=instance"}, traceEnvKV()...), // 阶段 2：自报角色（日志 role 字段 + /api/logs 过滤）；阶段 3：透传 OPENCODE2API_TRACE
		Dir:      dir, // Go core 把 stats.json 写在 cwd
		LogOut:   filepath.Join(dir, "logs", "opencode2api.out.log"),
		LogErr:   filepath.Join(dir, "logs", "opencode2api.err.log"),
		NoWindow: true,
	})
	if err != nil {
		_ = runner.Kill(sbPID)
		// 对齐 Rust：可执行文件缺失时给出专门文案
		if ocBin := m.binPath("opencode2api"); !fileExists(ocBin) {
			return fmt.Errorf("未找到 opencode2api 可执行文件: %s", ocBin)
		}
		return fmt.Errorf("启动 opencode2api 失败: %w", err)
	}
	inst.PID = &ocPID
	if err := waitForPort(inst.Port, openCodeWait); err != nil {
		_ = runner.Kill(ocPID)
		_ = runner.Kill(sbPID)
		return fmt.Errorf("opencode2api 在 15s 内未能监听 0.0.0.0:%d", inst.Port)
	}
	return nil
}

// StopInstance 停止实例（短锁三段式，对齐 StartInstance）：
//   - 阶段1（锁内）：定位 + 校验状态 + 快照 pid + 置 Stopping 并落盘（前端可见正在停止）；
//   - 阶段2（锁外）：kill opencode + kill sing-box（Kill 期间 m.mu 空闲，批量停止/巡检可并行）；
//   - 阶段3（锁内）：重新定位写回 Stopped 并清 pid。
func (m *Manager) StopInstance(runner Runner, name string) error {
	if runner == nil {
		runner = &realRunner{}
	}
	// 阶段1：锁内快照 + 置 Stopping
	m.mu.Lock()
	ocPID, sbPID, err := m.beginStopLocked(name)
	m.mu.Unlock()
	if err != nil {
		return err
	}
	// 阶段2：锁外 Kill（可长时间阻塞；此窗口不持 m.mu）
	if ocPID > 0 {
		_ = runner.Kill(ocPID)
	}
	if sbPID > 0 {
		_ = runner.Kill(sbPID)
	}
	// 阶段3：锁内写回 Stopped
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.finishStopLocked(name)
}

// beginStopLocked 锁内：定位实例、拒绝 Starting/Stopping、快照 pid 并置 Stopping。
func (m *Manager) beginStopLocked(name string) (int, int, error) {
	list := m.load()
	for i := range list {
		if list[i].Name != name {
			continue
		}
		switch list[i].Status.State {
		case "Starting", "Stopping":
			return 0, 0, fmt.Errorf("实例 '%s' 正在忙", name)
		}
		// 置 Stopping 并立即落盘：阶段2 锁外 Kill 期间前端/巡检可见中间态
		list[i].Status = StatusStopping()
		_ = m.save(list)
		return pidVal(list[i].PID), pidVal(list[i].SingboxPID), nil
	}
	return 0, 0, errors.New("实例不存在")
}

// finishStopLocked 锁内写回 Stopped 并清 pid；阶段2 期间实例可能被删除，
// 找不到即视为已完成（删除路径已接管记录）。
func (m *Manager) finishStopLocked(name string) error {
	list := m.load()
	for i := range list {
		if list[i].Name != name {
			continue
		}
		list[i].PID, list[i].SingboxPID = nil, nil
		list[i].Status = StatusStopped()
		return m.save(list)
	}
	return nil
}

// RemoveInstanceAlive 删除实例（短锁三段式）：锁内置 Stopping + 快照 pid → 锁外 Kill
// → 锁内从列表删除并落盘。
func (m *Manager) RemoveInstanceAlive(runner Runner, name string) error {
	if runner == nil {
		runner = &realRunner{}
	}
	// 阶段1：锁内快照 + 置 Stopping
	m.mu.Lock()
	ocPID, sbPID, err := m.beginStopLocked(name)
	m.mu.Unlock()
	if err != nil {
		return err
	}
	// 阶段2：锁外 Kill
	if ocPID > 0 {
		_ = runner.Kill(ocPID)
	}
	if sbPID > 0 {
		_ = runner.Kill(sbPID)
	}
	// 阶段3：锁内从列表删除（阶段2 期间可能已被并发删除，找不到即目标已达成）
	m.mu.Lock()
	defer m.mu.Unlock()
	list := m.load()
	for i := range list {
		if list[i].Name != name {
			continue
		}
		list = append(list[:i], list[i+1:]...)
		return m.save(list)
	}
	return nil
}

// ReconcileStates 校正状态：Running/Starting 但 pid 已不存在 → Stopped。
func (m *Manager) ReconcileStates(runner Runner) []Instance {
	_ = runner
	m.mu.Lock()
	defer m.mu.Unlock()
	list := m.load()
	changed := false
	for i := range list {
		st := list[i].Status.State
		if st != "Running" && st != "Starting" {
			continue
		}
		if pid := pidVal(list[i].PID); pid > 0 && !pidAlive(pid) {
			list[i].Status = StatusStopped()
			list[i].PID, list[i].SingboxPID = nil, nil
			changed = true
		}
	}
	if changed {
		_ = m.save(list)
	}
	return list
}

// RefreshStates 返回指定实例的最新状态（输入顺序；先 reconcile）。
func (m *Manager) RefreshStates(runner Runner, names []string) []Instance {
	_ = m.ReconcileStates(runner)
	byName := map[string]Instance{}
	for _, inst := range m.ListInstances() {
		byName[inst.Name] = inst
	}
	out := make([]Instance, 0, len(names))
	for _, n := range names {
		if inst, ok := byName[n]; ok {
			out = append(out, inst)
		}
	}
	return out
}

// pidVal 解指针 pid（nil → 0）。
func pidVal(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}
