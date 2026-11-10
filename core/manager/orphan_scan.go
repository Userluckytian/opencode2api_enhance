// 孤儿进程扫描与清理（M1，docs/ai-framework/plans/2026-11-10-orphan-governance-m1.md）。
//
// 扫描判据（三条同时满足才列为孤儿，见计划第四节）：
//  1. 命令行含独立的 --provider-serve token（插件子进程；边界判定见
//     internal/proctree.HasProviderServeToken，与 pluginprovider.reapOrphans 共用）
//  2. 父进程 PID 已不存在（pidAliveSafe(ppid) == false；查询失败保守视为存活，不列不杀）
//  3. 命令行所指 providers 目录 ≠ 当前 ProvidersDir
//     （本目录的孤儿由 pluginprovider 的 reapOrphans 自动回收，本端点不与之重复）
//
// M3 用根 PID 树杀覆盖后代：端点不单独列出孙进程（避免父/子重复出现在 killed
// 列表，保持前端 OrphanProcess 契约不变），probe 仍恒 0。
// 多实例时代的旧实现（orphan.go，依赖已删除的 Instance 类型）不复活。
package manager

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/6Kmfi6HP/opencode2api/core/manager/internal/proctree"
)

// OrphanProcess 扫描结果单条。JSON 契约与前端 src/lib/api.ts OrphanProcess 逐字对齐。
type OrphanProcess struct {
	PID      int    `json:"pid"`
	Name     string `json:"name"`
	Category string `json:"category"`           // "probe" | "orphan"；本阶段只产出 orphan
	Instance string `json:"instance,omitempty"` // 多实例时代字段，本阶段不填
	Port     int    `json:"port,omitempty"`     // 命令行 --port N（N>0）才填，否则省略
	Detail   string `json:"detail"`
}

// OrphanScan 扫描汇总（与前端 OrphanScan 对齐）。Items 恒为空数组非 nil。
type OrphanScan struct {
	Total  int             `json:"total"`
	Probe  int             `json:"probe"`
	Orphan int             `json:"orphan"`
	Items  []OrphanProcess `json:"items"`
}

// OrphanKillResult 清理结果（与前端 OrphanKillResult 对齐）。
// Killed 恒为数组、Errors 恒为对象（空时 []/{}/ 非 null）。
type OrphanKillResult struct {
	Killed []int             `json:"killed"`
	Errors map[string]string `json:"errors"`
}

// procLine 进程枚举行（pid/ppid/cmd 三元组），由平台文件 listAllProcesses 产出。
// 类型别名指向 internal/proctree.Process：M3 的后代判定同时在 core/manager 与
// pluginprovider 使用，共享定义下沉到 internal/proctree（避免跨包依赖 manager
// 的未导出实现）。字段名不变，既有调用与测试零改动。
type procLine = proctree.Process

// maxTreeDepth 后代展开深度上限：2 = 子进程 + 孙进程。
// 层级：opencode2api → provider-service → 插件（深度 0）→ node/msedge（1）→
// 它们再拉起的进程（2）。现场证据只到两层；更深既无需求，也放大 PID 复用误判面。
const maxTreeDepth = 2

// findDescendants 返回 procs 中目标根的后代（深度 1..maxDepth），按深度从深到浅
// 排列（先杀后代再杀父进程；非 Windows 父进程死后 PPID 关联失效，顺序不可颠倒）。
// 只按 PPID 关联、不匹配进程名；环/深度防护见 proctree.FindDescendants。
func findDescendants(procs []procLine, roots []int, maxDepth int) []procLine {
	return proctree.FindDescendants(procs, roots, maxDepth)
}

// killTreeWith 快照式树杀共享实现：先杀后代（深→浅），最后杀根。kill 注入便于
// 跨平台单测（非 Windows 生产传 killProcess）。根错误与后代错误分别返回（M3 复核
// P1-2）：调用方据此区分「根已杀、后代回收失败」的部分失败，不能把两者混成一个
// 笼统错误——端点若因此报 killed 却静默留下孙进程，下一轮扫描也找不回来（根死后
// PPID 关联已丢失）。
//
// root <= 0 直接返回且绝不下发 kill：Unix `kill(0, SIGKILL)` 会杀调用者所在进程组。
func killTreeWith(procs []procLine, root int, kill func(int) error, maxDepth int) (rootErr error, descErrs []error) {
	if root <= 0 {
		return nil, nil
	}
	for _, d := range findDescendants(procs, []int{root}, maxDepth) {
		if err := kill(d.PID); err != nil {
			descErrs = append(descErrs, fmt.Errorf("kill descendant %d: %w", d.PID, err))
		}
	}
	if err := kill(root); err != nil {
		rootErr = fmt.Errorf("kill root %d: %w", root, err)
	}
	return rootErr, descErrs
}

// treeKillError 树杀部分失败（M3 复核 P1-2）：根进程与后代/枚举失败分别携带。
//
// 判读规则（selectKillTargets 依赖）：
//   - RootErr == nil  → 根已被成功杀死；DescErrs/EnumErr 非空表示后代未完全回收
//     （或枚举失败根本无法得知后代），端点仍把该 PID 计入 Killed，同时把残余
//     失败写进 errors，绝不静默成功。
//   - RootErr != nil  → 根也没杀掉，只能如实报错。
type treeKillError struct {
	Root     int
	RootErr  error
	DescErrs []error
	EnumErr  error
}

func (e *treeKillError) Error() string {
	var parts []string
	if e.RootErr != nil {
		parts = append(parts, "根进程失败: "+e.RootErr.Error())
	} else {
		parts = append(parts, "根进程已杀")
	}
	if e.EnumErr != nil {
		parts = append(parts, "后代枚举失败: "+e.EnumErr.Error())
	}
	if len(e.DescErrs) > 0 {
		parts = append(parts, fmt.Sprintf("%d 个后代回收失败: %v",
			len(e.DescErrs), errors.Join(e.DescErrs...)))
	}
	return fmt.Sprintf("树杀进程 %d 未完全成功：%s", e.Root, strings.Join(parts, "；"))
}

// Unwrap 暴露全部底层错误，便于 errors.Is/As 与测试断言。
func (e *treeKillError) Unwrap() []error {
	errs := make([]error, 0, len(e.DescErrs)+2)
	if e.RootErr != nil {
		errs = append(errs, e.RootErr)
	}
	if e.EnumErr != nil {
		errs = append(errs, e.EnumErr)
	}
	return append(errs, e.DescErrs...)
}

// newTreeKillError 聚合树杀结果：全部成功返回 nil，否则返回 treeKillError。
func newTreeKillError(root int, rootErr error, descErrs []error, enumErr error) error {
	if rootErr == nil && len(descErrs) == 0 && enumErr == nil {
		return nil
	}
	return &treeKillError{Root: root, RootErr: rootErr, DescErrs: descErrs, EnumErr: enumErr}
}

// killProcessTreeSnapshot 枚举 → 树杀的可注入实现（M3 复核 P1-2 单测：注入枚举
// 失败）。枚举失败时退化为单杀根进程：根可能被回收，但后代 PID 未知、无法补杀，
// 返回 treeKillError 如实标注部分失败（RootErr == nil 表示根已杀掉）。
func killProcessTreeSnapshot(pid int, list func() ([]procLine, error), kill func(int) error) error {
	if pid <= 0 {
		return nil
	}
	procs, err := list()
	if err != nil {
		rootErr := kill(pid)
		return newTreeKillError(pid, rootErr, nil, err)
	}
	rootErr, descErrs := killTreeWith(procs, pid, kill, maxTreeDepth)
	return newTreeKillError(pid, rootErr, descErrs, nil)
}

// orphanPortRe 命令行 --port N（N>0 才计入 Port，spawn 契约是 --port 0 占位）。
var orphanPortRe = regexp.MustCompile(`--port[ =]+(\d+)`)

// normalizeProcPath M2 P2-3 / M3 复核 P2-2：实现下沉 internal/proctree，
// core/manager 与 pluginprovider 共用同一归一路径（`\`→`/` + 小写）。
func normalizeProcPath(s string) string {
	return proctree.NormalizePath(s)
}

// pidAliveDecision M2 P2-2 保守判定表（平台实现 pidAliveSafe 共用，纯函数便于单测）：
// 查询失败（err != nil）→ 视为存活（true，不列不杀）；查询成功 → 如实返回。
// 与 pidAlive 的 fail-closed（失败=已死）相反——扫描/清理路径宁可漏报一轮。
func pidAliveDecision(alive bool, err error) bool {
	if err != nil {
		return true
	}
	return alive
}

// memoizeAlive 单次扫描内的父 PID 探活记忆化（M1 端点慢的 P2 优化）。
//
// 一次全表枚举起多个插件子进程，但它们的父 PID 高度重复（同一管理面/provider-
// service 拉起一批插件），而 pidAliveSafe 每 PID 一次 CIM 单查实测 ~0.7s。
// 记忆化把同一父 PID 压缩为一次查询，只影响延迟，不改变任何判定语义。
//
// 仅允许在单次扫描、单 goroutine 内使用（无锁）。kill 前二次校验必须绕过本函数
// 重新查询（防扫描后父进程复活/PID 复用），因此 selectKillTargets 只用它包装
// classifyProcesses 的 alive 参数。
func memoizeAlive(alive func(int) bool) func(int) bool {
	cache := make(map[int]bool)
	return func(pid int) bool {
		if v, ok := cache[pid]; ok {
			return v
		}
		v := alive(pid)
		cache[pid] = v
		return v
	}
}

// classifyProcesses 纯函数分类：从全量进程行中筛出孤儿插件子进程（判据见文件头）。
// alive 注入父进程探活（生产传 pidAliveSafe——查询失败保守视为存活，M2 P2-2；
// 测试注入假实现）；providersDir 为当前生效的 providers 根目录，目录匹配用
// normalizeProcPath 归一（`\`→`/` + 小写，M2 P2-3；Windows 路径大小写不敏感且
// 分隔符混用等价）。
func classifyProcesses(procs []procLine, providersDir string, alive func(int) bool) OrphanScan {
	scan := OrphanScan{Items: []OrphanProcess{}}
	var dirLow string
	if providersDir != "" {
		dirLow = normalizeProcPath(filepath.Clean(providersDir))
	}
	for _, p := range procs {
		if p.PID <= 0 {
			continue // 无效 PID：不列出、不杀（kill(0) 会杀调用者进程组，P2-1）
		}
		low := normalizeProcPath(p.Cmd)
		if !proctree.HasProviderServeToken(low) {
			continue // 命令行没有独立的 --provider-serve token（M2 P2-1，共享判定）→ 不列出
		}
		if alive(p.PPID) {
			continue // 父进程存活（正常运行中的插件）→ 不列出
		}
		if dirLow != "" && strings.Contains(low, dirLow) {
			continue // 当前 ProvidersDir 下的孤儿 → 交给 reapOrphans 自动回收，不列出
		}
		item := OrphanProcess{
			PID:      p.PID,
			Name:     orphanProcName(p.Cmd),
			Category: "orphan",
			Detail:   fmt.Sprintf("父进程 %d 已退出；命令行 %s", p.PPID, p.Cmd),
		}
		if m := orphanPortRe.FindStringSubmatch(p.Cmd); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil && n > 0 {
				item.Port = n
			}
		}
		scan.Items = append(scan.Items, item)
		scan.Orphan++
	}
	scan.Total = scan.Probe + scan.Orphan // M3 用根 PID 树杀覆盖后代，端点不单独列出孙进程，probe 仍恒 0
	return scan
}

// orphanProcName 从命令行提取进程名（首个参数的基名，兼容引号路径）。
// 手动切分而非 filepath.Base：需在任意平台对 Windows 反斜杠路径得到一致结果（单测）。
func orphanProcName(cmd string) string {
	s := strings.TrimSpace(cmd)
	if s == "" {
		return ""
	}
	var token string
	if strings.HasPrefix(s, `"`) {
		if i := strings.IndexByte(s[1:], '"'); i >= 0 {
			token = s[1 : 1+i]
		} else {
			token = s[1:]
		}
	} else if i := strings.IndexAny(s, " \t"); i >= 0 {
		token = s[:i]
	} else {
		token = s
	}
	token = strings.ReplaceAll(token, `\`, `/`)
	if i := strings.LastIndexByte(token, '/'); i >= 0 {
		token = token[i+1:]
	}
	return token
}

// currentProvidersDir 当前生效的 providers/ 根目录，与 pluginprovider 的
// defaultProvidersDir 同一套规则（env 优先，其次 <exe 目录>/providers，兜底
// 相对 providers）。不能导入复用：pluginprovider 反向依赖本包（spawn 回调注入），
// 导入会成环，此处按同规则实现，改动需两处同步。
func currentProvidersDir() string {
	if d := os.Getenv("OPCODE2API_PLUGIN_DIR"); d != "" {
		return d
	}
	if exe, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(exe), "providers")
	}
	return "providers"
}

// selectKillTargets kill 安全核心：请求 PID 与「重新执行的扫描」取交集（只杀扫描
// 结果内的 PID），且执行前二次校验父进程仍不存在（防「扫描后 PID 被系统复用」）。
// alive/kill 注入便于单测（生产传 pidAliveSafe / killProcessTreeWith——M3 起使用
// 独立树杀并复用 handler 的进程快照，共享 killProcess 仍保持非树杀语义）。
func selectKillTargets(procs []procLine, providersDir string, reqPIDs []int, alive func(int) bool, kill func(int) error) OrphanKillResult {
	res := OrphanKillResult{Killed: []int{}, Errors: map[string]string{}}
	allowed := map[int]procLine{}
	// 分类阶段按父 PID 记忆化（同一父 PID 只查一次）；杀前二次校验（下方 alive
	// 调用）必须重新查询，不得复用这里的缓存——否则「扫描后父进程复活」检测失效。
	scanAlive := memoizeAlive(alive)
	for _, it := range classifyProcesses(procs, providersDir, scanAlive).Items {
		for _, p := range procs {
			if p.PID == it.PID {
				allowed[p.PID] = p
				break
			}
		}
	}
	for _, pid := range reqPIDs {
		key := strconv.Itoa(pid)
		p, ok := allowed[pid]
		if !ok {
			res.Errors[key] = "不在当前扫描结果内，拒绝清理"
			continue
		}
		if alive(p.PPID) {
			res.Errors[key] = fmt.Sprintf("父进程 %d 已重新出现，跳过（疑似 PID 复用）", p.PPID)
			continue
		}
		if err := kill(pid); err != nil {
			// 树杀部分失败（P1-2）：根已杀、后代未完全回收 → 仍计入 Killed，但必须
			// 把残余失败写进 errors，绝不静默成功（根死后 PPID 关联丢失，下一轮
			// 扫描无法补杀这些后代）。
			var tke *treeKillError
			if errors.As(err, &tke) && tke.RootErr == nil {
				res.Killed = append(res.Killed, pid)
				res.Errors[key] = "已杀根进程，但后代回收失败：" + err.Error()
				continue
			}
			res.Errors[key] = err.Error()
			continue
		}
		res.Killed = append(res.Killed, pid)
	}
	return res
}

// OrphanScanHandler GET /api/admin/processes/orphans：扫描孤儿插件子进程。
// 枚举失败返回明确错误而非空结果（空结果会被前端误读为「无残留」）。
func (m *Manager) OrphanScanHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethod(w, r, http.MethodGet) {
			return
		}
		procs, err := listAllProcesses(r.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "进程枚举失败: "+err.Error())
			return
		}
		// 同一次扫描内按父 PID 记忆化：同一父进程的多个插件只做一次 CIM 探活。
		writeJSON(w, classifyProcesses(procs, currentProvidersDir(), memoizeAlive(pidAliveSafe)))
	}
}

// OrphanKillHandler POST /api/admin/processes/orphans/kill，请求 {"pids":[...]}。
// 安全：重新执行扫描取交集 + 杀前二次校验父进程仍不存在（见 selectKillTargets）。
// M3：kill 改用独立 killProcessTreeWith（Windows taskkill /T；非 Windows 复用本
// handler 已拿到的 procs 快照，先深后浅回收后代再杀根），回收插件残留的孙进程。
// 共享 killProcess 保持非树杀语义（provider-service 管理路径依赖它，M2 D-M3-2），
// 不得替换。
func (m *Manager) OrphanKillHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethod(w, r, http.MethodPost) {
			return
		}
		var req struct {
			PIDs []int `json:"pids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.PIDs) == 0 {
			writeErr(w, http.StatusBadRequest, "请求体需含非空 pids 数组")
			return
		}
		procs, err := listAllProcesses(r.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "进程枚举失败: "+err.Error())
			return
		}
		// 复用本次 handler 已拿到的全量快照（P1-2）：树杀只在这份快照上找后代，
		// 避免每个 PID 再跑一次全量 ps/CIM。Windows 版本忽略快照（taskkill /T）。
		kill := func(pid int) error { return killProcessTreeWith(procs, pid) }
		writeJSON(w, selectKillTargets(procs, currentProvidersDir(), req.PIDs, pidAliveSafe, kill))
	}
}
