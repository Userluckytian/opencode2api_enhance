//go:build windows

package manager

import (
	"os/exec"
	"strconv"
	"strings"
)

// processImageName 返回进程的映像名（basename，含 .exe）。
// 不可用/进程不存在时返回空串——调用方把空串视为「不在允许列表内」。
func processImageName(pid int) string {
	if pid <= 0 {
		return ""
	}
	out, err := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/FO", "CSV", "/NH").Output()
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(out))
	if line == "" || strings.HasPrefix(strings.ToLower(line), "info:") {
		return ""
	}
	// CSV 首字段形如 "opencode2api-win.exe"
	if strings.HasPrefix(line, "\"") {
		if j := strings.Index(line[1:], "\""); j > 0 {
			return line[1 : 1+j]
		}
	}
	if i := strings.Index(line, ","); i > 0 {
		return strings.Trim(line[:i], "\"")
	}
	return ""
}
