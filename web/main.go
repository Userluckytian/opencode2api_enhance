package main

import (
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"time"
)

//go:embed static
var staticFS embed.FS

var version = "dev"

func buildMux(app *App) http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	mux := http.NewServeMux()
	// 静态页与脚本不加缓存：管理后台迭代频繁，缓存会让用户看到旧版页面，
	// 还得手动强刷。文件本身很小，每次回源的开销可以忽略。
	mux.Handle("/", noCacheHandler(http.FileServerFS(sub)))
	mux.HandleFunc("GET /api/models", app.HandleModels)
	mux.HandleFunc("GET /api/claim-rules", app.handleClaimRules)
	mux.HandleFunc("POST /api/keys", app.HandleCreateKey)
	mux.HandleFunc("POST /api/admin/login", app.handleLogin)
	mux.Handle("GET /api/admin/overview", app.adminAuth(http.HandlerFunc(app.handleOverview)))
	mux.Handle("GET /api/admin/models", app.adminAuth(http.HandlerFunc(app.handleAdminModels)))
	mux.Handle("POST /api/admin/models", app.adminAuth(http.HandlerFunc(app.handleSaveModels)))
	mux.Handle("GET /api/admin/settings", app.adminAuth(http.HandlerFunc(app.handleGetSettings)))
	mux.Handle("PUT /api/admin/settings", app.adminAuth(http.HandlerFunc(app.handlePutSettings)))
	// 多上游管理（2026-10-28）：上游列表独立于「参数设置」，每条 base_url 单独过 SSRF 校验。
	mux.Handle("GET /api/admin/upstreams", app.adminAuth(http.HandlerFunc(app.handleListUpstreams)))
	mux.Handle("POST /api/admin/upstreams", app.adminAuth(http.HandlerFunc(app.handleCreateUpstream)))
	mux.Handle("PUT /api/admin/upstreams/{id}", app.adminAuth(http.HandlerFunc(app.handleUpdateUpstream)))
	mux.Handle("DELETE /api/admin/upstreams/{id}", app.adminAuth(http.HandlerFunc(app.handleDeleteUpstream)))
	mux.Handle("POST /api/admin/upstreams/{id}/test", app.adminAuth(http.HandlerFunc(app.handleTestUpstream)))
	mux.Handle("PUT /api/admin/password", app.adminAuth(http.HandlerFunc(app.handlePutPassword)))
	mux.Handle("GET /api/admin/keys", app.adminAuth(http.HandlerFunc(app.handleListKeys)))
	mux.Handle("POST /api/admin/keys/{id}/quota", app.adminAuth(http.HandlerFunc(app.handleSetKeyQuota)))
	mux.Handle("POST /api/admin/keys/{id}/disable", app.adminAuth(http.HandlerFunc(app.handleDisableKey)))
	mux.Handle("POST /api/admin/keys/{id}/enable", app.adminAuth(http.HandlerFunc(app.handleEnableKey)))
	mux.Handle("DELETE /api/admin/keys/{id}", app.adminAuth(http.HandlerFunc(app.handleDeleteKey)))
	mux.Handle("GET /api/admin/logs", app.adminAuth(http.HandlerFunc(app.handleLogs)))
	mux.Handle("GET /api/admin/logs/aggregate", app.adminAuth(http.HandlerFunc(app.handleLogAggregate)))
	mux.Handle("GET /api/admin/ip-policies", app.adminAuth(http.HandlerFunc(app.handleGetIPPolicies)))
	mux.Handle("PUT /api/admin/ip-policies/defaults", app.adminAuth(http.HandlerFunc(app.handlePutIPDefaults)))
	mux.Handle("PUT /api/admin/ip-policies/{ip}", app.adminAuth(http.HandlerFunc(app.handlePutIPOverride)))
	mux.Handle("DELETE /api/admin/ip-policies/{ip}", app.adminAuth(http.HandlerFunc(app.handleDeleteIPOverride)))
	mux.Handle("POST /api/admin/ip-policies/{ip}/reset", app.adminAuth(http.HandlerFunc(app.handleResetIPWindow)))
	mux.Handle("POST /api/admin/ask", app.adminAuth(http.HandlerFunc(app.handleAsk)))
	// AI 建议应用（方案 A）：AI 只提议，管理员确认后由此接口真正写入。
	mux.Handle("POST /api/admin/ask/apply", app.adminAuth(http.HandlerFunc(app.handleApplyAskSuggestion)))
	mux.Handle("GET /api/admin/ask/usage", app.adminAuth(http.HandlerFunc(app.handleAskUsage)))
	mux.Handle("GET /api/admin/audit", app.adminAuth(http.HandlerFunc(app.handleAuditList)))
	mux.Handle("GET /api/admin/claim-policy", app.adminAuth(http.HandlerFunc(app.handleGetClaimPolicy)))
	mux.Handle("PUT /api/admin/claim-policy", app.adminAuth(http.HandlerFunc(app.handlePutClaimPolicy)))
	mux.HandleFunc("/v1/", app.HandleV1)
	return mux
}

// noCacheHandler 给静态资源加上明确的禁缓存响应头。
func noCacheHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Expires", "0")
		next.ServeHTTP(w, r)
	})
}

func main() {
	dataDir := flag.String("data-dir", "./web-data", "data directory (config.json, keys.json, logs/)")
	// 监听地址收敛（finding 547 / fact 589、592）：
	// 旧实现硬编码 0.0.0.0，服务一起来就把管理后台暴露到整个网段。
	// 现在默认只监听回环；确需对外必须显式 -listen 0.0.0.0（或环境变量
	// TOKEN_BUFFET_LISTEN / config.json 的 listen 字段），让暴露面成为显式决策。
	listenFlag := flag.String("listen", "",
		"listen address (default 127.0.0.1, loopback only). Use 0.0.0.0 to serve all interfaces (NOT recommended).")
	// requireLoopback 用于开发/测试实例：非回环监听直接拒绝启动，防止 dev 实例误上线。
	requireLoopback := flag.Bool("require-loopback", false,
		"refuse to start unless the effective listen address is loopback (dev/test hardening)")
	flag.Parse()

	cfg, err := LoadConfig(*dataDir)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	if cfg.AdminPasswordHash == "" {
		pw := "admin-" + GenerateKey()[3:]
		cfg.AdminPasswordHash = HashPassword(pw)
		if err := cfg.Save(*dataDir); err != nil {
			log.Fatalf("save bootstrap config: %v", err)
		}
		fmt.Printf("[首次启动] 管理员初始密码：%s（请尽快登录后台修改）\n", pw)
	}

	// SSRF 防护（finding 541 / fact 569）：上游白名单只从部署方配置（config.json）与
	// 环境变量 TOKEN_BUFFET_UPSTREAM_ALLOWED_HOSTS 读取，管理 API 无法写入。
	// 默认仅回环，兼容现网 http://127.0.0.1:18080/v1；云元数据地址永远拒绝。
	guard := newUpstreamGuard(ResolveUpstreamAllowedHosts(cfg.UpstreamAllowedHosts))
	log.Printf("[上游防护] 允许的上游主机: %s", strings.Join(guard.AllowedHosts(), ", "))
	// 逐条校验（多上游）：任何一条不合法都只告警不阻断启动——dial 层仍会拒绝，
	// 且其余上游照常可用。全部不合法时 /v1 会返回 502/503，日志里有明确线索。
	for _, u := range cfg.EffectiveUpstreams() {
		if err := ValidateUpstreamBaseURL(u.BaseURL, guard); err != nil {
			log.Printf("warning: 上游 %q 的 base_url=%q 未通过 SSRF 校验: %v；"+
				"该上游的请求将被拒绝，请在「上游管理」中改为合法地址", u.ID, u.BaseURL, err)
		}
	}

	store, err := NewStore(*dataDir)
	if err != nil {
		log.Fatalf("load store: %v", err)
	}
	// Key 额度每日 08:00 自动重置回默认额度。
	store.SetDefaultQuota(cfg.TokenQuota)
	store.RollQuota(cfg.TokenQuota, time.Now())
	// 「Key 有效期」全局策略（ttl_days=0 表示永不过期）在启动时生效。
	store.ApplyTTL(cfg.TTLDays, time.Now())
	// 2026-10-28 策略变更：Key 不再绑定单个模型（可用全部白名单模型）。
	// 存量 Key 的 Model 字段需要清空，否则老用户仍被锁在当初领的那个模型上。
	if n, err := store.UnbindKeyModels(); err != nil {
		log.Printf("warning: 解除存量 Key 的模型绑定失败: %v", err)
	} else if n > 0 {
		log.Printf("[模型授权] 已解除 %d 个存量 Key 的模型绑定（现在可用全部白名单模型）", n)
	}
	// 钳制存量 Key 中越界的额度（如 MaxInt64 脏值）。
	// 不做这一步，越界额度会被 MaxIssuedQuota 当成「已发放的最大额度」，
	// 管理员任何下调 token_quota 的操作都会被 400 拒绝（活锁）。
	if res, err := store.ClampOutOfRangeQuota(cfg.TokenQuota); err != nil {
		log.Printf("warning: 额度钳制失败: %v", err)
	} else if res.Clamped > 0 {
		log.Printf("[额度钳制] 修正 %d 个越界 Key 的额度：%s", res.Clamped, strings.Join(res.Details, "; "))
	}
	policies, err := NewIPPolicyStore(*dataDir)
	if err != nil {
		log.Fatalf("load ip policies: %v", err)
	}
	tracker, err := NewIPTracker(*dataDir, policies)
	if err != nil {
		log.Fatalf("load ip usage: %v", err)
	}
	claimRule := DefaultClaimRule()
	claimRule.AllowCIDRs = DefaultAllowedCIDR()
	claims := NewClaimPolicy(*dataDir, claimRule)

	// 可信反代网段：默认只信本机（nginx 与 web-server 同机）。
	// 多机部署时把 nginx 所在网段写进 config.json 的 trusted_proxies。
	if len(cfg.TrustedProxies) > 0 {
		// 受信配置也走严格解析：写错一项就启动失败，而不是静默少信任一个网段
		// （少信任会导致 clientIP 回落到直连地址，进而影响 IP 策略/白名单判定）。
		trusted, err := ParseCIDRsStrict(cfg.TrustedProxies)
		if err != nil {
			log.Fatalf("config.json 的 trusted_proxies 含非法网段: %v", err)
		}
		SetTrustedProxies(trusted)
	}
	// 反代共享密钥：只有带对密钥的请求才采信 X-Real-IP / X-Forwarded-For。
	// 未配置时自动生成并打印，同时写入 config.json 便于 nginx 侧配置。
	proxySecret := strings.TrimSpace(cfg.ProxySecret)
	if proxySecret == "" {
		proxySecret = generateSecret()
		cfg.ProxySecret = proxySecret
		if err := cfg.Save(*dataDir); err != nil {
			log.Printf("warning: 无法写入 proxy_secret: %v", err)
		}
		log.Printf("[反代密钥] %s", proxySecret)
		log.Printf("  请在 nginx 的 /token-buffet/ location 里加上：")
		log.Printf("  proxy_set_header X-Proxy-Secret \"%s\";", proxySecret)
	}
	SetProxySecret(proxySecret)

	app := &App{
		Cfg:      cfg,
		Store:    store,
		Rl:       NewRateLimiter(cfg.RateLimitPerMin),
		Sessions: NewSessionStore(),
		Logger:   NewLogger(*dataDir),
		// 上游 HTTP 客户端统一走 ssrfguard：dial 层校验（防 DNS Rebinding）、
		// 最多 5 跳重定向且每跳重校验、跨主机剥离 Authorization。
		Client:     newUpstreamClient(guard),
		Guard:      guard,
		ConfigPath: *dataDir,
		IPs:        tracker,
		// 登录限流阈值（finding 544 配套收紧）：旧值 10 次/10 分钟在「并发齐射」
		// 下每分钟可放大到数十次真实口令校验；口令强度下限提高到 8 位后
		// 爆破空间大幅缩小，因此把每 10 分钟的真实校验机会收紧到 5 次。
		// 判定与记账仍在 TryAttempt 的同一把锁内（原子语义不变）。
		Login:  NewLoginLimiter(LoginMaxAttempts, LoginWindow, LoginLockDuration),
		Ask:    NewAskMeter(*dataDir),
		Audit:  NewAuditLog(*dataDir),
		Claims: claims,
	}

	// 生效监听面：命令行 -listen > 环境变量 TOKEN_BUFFET_LISTEN > config.json 的 listen > 默认回环。
	listenHost, listenSource := resolveListenAddr(*listenFlag, cfg)
	// 启动自检：开发/测试实例要求仅回环；正式实例非回环时打高危告警（不阻断，
	// 因为此时唯一的边界是 nginx/防火墙，必须让部署者明确知道自己在裸奔）。
	if err := checkListenPolicy(listenHost, *requireLoopback); err != nil {
		log.Fatalf("[启动自检] %v", err)
	}
	addr := fmt.Sprintf("%s:%d", listenHost, cfg.Port)
	log.Printf("opencode2api 网页版 %s 启动，监听 %s，数据目录 %s", version, addr, *dataDir)
	log.Printf("[监听面] 生效地址 %s（来源：%s）— %s", listenHost, listenSource, describeListenExposure(listenHost))
	if !isLoopbackListen(listenHost) {
		log.Printf("[高危告警] 当前监听地址 %s 不是回环：同网段任何主机都可直连管理后台(/admin.html、/api/admin/*)"+
			"与领取口(/api/keys)。生产环境应当只监听 127.0.0.1 并由 nginx 反代统一收口；"+
			"若这是开发/测试实例，请加 -require-loopback 以拒绝非回环启动。", listenHost)
	}
	log.Printf("浏览器访问: http://127.0.0.1:%d", cfg.Port)
	srv := &http.Server{
		Addr:              addr,
		Handler:           buildMux(app),
		ReadHeaderTimeout: 30 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
