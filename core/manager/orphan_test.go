package manager

import (
	"path/filepath"
	"testing"
)

// TestClassifyOrphans 覆盖：探针残留 / 运行中保留 / 已停止孤儿 / 已删目录残留 /
// 管理器自身保留 / 网关保留 / 空命令行跳过 / 插件子进程（宿主存活保留、宿主退出孤儿）/
// 端口提取 / 汇总计数。
func TestClassifyOrphans(t *testing.T) {
	dir := filepath.Join("C:", "data", "runtime")
	exe := filepath.Join("C:", "prog", "opencode2api.exe")
	probe := filepath.Join(dir, "_probe", "worker-01")
	instances := []Instance{
		{Name: "ok1", Port: 18100, SingboxPort: 20100, Status: StatusRunning()},
		{Name: "stopped1", Port: 18101, SingboxPort: 20101, Status: StatusStopped()},
		{Name: "del1", Port: 18102, SingboxPort: 20102, Status: StatusStopped()},
	}
	procs := []procLine{
		{100, 0, "opencode2api.exe", `"` + exe + `" -port 40090 -config "` + filepath.Join(probe, "opencode2api.json") + `"`},
		{101, 0, "sing-box.exe", `"` + filepath.Join("C:", "prog", "sing-box.exe") + `" run -c "` + filepath.Join(probe, "singbox.json") + `"`},
		{200, 0, "opencode2api.exe", `"` + exe + `" -port 18110 -config "` + filepath.Join(dir, "ok1", "opencode2api.json") + `"`},
		{300, 0, "opencode2api.exe", `"` + exe + `" -port 18120 -config "` + filepath.Join(dir, "stopped1", "opencode2api.json") + `"`},
		{301, 0, "sing-box.exe", `"` + filepath.Join("C:", "prog", "sing-box.exe") + `" run -c "` + filepath.Join(dir, "stopped1", "singbox.json") + `"`},
		{302, 0, "sing-box.exe", `"` + filepath.Join("C:", "prog", "sing-box.exe") + `" run -c "` + filepath.Join(dir, "del1", "singbox.json") + `"`},
		{400, 0, "opencode2api.exe", `"` + exe + `" -port 8000 -config "` + filepath.Join("C:", "data", "config.json") + `"`},
		{401, 0, "opencode2api.exe", `"` + exe + `" -port 28080 -config "` + filepath.Join(dir, "_unified-gateway", "opencode2api.json") + `" -gateway`},
		{500, 0, "opencode2api.exe", ""},
		// 插件子进程：宿主 opencode2api 存活（PPID=700）→ 保留不列出。
		{600, 700, "loomy-provider.exe", `"D:\app\providers\loomy\loomy-provider.exe" --provider-serve --port 0`},
		// 插件子进程：宿主已退出（PPID=999 无对应进程）→ 孤儿，归属插件 loomy。
		{601, 999, "loomy-provider.exe", `"D:\app\providers\loomy\loomy-provider.exe" --provider-serve --port 0`},
		// 宿主 opencode2api 主进程（管理器本体，config.json 在数据目录）。
		{700, 0, "opencode2api.exe", `"` + exe + `" -port 50000 -config "C:\data\config.json"`},
	}
	scan := classifyOrphans(procs, dir, instances)
	if scan.Total != 6 {
		t.Fatalf("total=%d, want 6", scan.Total)
	}
	if scan.Probe != 2 {
		t.Fatalf("probe=%d, want 2", scan.Probe)
	}
	if scan.Orphan != 4 {
		t.Fatalf("orphan=%d, want 4", scan.Orphan)
	}
	byPID := map[int]OrphanProcess{}
	for _, it := range scan.Items {
		byPID[it.PID] = it
	}
	// 探针残留分类。
	if it := byPID[100]; it.Category != "probe" {
		t.Fatalf("pid100 category=%s, want probe", it.Category)
	}
	// 已停止实例 → orphan 且带实例名与端口。
	if it := byPID[300]; it.Category != "orphan" || it.Instance != "stopped1" || it.Port != 18120 {
		t.Fatalf("pid300=%+v, want orphan/stopped1/18120", it)
	}
	// 已删除实例目录残留 → orphan（未识别归属，但仍列出）。
	if it := byPID[302]; it.Category != "orphan" {
		t.Fatalf("pid302 category=%s, want orphan", it.Category)
	}
	// 插件子进程：宿主存活 → 不列出；宿主退出 → orphan 且归属插件 id。
	if _, ok := byPID[600]; ok {
		t.Fatalf("pid600 宿主存活，不应列为孤儿")
	}
	if it := byPID[601]; it.Category != "orphan" || it.Instance != "loomy" {
		t.Fatalf("pid601=%+v, want orphan/loomy", it)
	}
	// 运行中实例 / 管理器 / 网关 / 空命令行 / 宿主本体 → 不列出。
	for _, pid := range []int{200, 400, 401, 500, 700} {
		if _, ok := byPID[pid]; ok {
			t.Fatalf("pid%d should not be listed", pid)
		}
	}
}

// TestPortFromCmd 端口提取。
func TestPortFromCmd(t *testing.T) {
	cases := []struct {
		cmd  string
		want uint16
	}{
		{`"x\opencode2api.exe" -port 40130 -config "y.json"`, 40130},
		{`"x\sing-box.exe" run -c "y.json"`, 0},
		{`-port 0`, 0},
		{`-port 99999`, 0},
		{``, 0},
	}
	for _, c := range cases {
		if got := portFromCmd(c.cmd); got != c.want {
			t.Fatalf("portFromCmd(%q)=%d, want %d", c.cmd, got, c.want)
		}
	}
}

// TestPluginIDFromCmd 插件 id 提取（Windows 反斜杠 / 非 Windows 正斜杠 / 无 providers 段）。
func TestPluginIDFromCmd(t *testing.T) {
	cases := []struct {
		cmd  string
		want string
	}{
		{`"D:\app\providers\loomy\loomy-provider.exe" --provider-serve --port 0`, "loomy"},
		{`"/opt/app/providers/glm/glm-provider" --provider-serve --port 0`, "glm"},
		{`"x\opencode2api.exe" -port 40130 -config "y.json"`, ""},
		{``, ""},
	}
	for _, c := range cases {
		if got := pluginIDFromCmd(c.cmd); got != c.want {
			t.Fatalf("pluginIDFromCmd(%q)=%q, want %q", c.cmd, got, c.want)
		}
	}
}

// TestKillOrphansRejectsNonAppProcess：仅杀本应用进程，其余报错跳过。
func TestKillOrphansRejectsNonAppProcess(t *testing.T) {
	m := New(t.TempDir())
	res := m.KillOrphans([]int{4}) // PID 4 不是本应用进程（正常系统里）
	if len(res.Killed) != 0 {
		t.Fatalf("killed=%v, want none", res.Killed)
	}
	if len(res.Errors) != 1 {
		t.Fatalf("errors=%v, want 1", res.Errors)
	}
}
