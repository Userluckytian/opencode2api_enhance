// opencode2api 管理器主进程。
//
// v2.0.0 架构（三进程）：
//
//	40000  本进程 —— 管理面板 + /api/admin/*（控制面，不代理推理）
//	40080  opencode2api-win 引擎 —— OpenCode 免费层（独立进程，本进程托管）
//	50080  供应商服务 —— 自定义源 + 插件供应商（本进程托管）
//
// 推理请求只走 40080 / 50080；40000 只负责配置、启停、健康、日志与统计。
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/6Kmfi6HP/opencode2api/core/manager"
	pluginprovider "github.com/6Kmfi6HP/opencode2api/core/manager/pluginprovider"
)

// 构建期注入（-ldflags "-X main.version=..."）。
var (
	version = "dev"
	branch  = ""
	commit  = ""
	date    = ""
)

// providerServiceMode 供应商服务模式：只装配自定义源 + 插件供应商。
var providerServiceMode bool

// noPlugins 禁止本进程启动插件子进程。
var noPlugins bool

func versionString() string {
	return fmt.Sprintf("opencode2api %s (branch=%s, commit=%s, built=%s)", version, branch, commit, date)
}

// frontendDistDir 返回前端构建产物目录（缺失返回空串）。
func frontendDistDir() string {
	exeDir := "."
	if p, e := os.Executable(); e == nil {
		exeDir = filepath.Dir(p)
	}
	candidates := []string{
		filepath.Join(exeDir, "dist"),
		filepath.Join(exeDir, "..", "dist"),
		"dist",
	}
	for _, dir := range candidates {
		if fi, err := os.Stat(filepath.Join(dir, "index.html")); err == nil && !fi.IsDir() {
			return dir
		}
	}
	return ""
}

// isTopLevelStatic 顶层静态资源放行判定（public/ 产物）。
func isTopLevelStatic(p string) bool {
	switch p {
	case "/app-icon.png", "/favicon.ico", "/robots.txt":
		return true
	}
	return false
}

func main() {
	// 子命令：doctor（环境自检）。
	if len(os.Args) > 1 && os.Args[1] == "doctor" {
		os.Exit(runDoctor(os.Args[2:]))
	}

	var showVersion bool
	flag.StringVar(&port, "port", "40000", "管理面板端口")
	flag.StringVar(&configPath, "config", "config.json", "配置文件路径")
	flag.StringVar(&adminPassword, "password", "", "管理面板密码（默认空 = 不启用登录验证）")
	flag.BoolVar(&debugMode, "debug", false, "启用调试日志")
	flag.BoolVar(&providerServiceMode, "provider-service", false, "供应商服务模式（只装配自定义源 + 插件供应商）")
	flag.BoolVar(&noPlugins, "no-plugins", false, "不启动插件式供应商子进程")
	flag.BoolVar(&callLogFlag, "call-log", false, "启用调用日志写盘")
	flag.StringVar(&logLevel, "log-level", "info", "日志级别: debug/info/warn/error")
	flag.StringVar(&logFile, "log-file", "", "日志文件路径（留空输出到 stdout）")
	var listenAddr string
	flag.StringVar(&listenAddr, "listen", "", "监听地址（默认 :<port> 全接口）")
	flag.BoolVar(&showVersion, "version", false, "显示版本信息")
	flag.Parse()

	initLogger()

	if showVersion {
		fmt.Println(versionString())
		return
	}

	cfg := loadConfig(configPath)
	applyConfig(cfg)
	if configStartupRewriteNeeded(configPath) {
		if err := saveConfig(configPath, cfg); err != nil {
			slog.Warn("failed to save config", "path", configPath, "error", err)
		}
	}
	startConfigWatcher(configPath)

	loadTokenStats()
	initCallLog()
	callLogEnabled = callLogFlag || providerServiceMode
	slog.Info("config loaded", "path", configPath)

	globalAgg = newAggregator()
	chatRouterVar = newChatRouter(globalAgg)
	startInitialCatalogRefresh()
	startModelRefresh()

	if providerServiceMode {
		runProviderServiceMode(port, listenAddr)
		return
	}

	// ---- 管理进程（40000）----
	slog.Info("manager starting", "port", port, "aliases", len(modelAlias))

	managerInst := manager.New("")
	managerInst.SetRunner(manager.NewRealRunner())

	// 供应商服务（50080）：自定义源 + 插件的唯一宿主。
	providerSvc := manager.NewProviderService(managerInst, configPath)
	if err := providerSvc.Start(manager.NewRealRunner()); err != nil {
		slog.Warn("provider service unavailable", "error", err)
	}
	go providerSvc.Watchdog(manager.NewRealRunner())
	defer providerSvc.Close()

	// 免费层引擎（40080）：外部程序，托管启停 + 看门狗。
	// 必须在后台启动：WARP 开启时引擎要等首次出口注册（上游最长 90s）才监听，
	// 同步等待会把管理面板（40000）的监听一起拖后，面板表现为"打不开"。
	engineRunner := manager.NewRealRunner()
	go func() {
		if err := managerInst.StartFreeTierEngine(engineRunner); err != nil {
			slog.Warn("free-tier engine unavailable", "error", err, "hint", "把 opencode2api-win 放到 bin/ 目录")
		} else {
			slog.Info("free-tier engine started")
		}
	}()
	go managerInst.WatchdogFreeTierEngine(engineRunner)

	mux := http.NewServeMux()
	registerHTTPRoutes(mux, managerInst, nil, providerSvc)

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

// withRecover 全局 panic 兜底：记录堆栈并返回 500 JSON。
func withRecover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("handler panic", "panic", rec, "path", r.URL.Path)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":"internal error: handler panic"}`))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// runProviderServiceMode 供应商服务进程（-provider-service）：
// 只注册模型端点 + 插件管理 API，不装配管理器/实例/引擎。
func runProviderServiceMode(port, listenAddr string) {
	mux := http.NewServeMux()
	// v2.0.0: 插件厂商必须绑定全局引用，否则 syncPlugins 直接 return，
	// 插件供应商永远不会注册进聚合器（表现为 /v1/models 无插件模型）。
	pluginMgr := bindPluginMgr(pluginprovider.New(pluginprovider.Config{
		OnChange:       syncPlugins,
		OnModelRefresh: onPluginModelRefresh,
	}))
	pluginMgr.Start()
	defer pluginMgr.Close()
	// 本进程是真正服务自定义源的一方：定时目录刷新与活性探测都必须在这里跑，
	// 否则管理面板的 key 冷却/熔断徽标永远停在「全部可用」。
	startModelRefresh()
	startCustomProbeLoop()

	mux.HandleFunc("/v1/chat/completions", loggingMiddleware(apiKeyAuthMiddleware(chatCompletionsHandler)))
	mux.HandleFunc("/v1/responses", loggingMiddleware(apiKeyAuthMiddleware(responsesHandler)))
	mux.HandleFunc("/v1/messages", loggingMiddleware(apiKeyAuthMiddleware(claudeMessagesHandler)))
	mux.HandleFunc("/v1/models", loggingMiddleware(apiKeyAuthMiddleware(listModelsHandler)))
	mux.HandleFunc("/api/reset-stats", loggingMiddleware(apiKeyAuthMiddleware(resetStatsHandler)))
	mux.HandleFunc("/api/clear-call-log", loggingMiddleware(apiKeyAuthMiddleware(clearCallLogHandler)))
	mux.HandleFunc("/api/admin/shutdown", loggingMiddleware(apiKeyAuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		go func() {
			time.Sleep(100 * time.Millisecond)
			pluginMgr.Close()
			os.Exit(0)
		}()
	})))
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})
	mux.HandleFunc("/api/admin/plugins", loggingMiddleware(apiKeyAuthMiddleware(pluginMgr.ListHandler())))
	mux.HandleFunc("/api/admin/plugins/rescan", loggingMiddleware(apiKeyAuthMiddleware(pluginMgr.RescanHandler())))
	mux.HandleFunc("/api/admin/plugins/{id}/config", loggingMiddleware(apiKeyAuthMiddleware(pluginMgr.ConfigSaveHandler())))
	mux.HandleFunc("/api/admin/plugins/{id}/toggle", loggingMiddleware(apiKeyAuthMiddleware(pluginMgr.ToggleHandler())))
	mux.HandleFunc("/api/admin/plugins/{id}/exposed-models", loggingMiddleware(apiKeyAuthMiddleware(pluginMgr.ExposedModelsHandler())))
	mux.HandleFunc("/api/admin/plugins/{id}/refresh-models", loggingMiddleware(apiKeyAuthMiddleware(pluginMgr.RefreshModelsHandler())))
	mux.HandleFunc("/api/admin/plugins/{id}", loggingMiddleware(apiKeyAuthMiddleware(pluginMgr.DeleteHandler())))

	addr := ":" + port
	if listenAddr != "" {
		addr = listenAddr + ":" + port
	}
	slog.Info("provider service listening", "addr", addr)
	if err := http.ListenAndServe(addr, withRecover(mux)); err != nil {
		slog.Error("provider service terminated", "error", err)
		os.Exit(1)
	}
}

func providerServiceStatusHandler(p *manager.ProviderService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(p.Status())
	}
}

func providerServiceModelsRefreshHandler(p *manager.ProviderService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(p.ForceRefreshModels())
	}
}

func providerServiceStartHandler(p *manager.ProviderService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := p.Start(manager.NewRealRunner()); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(p.Status())
	}
}

func providerServiceStopHandler(p *manager.ProviderService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_ = p.Stop(manager.NewRealRunner())
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(p.Status())
	}
}

// freeTierEngineHandlers 免费层引擎（40080）状态与启停。
func freeTierEngineStatusHandler(m *manager.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSONResp(w, m.FreeTierEngineStatus())
	}
}

func freeTierEngineStartHandler(m *manager.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := m.StartFreeTierEngine(manager.NewRealRunner()); err != nil {
			writeErrResp(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSONResp(w, m.FreeTierEngineStatus())
	}
}

func freeTierEngineStopHandler(m *manager.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := m.StopFreeTierEngine(manager.NewRealRunner()); err != nil {
			writeErrResp(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSONResp(w, m.FreeTierEngineStatus())
	}
}

func freeTierEngineRestartHandler(m *manager.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := m.RestartFreeTierEngine(manager.NewRealRunner()); err != nil {
			writeErrResp(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSONResp(w, m.FreeTierEngineStatus())
	}
}

func freeTierModelsHandler(m *manager.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSONResp(w, m.FreeTierModels(r.Context()))
	}
}

func freeTierEngineConfigHandler(m *manager.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := m.WriteEngineConfig(); err != nil {
			writeErrResp(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSONResp(w, map[string]string{"status": "ok", "path": m.EngineConfigPath()})
	}
}

func writeJSONResp(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeErrResp(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// registerHTTPRoutes 注册 40000 管理面全部路由（不代理推理）。
func registerHTTPRoutes(mux *http.ServeMux, managerInst *manager.Manager, pluginMgr *pluginprovider.Manager, providerSvc *manager.ProviderService) {
	mux.HandleFunc("/login", loggingMiddleware(loginHandler))
	mux.HandleFunc("/logout", loggingMiddleware(logoutHandler))
	mux.HandleFunc("/api/reset-stats", loggingMiddleware(apiKeyAuthMiddleware(resetStatsHandler)))
	mux.HandleFunc("/api/clear-call-log", loggingMiddleware(apiKeyAuthMiddleware(clearCallLogHandler)))

	// 管理域基础
	mux.HandleFunc("/api/admin/config", loggingMiddleware(requireAuth(managerInst.ConfigGetHandler())))
	mux.HandleFunc("/api/admin/config/set", loggingMiddleware(requireAuth(managerInst.ConfigSetHandler())))
	mux.HandleFunc("/api/admin/stats", loggingMiddleware(requireAuth(managerInst.StatsHandler())))
	mux.HandleFunc("/api/admin/stats/by-day", loggingMiddleware(requireAuth(managerInst.StatsByDayHandler())))
	mux.HandleFunc("/api/admin/stats/reset", loggingMiddleware(requireAuth(managerInst.ResetStatsHandler())))
	mux.HandleFunc("/api/admin/call-log", loggingMiddleware(requireAuth(managerInst.CallLogHandler())))
	mux.HandleFunc("/api/admin/call-log/clear", loggingMiddleware(requireAuth(managerInst.ClearCallLogHandler())))
	mux.HandleFunc("/api/admin/call-log/filtered", loggingMiddleware(requireAuth(managerInst.CallLogFilteredHandler())))
	mux.HandleFunc("/api/admin/call-log/aggregate", loggingMiddleware(requireAuth(managerInst.CallLogAggregateHandler())))
	// 失败原因分布（厂商 × 错误类别 · 进程内存态，重启清零）
	mux.HandleFunc("/api/admin/fail-stats", loggingMiddleware(requireAuth(failStatsHandler)))
	mux.HandleFunc("/api/logs", loggingMiddleware(requireAuth(managerInst.LogsHandler())))
	mux.HandleFunc("/api/admin/binaries", loggingMiddleware(apiKeyAuthMiddleware(managerInst.BinariesHandler())))
	mux.HandleFunc("/api/admin/port/suggest", loggingMiddleware(requireAuth(managerInst.PortSuggestHandler())))
	mux.HandleFunc("/api/admin/port/check", loggingMiddleware(requireAuth(managerInst.PortCheckHandler())))
	mux.HandleFunc("/api/admin/autostart", loggingMiddleware(requireAuth(managerInst.AutostartGetHandler())))
	mux.HandleFunc("/api/admin/autostart/set", loggingMiddleware(requireAuth(managerInst.AutostartSetHandler())))
	mux.HandleFunc("/api/admin/data/clean", loggingMiddleware(requireAuth(managerInst.DataCleanHandler())))
	mux.HandleFunc("/api/admin/open-url", loggingMiddleware(requireAuth(managerInst.OpenURLHandler())))
	mux.HandleFunc("/api/config/history", loggingMiddleware(requireAuth(configHistoryHandler)))
	mux.HandleFunc("/api/config/effective", loggingMiddleware(requireAuth(configEffectiveHandler)))

	// 免费层引擎（40080）
	mux.HandleFunc("/api/admin/free-tier/status", loggingMiddleware(requireAuth(freeTierEngineStatusHandler(managerInst))))
	mux.HandleFunc("/api/admin/free-tier/start", loggingMiddleware(requireAuth(freeTierEngineStartHandler(managerInst))))
	mux.HandleFunc("/api/admin/free-tier/stop", loggingMiddleware(requireAuth(freeTierEngineStopHandler(managerInst))))
	mux.HandleFunc("/api/admin/free-tier/restart", loggingMiddleware(requireAuth(freeTierEngineRestartHandler(managerInst))))
	mux.HandleFunc("/api/admin/free-tier/config", loggingMiddleware(requireAuth(freeTierEngineConfigHandler(managerInst))))
	// 免费层可用模型清单：合并引擎 /v1/models 与它自写的 availability 探测结果
	mux.HandleFunc("/api/admin/free-tier/models", loggingMiddleware(requireAuth(freeTierModelsHandler(managerInst))))
	// 手动探测：对引擎发一次最小请求，用真实状态码判定模型可用性
	mux.HandleFunc("/api/admin/free-tier/probe", loggingMiddleware(requireAuth(modelProbeHandler(managerInst))))
	// 诊断：三端口自检 + 配置/二进制检查
	mux.HandleFunc("/api/admin/diag", loggingMiddleware(requireAuth(diagHandler(managerInst))))
	// 失败聚合：从调用日志现算「厂商 × 错误码」
	mux.HandleFunc("/api/admin/fail-aggregate", loggingMiddleware(requireAuth(failAggregateHandler())))

	// 自定义模型源
	mux.HandleFunc("/api/admin/custom-providers", loggingMiddleware(requireAuth(customProvidersHandler())))
	mux.HandleFunc("/api/admin/custom-providers/save", loggingMiddleware(requireAuth(customProvidersSaveHandler(managerInst))))
	mux.HandleFunc("/api/admin/custom-providers/test", loggingMiddleware(requireAuth(customProvidersTestHandler())))
	mux.HandleFunc("/api/admin/custom-providers/probe", loggingMiddleware(requireAuth(customProvidersProbeHandler())))
	mux.HandleFunc("/api/admin/custom-providers/reset-keys", loggingMiddleware(requireAuth(customProvidersResetKeysHandler())))
	mux.HandleFunc("/api/admin/custom-providers/clear", loggingMiddleware(requireAuth(customProvidersClearHandler(managerInst))))

	// 插件式供应商：由供应商服务（50080）唯一托管，管理进程只做反代。
	if pluginMgr != nil {
		mux.HandleFunc("/api/admin/plugins", loggingMiddleware(requireAuth(pluginMgr.ListHandler())))
		mux.HandleFunc("/api/admin/plugins/rescan", loggingMiddleware(requireAuth(pluginMgr.RescanHandler())))
		mux.HandleFunc("/api/admin/plugins/{id}/config", loggingMiddleware(requireAuth(pluginMgr.ConfigSaveHandler())))
		mux.HandleFunc("/api/admin/plugins/{id}/toggle", loggingMiddleware(requireAuth(pluginMgr.ToggleHandler())))
		mux.HandleFunc("/api/admin/plugins/{id}/exposed-models", loggingMiddleware(requireAuth(pluginMgr.ExposedModelsHandler())))
		mux.HandleFunc("/api/admin/plugins/{id}/refresh-models", loggingMiddleware(requireAuth(pluginMgr.RefreshModelsHandler())))
		mux.HandleFunc("/api/admin/plugins/{id}", loggingMiddleware(requireAuth(pluginMgr.DeleteHandler())))
	} else if providerSvc != nil {
		proxy := providerSvc.ProxyHandler()
		mux.Handle("/api/admin/plugins", loggingMiddleware(requireAuth(proxy.ServeHTTP)))
		mux.Handle("/api/admin/plugins/", loggingMiddleware(requireAuth(proxy.ServeHTTP)))
	}

	// 供应商服务状态 / 启停
	if providerSvc != nil {
		mux.HandleFunc("/api/admin/provider-service", loggingMiddleware(requireAuth(providerServiceStatusHandler(providerSvc))))
		mux.HandleFunc("/api/admin/provider-service/start", loggingMiddleware(requireAuth(providerServiceStartHandler(providerSvc))))
		mux.HandleFunc("/api/admin/provider-service/stop", loggingMiddleware(requireAuth(providerServiceStopHandler(providerSvc))))
		mux.HandleFunc("/api/admin/provider-service/models/refresh", loggingMiddleware(requireAuth(providerServiceModelsRefreshHandler(providerSvc))))
	}

	mux.HandleFunc("/api/admin/auto-model", loggingMiddleware(requireAuth(managerInst.AutoModelConfigHandler())))

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})

	// 前端静态托管
	if distDir := frontendDistDir(); distDir != "" {
		mux.Handle("/assets/", http.StripPrefix("/assets/", http.FileServer(http.Dir(filepath.Join(distDir, "assets")))))
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
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/" {
				http.Error(w, "前端资源缺失：请确保 dist/ 存在（Web/Docker 需打包前端）", http.StatusNotFound)
				return
			}
			http.NotFound(w, r)
		})
	}
}

// runDoctor 环境自检（子命令 doctor）。
func runDoctor(args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "以 JSON 输出")
	_ = fs.Parse(args)

	type check struct {
		Name   string `json:"name"`
		OK     bool   `json:"ok"`
		Detail string `json:"detail,omitempty"`
	}
	checks := []check{
		{Name: "go_runtime", OK: true, Detail: runtime.Version() + " " + runtime.GOOS + "/" + runtime.GOARCH},
	}
	// 免费层引擎二进制
	binName := "opencode2api-win"
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	mgr := manager.New("")
	binPath := mgr.EngineBinPath()
	if fi, err := os.Stat(binPath); err == nil && !fi.IsDir() {
		checks = append(checks, check{Name: "free_tier_engine", OK: true, Detail: binPath})
	} else {
		checks = append(checks, check{Name: "free_tier_engine", OK: false, Detail: "未安装: " + binPath})
	}
	// 配置文件
	if _, err := os.Stat(configPath); err == nil {
		checks = append(checks, check{Name: "config", OK: true, Detail: configPath})
	} else {
		checks = append(checks, check{Name: "config", OK: false, Detail: "缺失: " + configPath})
	}

	if *asJSON {
		data, _ := json.MarshalIndent(checks, "", "  ")
		fmt.Println(string(data))
	} else {
		for _, c := range checks {
			mark := "OK  "
			if !c.OK {
				mark = "FAIL"
			}
			fmt.Printf("[%s] %-20s %s\n", mark, c.Name, c.Detail)
		}
	}
	for _, c := range checks {
		if !c.OK {
			return 1
		}
	}
	return 0
}

// drainAndClose 读完并关闭响应体（复用连接）。
func drainAndClose(r io.ReadCloser) {
	if r == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(r, 1<<20))
	_ = r.Close()
}

// runCommand 执行外部命令并返回合并输出（供调试/诊断使用）。
func runCommand(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var sb strings.Builder
	cmd.Stdout = &sb
	cmd.Stderr = &sb
	err := cmd.Run()
	return sb.String(), err
}

// scanLines 逐行扫描字符串（供日志解析复用）。
func scanLines(s string, fn func(line string)) {
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		fn(sc.Text())
	}
}
