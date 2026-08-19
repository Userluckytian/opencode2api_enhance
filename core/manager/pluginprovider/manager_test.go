// 插件管理器场景测试：全部用 t.TempDir 独立 providers/ 目录 + 伪子进程（测试二进制
// 自复制重执行），httptest 随机端口，不触网、不碰真实 providers/、不占用固定端口。
package pluginprovider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// harness 独立测试环境：providers 根目录 + 短超时/短退避配置。
type harness struct {
	t   *testing.T
	pm  *Manager
	dir string
}

func newHarness(t *testing.T, cfg Config) *harness {
	t.Helper()
	if cfg.ProvidersDir == "" {
		cfg.ProvidersDir = filepath.Join(t.TempDir(), "providers")
	}
	if cfg.StartupTimeout == 0 {
		cfg.StartupTimeout = 3 * time.Second
	}
	if cfg.BackoffBase == 0 {
		cfg.BackoffBase = 100 * time.Millisecond
	}
	if cfg.BackoffCap == 0 {
		cfg.BackoffCap = 500 * time.Millisecond
	}
	if cfg.RescanInterval == 0 {
		cfg.RescanInterval = 50 * time.Millisecond
	}
	pm := New(cfg)
	h := &harness{t: t, pm: pm, dir: cfg.ProvidersDir}
	t.Cleanup(func() { pm.Close() })
	return h
}

// installPlugin 把当前测试二进制复制为 providers/<id>/<entry> 并写入 provider.json。
func (h *harness) installPlugin(id, entry, extraJSON string) string {
	h.t.Helper()
	if entry == "" {
		entry = "fake-provider.exe"
	}
	dir := filepath.Join(h.dir, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		h.t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		h.t.Fatal(err)
	}
	data, err := os.ReadFile(self)
	if err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, entry), data, 0o755); err != nil {
		h.t.Fatal(err)
	}
	man := fmt.Sprintf(`{"id":%q,"name":"测试供应商 %s","version":"1.0.0","api_version":1,"entry":%q%s}`,
		id, id, entry, extraJSON)
	if err := os.WriteFile(filepath.Join(dir, "provider.json"), []byte(man), 0o644); err != nil {
		h.t.Fatal(err)
	}
	return dir
}

// setHelper 指定伪子进程行为（env 经 os.Environ 继承给插件子进程）。
func setHelper(t *testing.T, mode, id string) {
	t.Helper()
	t.Setenv(envHelperMode, mode)
	t.Setenv(envHelperID, id)
}

func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("等待超时：%s", what)
}

func waitStatus(t *testing.T, pm *Manager, id, want string, timeout time.Duration) View {
	t.Helper()
	waitFor(t, timeout, fmt.Sprintf("插件 %s 状态 = %s", id, want), func() bool {
		return pm.View(id).Status == want
	})
	return pm.View(id)
}

func waitGone(t *testing.T, pm *Manager, id string, timeout time.Duration) {
	t.Helper()
	waitFor(t, timeout, fmt.Sprintf("插件 %s 从列表移除", id), func() bool {
		return pm.View(id).ID == ""
	})
}

// waitStableRunning 轮询等待插件稳定在 running（替代固定 time.Sleep）：
// 重启/启停后的短暂 starting 窗口在全量测试负载下可能超过固定时长，
// 改为在窗口内持续轮询直到收敛，超时失败。
func waitStableRunning(t *testing.T, pm *Manager, id string, timeout time.Duration) {
	t.Helper()
	waitFor(t, timeout, fmt.Sprintf("插件 %s 稳定运行（running）", id), func() bool {
		return pm.View(id).Status == StatusRunning
	})
}

func waitDead(t *testing.T, pid int, timeout time.Duration) {
	t.Helper()
	if pid <= 0 {
		t.Fatal("waitDead: 无效 pid")
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !pidAlive(pid) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("进程 %d 应已退出", pid)
}

// removeWithRetry 删除目录（带重试）：扫描协程瞬读 provider.json 时 Windows 拒绝
// 删除被打开的文件（Go 默认共享模式不含 FILE_SHARE_DELETE），读窗口为微秒级。
func removeWithRetry(t *testing.T, path string) {
	t.Helper()
	var err error
	for i := 0; i < 10; i++ {
		err = os.RemoveAll(path)
		if err == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("RemoveAll(%s) 失败: %v", path, err)
}

// ---------------------------------------------------------------- 场景

// 发现 → 就绪 → 注册（URL/模型数/pid/全文回显）。
func TestPluginDiscoveryReady(t *testing.T) {
	setHelper(t, "ready", "demo")
	h := newHarness(t, Config{})
	h.installPlugin("demo", "demo-provider.exe", "")
	h.pm.Start()

	v := waitStatus(t, h.pm, "demo", StatusRunning, 5*time.Second)
	if v.PID <= 0 {
		t.Fatalf("pid = %d", v.PID)
	}
	if v.Name != "测试供应商 demo" || v.Version != "1.0.0" {
		t.Fatalf("view = %v", v)
	}
	if !strings.HasPrefix(v.URL, "http://127.0.0.1:") {
		t.Fatalf("url = %q", v.URL)
	}
	if v.Path == "" || v.ProviderJSON == "" {
		t.Fatalf("path/provider_json 应回显: %v", v)
	}
	waitFor(t, 5*time.Second, "模型数 = 2", func() bool { return h.pm.View("demo").Models == 2 })
}

// need_config：待配置态，不注册，子进程存活。
func TestPluginNeedConfig(t *testing.T) {
	setHelper(t, "need_config", "cfg1")
	h := newHarness(t, Config{})
	h.installPlugin("cfg1", "", "")
	h.pm.Start()

	v := waitStatus(t, h.pm, "cfg1", StatusNeedCfg, 5*time.Second)
	if v.PID <= 0 {
		t.Fatal("need_config 子进程应存活")
	}
	if !strings.Contains(v.LastError, "provider_private_configs") {
		t.Fatalf("need_config 应带 hint，last_error = %q", v.LastError)
	}
}

// need_config → 后续补打 ready：宿主必须持续消费 stdout 行流才能捕获该就绪行
// （设计文档 §4.1；R5 回归——修复前漏掉此转换，配置补齐后插件永不注册）。
func TestPluginNeedConfigThenReady(t *testing.T) {
	setHelper(t, "need_then_ready", "cfg2")
	h := newHarness(t, Config{})
	h.installPlugin("cfg2", "", "")
	h.pm.Start()

	v := waitStatus(t, h.pm, "cfg2", StatusNeedCfg, 5*time.Second)
	if v.PID <= 0 {
		t.Fatal("need_config 子进程应存活")
	}
	// 子进程 800ms 后补打 ready 行：宿主应消费到并转 running。
	v = waitStatus(t, h.pm, "cfg2", StatusRunning, 5*time.Second)
	if v.URL == "" {
		t.Fatalf("转 running 后应有 url，got %+v", v)
	}
	if v.Models != 2 { // fake provider /v1/models 返回 2 个模型
		t.Fatalf("模型数 = %d，期望 2", v.Models)
	}
}

// fatal：启动失败 → 面板异常；退避重启后仍 fatal（状态保持 error）。
func TestPluginFatal(t *testing.T) {
	setHelper(t, "fatal", "f1")
	h := newHarness(t, Config{BackoffBase: 100 * time.Millisecond, BackoffCap: 300 * time.Millisecond})
	h.installPlugin("f1", "", "")
	h.pm.Start()

	v := waitStatus(t, h.pm, "f1", StatusError, 5*time.Second)
	if !strings.Contains(v.LastError, "缺少关键配置") {
		t.Fatalf("last_error = %q", v.LastError)
	}
	// 退避重启后仍 fatal（中间会短暂出现 starting，容错轮询）。
	waitFor(t, 5*time.Second, "fatal 重启后保持 error", func() bool {
		v := h.pm.View("f1")
		return v.Status == StatusError && strings.Contains(v.LastError, "缺少关键配置")
	})
}

// 启动超时（伪子进程不输出就绪行）→ 启动失败 + 子进程被清理。
func TestPluginStartupTimeout(t *testing.T) {
	setHelper(t, "slow", "slow1")
	h := newHarness(t, Config{StartupTimeout: 300 * time.Millisecond})
	h.installPlugin("slow1", "", "")
	h.pm.Start()

	v := waitStatus(t, h.pm, "slow1", StatusError, 5*time.Second)
	if !strings.Contains(v.LastError, "启动超时") {
		t.Fatalf("last_error = %q", v.LastError)
	}
}

// 就绪行令牌不匹配（伪造就绪行）→ 拒绝。
func TestPluginBadAuth(t *testing.T) {
	setHelper(t, "bad_auth", "ba1")
	h := newHarness(t, Config{})
	h.installPlugin("ba1", "", "")
	h.pm.Start()

	v := waitStatus(t, h.pm, "ba1", StatusError, 5*time.Second)
	if !strings.Contains(v.LastError, "令牌") {
		t.Fatalf("last_error = %q", v.LastError)
	}
}

// 就绪行 id 与目录不一致 → 拒绝。
func TestPluginWrongID(t *testing.T) {
	setHelper(t, "wrong_id", "w1")
	h := newHarness(t, Config{})
	h.installPlugin("w1", "", "")
	h.pm.Start()

	v := waitStatus(t, h.pm, "w1", StatusError, 5*time.Second)
	if !strings.Contains(v.LastError, "id") {
		t.Fatalf("last_error = %q", v.LastError)
	}
}

// 崩溃 → 指数退避重启（多次就绪后崩溃，restart_count 增长）。
func TestPluginCrashRestartBackoff(t *testing.T) {
	setHelper(t, "crash", "cr1")
	h := newHarness(t, Config{BackoffBase: 100 * time.Millisecond, BackoffCap: 300 * time.Millisecond})
	h.installPlugin("cr1", "", "")
	h.pm.Start()

	waitFor(t, 8*time.Second, "restart_count >= 2", func() bool {
		return h.pm.View("cr1").RestartCount >= 2
	})
	if h.pm.View("cr1").Status != StatusError && h.pm.View("cr1").Status != StatusRunning {
		t.Fatalf("崩溃循环中状态应 error/running: %v", h.pm.View("cr1"))
	}
}

// 启停：停进程 + 注销，不删文件；再启用拉起（新 pid）。
func TestPluginToggle(t *testing.T) {
	setHelper(t, "ready", "tg1")
	h := newHarness(t, Config{})
	h.installPlugin("tg1", "", "")
	h.pm.Start()

	v := waitStatus(t, h.pm, "tg1", StatusRunning, 5*time.Second)
	pid1 := v.PID

	v, err := h.pm.Toggle("tg1", false)
	if err != nil {
		t.Fatal(err)
	}
	if v.Status != StatusDisabled {
		t.Fatalf("停用后 status = %s", v.Status)
	}
	waitDead(t, pid1, 3*time.Second)
	if _, err := os.Stat(filepath.Join(h.dir, "tg1", "provider.json")); err != nil {
		t.Fatalf("停用不应删文件: %v", err)
	}

	v, err = h.pm.Toggle("tg1", true)
	if err != nil {
		t.Fatal(err)
	}
	v = waitStatus(t, h.pm, "tg1", StatusRunning, 5*time.Second)
	if v.PID == pid1 {
		t.Fatalf("重新启用后 pid 应变化: %d", v.PID)
	}
	// 重新启用后应稳定运行（停用期的退出通知不得触发误判重启）。
	waitStableRunning(t, h.pm, "tg1", 3*time.Second)
}

// 删除：停进程 + 整目录删除。
func TestPluginDelete(t *testing.T) {
	setHelper(t, "ready", "del1")
	h := newHarness(t, Config{})
	dir := h.installPlugin("del1", "", "")
	h.pm.Start()

	v := waitStatus(t, h.pm, "del1", StatusRunning, 5*time.Second)
	if err := h.pm.Delete("del1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("目录应被删除，stat err = %v", err)
	}
	waitDead(t, v.PID, 3*time.Second)
	if len(h.pm.Views()) != 0 {
		t.Fatalf("删除后列表应空: %v", h.pm.Views())
	}
}

// 手动 rescan：外部新增目录后立即发现（关闭自动扫描以隔离变量）。
func TestPluginRescan(t *testing.T) {
	setHelper(t, "ready", "r1")
	h := newHarness(t, Config{RescanInterval: 0}) // 关闭自动扫描
	h.installPlugin("r1", "", "")
	h.pm.Start()

	if len(h.pm.Views()) != 1 {
		t.Fatalf("start views = %v", h.pm.Views())
	}
	setHelper(t, "ready", "r2")
	h.installPlugin("r2", "", "")
	if len(h.pm.Views()) != 1 {
		t.Fatalf("未 rescan 前不应发现 r2: %v", h.pm.Views())
	}
	h.pm.Rescan()
	v := waitStatus(t, h.pm, "r2", StatusRunning, 5*time.Second)
	if v.ID != "r2" {
		t.Fatalf("rescan 后应发现 r2: %v", v)
	}
}

// 目录 watcher：自动发现新增；目录被移除 → 停进程并注销。
func TestPluginWatcherDiscoverAndRemove(t *testing.T) {
	setHelper(t, "ready", "w1")
	h := newHarness(t, Config{RescanInterval: 50 * time.Millisecond})
	h.pm.Start()

	h.installPlugin("w1", "", "") // 先启动再放目录 → 由 watcher 发现
	v := waitStatus(t, h.pm, "w1", StatusRunning, 5*time.Second)

	// 移除目录：先停进程——Windows 上运行中的 exe 文件被锁定，无法直接删除。
	if _, err := h.pm.Toggle("w1", false); err != nil {
		t.Fatal(err)
	}
	waitDead(t, v.PID, 3*time.Second)
	removeWithRetry(t, filepath.Join(h.dir, "w1"))
	waitGone(t, h.pm, "w1", 5*time.Second)
}

// 扫描只认目录 + provider.json：忽略普通文件与无清单目录。
func TestPluginScanIgnoresNonDirs(t *testing.T) {
	setHelper(t, "ready", "ok1")
	h := newHarness(t, Config{})
	if err := os.MkdirAll(h.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.dir, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(h.dir, "empty-dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	h.installPlugin("ok1", "", "")
	h.pm.Start()

	waitStatus(t, h.pm, "ok1", StatusRunning, 5*time.Second)
	views := h.pm.Views()
	if len(views) != 1 || views[0].ID != "ok1" {
		t.Fatalf("views = %v", views)
	}
}

// 非法清单：面板告警不拉起；修复保存后自动拉起。
func TestPluginInvalidManifestRecover(t *testing.T) {
	h := newHarness(t, Config{})
	dir := filepath.Join(h.dir, "bad1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// api_version 不兼容 → 拒绝加载。
	if err := os.WriteFile(filepath.Join(dir, "provider.json"),
		[]byte(`{"id":"bad1","api_version":2,"entry":"x.exe"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	h.pm.Start()
	v := waitStatus(t, h.pm, "bad1", StatusError, 5*time.Second)
	if !strings.Contains(v.LastError, "api_version") {
		t.Fatalf("last_error = %q", v.LastError)
	}

	// 修复清单（合法 entry）→ 保存 → 自动拉起。
	setHelper(t, "ready", "bad1")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fake-provider.exe"), data, 0o755); err != nil {
		t.Fatal(err)
	}
	fixed := `{"id":"bad1","name":"修复后","version":"1.0.0","api_version":1,"entry":"fake-provider.exe"}`
	if err := h.pm.SaveConfig("bad1", []byte(fixed)); err != nil {
		t.Fatal(err)
	}
	v = waitStatus(t, h.pm, "bad1", StatusRunning, 5*time.Second)
	if v.Name != "修复后" {
		t.Fatalf("view = %v", v)
	}
}

// 配置保存：校验拒绝（非法 JSON / id 不一致 / entry 不存在）、原子写盘、
// 仅私有配置变更不重启、entry 变更重启。
func TestPluginSaveConfig(t *testing.T) {
	setHelper(t, "ready", "sv1")
	h := newHarness(t, Config{})
	dir := h.installPlugin("sv1", "fake-provider.exe", "")
	h.pm.Start()
	waitStatus(t, h.pm, "sv1", StatusRunning, 5*time.Second)
	pidBefore := h.pm.View("sv1").PID

	if err := h.pm.SaveConfig("sv1", []byte("{bad")); err == nil {
		t.Fatal("非法 JSON 应拒绝")
	}
	if err := h.pm.SaveConfig("sv1", []byte(`{"id":"other","api_version":1,"entry":"fake-provider.exe"}`)); err == nil {
		t.Fatal("id 与目录名不一致应拒绝")
	}
	if err := h.pm.SaveConfig("sv1", []byte(`{"id":"sv1","api_version":1,"entry":"nope.exe"}`)); err == nil {
		t.Fatal("entry 指向不存在的文件应拒绝")
	}

	newMan := `{"id":"sv1","name":"改名","version":"2.0.0","api_version":1,"entry":"fake-provider.exe","provider_private_configs":{"k":"v"}}`
	if err := h.pm.SaveConfig("sv1", []byte(newMan)); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "provider.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != newMan {
		t.Fatalf("写盘内容不一致: %s", raw)
	}
	v := h.pm.View("sv1")
	if v.Name != "改名" || v.Version != "2.0.0" {
		t.Fatalf("view = %v", v)
	}
	if v.PID != pidBefore {
		t.Fatal("仅私有配置变更不应重启子进程")
	}
	if !strings.Contains(v.ProviderJSON, "provider_private_configs") {
		t.Fatal("provider.json 全文应回显（面板编辑回填）")
	}

	// entry 变更 → 重启子进程（新 pid）。
	setHelper(t, "ready", "sv1")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "other-provider.exe"), data, 0o755); err != nil {
		t.Fatal(err)
	}
	entryChanged := `{"id":"sv1","name":"改名","version":"2.0.0","api_version":1,"entry":"other-provider.exe"}`
	if err := h.pm.SaveConfig("sv1", []byte(entryChanged)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, "entry 变更后子进程重启（新 pid）", func() bool {
		v := h.pm.View("sv1")
		return v.Status == StatusRunning && v.PID != pidBefore
	})
	// 重启后应稳定运行（旧子进程的退出通知不得触发误判重启）。
	waitStableRunning(t, h.pm, "sv1", 3*time.Second)
}

// 管理 API（httptest 直测 handler，requireAuth 由 main 装配，此处直挂）。
func TestPluginHTTPHandlers(t *testing.T) {
	setHelper(t, "ready", "http1")
	h := newHarness(t, Config{})
	h.installPlugin("http1", "", "")
	h.pm.Start()
	waitStatus(t, h.pm, "http1", StatusRunning, 5*time.Second)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/admin/plugins", h.pm.ListHandler())
	mux.HandleFunc("/api/admin/plugins/rescan", h.pm.RescanHandler())
	mux.HandleFunc("/api/admin/plugins/{id}/config", h.pm.ConfigSaveHandler())
	mux.HandleFunc("/api/admin/plugins/{id}/toggle", h.pm.ToggleHandler())
	mux.HandleFunc("/api/admin/plugins/{id}", h.pm.DeleteHandler())
	srv := httptest.NewServer(mux)
	defer srv.Close()

	get := func(path string) (int, map[string]any) {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	post := func(path, body string) (int, map[string]any) {
		resp, err := http.Post(srv.URL+path, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	del := func(path string) (int, map[string]any) {
		req, _ := http.NewRequest(http.MethodDelete, srv.URL+path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}

	// GET 列表。
	code, out := get("/api/admin/plugins")
	if code != http.StatusOK {
		t.Fatalf("list = %d", code)
	}
	plist := out["plugins"].([]any)
	if len(plist) != 1 {
		t.Fatalf("plugins = %v", plist)
	}
	p0 := plist[0].(map[string]any)
	if p0["id"] != "http1" || p0["status"] != "running" {
		t.Fatalf("view = %v", p0)
	}
	if _, hasFull := p0["provider_json"]; !hasFull {
		t.Fatal("provider_json 全文应回显")
	}

	// toggle 关闭（空 body = 翻转）。
	code, out = post("/api/admin/plugins/http1/toggle", `{}`)
	if code != http.StatusOK || out["plugin"].(map[string]any)["status"] != "disabled" {
		t.Fatalf("toggle off = %d %v", code, out)
	}
	// toggle 打开（显式 enabled）。
	code, out = post("/api/admin/plugins/http1/toggle", `{"enabled":true}`)
	if code != http.StatusOK {
		t.Fatalf("toggle on = %d %v", code, out)
	}
	waitStatus(t, h.pm, "http1", StatusRunning, 5*time.Second)

	// config：非法 JSON 拒绝；合法保存成功。
	code, out = post("/api/admin/plugins/http1/config", `{"provider_json":"{bad"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("非法 JSON 应 400: %d %v", code, out)
	}
	code, _ = post("/api/admin/plugins/http1/config",
		`{"provider_json":"{\"id\":\"http1\",\"api_version\":1,\"entry\":\"fake-provider.exe\"}"}`)
	if code != http.StatusOK {
		t.Fatalf("config save = %d", code)
	}

	// 404：未知插件。
	code, _ = post("/api/admin/plugins/nope/toggle", `{}`)
	if code != http.StatusNotFound {
		t.Fatalf("未知插件 toggle 应 404: %d", code)
	}

	// 删除。
	code, out = del("/api/admin/plugins/http1")
	if code != http.StatusOK {
		t.Fatalf("delete = %d %v", code, out)
	}
	if _, err := os.Stat(filepath.Join(h.dir, "http1")); !os.IsNotExist(err) {
		t.Fatal("目录应被删除")
	}
	code, out = get("/api/admin/plugins")
	if len(out["plugins"].([]any)) != 0 {
		t.Fatalf("删除后列表应空: %v", out)
	}

	// rescan。
	code, out = post("/api/admin/plugins/rescan", "")
	if code != http.StatusOK {
		t.Fatalf("rescan = %d", code)
	}
}

// RunningPlugins：R2 装配契约——只有 running 且端点就绪的插件进桥接集合。
func TestRunningPlugins(t *testing.T) {
	setHelper(t, "ready", "rp1")
	h := newHarness(t, Config{})
	h.installPlugin("rp1", "", "")
	h.pm.Start()
	waitStatus(t, h.pm, "rp1", StatusRunning, 5*time.Second)

	rps := h.pm.RunningPlugins()
	if len(rps) != 1 || rps[0].ID != "rp1" || rps[0].URL == "" || rps[0].Auth == "" {
		t.Fatalf("RunningPlugins = %v, want [rp1 url auth]", rps)
	}
	if !strings.HasPrefix(rps[0].URL, "http://127.0.0.1:") {
		t.Fatalf("url = %q, want 127.0.0.1 随机端口", rps[0].URL)
	}
	if rps[0].Name != "测试供应商 rp1" {
		t.Fatalf("name = %q", rps[0].Name)
	}

	// 停用 → 不再列入（厂商注销路径依据）。
	if _, err := h.pm.Toggle("rp1", false); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, h.pm, "rp1", StatusDisabled, 5*time.Second)
	if rps = h.pm.RunningPlugins(); len(rps) != 0 {
		t.Fatalf("disabled 插件不应在列: %v", rps)
	}
}

// Close 统一回收：运行中的子进程随管理器关闭被杀。
func TestPluginCloseReapsChildren(t *testing.T) {
	setHelper(t, "ready", "cl1")
	cfg := Config{ProvidersDir: filepath.Join(t.TempDir(), "providers"), StartupTimeout: 3 * time.Second}
	pm := New(cfg)
	dir := filepath.Join(cfg.ProvidersDir, "cl1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fake-provider.exe"), data, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "provider.json"),
		[]byte(`{"id":"cl1","api_version":1,"entry":"fake-provider.exe"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	pm.Start()
	v := waitStatus(t, pm, "cl1", StatusRunning, 5*time.Second)
	pm.Close()
	waitDead(t, v.PID, 3*time.Second)
}
