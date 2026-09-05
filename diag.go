// 阶段 4（诊断端点 + doctor）：统一健康检查核心。
// 设计要点（对齐 docs/DEBUG-OBSERVABILITY-PLAN.md §5 阶段4）：
//   - buildDiagReport 为纯函数：只读 DiagSnapshot（平凡数据），产出结构化报告，
//     无副作用、可单测；两个入口共用它——
//       * GET /api/diag  → HTTP 包装（复用既有 requireAuth 管理鉴权）；
//       * opencode2api doctor → CLI 包装（人类可读报告 + 非零退出码）。
//   - 采集只读：枚举实例/端口/残留进程/配置文件，绝不启动或杀任何进程。
//   - 不泄露密钥明文/请求体：门禁密钥只报「是否设置 + 末 4 位」。
//   - 不触碰 call_log 写入路径，不新增任何外部依赖。
// Same package (main) - do not change package clause manually.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/6Kmfi6HP/opencode2api/core/manager"
)

// DiagStatus 三态健康标签（绿/黄/红）。
type DiagStatus string

const (
	DiagOK    DiagStatus = "ok"
	DiagWarn  DiagStatus = "warn"
	DiagError DiagStatus = "error"
)

// diagStatusRank 严重度排序（聚合总状态时取最坏）。
func diagStatusRank(s DiagStatus) int {
	switch s {
	case DiagError:
		return 2
	case DiagWarn:
		return 1
	default:
		return 0
	}
}

// diagWorse 返回两者中更严重的状态。
func diagWorse(a, b DiagStatus) DiagStatus {
	if diagStatusRank(b) > diagStatusRank(a) {
		return b
	}
	return a
}

// DiagCheck 一条检查项结果。
type DiagCheck struct {
	Name   string     `json:"name"`
	Status DiagStatus `json:"status"`
	Detail string     `json:"detail"`
}

// DiagReport 诊断报告（/api/diag JSON 契约 + doctor 输出源）。
type DiagReport struct {
	Status      DiagStatus  `json:"status"`
	Role        string      `json:"role"`
	Version     string      `json:"version"`
	GeneratedAt string      `json:"generated_at"`
	Checks      []DiagCheck `json:"checks"`
}

// DiagInstance 快照里的单实例（拷贝必要字段，避免 buildDiagReport 依赖 manager 类型）。
type DiagInstance struct {
	Name          string
	Port          uint16
	SingboxPort   uint16
	State         string
	JoinGateway   bool
	HasPID        bool
	HasSingboxPID bool
}

// DiagSnapshot 纯数据快照——buildDiagReport 只读它，便于单测（不触碰进程/网络/全局）。
type DiagSnapshot struct {
	Role    string
	Version string
	Now     time.Time

	ServingPort string
	GatewayPort uint16
	Instances   []DiagInstance

	ActiveSocks5     string
	Socks5ProxyCount int
	RouteMode        string

	OrphanTotal   int
	OrphanProbe   int
	OrphanOrphan  int
	OrphanDetails []string

	SingboxBinPath    string
	SingboxBinPresent bool

	AdminKeySet  bool
	AdminKeyTail string

	DataDir       string
	RuntimeDir    string
	ConfigPath    string
	ConfigExists  bool
	ConfigParseOK bool
}

// validRouteModes 合法路由模式集合（与 applyConfig / socks.go 对齐）。
var validRouteModes = map[string]bool{"smart": true, "failover": true, "round_robin": true}

// buildDiagReport 纯函数：把快照映射为分项检查 + 总状态（无副作用，可单测）。
func buildDiagReport(snap DiagSnapshot) DiagReport {
	report := DiagReport{
		Role:    snap.Role,
		Version: snap.Version,
		Checks:  []DiagCheck{},
	}
	now := snap.Now
	if now.IsZero() {
		now = time.Now()
	}
	report.GeneratedAt = now.UTC().Format(time.RFC3339)

	report.Checks = append(report.Checks,
		diagCheckPorts(snap),
		diagCheckSocks(snap),
		diagCheckInstances(snap),
		diagCheckSingbox(snap),
		diagCheckOrphans(snap),
		diagCheckAdminKey(snap),
		diagCheckConfig(snap),
	)

	overall := DiagOK
	for _, c := range report.Checks {
		overall = diagWorse(overall, c.Status)
	}
	report.Status = overall
	return report
}

// diagCheckPorts 端口检查：配置层重复检测（不实际绑定端口，真实占用需真机联调）。
func diagCheckPorts(snap DiagSnapshot) DiagCheck {
	type portUse struct {
		port int
		who  string
	}
	var uses []portUse
	if p := strings.TrimSpace(snap.ServingPort); p != "" {
		if n, err := strconv.Atoi(p); err == nil && n > 0 {
			uses = append(uses, portUse{n, "管理端口"})
		}
	}
	if snap.GatewayPort > 0 {
		uses = append(uses, portUse{int(snap.GatewayPort), "统一网关"})
	}
	for _, inst := range snap.Instances {
		if inst.Port > 0 {
			uses = append(uses, portUse{int(inst.Port), "实例 " + inst.Name})
		}
		if inst.SingboxPort > 0 {
			uses = append(uses, portUse{int(inst.SingboxPort), "sing-box " + inst.Name})
		}
	}
	if len(uses) == 0 {
		return DiagCheck{Name: "端口占用", Status: DiagWarn, Detail: "未探测到任何配置端口（进程可能未运行或配置为空）"}
	}
	seen := map[int][]string{}
	for _, u := range uses {
		seen[u.port] = append(seen[u.port], u.who)
	}
	ports := make([]int, 0, len(seen))
	for p := range seen {
		ports = append(ports, p)
	}
	sort.Ints(ports)
	var conflicts []string
	for _, p := range ports {
		if len(seen[p]) > 1 {
			conflicts = append(conflicts, fmt.Sprintf("%d ← %s", p, strings.Join(seen[p], " / ")))
		}
	}
	if len(conflicts) > 0 {
		return DiagCheck{Name: "端口占用", Status: DiagError, Detail: "检测到端口配置冲突：" + strings.Join(conflicts, "；")}
	}
	return DiagCheck{Name: "端口占用", Status: DiagOK, Detail: fmt.Sprintf("已登记 %d 个端口，无配置冲突（真实监听占用需运行态探测）", len(uses))}
}

// diagCheckSocks SOCKS 三键检查：route_mode 合法性 + 轮询空池退化。
func diagCheckSocks(snap DiagSnapshot) DiagCheck {
	mode := strings.TrimSpace(snap.RouteMode)
	active := strings.TrimSpace(snap.ActiveSocks5)
	detail := fmt.Sprintf("route_mode=%s，active_socks5=%s，代理池=%d", diagValueOrEmpty(mode), diagSocksActiveLabel(active), snap.Socks5ProxyCount)
	status := DiagOK
	var notes []string
	if mode != "" && !validRouteModes[mode] {
		status = diagWorse(status, DiagWarn)
		notes = append(notes, "route_mode 非法（应为 smart/failover/round_robin）")
	}
	if active == socks5RR && snap.Socks5ProxyCount == 0 {
		status = diagWorse(status, DiagWarn)
		notes = append(notes, "已启用轮询代理但代理池为空（将退化为直连）")
	}
	if len(notes) > 0 {
		detail += "；" + strings.Join(notes, "；")
	}
	return DiagCheck{Name: "SOCKS 代理", Status: status, Detail: detail}
}

// diagCheckInstances 实例/节点健康：错误态 → 红；运行态缺 PID → 黄。
func diagCheckInstances(snap DiagSnapshot) DiagCheck {
	if len(snap.Instances) == 0 {
		return DiagCheck{Name: "实例健康", Status: DiagOK, Detail: "未注册任何实例"}
	}
	var running, stopped, errored int
	var errNames, noPIDNames []string
	for _, inst := range snap.Instances {
		state := inst.State
		if state == "" {
			state = "Stopped"
		}
		switch state {
		case "Running":
			running++
			if !inst.HasPID {
				noPIDNames = append(noPIDNames, inst.Name)
			}
		case "Error":
			errored++
			errNames = append(errNames, inst.Name)
		default:
			stopped++
		}
	}
	status := DiagOK
	detail := fmt.Sprintf("共 %d 实例：运行 %d / 停止 %d / 错误 %d", len(snap.Instances), running, stopped, errored)
	var notes []string
	if errored > 0 {
		status = diagWorse(status, DiagError)
		notes = append(notes, "错误态实例："+strings.Join(errNames, ", "))
	}
	if len(noPIDNames) > 0 {
		status = diagWorse(status, DiagWarn)
		notes = append(notes, "运行态但无 PID 记录："+strings.Join(noPIDNames, ", "))
	}
	if len(notes) > 0 {
		detail += "；" + strings.Join(notes, "；")
	}
	return DiagCheck{Name: "实例健康", Status: status, Detail: detail}
}

// diagCheckSingbox sing-box 可执行文件就位 + 运行态实例 sing-box PID 记录。
func diagCheckSingbox(snap DiagSnapshot) DiagCheck {
	if !snap.SingboxBinPresent {
		return DiagCheck{Name: "sing-box", Status: DiagWarn, Detail: "未找到 sing-box 可执行文件：" + diagValueOrEmpty(snap.SingboxBinPath) + "（代理节点将无法启动）"}
	}
	var missing []string
	for _, inst := range snap.Instances {
		if inst.State == "Running" && inst.SingboxPort > 0 && !inst.HasSingboxPID {
			missing = append(missing, inst.Name)
		}
	}
	if len(missing) > 0 {
		return DiagCheck{Name: "sing-box", Status: DiagWarn, Detail: "运行态实例缺少 sing-box PID 记录：" + strings.Join(missing, ", ")}
	}
	return DiagCheck{Name: "sing-box", Status: DiagOK, Detail: "可执行文件就位：" + snap.SingboxBinPath}
}

// diagCheckOrphans 残留进程：>0 → 黄，并提示可在管理面板一键清除。
func diagCheckOrphans(snap DiagSnapshot) DiagCheck {
	if snap.OrphanTotal <= 0 {
		return DiagCheck{Name: "残留进程", Status: DiagOK, Detail: "未发现残留进程"}
	}
	detail := fmt.Sprintf("发现 %d 个残留进程（探针 %d / 孤儿 %d）", snap.OrphanTotal, snap.OrphanProbe, snap.OrphanOrphan)
	if len(snap.OrphanDetails) > 0 {
		detail += "：" + strings.Join(snap.OrphanDetails, "；")
	}
	detail += "。可在管理面板一键清除"
	return DiagCheck{Name: "残留进程", Status: DiagWarn, Detail: detail}
}

// diagCheckAdminKey 门禁密钥：只报是否设置与末 4 位，绝不泄露明文。
func diagCheckAdminKey(snap DiagSnapshot) DiagCheck {
	if !snap.AdminKeySet {
		return DiagCheck{Name: "门禁密钥", Status: DiagWarn, Detail: "未设置管理密钥（-password 为空）：管理面板与 /v1 接口当前无鉴权"}
	}
	return DiagCheck{Name: "门禁密钥", Status: DiagOK, Detail: "已设置管理密钥（末 4 位 " + snap.AdminKeyTail + "）"}
}

// diagCheckConfig 配置文件：缺失 → 黄（首次运行自愈）；解析失败 → 红。
func diagCheckConfig(snap DiagSnapshot) DiagCheck {
	if !snap.ConfigExists {
		return DiagCheck{Name: "配置文件", Status: DiagWarn, Detail: "配置文件不存在：" + diagValueOrEmpty(snap.ConfigPath) + "（首次运行会自动创建）"}
	}
	if !snap.ConfigParseOK {
		return DiagCheck{Name: "配置文件", Status: DiagError, Detail: "配置文件解析失败（JSON 损坏）：" + snap.ConfigPath}
	}
	return DiagCheck{Name: "配置文件", Status: DiagOK, Detail: "配置文件可正常解析：" + snap.ConfigPath}
}

// diagValueOrEmpty 空值占位（避免报告里出现裸空串）。
func diagValueOrEmpty(v string) string {
	if strings.TrimSpace(v) == "" {
		return "(空)"
	}
	return v
}

// diagSocksActiveLabel active_socks5 语义标签。
func diagSocksActiveLabel(active string) string {
	switch active {
	case "":
		return "直连"
	case socks5RR:
		return "轮询"
	default:
		return active
	}
}

// diagKeyTail 只返回密钥末 4 位（绝不泄露明文）；过短则完全掩码。
func diagKeyTail(key string) string {
	k := strings.TrimSpace(key)
	if k == "" {
		return ""
	}
	if len(k) <= 4 {
		return "****"
	}
	return k[len(k)-4:]
}

// collectDiagSnapshot 采集当前进程 + 本机诊断快照（只读；不启动/杀任何进程）。
// m 可为 nil（无管理器上下文时仅采集进程级 SOCKS/密钥/配置维度）。
func collectDiagSnapshot(m *manager.Manager) DiagSnapshot {
	role := getProcessRole()
	if role == "" {
		role = RoleManager
	}
	snap := DiagSnapshot{
		Role:        role,
		Version:     version,
		Now:         time.Now(),
		ServingPort: processServingPort(),
		ConfigPath:  configPath,
	}

	// SOCKS 实时全局态（配置热加载后即时反映；锁序与请求路径同向）。
	socks5Mu.RLock()
	snap.ActiveSocks5 = activeSocks5
	snap.Socks5ProxyCount = len(socks5Proxies)
	socks5Mu.RUnlock()
	if rm, ok := routeMode.Load().(string); ok {
		snap.RouteMode = rm
	}

	// 门禁密钥（只取末 4 位）。
	snap.AdminKeySet = adminPassword != ""
	snap.AdminKeyTail = diagKeyTail(adminPassword)

	// 配置文件存在性 + 可解析性（只读，不回写）。
	snap.ConfigExists, snap.ConfigParseOK = diagProbeConfig(configPath)

	if m != nil {
		p := m.Paths()
		snap.DataDir = p.DataDir
		snap.RuntimeDir = p.RuntimeDir
		snap.SingboxBinPath, snap.SingboxBinPresent = diagProbeSingbox(p.BinDir)

		// 网关端口：Gateway() 惰性构造仅读端口，不拉起子进程。
		if gw := m.Gateway(); gw != nil {
			snap.GatewayPort = gw.Port()
		}

		for _, inst := range m.ListInstances() {
			state := inst.Status.State
			if state == "" {
				state = "Stopped"
			}
			snap.Instances = append(snap.Instances, DiagInstance{
				Name:          inst.Name,
				Port:          inst.Port,
				SingboxPort:   inst.SingboxPort,
				State:         state,
				JoinGateway:   inst.JoinGateway,
				HasPID:        inst.PID != nil,
				HasSingboxPID: inst.SingboxPID != nil,
			})
		}

		// 残留进程扫描（枚举进程，只读，不杀）。
		orphans := m.ScanOrphans()
		snap.OrphanTotal = orphans.Total
		snap.OrphanProbe = orphans.Probe
		snap.OrphanOrphan = orphans.Orphan
		for _, it := range orphans.Items {
			snap.OrphanDetails = append(snap.OrphanDetails, fmt.Sprintf("PID %d %s（%s）", it.PID, it.Name, it.Detail))
		}
	}

	return snap
}

// diagProbeConfig 只读探查配置文件存在性与可解析性（不回写、不修改）。
func diagProbeConfig(path string) (exists bool, parseOK bool) {
	if strings.TrimSpace(path) == "" {
		return false, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false, false
	}
	var cfg AppConfig
	if json.Unmarshal(data, &cfg) != nil {
		return true, false
	}
	return true, true
}

// diagProbeSingbox 探查 sing-box 可执行文件是否就位（不执行它）。
func diagProbeSingbox(binDir string) (path string, present bool) {
	name := "sing-box"
	if runtime.GOOS == "windows" {
		name = "sing-box.exe"
	}
	full := filepath.Join(binDir, name)
	if fi, err := os.Stat(full); err == nil && !fi.IsDir() {
		return full, true
	}
	return full, false
}

// diagHandler GET /api/diag：聚合健康检查，返回结构化 JSON（管理鉴权由 requireAuth 包裹）。
func diagHandler(m *manager.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		report := buildDiagReport(collectDiagSnapshot(m))
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			slog.Error("diag encode failed", "error", err)
		}
	}
}

// runDoctor 执行 opencode2api doctor：本地健康自检，人类可读报告 + 非零退出码。
// 退出码：0=健康，1=有告警，2=有错误。
func runDoctor(args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	var cfgPath, dataDir string
	var jsonOut bool
	fs.StringVar(&cfgPath, "config", "config.json", "配置文件路径")
	fs.StringVar(&dataDir, "data-dir", "", "管理数据目录（空 = 默认）")
	fs.BoolVar(&jsonOut, "json", false, "以 JSON 输出报告")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	// doctor 为一次性进程：载入配置到全局，使 SOCKS/门禁快照反映磁盘配置（副作用无害）。
	setProcessRole("doctor")
	configPath = cfgPath
	cfg := loadConfig(cfgPath)
	applyConfig(cfg)

	m := manager.New(dataDir)
	report := buildDiagReport(collectDiagSnapshot(m))

	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(report)
	} else {
		writeDoctorReport(os.Stdout, report)
	}

	switch report.Status {
	case DiagError:
		return 2
	case DiagWarn:
		return 1
	default:
		return 0
	}
}

// writeDoctorReport 人类可读报告输出。
func writeDoctorReport(w io.Writer, report DiagReport) {
	fmt.Fprintln(w, "opencode2api doctor — 健康自检")
	fmt.Fprintf(w, "版本: %s  角色: %s  时间: %s\r\n", report.Version, report.Role, report.GeneratedAt)
	fmt.Fprintf(w, "总体状态: %s\r\n\r\n", diagStatusText(report.Status))
	for _, c := range report.Checks {
		fmt.Fprintf(w, "  [%s] %s\r\n        %s\r\n", diagStatusMark(c.Status), c.Name, c.Detail)
	}
	fmt.Fprintln(w)
}

// diagStatusText 总状态中文标签。
func diagStatusText(s DiagStatus) string {
	switch s {
	case DiagOK:
		return "健康 (OK)"
	case DiagWarn:
		return "告警 (WARN)"
	case DiagError:
		return "错误 (ERROR)"
	default:
		return string(s)
	}
}

// diagStatusMark 单项状态定宽标记。
func diagStatusMark(s DiagStatus) string {
	switch s {
	case DiagOK:
		return "OK  "
	case DiagWarn:
		return "WARN"
	case DiagError:
		return "ERR "
	default:
		return string(s)
	}
}
