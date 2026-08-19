package main

import (
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"github.com/6Kmfi6HP/opencode2api/core/manager"
	"github.com/6Kmfi6HP/opencode2api/core/manager/pluginprovider"
)

// frontendDistDir 返回前端构建产物目录（存在 dist/index.html 时）。
// 查找顺序：
//   - dev/本机构建产物（cargo target 树内的 core）：优先仓库根 dist
//     （src-tauri/target/debug/bin → 仓库根 4 级上溯），exe 旁 dist（陈旧副本）仅兜底；
//   - 便携包/发布（core 不在 target 树内）：dist 固定 exe 旁 bin\dist；
//   - Tauri deb/AppImage：resources 装在 /usr/lib/<app>/bin/dist（exe 在 /usr/bin）；
//   - cwd 兜底（web-dev/headless：cwd=仓库根 → dist；Docker：cwd=/app → /app/dist）。
func frontendDistDir() string {
	var cands []string
	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		sep := string(filepath.Separator)
		inTargetTree := strings.Contains(exeDir, sep+"target"+sep+"debug") ||
			strings.Contains(exeDir, sep+"target"+sep+"release")
		if inTargetTree {
			// dev：src-tauri/target/debug/bin → 仓库根（4 级上溯）+ dist；
			// exe 旁 dist（构建产物）放其后再兜底，避免陈旧副本抢跑新面板。
			cands = append(cands,
				filepath.Join(exeDir, "..", "..", "..", "..", "dist"),
				filepath.Join(exeDir, "dist"),
			)
		} else {
			// 便携包/发布：dist 固定 exe 旁 bin\dist。
			cands = append(cands, filepath.Join(exeDir, "dist"))
			// Tauri Linux deb/AppImage：resources 装在 /usr/lib/<app>/bin/dist（exe 在 /usr/bin）。
			// 仅 Linux 生效；Windows/macOS 的安装布局不同，不参与查找，避免误命中。
			if runtime.GOOS == "linux" {
				cands = append(cands,
					filepath.Join(exeDir, "..", "lib", filepath.Base(exe), "bin", "dist"),
				)
			}
		}
	}
	if wd, err := os.Getwd(); err == nil {
		cands = append(cands,
			filepath.Join(wd, "dist"),       // 常规：cwd/dist
			filepath.Join(wd, "..", "dist"), // dev：cwd=src-tauri → 仓库根
		)
	}
	for _, d := range cands {
		if _, err := os.Stat(filepath.Join(d, "index.html")); err == nil {
			return d
		}
	}
	return ""
}

// isTopLevelStatic 判断路径是否为 dist 根目录下的顶层静态文件（如 app-icon.png）。
// 仅放行单层、无目录穿越的文件名，其余路径不通过（SPA 路由由前端处理）。
func isTopLevelStatic(p string) bool {
	if len(p) < 2 || p[0] != '/' {
		return false
	}
	name := p[1:]
	if name == "" || strings.ContainsAny(name, "/\\") || strings.Contains(name, "..") {
		return false
	}
	return true
}

var httpClient = &http.Client{
	Timeout: 300 * time.Second,
	Transport: &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 20,
		IdleConnTimeout:     90 * time.Second,
	},
}

var (
	version = "v1.5.4"
	commit  = "none"
	date    = "unknown"
)

func versionString() string {
	return fmt.Sprintf("opencode2api %s (commit=%s, date=%s)", version, commit, date)
}

func main() {
	var showVersion bool
	flag.StringVar(&port, "port", "8000", "服务端口")
	flag.StringVar(&configPath, "config", "config.json", "配置文件路径")
	// 管理鉴权默认关闭（默认空密码）：当前 WebUI 无身份校验步骤，开启会 302 拦截 /api/admin/* 导致数据无法加载。
	// 需要开启时显式传 -password <密码>（同时作为 /v1 API 密钥）。
	flag.StringVar(&adminPassword, "password", "", "管理面板密码（默认空 = 不启用登录验证；设置后需经 /login 登录）")
	flag.BoolVar(&debugMode, "debug", false, "启用调试日志")
	flag.BoolVar(&gatewayMode, "gateway", false, "统一网关模式（记录节点级统计）")
	flag.BoolVar(&callLogFlag, "call-log", false, "启用调用日志写盘（实例子进程注入；-gateway 自动启用）")
	flag.StringVar(&poolQualityPath, "pool-quality", "", "实例池质量文件路径（网关子进程注入；空 = 无质量约束）")
	flag.StringVar(&logLevel, "log-level", "info", "日志级别: debug/info/warn/error")
	flag.StringVar(&logFile, "log-file", "", "日志文件路径（留空输出到 stdout）")
	var listenAddr string
	flag.StringVar(&listenAddr, "listen", "", "监听地址（默认 :<port> 全接口；headless/服务器部署可显式 127.0.0.1 收紧或 0.0.0.0 暴露）")
	flag.BoolVar(&showVersion, "version", false, "显示版本信息")
	flag.Parse()

	// L2：flag.Parse 后（poolQualityPath 已定）惰性启动质量文件后台刷新器，
	// 请求路径零读盘，质量刷新改由后台 ticker 驱动。
	startPoolQualityRefresher()

	initLogger()

	if showVersion {
		fmt.Println(versionString())
		return
	}

	cfg := loadConfig(configPath)
	applyConfig(cfg)
	if err := saveConfig(configPath, cfg); err != nil {
		slog.Warn("failed to save config", "path", configPath, "error", err)
	}
	startConfigWatcher(configPath)

	loadTokenStats()
	loadNodeStats()
	initCallLog()
	callLogEnabled = gatewayMode || callLogFlag // 网关自动记录；独享/池成员实例子进程经 -call-log 注入后同样写盘（cwd/call_log.jsonl）
	slog.Info("config loaded", "path", configPath)
	globalAgg = newAggregator()
	chatRouterVar = newChatRouter(globalAgg)
	initVendorsSignature()
	refreshModelCatalog()
	modelMu.RLock()
	nLoaded := len(modelsCache)
	modelMu.RUnlock()
	if nLoaded > 0 {
		slog.Info("models loaded", "count", nLoaded)
	}
	startModelRefresh()
	startCustomProbeLoop() // 自定义源后台活性探测（5 分钟一轮，刷新健康徽标）
	slog.Info("server starting",
		"port", port,
		"log_level", logLevel,
		"models", len(getModelIDs()),
		"aliases", len(modelAlias),
	)
	if adminPassword != "" {
		slog.Info("admin panel enabled", "url", fmt.Sprintf("http://localhost:%s/", port))
	} else {
		slog.Info("admin panel disabled (no password)")
	}
	// P4: 管理域（实例/统计/日志/配置）并入 core，Web/桌面共用一份实现。
	managerInst := manager.New("")
	// P4-3：装配实例/探针接缝（clash 节点解析 + sing-box 配置 + opencode2api 配置生成）。
	managerInst.SetSeams(&manager.SeamFuncs{
		ResolveNode: func(name string) (manager.ClashNode, bool) {
			for _, n := range managerInst.ListNodesWithGroup() {
				if n.Name == name {
					return n, true
				}
			}
			return manager.ClashNode{}, false
		},
		BuildSingbox: func(node manager.ClashNode, port uint16) ([]byte, error) {
			return manager.BuildSingboxConfigFor(node, port)
		},
		BuildOpenCfg: managerInst.BuildOpenCodeCfgFor,
		ListNodes:    managerInst.ListNodesWithGroup,
	})

	mux := http.NewServeMux()
	// R1 插件式供应商：providers/ 目录扫描 + 子进程生命周期 + 管理 API。
	// 主进程正常退出时经 Close 统一 kill 全部插件子进程（设计文档 §4.3）；
	// 强杀路径（taskkill /F / SIGKILL）无法触发，Linux 由 systemd cgroup 兜底，
	// Windows 桌面壳按进程树回收，属既有进程管理边界（详见 PLUGIN-PROVIDERS.md §9）。
	// R2：OnChange（子进程就绪/状态/增删）→ syncPlugins 重建插件厂商并入 rebuildVendors。
	// bind 必须在 Start 之前（就绪行由监督协程异步到达，先记引用再拉起）。
	pluginMgr := bindPluginMgr(pluginprovider.New(pluginprovider.Config{OnChange: syncPlugins}))
	pluginMgr.Start()
	defer pluginMgr.Close()
	registerHTTPRoutes(mux, managerInst, pluginMgr)
	addr := ":" + port
	if listenAddr != "" {
		addr = listenAddr + ":" + port
	}
	slog.Info("listening", "addr", addr)
	if err := http.ListenAndServe(addr, withRecover(mux)); err != nil {
		slog.Error("server terminated", "error", err)
		os.Exit(1)
	}
}

// withRecover 全局 panic 兜底：任何 handler panic 都会记录堆栈并返回 500 JSON，
// 而不是 net/http 默认的空 500（前端无法定位、表现为"按钮无效"）。
func withRecover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("handler panic",
					"method", r.Method,
					"path", r.URL.Path,
					"panic", rec,
					"stack", string(debug.Stack()),
				)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":"internal error: handler panic"}`))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// registerHTTPRoutes 注册全部 HTTP 路由（/v1、/api、/api/admin、/health、静态 SPA）。
// pluginMgr 为 R1 插件式供应商管理器（nil = 不注册插件路由，测试用）。
func registerHTTPRoutes(mux *http.ServeMux, managerInst *manager.Manager, pluginMgr *pluginprovider.Manager) {
	mux.HandleFunc("/v1/chat/completions", loggingMiddleware(apiKeyAuthMiddleware(chatCompletionsHandler)))
	mux.HandleFunc("/v1/responses", loggingMiddleware(apiKeyAuthMiddleware(responsesHandler)))
	mux.HandleFunc("/v1/messages", loggingMiddleware(apiKeyAuthMiddleware(claudeMessagesHandler)))
	mux.HandleFunc("/v1/models", loggingMiddleware(apiKeyAuthMiddleware(listModelsHandler)))
	mux.HandleFunc("/login", loggingMiddleware(loginHandler))
	mux.HandleFunc("/logout", loggingMiddleware(logoutHandler))
	// /api/reset-stats 保留：实例子进程/网关子进程的复位契约（stats.go ResetStats 对运行中实例 HTTP DELETE）。
	mux.HandleFunc("/api/reset-stats", loggingMiddleware(apiKeyAuthMiddleware(resetStatsHandler)))
	// /api/clear-call-log：实例子进程/网关子进程的日志清空契约（ClearCallLog 对运行中进程 HTTP DELETE）。
	mux.HandleFunc("/api/clear-call-log", loggingMiddleware(apiKeyAuthMiddleware(clearCallLogHandler)))
	// P4: 管理域并入 core（/api/admin/*，鉴权与既有 /api/* 一致；由 core/manager 实现）
	mux.HandleFunc("/api/admin/config", loggingMiddleware(requireAuth(managerInst.ConfigGetHandler())))
	mux.HandleFunc("/api/admin/config/set", loggingMiddleware(requireAuth(managerInst.ConfigSetHandler())))
	mux.HandleFunc("/api/admin/stats", loggingMiddleware(requireAuth(managerInst.StatsHandler())))
	mux.HandleFunc("/api/admin/stats/by-day", loggingMiddleware(requireAuth(managerInst.StatsByDayHandler())))
	mux.HandleFunc("/api/admin/stats/reset", loggingMiddleware(requireAuth(managerInst.ResetStatsHandler())))
	mux.HandleFunc("/api/admin/call-log", loggingMiddleware(requireAuth(managerInst.CallLogHandler())))
	mux.HandleFunc("/api/admin/call-log/clear", loggingMiddleware(requireAuth(managerInst.ClearCallLogHandler())))
	// 调用日志过滤与聚合（main 分支功能迁移 M4）。
	mux.HandleFunc("/api/admin/call-log/filtered", loggingMiddleware(requireAuth(managerInst.CallLogFilteredHandler())))
	mux.HandleFunc("/api/admin/call-log/aggregate", loggingMiddleware(requireAuth(managerInst.CallLogAggregateHandler())))
	mux.HandleFunc("/api/admin/binaries", loggingMiddleware(apiKeyAuthMiddleware(managerInst.BinariesHandler())))
	mux.HandleFunc("/api/admin/instances", loggingMiddleware(requireAuth(managerInst.InstancesHandler())))
	// P4-5：装配运行依赖（进程执行器 / 网关 / 扫描），HTTP 管理面用同一份核心。
	managerInst.SetDeps(manager.NewRealRunner(), manager.NewGateway(managerInst, 0), nil)
	// M1/T3: 订阅自动拉取后台循环（多订阅源列表，各自间隔；配置热更新无需重启）。
	managerInst.RunAllSubscriptionLoop()
	// P1: 实例池链路探活后台循环（pool_probe_enabled 生效时运行）。
	managerInst.StartPoolQualityLoop()
	// P4-5: 管理域操作面路由（/api/admin/*）。
	mux.HandleFunc("/api/admin/nodes", loggingMiddleware(requireAuth(managerInst.NodesHandler())))
	// 节点删除（main 分支功能迁移 M5；仅订阅缓存节点可删）。
	mux.HandleFunc("/api/admin/nodes/delete", loggingMiddleware(requireAuth(managerInst.NodeDeleteHandler())))
	mux.HandleFunc("/api/admin/nodes/delete-batch", loggingMiddleware(requireAuth(managerInst.NodeDeleteBatchHandler())))
	mux.HandleFunc("/api/admin/instances/add", loggingMiddleware(requireAuth(managerInst.InstancesAddHandler())))
	mux.HandleFunc("/api/admin/instances/remove", loggingMiddleware(requireAuth(managerInst.InstancesRemoveHandler())))
	mux.HandleFunc("/api/admin/instances/start", loggingMiddleware(requireAuth(managerInst.InstancesStartHandler())))
	mux.HandleFunc("/api/admin/instances/stop", loggingMiddleware(requireAuth(managerInst.InstancesStopHandler())))
	mux.HandleFunc("/api/admin/instances/refresh", loggingMiddleware(requireAuth(managerInst.InstancesRefreshHandler())))
	mux.HandleFunc("/api/admin/instances/test", loggingMiddleware(requireAuth(managerInst.InstancesTestHandler())))
	mux.HandleFunc("/api/admin/instances/batch/add", loggingMiddleware(requireAuth(managerInst.BatchAddHandler())))
	mux.HandleFunc("/api/admin/instances/batch/start", loggingMiddleware(requireAuth(managerInst.BatchStartHandler())))
	mux.HandleFunc("/api/admin/instances/batch/stop", loggingMiddleware(requireAuth(managerInst.BatchStopHandler())))
	mux.HandleFunc("/api/admin/instances/batch/delete", loggingMiddleware(requireAuth(managerInst.BatchDeleteHandler())))
	mux.HandleFunc("/api/admin/instances/join-gateway", loggingMiddleware(requireAuth(managerInst.JoinGatewayHandler())))
	mux.HandleFunc("/api/admin/port/suggest", loggingMiddleware(requireAuth(managerInst.PortSuggestHandler())))
	mux.HandleFunc("/api/admin/port/check", loggingMiddleware(requireAuth(managerInst.PortCheckHandler())))
	mux.HandleFunc("/api/admin/scan/start", loggingMiddleware(requireAuth(managerInst.ScanStartHandler())))
	mux.HandleFunc("/api/admin/scan/status", loggingMiddleware(requireAuth(managerInst.ScanStatusHandler())))
	mux.HandleFunc("/api/admin/scan/stop", loggingMiddleware(requireAuth(managerInst.ScanStopHandler())))
	mux.HandleFunc("/api/admin/autostart", loggingMiddleware(requireAuth(managerInst.AutostartGetHandler())))
	mux.HandleFunc("/api/admin/autostart/set", loggingMiddleware(requireAuth(managerInst.AutostartSetHandler())))
	// 订阅拉取与批量导入（main 分支功能迁移 M1）。
	mux.HandleFunc("/api/admin/subscribe/preview", loggingMiddleware(requireAuth(managerInst.SubscribePreviewHandler())))
	mux.HandleFunc("/api/admin/subscribe/import", loggingMiddleware(requireAuth(managerInst.SubscribeImportHandler())))
	mux.HandleFunc("/api/admin/subscribe/import-pool", loggingMiddleware(requireAuth(managerInst.SubscribeImportPoolHandler())))
	// 自定义模型源（第七页「自定义模型」）：列表 / 整表保存 / 连通测试。
	mux.HandleFunc("/api/admin/custom-providers", loggingMiddleware(requireAuth(customProvidersHandler())))
	mux.HandleFunc("/api/admin/custom-providers/save", loggingMiddleware(requireAuth(customProvidersSaveHandler(managerInst))))
	mux.HandleFunc("/api/admin/custom-providers/test", loggingMiddleware(requireAuth(customProvidersTestHandler())))
	mux.HandleFunc("/api/admin/custom-providers/probe", loggingMiddleware(requireAuth(customProvidersProbeHandler())))
	mux.HandleFunc("/api/admin/custom-providers/clear", loggingMiddleware(requireAuth(customProvidersClearHandler(managerInst))))
	// R1 插件式供应商（第七页「自定义模型」插件 tab）：列表 / 配置保存 / 启停 / 删除 / 手动重扫。
	if pluginMgr != nil {
		mux.HandleFunc("/api/admin/plugins", loggingMiddleware(requireAuth(pluginMgr.ListHandler())))
		mux.HandleFunc("/api/admin/plugins/rescan", loggingMiddleware(requireAuth(pluginMgr.RescanHandler())))
		mux.HandleFunc("/api/admin/plugins/{id}/config", loggingMiddleware(requireAuth(pluginMgr.ConfigSaveHandler())))
		mux.HandleFunc("/api/admin/plugins/{id}/toggle", loggingMiddleware(requireAuth(pluginMgr.ToggleHandler())))
		mux.HandleFunc("/api/admin/plugins/{id}", loggingMiddleware(requireAuth(pluginMgr.DeleteHandler())))
	}
	// T3: 订阅源列表管理（新增/删除/立即拉取）+ 列表查看。
	mux.HandleFunc("/api/admin/subscriptions", loggingMiddleware(requireAuth(managerInst.SubscriptionsListHandler())))
	mux.HandleFunc("/api/admin/subscriptions/count", loggingMiddleware(requireAuth(managerInst.SubscriptionsCountHandler())))
	mux.HandleFunc("/api/admin/subscriptions/add", loggingMiddleware(requireAuth(managerInst.SubscriptionsAddHandler())))
	mux.HandleFunc("/api/admin/subscriptions/delete", loggingMiddleware(requireAuth(managerInst.SubscriptionsDeleteHandler())))
	mux.HandleFunc("/api/admin/subscriptions/import", loggingMiddleware(requireAuth(managerInst.SubscriptionsImportHandler())))
	// 残留进程：探测 + 一键清除（孤儿实例 / 探针残留）。
	mux.HandleFunc("/api/admin/processes/orphans", loggingMiddleware(requireAuth(managerInst.OrphanScanHandler())))
	mux.HandleFunc("/api/admin/processes/orphans/kill", loggingMiddleware(requireAuth(managerInst.OrphanKillHandler())))
	// P1: 实例池链路质量（质量汇总视图 + 手动触发一轮探活）。
	mux.HandleFunc("/api/admin/pool/quality", loggingMiddleware(requireAuth(managerInst.PoolQualityHandler())))
	mux.HandleFunc("/api/admin/pool/quality/probe", loggingMiddleware(requireAuth(managerInst.PoolProbeTriggerHandler())))
	mux.HandleFunc("/api/admin/data/clean", loggingMiddleware(requireAuth(managerInst.DataCleanHandler())))
	mux.HandleFunc("/api/admin/gateway/status", loggingMiddleware(requireAuth(managerInst.GatewayStatusHandler())))
	mux.HandleFunc("/api/admin/gateway/route-mode", loggingMiddleware(requireAuth(managerInst.GatewayRouteModeHandler())))
	mux.HandleFunc("/api/admin/auto-model", loggingMiddleware(requireAuth(managerInst.AutoModelConfigHandler())))
	mux.HandleFunc("/api/admin/gateway/stop", loggingMiddleware(requireAuth(managerInst.GatewayStopHandler())))
	mux.HandleFunc("/api/admin/pool/restart", loggingMiddleware(requireAuth(managerInst.RestartPoolHandler())))
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})
	// P4-5: 前端静态托管。仓库构建产物 dist/「存在」时托管 SPA（Web 版），否则退回内嵌管理面板。
	if distDir := frontendDistDir(); distDir != "" {
		mux.Handle("/assets/", http.StripPrefix("/assets/", http.FileServer(http.Dir(filepath.Join(distDir, "assets")))))
		// 顶层静态资源（app-icon.png 等 public 产物）：按文件名精确放行，其余回退 SPA。
		fileServer := http.FileServer(http.Dir(distDir))
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.URL.Path == "/" || r.URL.Path == "/index.html":
				http.ServeFile(w, r, filepath.Join(distDir, "index.html"))
			case isTopLevelStatic(r.URL.Path):
				fileServer.ServeHTTP(w, r)
			default:
				http.NotFound(w, r)
			}
		})
		slog.Info("frontend dist served", "dir", distDir)
	} else {
		// 旧版内嵌管理面板已移除：无 dist/ 时不提供页面，仅提示。
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/" {
				http.Error(w, "前端资源缺失：请确保 dist/ 存在（Web/Docker 需打包前端）", http.StatusNotFound)
				return
			}
			http.NotFound(w, r)
		})
	}

}
