// M2 T2 — provider-service 父进程自检（缺陷 D3 止血，计划
// docs/ai-framework/plans/2026-11-10-orphan-governance-m2.md T2）。
//
// 背景：三层进程架构中 provider-service（-provider-service，:50080）的父进程是
// 管理面。管理面被强杀/崩溃后，provider-service 存活且其插件管理器持续拉起插件，
// 整棵子树沦为孤儿。本看护在父进程消失时回收全部插件并退出本进程。
//
// PID 复用防护（硬要求，计划 §五.1）：判活不能只比 PID——父进程死后其 PID 可能被
// 系统复用给无关进程。判活 = 该 PID 存在 且 进程启动时间与启动时记录的一致
// （Windows：CIM CreationDate FILETIME；Linux：/proc/<pid>/stat 字段 22 starttime；
// 均为平台文件 parentCheck 提供）。启动时间取不到时降级为仅 PID 比对并告警
// （保守方向：最坏是漏检维持旧行为，不会误杀存活父进程）。
//
// POSIX reparent 检测（复核 P1-1）：Linux/macOS 上父进程退出后子进程会被 subreaper/
// init 收养，getppid() 立刻变化；而僵尸父进程仍能被 signal 0 探到「存活」。因此
// Windows 之外每轮先查 parentReparented（os.Getppid() != 启动时记录的父 PID）：
// 启动即已 reparent → 父进程早已死亡，直接 onGone；运行中 reparent → 判死。
// 该检测 O(1)，不依赖 CIM/procfs，也绕开僵尸 PID 的假存活。Windows 无 reparent
// 语义（Getppid 保持创建者 PID），平台实现恒 false，仍走 parentCheck。
//
// 查询失败语义：查询失败 ≠ 父进程死亡（WMI 仓库抖动 / powershell 超时等偶发）。
// 单轮失败按「存活」处理，连续 failLimit 次（默认 3 次 ≈ 15s）才判定死亡——
// 宁可多等，不可把还在服务的进程退掉。Windows 的 parentCheck 在 CIM 失败时还有
// tasklist 第二探活（复核 P1-2），只有两个探活都失败才算「未知」。
//
// 本文件为平台无关的可测内核；平台相关部分在 parentwatch_windows.go /
// parentwatch_other.go（parentCheck / parentReparented）。禁止引入外部依赖。
package manager

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// 看护节奏（生产默认值；测试经 parentWatchSpec 缩短）。
const (
	// ParentWatchInterval 每 5 秒检查一次父进程。
	ParentWatchInterval = 5 * time.Second
	// ParentWatchFailLimit 连续查询失败达到此数才判定父进程死亡（3 次 ≈ 15s）。
	ParentWatchFailLimit = 3
)

// parentProcessKey 看护目标：父进程 PID。启动时间记录在 parentWatchState
// （支持启动期查询失败、后续轮次补记）。
type parentProcessKey struct {
	pid int
}

// parentWatchState 看护跨轮状态。
type parentWatchState struct {
	failures int   // 连续查询失败计数（成功一轮即清零）
	started  int64 // 父进程启动时间（平台刻度；0 = 尚未捕获/平台不可得）
}

// parentWatchSpec 看护协程的全部外部依赖（注入便于单测；生产用
// defaultParentWatchSpec）。check 返回 (存活, 启动时间刻度, 查询错误)：
// err != nil 时前两个返回值无意义（语义为「未知」，保守处理）。
// parentPID 是启动瞬间捕获的父 PID（复核 P1-1：不得在插件启动后再取
// os.Getppid()，否则管理面在窗口期死亡时会把 subreaper/init 当父进程）。
// reparented 为平台 reparent 检测（Windows 恒 false；POSIX 比较 os.Getppid()）。
type parentWatchSpec struct {
	interval   time.Duration
	failLimit  int
	parentPID  int
	check      func(pid int) (bool, int64, error)
	reparented func(recorded int) bool
}

// defaultParentWatchSpec 生产配置（平台实现见 parentwatch_windows.go / _other.go）。
func defaultParentWatchSpec(parentPID int) parentWatchSpec {
	return parentWatchSpec{
		interval:   ParentWatchInterval,
		failLimit:  ParentWatchFailLimit,
		parentPID:  parentPID,
		check:      parentCheck,
		reparented: parentReparented,
	}
}

// StartParentWatch 启动父进程看护协程（非阻塞）。
//
//   - parentPID 必须是进程启动瞬间捕获的父 PID（main() 第一条语句的
//     os.Getppid()），不能在此刻重新获取：POSIX 上父进程若已退出，重新获取
//     会得到 subreaper/init，看护将永远不判死（复核 P1-1）。
//   - onGone 在判定父进程消失后被调用一次（调用方负责回收插件并退出进程）。
//   - ctx 取消即停：provider-service 正常退出路径必须取消看护，不得被误触发。
//   - 父 PID 不可得（<=0）时不启动看护：宁可漏检（维持旧行为）也不误退。
func StartParentWatch(ctx context.Context, parentPID int, onGone func()) {
	startParentWatch(ctx, onGone, defaultParentWatchSpec(parentPID))
}

// startParentWatch 可注入内核（单测直接驱动，勿在生产调用）。
func startParentWatch(ctx context.Context, onGone func(), spec parentWatchSpec) {
	ppid := spec.parentPID
	if ppid <= 0 {
		slog.Warn("parent watch disabled: parent pid unavailable", "ppid", ppid)
		return
	}
	key := parentProcessKey{pid: ppid}
	st := &parentWatchState{}
	// 启动前先查 reparent（P1-1）：若本进程已被 subreaper/init 收养，说明父进程
	// 在「main() 捕获 ppid → 看护启动」窗口内已死亡。此时 check(ppid) 可能探到
	// 已死的僵尸 PID 或复用 PID，不能作为存活依据，直接判死。
	if spec.reparented != nil && spec.reparented(ppid) {
		slog.Warn("parent watch: process already reparented at startup, original parent gone; self-terminating",
			"ppid", ppid)
		onGone()
		return
	}
	// 启动时立即捕获父进程标识（此刻父进程几乎必然存活——它就是刚拉起我们的进程；
	// 查询失败不致命，循环里会补记）。
	alive, started, err := spec.check(ppid)
	switch {
	case err != nil:
		slog.Warn("parent watch: initial parent check failed, will retry each tick", "ppid", ppid, "error", err)
	case !alive:
		slog.Warn("parent watch: parent already gone at startup; self-terminating", "ppid", ppid)
		onGone()
		return
	default:
		st.started = started
		if started == 0 {
			slog.Warn("parent watch: parent start time unavailable, degrading to PID-only match", "ppid", ppid)
		}
	}
	slog.Info("parent watch started", "ppid", ppid, "interval", spec.interval.String(), "failLimit", spec.failLimit)
	go parentWatchLoop(ctx, spec, key, st, onGone)
}

// parentWatchLoop 看护主循环：每 tick 判定一次；ctx 取消优先于一切。
func parentWatchLoop(ctx context.Context, spec parentWatchSpec, key parentProcessKey, st *parentWatchState, onGone func()) {
	ticker := time.NewTicker(spec.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if parentWatchTick(spec, key, st) {
				slog.Warn("parent process gone; provider-service self-terminating",
					"ppid", key.pid, "consecutiveQueryFailures", st.failures)
				onGone()
				return
			}
		}
	}
}

// parentWatchTick 单轮判定（纯决策，无时钟；单测直接驱动）。
// 返回 true = 判定父进程已消失。判据（按优先级）：
//  0. 平台报 reparent（POSIX getppid 变化）→ 死（确凿，P1-1；僵尸父进程也逃不掉）
//  1. 查询成功且进程不存在 → 死（确凿）
//  2. 查询成功但启动时间与记录不符 → 死（PID 被复用，原父进程必然已退出）
//  3. 查询连续失败达 failLimit → 判死（兜底；单次失败保守视为存活）
func parentWatchTick(spec parentWatchSpec, key parentProcessKey, st *parentWatchState) bool {
	if spec.reparented != nil && spec.reparented(key.pid) {
		slog.Warn("parent watch: process reparented, original parent gone", "ppid", key.pid)
		return true
	}
	alive, started, err := spec.check(key.pid)
	if err != nil {
		st.failures++
		slog.Warn("parent watch query failed (treated as alive, conservative)",
			"ppid", key.pid, "consecutiveFailures", st.failures, "limit", spec.failLimit, "error", err)
		return st.failures >= spec.failLimit
	}
	st.failures = 0
	if !alive {
		return true
	}
	if st.started == 0 {
		// 启动时没捕到启动时间（查询失败/平台限制）：本轮补记，暂按 PID-only。
		if started != 0 {
			st.started = started
			slog.Info("parent watch: parent start time captured late", "ppid", key.pid)
		}
		return false
	}
	if started != 0 && started != st.started {
		slog.Warn("parent watch: same PID with different start time, original parent must be dead (PID reused)",
			"ppid", key.pid, "recorded", st.started, "observed", started)
		return true
	}
	return false
}

// parseProcStatStart 解析 Linux /proc/<pid>/stat 的 starttime（字段 22，自系统
// 启动的时钟节拍，同进程两次读取恒等 → 可作 PID 复用指纹）。
//
// comm（字段 2）可含空格与括号，必须先定位最后一个 ')' 再按空白切分：切分结果
// fields[0] 即字段 3（state），字段 22 对应 fields[19]。
// 返回 (starttime, ok)；解析失败 ok=false（调用方降级 PID-only）。
func parseProcStatStart(stat string) (int64, bool) {
	i := strings.LastIndexByte(stat, ')')
	if i < 0 {
		return 0, false
	}
	fields := strings.Fields(stat[i+1:])
	if len(fields) <= 19 {
		return 0, false
	}
	v, err := strconv.ParseInt(fields[19], 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}
