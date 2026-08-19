//go:build windows

// Windows 平台子进程帮助：CREATE_NO_WINDOW（防弹窗）+ taskkill 强杀 + tasklist 探活。
package pluginprovider

import (
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// applyNoWindow Windows：CREATE_NO_WINDOW（0x08000000）。
func applyNoWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000}
}

// killProcess 强杀（taskkill /PID <pid> /F，与 core/manager 同款）。
func killProcess(pid int) error {
	taskkill := exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/F")
	applyNoWindow(taskkill)
	return taskkill.Run()
}

// pidAlive tasklist CSV 输出按 PID 字段匹配（不依赖本地化提示文案——
// 英文系统匹配 "No tasks"，中文等其它语言是另一套提示，但数据行的 PID 恒为数字）。
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	out, err := exec.Command("tasklist", "/FO", "CSV", "/NH", "/FI", "PID eq "+strconv.Itoa(pid)).Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), `","`+strconv.Itoa(pid)+`",`)
}
