//go:build windows

package manager

import (
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// applyNoWindow Windows：CREATE_NO_WINDOW（0x08000000）。
func applyNoWindow(cmd *exec.Cmd, enabled bool) {
	if enabled {
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000}
	}
}

// killProcess Windows：taskkill /PID <pid> /F（与 Rust 一致）。
func killProcess(pid int) error {
	taskkill := exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/F")
	applyNoWindow(taskkill, true)
	return taskkill.Run()
}

// pidAlive Windows：tasklist 过滤该 PID，按 CSV 第二列精确比对。
// 不能匹配英文 "No tasks"——中文/其它语言的 Windows 输出为本地化文案
// （如「没有运行的任务匹配指定标准」），旧实现会把已退出进程误判为存活，
// 导致供应商服务/网关看护协程永远不重启。
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	cmd := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/FO", "CSV", "/NH")
	applyNoWindow(cmd, true)
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	line := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0]
	fields := strings.Split(line, ",")
	if len(fields) < 2 {
		return false
	}
	return strings.Trim(fields[1], "\" \r") == strconv.Itoa(pid)
}

func intToToken(n int) string {
	if n == 0 {
		return "0"
	}
	var b [16]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
