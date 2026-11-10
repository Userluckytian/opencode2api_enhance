//go:build windows

// M2 T2 — Windows 父进程看护的平台实现。
//
// 单次 CIM 定向查询同时得到「是否存在」与「启动时间」（CreationDate FILETIME，
// 100ns 刻度，同进程两次读取恒等 → PID 复用指纹）。实测单查 ~0.7s，5s 节奏可承受
// （不同于 M1 全表枚举，这里只查一个 PID）。
//
// 注意：不得改变 manager.pidAlive（process_windows.go）与 pluginprovider.pidAlive
// 的既有语义——那是别处的生产路径；本函数是看护/保守探活的独立实现。
package manager

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// parentCheckTimeout 单次父进程查询超时（小于 5s 看护间隔）。
const parentCheckTimeout = 4 * time.Second

// tasklistProbeTimeout tasklist 第二探活超时（复核 P1-2：它只是 CIM 故障时的
// 降级通道，必须短于 CIM 超时，避免「CIM 4s + tasklist 2s」把单轮拖得过久）。
const tasklistProbeTimeout = 2 * time.Second

// parentReparented Windows 无 POSIX reparent 语义：os.Getppid() 始终保持创建者
// PID（父进程退出后也不变），因此这里恒 false，父进程死亡由 parentCheck 判定。
func parentReparented(int) bool { return false }

// parentCheck 查询指定 PID：返回 (存活, 启动时间 FILETIME 刻度, 查询错误)。
// err != nil 表示「未知」（CIM/powershell 故障、超时，且 tasklist 第二探活也
// 失败），调用方按保守语义处理；进程确不存在时 alive=false 且 err=nil。
//
// 复核 P1-2：CIM 路径的任何「未知」结果（超时、exec 错误、输出无法识别）都先
// 走 tasklistProbe，而不是直接上报未知。WMI 仓库损坏或 PowerShell 被策略禁用
// 时，旧实现连续 3 次（≈15s）即误判父进程死亡并退出健康服务，与 Watchdog 形成
// 拉起循环。tasklist 明确存活 → (true, 0, nil)（started=0 = 不校验启动时间）；
// 明确死亡 → (false, 0, nil)；tasklist 也失败才返回原 CIM 错误。
func parentCheck(pid int) (bool, int64, error) {
	if pid <= 0 {
		return false, 0, fmt.Errorf("无效 PID %d", pid)
	}
	alive, started, err := parentCheckCIM(pid)
	if err == nil {
		return alive, started, nil // CIM 成功路径不变（保留 FILETIME 指纹）
	}
	alive2, ok := tasklistProbe(pid)
	if !ok {
		return false, 0, err // 两个探活都失败 → 未知（保留 CIM 的原始错误）
	}
	if alive2 {
		slog.Warn("parent check: CIM unavailable, tasklist says alive; degrading to PID-only",
			"pid", pid, "error", err)
		return true, 0, nil
	}
	slog.Warn("parent check: CIM unavailable, tasklist says gone", "pid", pid, "error", err)
	return false, 0, nil
}

// parentCheckCIM 定向 CIM 单查（存在性 + CreationDate FILETIME 指纹）。
// err != nil = 未知；GONE = 确凿死亡。
func parentCheckCIM(pid int) (bool, int64, error) {
	// CreationDate 理论上非空；防御性输出 0（调用方降级 PID-only）。
	ps := `$p = Get-CimInstance Win32_Process -Filter "ProcessId=` + strconv.Itoa(pid) + `" -ErrorAction Stop; ` +
		`if ($null -eq $p) { 'GONE' } ` +
		`elseif ($null -eq $p.CreationDate) { 'OK 0' } ` +
		`else { 'OK ' + $p.CreationDate.ToFileTime() }`
	ctx, cancel := context.WithTimeout(context.Background(), parentCheckTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", ps)
	applyNoWindow(cmd, true)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return false, 0, fmt.Errorf("父进程查询超时: %w", ctx.Err())
		}
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return false, 0, fmt.Errorf("父进程查询失败: %w（stderr: %s）", err, msg)
		}
		return false, 0, fmt.Errorf("父进程查询失败: %w", err)
	}
	s := strings.TrimSpace(string(out))
	if s == "GONE" {
		return false, 0, nil
	}
	if !strings.HasPrefix(s, "OK") {
		return false, 0, fmt.Errorf("父进程查询输出无法识别: %q", s)
	}
	var started int64
	if rest := strings.TrimSpace(strings.TrimPrefix(s, "OK")); rest != "" {
		started, err = strconv.ParseInt(rest, 10, 64)
		if err != nil {
			return false, 0, fmt.Errorf("父进程启动时间解析失败: %q", rest)
		}
	}
	return true, started, nil
}

// tasklistProbe 第二探活（P1-2）：tasklist 过滤 PID，按 CSV 第二列精确比对。
// ok=false 表示 tasklist 本身失败/超时（未知）；ok=true 时 alive 为清晰结论。
// 不解析本地化提示文案（中文 Windows 输出「信息: 没有运行的任务匹配...」），
// 只认第二列的 PID 数字——与 manager.pidAlive 的意图一致但独立实现，不改其语义。
func tasklistProbe(pid int) (alive bool, ok bool) {
	if pid <= 0 {
		return false, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), tasklistProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/FO", "CSV", "/NH")
	applyNoWindow(cmd, true)
	out, err := cmd.Output()
	if err != nil {
		return false, false
	}
	return parseTasklistPID(string(out), pid), true
}

// parseTasklistPID 解析 tasklist /FO CSV /NH 输出：逐行按 CSV 解析，第二列
// （映像名称, PID, 会话名, 会话#, 内存）必须精确等于 pid。纯函数，便于单测
// （引号、含逗号的内存列、中文提示行、空输出、PID 不匹配）。
func parseTasklistPID(out string, pid int) bool {
	if pid <= 0 {
		return false
	}
	r := csv.NewReader(strings.NewReader(out))
	r.FieldsPerRecord = -1 // 提示行只有 1 列，数据行 5 列，不能按首行定列数
	r.TrimLeadingSpace = true
	for {
		rec, err := r.Read()
		if err != nil {
			return false // io.EOF 或畸形输出 → 未找到匹配
		}
		if len(rec) < 2 {
			continue // 本地化提示行（无任务匹配）等
		}
		v, err := strconv.Atoi(strings.TrimSpace(rec[1]))
		if err == nil && v == pid {
			return true
		}
	}
}
