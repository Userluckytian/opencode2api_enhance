//go:build !windows

// 非 Windows 平台子进程帮助：无窗口概念 + SIGKILL + signal 0 探活。
package pluginprovider

import (
	"os"
	"os/exec"
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
