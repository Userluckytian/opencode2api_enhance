//go:build !windows

// M2 T2 — 非 Windows 父进程看护的平台实现。
//
// Linux：/proc/<pid>/stat 字段 22（starttime，自系统启动的时钟节拍）作 PID 复用
// 指纹。无 /proc 的平台退化为 signal 0（仅 PID 比对，started=0）。
package manager

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"syscall"
)

// parentReparented 检测父进程是否已被 subreaper/init 收养（复核 P1-1）。
// 父进程退出后 POSIX 会立即把子进程 reparent，os.Getppid() 不再等于启动时记录
// 的父 PID；僵尸父进程（尚未被其父回收）仍能被 signal 0 探到「存在」，只有本
// 检测能确定它已经死了。O(1)，不依赖 /proc 或 CIM。
func parentReparented(recorded int) bool { return os.Getppid() != recorded }

// parentCheck 查询指定 PID：返回 (存活, 启动时间指纹, 查询错误)。
// 统一 signal-0 兜底（复核 P1）：/proc 读取失败时**不区分原因**一律走 signal 0。
// 无 procfs 的平台（macOS/BSD，均为发布目标）ReadFile("/proc/<pid>/stat") 同样
// 报 ENOENT——旧实现按 os.IsNotExist 分流、跳过 signal 0 落到「无法判定」错误，
// 看护连续 3 次失败（15s）即把健康服务误杀，再被管理面 Watchdog 拉起 → 永久
// crash-restart 循环。
// signal 0 判定（必须用 errors.Is，二轮 P1b）：os.Process.Signal 可能返回裸
// syscall.Errno、也可能返回 *os.SyscallError 包装（== 比较会漏，EPERM/ESRCH
// 都会被当成「未知」）；且 WSL Ubuntu 24.04 实测：Linux 上 Go 对已退出 PID 的
// signal 0 返回 os.ErrProcessDone（"os: process already finished"），不返回裸
// ESRCH——旧 switch 落到 default「未知」→ pidAliveSafe 保守判存活 → Linux 孤儿
// 扫描永远列不出孤儿。归类：nil/EPERM=存在；ESRCH/ErrProcessDone=确凿死亡；
// 其它=未知（调用方保守处理）。
// Linux 行为不变：目标已退出 → /proc 无条目（读失败）→ signal 0 返回 ESRCH →
// 判死，仍走 err=nil 的确凿结论路径。
func parentCheck(pid int) (bool, int64, error) {
	if pid <= 0 {
		return false, 0, fmt.Errorf("无效 PID %d", pid)
	}
	if data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat"); err == nil {
		started, ok := parseProcStatStart(string(data))
		if !ok {
			return true, 0, nil // 存在但解析不了 → 降级 PID-only
		}
		return true, started, nil
	}
	// /proc 不存在（非 Linux）或该 PID 无 procfs 条目（已退出/procfs 隐藏）：
	// signal 0 统一兜底。
	p, ferr := os.FindProcess(pid)
	if ferr != nil {
		return false, 0, ferr
	}
	serr := p.Signal(syscall.Signal(0))
	switch {
	case serr == nil:
		return true, 0, nil // 存在
	case errors.Is(serr, syscall.EPERM):
		return true, 0, nil // 存在但无权限发信号，仍算活着
	case errors.Is(serr, syscall.ESRCH), errors.Is(serr, os.ErrProcessDone):
		return false, 0, nil // 确凿不存在 / 已退出（Linux Go 的 ErrProcessDone 形态）
	default:
		return false, 0, fmt.Errorf("signal 0 探活失败: %w", serr) // 未知 → 保守
	}
}
