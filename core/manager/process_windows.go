//go:build windows

package manager

import (
	"fmt"
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
//
// 禁止给本函数加 /T（M2 D-M3-2）：provider-service 管理路径复用本函数，它自己
// 有优雅退出（pluginMgr.Close 回收插件），强杀树会跳过清理。孤儿清理端点需要
// 树杀时使用下方独立的 killProcessTree。
func killProcess(pid int) error {
	taskkill := exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/F")
	applyNoWindow(taskkill, true)
	return taskkill.Run()
}

// killTreeArgs taskkill 树杀参数（纯函数，单测锁定 argv；M3 C7）。
// /T 连进程树一并强杀：孤儿插件（父进程已死）留下的 node/msedge/mihomo 等
// 孙进程随插件一起回收。
func killTreeArgs(pid int) []string {
	return []string{"/PID", strconv.Itoa(pid), "/T", "/F"}
}

// killProcessTree 强杀进程树（仅 M1 孤儿清理端点 OrphanKillHandler 使用）。
// 不得改写成调用/替换 killProcess：共享 killProcess 必须保持非 /T 语义。
func killProcessTree(pid int) error {
	return killProcessTreeWith(nil, pid)
}

// killProcessTreeWith Windows：procs 仅为跨平台签名一致而保留——taskkill /T 由
// 系统负责枚举后代，无需进程快照（P1-2：端点复用快照的优化只对非 Windows 生效）。
func killProcessTreeWith(_ []procLine, pid int) error {
	if pid <= 0 {
		return nil
	}
	taskkill := exec.Command("taskkill", killTreeArgs(pid)...)
	applyNoWindow(taskkill, true)
	if err := taskkill.Run(); err != nil {
		return fmt.Errorf("taskkill /T 杀进程树 %d 失败: %w", pid, err)
	}
	return nil
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
