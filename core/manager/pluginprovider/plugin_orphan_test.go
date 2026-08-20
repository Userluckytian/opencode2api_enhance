// 插件子进程泄漏/孤儿回收回归测试（2026-08-19 现场问题）：
//   - 反复启停：旧实现每次重新启用 spawn 新进程不杀旧进程，关闭只杀最后一个
//     pid，历史进程残留占端口（现象：开关关闭后仍能从端口读取到）。
//   - 主进程强杀/崩溃：插件子进程无 Job 保护成孤儿，下次启动不回收。
//
// 全部用 t.TempDir 独立 providers 目录 + 伪子进程，不触网、不占用固定端口。
package pluginprovider

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// assertNoOrphanProc 断言 providers 目录下没有残留的插件子进程
// （listProviderProcesses 过滤命令行含该目录的 --provider-serve 进程）。
func assertNoOrphanProc(t *testing.T, providersDir string) {
	t.Helper()
	dirLow := strings.ToLower(filepath.Clean(providersDir))
	waitFor(t, 5*time.Second, "providers 目录无残留插件进程", func() bool {
		procs, err := listProviderProcesses()
		if err != nil {
			return true // 枚举失败不阻塞断言（平台工具缺失场景）
		}
		for _, pr := range procs {
			if strings.Contains(strings.ToLower(pr.Cmd), dirLow) {
				return false
			}
		}
		return true
	})
}

// TestRepeatedToggleNoLeak 反复启停不泄漏子进程：每次重新启用 spawn 新进程前
// 必须回收上一周期进程（回归：只杀最后 pid 导致历史进程长期占端口）。
func TestRepeatedToggleNoLeak(t *testing.T) {
	h := newHarness(t, Config{})
	setHelper(t, "ready", "p1")
	h.installPlugin("p1", "", "")
	h.pm.Start()

	waitStableRunning(t, h.pm, "p1", 5*time.Second)
	first := h.pm.View("p1").PID
	if first <= 0 {
		t.Fatalf("插件 pid 未记录")
	}

	for i := 0; i < 3; i++ { // 启→停→启→停 三轮，每轮验证无进程残留
		if _, err := h.pm.Toggle("p1", false); err != nil {
			t.Fatal(err)
		}
		waitStatus(t, h.pm, "p1", StatusDisabled, 3*time.Second)
		waitDead(t, first, 5*time.Second) // 当前 pid 必须死透
		assertNoOrphanProc(t, h.dir)      // 目录下无任何残留进程

		if _, err := h.pm.Toggle("p1", true); err != nil {
			t.Fatal(err)
		}
		waitStableRunning(t, h.pm, "p1", 5*time.Second)
		first = h.pm.View("p1").PID
	}
}

// TestReapOrphansOnStart 主进程启动时自动回收宿主已退出的插件孤儿：
// 预置一个独立 spawn 的插件子进程（命令行指向 providers 目录），
// Start 的 reapOrphans 应将其杀掉，且不妨碍正常插件随后拉起。
func TestReapOrphansOnStart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "providers")
	h := newHarness(t, Config{ProvidersDir: dir})
	setHelper(t, "ready", "orphan")
	h.installPlugin("orphan", "", "")

	// 直接 spawn 假孤儿：用 installPlugin 复制的插件入口路径（命令行为完整路径，
	// 含 providers 目录，匹配 reapOrphans 的过滤规则）。
	exePath := filepath.Join(dir, "orphan", "fake-provider.exe")
	cmd := exec.Command(exePath, "--provider-serve", "--port", "0")
	cmd.Env = append(os.Environ(), envHelperMode+"=ready", envHelperID+"=orphan")
	cmd.Dir = filepath.Join(dir, "orphan")
	cmd.Stdout = nil
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	orphanPID := cmd.Process.Pid
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()
	if !pidAlive(orphanPID) {
		t.Fatal("孤儿子进程未启动")
	}

	h.pm.Start()
	// Start 的 reapOrphans 应杀掉孤儿（命令行含 providers 目录、非本管理器持活）。
	waitDead(t, orphanPID, 5*time.Second)
	// 正常插件随后照常拉起（互不干扰）。
	waitStableRunning(t, h.pm, "orphan", 5*time.Second)
	// 收尾：停用后目录同样无残留。
	if _, err := h.pm.Toggle("orphan", false); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, h.pm, "orphan", StatusDisabled, 3*time.Second)
	assertNoOrphanProc(t, dir)
	_ = os.Remove(filepath.Join(dir, "orphan", "provider.json"))
}
