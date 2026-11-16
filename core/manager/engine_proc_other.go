//go:build !windows

package manager

import (
	"os/exec"
	"strconv"
	"strings"
)

// processImageName 返回进程的 command name（不含路径）。
// lsof/ps 不可用时返回空串——调用方按「不在允许列表内」处理。
func processImageName(pid int) string {
	if pid <= 0 {
		return ""
	}
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
