// 一键重启实例池（Rust commands.rs restart_pool 移植，T1 语义修正）。
// 仅重启「运行中」的池成员：并行 stop → 短暂等待 → 并行 start → 同步网关（自动拉起）。
// 独享实例与已停止的池成员一律不碰；启动失败如实返回（触发启动即算完成，不做多余补救）。
package manager

import "time"

// RestartPool 一键重启池：仅对 Running 状态的池成员做 stop→start。
// gw 为 nil 时跳过网关步骤。
func (m *Manager) RestartPool(runner Runner, gw *Gateway) RestartPoolResult {
	res := RestartPoolResult{Stopped: 0, Started: 0, FreedPorts: []uint16{}}
	if runner == nil {
		runner = &realRunner{}
	}

	// 1) 停统一网关（调度员先停，避免重启期间请求打到正在被重启的成员）
	if gw != nil {
		gw.stop(runner)
	}

	// 2) 仅收集「运行中」的池成员；独享实例、已停止的池成员不参与
	var targets []string
	for _, inst := range m.ListInstances() {
		if inst.JoinGateway && inst.Status.State == "Running" {
			targets = append(targets, inst.Name)
		}
	}
	res.Stopped = len(targets)

	// 3) 并行停止这些成员（含运行中判定，幂等）
	if len(targets) > 0 {
		_ = m.BatchStop(runner, targets)
	}
	time.Sleep(300 * time.Millisecond)

	// 4) 并行启动这些成员；失败如实记录（触发启动即算完成）
	if len(targets) > 0 {
		results := m.BatchStart(runner, targets)
		for _, err := range results {
			if err == nil {
				res.Started++
			}
		}
	}

	// 5) 同步网关（成员已重启，网关需重读配置）
	if gw != nil {
		if err := gw.sync(runner); err == nil {
			res.GatewayRunning = gw.isRunning(runner)
		} else {
			res.Error = err.Error()
		}
	}
	return res
}
