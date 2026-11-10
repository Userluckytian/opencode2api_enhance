//go:build windows

// Windows 平台子进程帮助：CREATE_NO_WINDOW（防弹窗）+ taskkill 强杀 + tasklist 探活。
package pluginprovider

import (
	"encoding/csv"
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

// taskkillArgs 构造 taskkill 参数（纯函数，单测锁定 /T 树杀语义）。
// M2 T1：加 /T 连进程树一并强杀——插件的子进程（node 内核 / 浏览器等）若只杀父
// 会沦为孤儿（缺陷 D2）。core/manager/process_windows.go 的 killProcess（管
// provider-service 的另一条路径）**不加** /T：provider-service 有优雅退出
// （pluginMgr.Close 自行回收插件），强杀树会跳过清理。决策见
// docs/ai-framework/plans/2026-11-10-orphan-governance-m2.md T1。
func taskkillArgs(pid int) []string {
	return []string{"/PID", strconv.Itoa(pid), "/T", "/F"}
}

// killProcess 强杀进程树（taskkill /PID <pid> /T /F）。
func killProcess(pid int) error {
	taskkill := exec.Command("taskkill", taskkillArgs(pid)...)
	applyNoWindow(taskkill)
	return taskkill.Run()
}

// killOrphanTree Windows：M3 自动回收插件孤儿。killProcess 的 taskkill 已带 /T
// （M2 T1），杀插件时其 node/msedge/mihomo 等后代一并回收，无需显式枚举后代。
// 返回错误供 reapOrphans 区分「已回收」与「回收失败」两态日志（P1-2）。
func killOrphanTree(pid int) error {
	if pid <= 0 {
		return nil
	}
	if err := killProcess(pid); err != nil {
		return fmt.Errorf("taskkill /T 回收插件 %d 失败: %w", pid, err)
	}
	return nil
}

// pidAliveConservative 保守探活（P1-1）：查询失败 = 存活（true），只有 tasklist
// 明确返回「该 PID 不存在」才判死。
//
// 与 pidAlive 的 fail-closed 语义相反，且**不得**改 pidAlive（它是插件子进程
// 生命周期路径的生产语义）。reapOrphans 用它判断「插件父进程是否已死」：把未知
// 当死亡会误杀有主插件（审查实测：宿主命令行不含 opencode2api 时旧 hosts 名称
// 启发式会误树杀活插件），宁可漏回收一轮。
//
// 不解析本地化提示文案：tasklist 在中文 Windows 输出「信息: 没有运行的任务…」，
// 只按 CSV 第二列精确比对 PID（数据行恒为数字）。
func pidAliveConservative(pid int) bool {
	if pid <= 0 {
		return true // 无效 PID 属「未知」，保守按存活处理，绝不据此杀进程
	}
	out, err := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/FO", "CSV", "/NH").Output()
	if err != nil {
		return true // 查询失败 = 未知 = 存活
	}
	return parseTasklistPIDCSV(string(out), pid)
}

// parseTasklistPIDCSV 解析 tasklist /FO CSV /NH 输出：逐行按 CSV 解析，第二列
// （映像名称, PID, ...）必须精确等于 pid。提示行只有 1 列（或本地化文案），跳过。
// 纯函数，便于单测（引号、空输出、PID 不匹配）。本包独立实现，不 import
// core/manager 的未导出 parseTasklistPID。
func parseTasklistPIDCSV(out string, pid int) bool {
	if pid <= 0 {
		return false
	}
	r := csv.NewReader(strings.NewReader(out))
	r.FieldsPerRecord = -1
	r.TrimLeadingSpace = true
	for {
		rec, err := r.Read()
		if err != nil {
			return false // io.EOF 或畸形输出 → 未找到匹配
		}
		if len(rec) < 2 {
			continue
		}
		v, err := strconv.Atoi(strings.TrimSpace(rec[1]))
		if err == nil && v == pid {
			return true
		}
	}
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
