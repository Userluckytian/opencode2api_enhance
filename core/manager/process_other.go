//go:build !windows

package manager

import (
	"context"
	"os"
	"os/exec"
	"syscall"
)

// applyNoWindow 非 Windows：无窗口概念。
func applyNoWindow(_ *exec.Cmd, _ bool) {}

// killProcess 非 Windows：SIGKILL。
// pid <= 0 直接返回：Unix `kill(0, SIGKILL)` 会杀调用者所在进程组（P2-1）。
func killProcess(pid int) error {
	if pid <= 0 {
		return nil
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Signal(syscall.SIGKILL)
}

// killProcessTree 非 Windows 树杀（仅 M1 孤儿清理端点使用；M3 C7）：SIGKILL 没有
// 树语义，先全量枚举进程按 PPID 找出后代快照（深度 ≤ maxTreeDepth），先深后浅
// 回收，最后杀根——顺序不可颠倒，插件死后其 PPID 关联即丢失。
//
// 枚举失败或后代回收失败返回 treeKillError（P1-2）：根可能已被杀，但后代未回收
// 必须如实上报，不能退化为「单杀根 + 静默成功」。端点侧优先用 killProcessTreeWith
// 复用已拿到的快照，本函数供无快照的调用方使用。
//
// 已知限制（M3 C8，经批准显式接受）：后代判定只按 PPID、不校验创建时间，PID 复用
// 窗口 = 枚举→杀之间（极小）；完整消除需路线 B（Job Object/带创建时间的快照）。
func killProcessTree(pid int) error {
	if pid <= 0 {
		return nil
	}
	return killProcessTreeSnapshot(pid, func() ([]procLine, error) {
		return listAllProcesses(context.Background())
	}, killProcess)
}

// killProcessTreeWith 用调用方已枚举的全量快照做树杀（P1-2）：OrphanKillHandler
// 已经为扫描拿过一次 procs，清理时直接复用，避免每个 PID 再跑一次全量 ps。
// 与 killProcessTree 同语义（先深后浅、根最后、部分失败返回 treeKillError）。
func killProcessTreeWith(procs []procLine, pid int) error {
	if pid <= 0 {
		return nil
	}
	rootErr, descErrs := killTreeWith(procs, pid, killProcess, maxTreeDepth)
	return newTreeKillError(pid, rootErr, descErrs, nil)
}

// pidAlive 非 Windows：signal 0 探活。
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}
