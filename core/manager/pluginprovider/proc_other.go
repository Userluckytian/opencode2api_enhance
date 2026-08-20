//go:build !windows

// 非 Windows 平台子进程帮助：无窗口概念 + SIGKILL + signal 0 探活。
package pluginprovider

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

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
