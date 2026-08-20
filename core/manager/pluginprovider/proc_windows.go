//go:build windows

// Windows 平台子进程帮助：CREATE_NO_WINDOW（防弹窗）+ taskkill 强杀 + tasklist 探活。
package pluginprovider

import (
	"encoding/json"
	"fmt"
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

// listProviderProcesses 枚举系统插件子进程（--provider-serve）与 opencode2api 宿主
// 进程（pid/ppid/cmd）。供 Start 时回收宿主已退出的残留（孤儿）——宿主存活的插件
// 子进程（主管理器或其它实例子进程 spawn）不视为孤儿。
func listProviderProcesses() ([]procInfo, error) {
	ps := "Get-CimInstance Win32_Process -Filter \"Name='opencode2api.exe' OR CommandLine LIKE '%--provider-serve%'\" | ForEach-Object { [PSCustomObject]@{ pid=$_.ProcessId; ppid=$_.ParentProcessId; cmd=$_.CommandLine } } | ConvertTo-Json -Compress"
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", ps)
	applyNoWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var procs []procInfo
	if err := json.Unmarshal(out, &procs); err == nil {
		return procs, nil
	}
	var one procInfo
	if err := json.Unmarshal(out, &one); err == nil && one.PID > 0 {
		return []procInfo{one}, nil
	}
	return nil, fmt.Errorf("parse process list: %s", strings.TrimSpace(string(out)))
}
