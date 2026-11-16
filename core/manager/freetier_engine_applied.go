package manager

// P0 配置漂移修复（2026-11-16）。
//
// 背景：11-11 的事故是「引擎在跑、但管理器不掌握它的 pid」，于是 Stop 杀不到、
// Start 见到端口在听就直接 return ready —— 改配置与点重启全是空操作，进程
// 从 11-06 起没加载过新配置。今天（11-16）实测再次复现：引擎进程从 11-11
// 20:39 一直跑到 16:12，管理器 pid=0。
//
// 修法：给「已生效配置」记一个指纹（落盘），Start 只在
// 「端口活着 + 指纹一致 + pid 一致」三条件全满足时复用进程；
// 否则按端口认领进程并重启。认领前必须通过 /healthz 身份校验，
// 避免端口被别的程序占用时误杀。

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// engineApplied 记录「上一次成功应用到引擎的配置」。
type engineApplied struct {
	Fingerprint string `json:"fingerprint"`
	PID         int    `json:"pid"`
	At          string `json:"at"`
}

// engineStartDecision Start 的三分支。
type engineStartDecision int

const (
	// engineStartFresh：端口空闲，直接写盘并启动。
	engineStartFresh engineStartDecision = iota
	// engineReuseRunning：端口活着，且能证明它跑的就是当前配置。
	engineReuseRunning
	// engineRestartStale：端口活着，但配置/进程对不上，必须先停再启。
	engineRestartStale
)

// decideEngineStart 是 Start 的唯一判定点（纯函数，便于回归测试）。
func decideEngineStart(portAlive bool, ownerPID int, applied engineApplied, appliedOK bool, desiredFP string) engineStartDecision {
	if !portAlive {
		return engineStartFresh
	}
	if !appliedOK || ownerPID <= 0 {
		return engineRestartStale
	}
	if applied.Fingerprint == "" || applied.Fingerprint != desiredFP {
		return engineRestartStale
	}
	if applied.PID != ownerPID {
		return engineRestartStale
	}
	return engineReuseRunning
}

// engineConfigFingerprint 配置字节的稳定指纹。
func engineConfigFingerprint(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// shortFP 日志用短指纹（空值也安全）。
func shortFP(fp string) string {
	if len(fp) > 12 {
		return fp[:12]
	}
	return fp
}

// engineAppliedPath 指纹标记文件（与引擎共用工作目录，便于一起清理）。
func (m *Manager) engineAppliedPath() string {
	return filepath.Join(m.paths.FreeTierEngineDir(), "engine.applied.json")
}

func (m *Manager) readEngineApplied() (engineApplied, bool) {
	var a engineApplied
	data, err := os.ReadFile(m.engineAppliedPath())
	if err != nil {
		return a, false
	}
	if err := json.Unmarshal(data, &a); err != nil {
		return a, false
	}
	if a.Fingerprint == "" {
		return a, false
	}
	return a, true
}

func (m *Manager) writeEngineApplied(a engineApplied) error {
	data, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(m.engineAppliedPath(), data)
}

func (m *Manager) clearEngineApplied() {
	if err := os.Remove(m.engineAppliedPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		// 标记清不掉不阻断停止流程；下次 Start 会因指纹缺失而重启，仍然是安全侧。
		_ = err
	}
}

// engineImageAllowlist 引擎二进制的 basename 允许列表。
//
// 文件名是历史遗留（`opencode2api-win`），未来若改名只需在这里加一项；
// 判定主闸门是 /healthz 身份，不是文件名。
var engineImageAllowlist = map[string]bool{
	"opencode2api-win.exe":    true,
	"opencode2api-win":        true,
	"opencode2api-engine.exe": true,
	"opencode2api-engine":     true,
	"opencode2api.exe":        true,
	"opencode2api":            true,
}

// engineHealthIsOurs 判断端口上是否是本项目的引擎（身份闸门）。
// 只认 /healthz 里同时出现 keys 与 models 两个结构的响应。
func engineHealthIsOurs(port uint16) bool {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/healthz", port))
	if err != nil {
		return false
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	// 200 = ready；503 = 正在启动（模型目录 pending）。两者都算「是我们的」；
	// 其余状态码一律不认，避免把别人的 4xx/5xx 误判成引擎。
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusServiceUnavailable {
		return false
	}
	var probe struct {
		Keys *struct {
			Total int `json:"total"`
		} `json:"keys"`
		Models *struct {
			Status string `json:"status"`
		} `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&probe); err != nil {
		return false
	}
	return probe.Keys != nil && probe.Models != nil
}

// enginePidByPort 通过端口反查引擎进程。
//
// 三重判据：① /healthz 身份；② 端口监听者唯一，或用 basename 允许列表收敛到唯一；
// ③ 收敛失败就返回 false（宁可不杀，也不误杀）。
func (m *Manager) enginePidByPort(port uint16) (int, bool) {
	if !engineHealthIsOurs(port) {
		return 0, false
	}
	pids := listPortPids(port)
	if len(pids) == 0 {
		return 0, false
	}
	if len(pids) == 1 {
		return pids[0], true
	}
	var named []int
	for _, pid := range pids {
		if engineImageAllowlist[processImageName(pid)] {
			named = append(named, pid)
		}
	}
	if len(named) == 1 {
		return named[0], true
	}
	return 0, false
}

// stopEngineByPort 停掉端口上的引擎（认领 + 杀 + 等端口释放）。
// ownerPID<=0 时自行认领；认领失败返回错误，调用方不得继续启动新进程。
func (m *Manager) stopEngineByPort(port uint16, ownerPID int, runner Runner) error {
	pid := ownerPID
	if pid <= 0 {
		var ok bool
		pid, ok = m.enginePidByPort(port)
		if !ok {
			return fmt.Errorf("端口 %d 有监听但 /healthz 不是本项目引擎（或无法确定唯一进程）", port)
		}
	}
	_ = runner.Kill(pid)
	if err := waitPortClosed(port, 5*time.Second); err == nil {
		return nil
	}
	// 二次收口：可能换了 pid（或第一次 kill 落在启动窗口内）。
	if again, ok := m.enginePidByPort(port); ok {
		_ = runner.Kill(again)
		if err := waitPortClosed(port, 3*time.Second); err == nil {
			return nil
		}
	}
	return fmt.Errorf("端口 %d 仍被占用（引擎未退出）", port)
}
