//go:build !windows

// 非 Windows 平台子进程帮助：无窗口概念 + SIGKILL + signal 0 探活。
package pluginprovider

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/6Kmfi6HP/opencode2api/core/manager/internal/proctree"
)

// orphanTreeMaxDepth 非 Windows 自动回收的后代展开深度上限：2 = 插件 → node/msedge
// → 它们再拉起的进程。与 core/manager.maxTreeDepth 同值；共享判定逻辑在
// internal/proctree（共享纯逻辑下沉 internal，避免依赖 manager 的未导出实现）。
const orphanTreeMaxDepth = 2

// listAllProcessesTimeout 全量 ps 枚举超时（P2-4）：ps 本身挂起/系统异常时无超时
// 的 exec 会让 reapOrphans 卡住整个扫描协程。
const listAllProcessesTimeout = 10 * time.Second

// applyNoWindow 非 Windows：无窗口概念。
func applyNoWindow(_ *exec.Cmd) {}

// killProcess 非 Windows：SIGKILL。
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

// pidAliveConservative 保守探活（P1-1）：查询失败 = 存活（true）。
//
// 语义与 core/manager 的 pidAliveSafe 一致但本包独立实现（不能 import manager
// 的未导出函数）：nil/EPERM = 存在；ESRCH/os.ErrProcessDone = 确凿死亡；
// 其它错误 = 未知 = 存活。必须用 errors.Is：Go 对已退出 PID 的 signal 0 在
// Linux 上返回 os.ErrProcessDone，且可能被 *os.SyscallError 包装。
//
// 注意 POSIX reparent：插件父进程死后子进程立即被 init/subreaper 收养，PPID 不再
// 指向已死宿主，因此本函数在非 Windows 上无法识别「reparent 后的真孤儿」——
// M3 路线 A 的自动回收正向场景以 Windows 为主（见 M3 计划 §3.4 / C8）。
func pidAliveConservative(pid int) bool {
	if pid <= 0 {
		return true
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return true
	}
	err = p.Signal(syscall.Signal(0))
	switch {
	case err == nil:
		return true
	case errors.Is(err, syscall.EPERM):
		return true
	case errors.Is(err, syscall.ESRCH), errors.Is(err, os.ErrProcessDone):
		return false
	default:
		return true
	}
}

// killOrphanTree 非 Windows：SIGKILL 不树杀。先全量枚举进程、按 PPID 找出孤儿
// 插件的后代快照（先深后浅），再杀插件自身——顺序不可颠倒：插件死后其子进程被
// init 收养，PPID 关联即失效，再也找不到。只按 PPID 关联，不做进程名匹配
// （现场 ARTEX 的 mihomo 等同名进程 PPID 不指向本插件，天然排除）。
//
// 返回错误表示回收不完整（P1-2）：枚举失败时仍尝试杀根，但后代 PID 未知、无法
// 补杀；后代或根单个 kill 失败也聚合返回，调用方不得把失败当成功。
func killOrphanTree(pid int) error {
	if pid <= 0 {
		return nil
	}
	procs, err := listAllProcesses()
	if err != nil {
		rootErr := killProcess(pid)
		return fmt.Errorf("后代枚举失败，仅尝试杀根进程 %d（后代可能残留）: %w",
			pid, errors.Join(err, rootErr))
	}
	var errs []error
	for _, d := range proctree.FindDescendants(procs, []int{pid}, orphanTreeMaxDepth) {
		if err := killProcess(d.PID); err != nil {
			errs = append(errs, fmt.Errorf("kill descendant %d: %w", d.PID, err))
		}
	}
	if err := killProcess(pid); err != nil {
		errs = append(errs, fmt.Errorf("kill root %d: %w", pid, err))
	}
	return errors.Join(errs...)
}

// listAllProcesses 非 Windows 全量进程枚举（pid/ppid/cmd）。reapOrphans 的后代
// 回收需要全量表：插件子进程（node/msedge/mihomo）命令行通常不含 --provider-serve，
// 会被 listProviderProcesses 的过滤条件排除，从来进不了那份结果。
func listAllProcesses() ([]procInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), listAllProcessesTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-eo", "pid=,ppid=,args=").Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("进程枚举超时（%s）: %w", listAllProcessesTimeout, ctx.Err())
		}
		return nil, err
	}
	var procs []procInfo
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		pid, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}
		ppid, _ := strconv.Atoi(f[1])
		procs = append(procs, procInfo{PID: pid, PPID: ppid, Cmd: strings.Join(f[2:], " ")})
	}
	return procs, nil
}

// listProviderProcesses 非 Windows：ps 枚举 opencode2api 宿主与 --provider-serve
// 插件子进程（pid/ppid/cmd），供 Start 时回收宿主已退出的残留（孤儿）。
func listProviderProcesses() ([]procInfo, error) {
	out, err := exec.Command("ps", "-eo", "pid=,ppid=,args=").Output()
	if err != nil {
		return nil, err
	}
	var procs []procInfo
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		pid, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}
		ppid, _ := strconv.Atoi(f[1])
		cmdline := strings.Join(f[2:], " ")
		if !strings.Contains(cmdline, "opencode2api") && !strings.Contains(cmdline, "--provider-serve") {
			continue
		}
		procs = append(procs, procInfo{PID: pid, PPID: ppid, Cmd: cmdline})
	}
	return procs, nil
}
