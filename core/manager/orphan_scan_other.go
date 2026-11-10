//go:build !windows

// 非 Windows 全量进程枚举（孤儿扫描用）：先取系统全量进程，再由
// classifyProcesses 按判据分类。
package manager

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// listAllProcessesTimeout 枚举超时（与 windows 实现同值，理由见 orphan_scan_windows.go）。
const listAllProcessesTimeout = 10 * time.Second

// listAllProcesses 枚举系统全部进程（pid/ppid/args）。args 可含空格：
// 取行首两个数字字段（pid/ppid），剩余整段为命令行。
//
// ctx 由 handler 传 r.Context()：客户端断开时一并取消。
func listAllProcesses(ctx context.Context) ([]procLine, error) {
	ctx, cancel := context.WithTimeout(ctx, listAllProcessesTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-eo", "pid=,ppid=,args=").Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	var procs []procLine
	for _, raw := range strings.Split(string(out), "\n") {
		rest := strings.TrimSpace(raw)
		if rest == "" {
			continue
		}
		var pid, ppid int
		ok := true
		for _, target := range []*int{&pid, &ppid} {
			i := strings.IndexByte(rest, ' ')
			if i < 0 {
				ok = false // 只有 pid/ppid 没有命令行的行，无分类价值
				break
			}
			v, err := strconv.Atoi(rest[:i])
			if err != nil {
				ok = false
				break
			}
			*target = v
			rest = strings.TrimLeft(rest[i+1:], " ")
		}
		if !ok {
			continue
		}
		procs = append(procs, procLine{PID: pid, PPID: ppid, Cmd: rest})
	}
	return procs, nil
}

// pidAliveSafe M2 P2-2：孤儿扫描用的保守探活（与 windows 版同语义：查询失败
// 视为「存活」，不列不杀；判活复用 parentCheck——signal 0 的 EPERM 算存活、
// ESRCH/os.ErrProcessDone 算确凿死亡，其它异常算未知；errors.Is 判定，见
// parentwatch_other.go 注释）。不改 pidAlive（process_other.go）既有语义。
func pidAliveSafe(pid int) bool {
	if pid <= 0 {
		return false
	}
	alive, _, err := parentCheck(pid)
	return pidAliveDecision(alive, err)
}
