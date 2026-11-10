//go:build windows

// Windows 全量进程枚举（孤儿扫描用）。不得复用 pluginprovider.listProviderProcesses：
// 那个在 CIM 过滤层只取 opencode2api/插件子进程；本端点需要先取系统全量进程，
// 再由 classifyProcesses 按判据分类。
package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// listAllProcessesTimeout 枚举超时。WMI/CIM 仓库损坏或 powershell 挂起时，无超时的
// exec 会让 HTTP handler 无限期阻塞（扫描与清理各挂一条）。实测正常 0.6~5s，10s 留余量。
const listAllProcessesTimeout = 10 * time.Second

// listAllProcesses 枚举系统全部进程（pid/ppid/cmd）。Get-CimInstance Win32_Process
// 全表 + ConvertTo-Json：多对象输出 JSON 数组、单对象输出 object，两种都要能解析
// （参照 pluginprovider/proc_windows.go）。系统进程的 CommandLine 可为 null，
// 解析为空串（不含 --provider-serve，不影响判据）。
//
// ctx 由 handler 传 r.Context()：客户端断开时一并取消。
func listAllProcesses(ctx context.Context) ([]procLine, error) {
	ctx, cancel := context.WithTimeout(ctx, listAllProcessesTimeout)
	defer cancel()
	ps := `Get-CimInstance Win32_Process | ForEach-Object { [PSCustomObject]@{ pid=$_.ProcessId; ppid=$_.ParentProcessId; cmd=$_.CommandLine } } | ConvertTo-Json -Compress`
	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", ps)
	applyNoWindow(cmd, true)
	// stderr 接进缓冲：Output() 的 err 只含 "exit status N"，不带 powershell 的
	// 真实报错，枚举失败时无从排障。
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("进程枚举超时（%s）: %w", listAllProcessesTimeout, ctx.Err())
		}
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("进程枚举失败: %w（stderr: %s）", err, msg)
		}
		return nil, err
	}
	var procs []procLine
	if err := json.Unmarshal(out, &procs); err == nil {
		return procs, nil
	}
	var one procLine
	if err := json.Unmarshal(out, &one); err == nil && one.PID > 0 {
		return []procLine{one}, nil
	}
	return nil, fmt.Errorf("parse process list: %s", strings.TrimSpace(string(out)))
}

// pidAliveSafe M2 P2-2：孤儿扫描（列出与 kill 前二次校验）用的保守探活。
//
// 与 pidAlive 的差别只在失败语义：pidAlive 查询失败返回 false（=「已死」，
// fail-closed），tasklist/CIM 偶发故障会把还活着的父进程判死 → 误列/误杀。
// 本函数查询失败视为「存活」→ 不列不杀（宁可漏报一轮，等下次扫描再显示，
// 绝不误杀有主进程）。判活本身复用 parentCheck（定向 CIM 单查：存在性 + 启动
// 时间，见 parentwatch_windows.go）。
//
// 约束：不改 manager.pidAlive / pluginprovider.pidAlive —— 那是别处的生产路径。
func pidAliveSafe(pid int) bool {
	if pid <= 0 {
		return false
	}
	alive, _, err := parentCheck(pid)
	return pidAliveDecision(alive, err)
}
