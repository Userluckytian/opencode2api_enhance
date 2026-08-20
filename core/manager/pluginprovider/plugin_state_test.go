// 跨进程启停状态共享回归测试（2026-08-19/20 现场）：
// 主管理器（UI 开关）与实例子进程共享同一 providers 目录 —— 开关经
// <providers>/.plugin-state.json 落盘，子进程 ≤1 个扫描周期跟随。
// 回归场景：开关关闭后实例端口（models/对话）仍可获取。
package pluginprovider

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestStateFileFollow 主管理器 Toggle 落盘 → 共享 providers 目录的实例子进程跟随：
// 关闭后子进程停、状态 disabled；重新启用恢复 running。
func TestStateFileFollow(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "providers")
	setHelper(t, "ready", "p1")

	// 主管理器（唯一写入方：UI 开关）
	h1 := newHarness(t, Config{ProvidersDir: dir})
	h1.installPlugin("p1", "", "")
	h1.pm.Start()
	waitStableRunning(t, h1.pm, "p1", 5*time.Second)

	// 实例子进程：共享 providers 目录 + 状态文件。Start 时 reapOrphans 不得误杀
	// 主管理器持活的插件（宿主存活判定），也不得重复 spawn 自己的进程（跟随状态）。
	h2 := newHarness(t, Config{ProvidersDir: dir})
	h2.pm.Start()
	waitStableRunning(t, h2.pm, "p1", 5*time.Second)
	pid2 := h2.pm.View("p1").PID
	if pid2 <= 0 {
		t.Fatal("实例子进程插件 pid 未记录")
	}

	// 主管理器关闭 → 状态文件更新 → 实例 watcher(50ms) 自动跟随（kill + disabled）。
	if _, err := h1.pm.Toggle("p1", false); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, h2.pm, "p1", StatusDisabled, 5*time.Second)
	waitDead(t, pid2, 5*time.Second)
	assertNoOrphanProc(t, dir)

	// 重新启用 → 实例恢复 running（再次拉起）。
	if _, err := h1.pm.Toggle("p1", true); err != nil {
		t.Fatal(err)
	}
	waitStableRunning(t, h2.pm, "p1", 5*time.Second)
	// 两进程各一个插件实例（各自聚合；关闭时全部跟随停）。
	waitStatus(t, h1.pm, "p1", StatusRunning, 5*time.Second)
}

// TestStateFilePreDisabled 状态文件预置禁用：进程启动即不拉起（重启后开关状态保持），
// 且启用后正常恢复。
func TestStateFilePreDisabled(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "providers")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(dir, ".plugin-state.json")
	if err := os.WriteFile(state, []byte(`{"enabled":{"p1":false}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	setHelper(t, "ready", "p1")
	h := newHarness(t, Config{ProvidersDir: dir})
	h.installPlugin("p1", "", "")
	h.pm.Start()
	waitStatus(t, h.pm, "p1", StatusDisabled, 3*time.Second)
	assertNoOrphanProc(t, dir)

	if _, err := h.pm.Toggle("p1", true); err != nil {
		t.Fatal(err)
	}
	waitStableRunning(t, h.pm, "p1", 5*time.Second)
}
