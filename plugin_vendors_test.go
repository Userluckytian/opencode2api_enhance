// 插件式供应商 → remote 厂商装配集成测试（main 包）：
// 伪插件子进程（测试二进制自复制，PLUGIN_TEST_HELPER=ready 时经 TestMain 门控进入
// 伪供应商入口，复用 R1 的 fake_provider 模式）→ 真实 spawn → OnChange(syncPlugins)
// → 聚合器目录出现 {id}/ 前缀模型；停用 → 移出目录；重新启用 → 恢复。全部本地
// 进程间交互 + 随机端口，不触网、不占固定端口。
package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/6Kmfi6HP/opencode2api/core/aggregator"
	"github.com/6Kmfi6HP/opencode2api/core/contract"
	"github.com/6Kmfi6HP/opencode2api/core/manager/pluginprovider"
	"github.com/6Kmfi6HP/opencode2api/vendors/remote"
)

// 伪插件子进程 env（与 pluginprovider 测试同款：测试进程 env → os.Environ 继承给子进程）。
const (
	envPluginTestHelper = "PLUGIN_TEST_HELPER"
	envPluginTestID     = "PLUGIN_TEST_ID"
)

func TestMain(m *testing.M) {
	if os.Getenv(envPluginTestHelper) != "" {
		pluginChildMain()
	}
	os.Exit(m.Run())
}

// pluginChildMain 伪插件子进程入口（仅以 helper 身份运行的测试进程进入）。
// 打印就绪行 + 起 127.0.0.1 随机端口 HTTP 服务（/v1/models + /v1/chat/completions），
// 服务 goroutine 常驻防 Go runtime deadlock 自杀。
func pluginChildMain() {
	auth := os.Getenv("PLUGIN_AUTH_TOKEN")
	id := os.Getenv(envPluginTestID)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		os.Exit(3)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"data":[{"id":"pm1"},{"id":"pm2"}]}`)
		case "/v1/chat/completions":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"id":"c1","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"from-plugin"}}]}`)
		default:
			http.NotFound(w, r)
		}
	})}
	go func() { _ = srv.Serve(ln) }() // 监听 socket 已就绪，连接进内核 backlog，无竞态
	port := ln.Addr().(*net.TCPAddr).Port
	fmt.Printf(`{"state":"ready","port":%d,"auth":%q,"id":%q,"version":"9.9.9"}`+"\n", port, auth, id)
	select {} // 运行中，等待被管理器 kill
}

// installPluginBinary 把当前测试二进制复制为 providers/<id>/<entry> 并写 provider.json。
func installPluginBinary(t *testing.T, root, id string) {
	t.Helper()
	dir := filepath.Join(root, id)
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
	man := fmt.Sprintf(`{"id":%q,"name":"插件测试 %s","version":"1.0.0","api_version":1,"entry":"fake-provider.exe"}`, id, id)
	if err := os.WriteFile(filepath.Join(dir, "provider.json"), []byte(man), 0o644); err != nil {
		t.Fatal(err)
	}
}

func boolPtr(b bool) *bool { return &b }

// waitForCond 轮询等待条件成立（插件就绪/目录变化均异步，需有界等待）。
func waitForCond(t *testing.T, timeout time.Duration, what string, cond func() bool) {
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

// pluginTestEnv 装配测试的环境：独立聚合器 + 空 providersCfg（显式禁用条目，
// 防 rebuildVendors 空配置自动注册内建厂商触网）+ 插件全局状态快照。
type pluginTestEnv struct {
	pm *pluginprovider.Manager
}

func setupPluginTestEnv(t *testing.T) *pluginTestEnv {
	t.Helper()
	snapshotCatalogGen(t) // 快照/恢复 globalAgg、catalogGen、modelsCache 等
	globalAgg = aggregator.New()
	setProvidersCfg(t, []ProviderCfg{{ID: "sz-disabled", Type: "custom", Enabled: boolPtr(false)}})
	oldPm, oldPvs, oldSig := pluginMgrGlobal, pluginVendors, lastPluginSig
	t.Cleanup(func() {
		pluginMgrGlobal = oldPm
		pluginVendorsMu.Lock()
		pluginVendors = oldPvs
		pluginVendorsMu.Unlock()
		pluginSigMu.Lock()
		lastPluginSig = oldSig
		pluginSigMu.Unlock()
	})
	return &pluginTestEnv{}
}

func (e *pluginTestEnv) start(t *testing.T, providersDir string) {
	t.Helper()
	e.pm = bindPluginMgr(pluginprovider.New(pluginprovider.Config{
		ProvidersDir:   providersDir,
		StartupTimeout: 5 * time.Second,
		BackoffBase:    100 * time.Millisecond,
		BackoffCap:     500 * time.Millisecond,
		RescanInterval: 50 * time.Millisecond,
		OnChange:       syncPlugins,
	}))
	t.Cleanup(e.pm.Close)
	e.pm.Start()
}

// catalogHasPlugin 聚合目录是否含某插件的 {id}/ 前缀模型。
func catalogHasPlugin(id string, want bool) bool {
	for _, m := range globalAgg.Catalog() {
		if strings.HasPrefix(m.ID, id+"/") {
			return want
		}
	}
	return !want
}

func TestPluginVendorsAssembly(t *testing.T) {
	env := setupPluginTestEnv(t)
	t.Setenv(envPluginTestHelper, "ready")
	t.Setenv(envPluginTestID, "plug1")

	root := filepath.Join(t.TempDir(), "providers")
	installPluginBinary(t, root, "plug1")
	env.start(t, root)

	// 子进程就绪 → OnChange → rebuildVendors → 聚合器出现 plug1/ 前缀模型（Free）。
	waitForCond(t, 15*time.Second, "聚合器目录出现 plug1/ 前缀模型", func() bool {
		for _, m := range globalAgg.Catalog() {
			if m.ID == "plug1/pm1" && m.Provider == "plug1" && m.Free {
				return true
			}
		}
		return false
	})

	// remote vendor 已装配进聚合器（含端点/令牌/名称）。
	var rv *remote.Vendor
	for _, v := range globalAgg.Vendors() {
		if v.ID() == "plug1" {
			rv = v.(*remote.Vendor)
		}
	}
	if rv == nil || rv.Name() != "插件测试 plug1" {
		t.Fatalf("remote vendor 未装配或名称不符: %v", globalAgg.Vendors())
	}

	// 桥接 chat：raw body 透传 → 子进程（模型前缀剥除由 remote 处理，响应透传）。
	raw := `{"model":"plug1/pm1","messages":[{"role":"user","content":"hello"}]}`
	msg := &contract.Message{Model: "plug1/pm1", Extra: map[string]any{remoteKeyRawBody: []byte(raw)}}
	reply, err := rv.Chat(context.Background(), msg)
	if err != nil {
		t.Fatalf("plugin vendor Chat: %v", err)
	}
	if reply.Status != http.StatusOK || !strings.Contains(string(reply.Body), "from-plugin") {
		t.Fatalf("plugin vendor chat: status=%d body=%s", reply.Status, reply.Body)
	}

	// 停用 → 模型移出聚合目录（注销路径）。
	if _, err := env.pm.Toggle("plug1", false); err != nil {
		t.Fatalf("Toggle(false): %v", err)
	}
	waitForCond(t, 15*time.Second, "停用后聚合目录移除 plug1/ 模型", func() bool {
		return catalogHasPlugin("plug1", false)
	})

	// 重新启用 → 模型恢复。
	if _, err := env.pm.Toggle("plug1", true); err != nil {
		t.Fatalf("Toggle(true): %v", err)
	}
	waitForCond(t, 15*time.Second, "重新启用后聚合目录恢复 plug1/ 模型", func() bool {
		return catalogHasPlugin("plug1", true)
	})
}

// remoteKeyRawBody 与 vendors/remote 内部键同值（测试注入 raw body 用）。
const remoteKeyRawBody = "_oc_raw_body"
