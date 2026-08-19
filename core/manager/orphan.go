// 残留进程探测与一键清除（占着进程但未使用的节点/实例/探针）。
//
// 枚举系统中本应用（opencode2api.exe / sing-box.exe / 插件子进程）的全部进程，
// 按命令行分类：
//   - probe：探针扫描残留（runtime/_probe/worker-*）
//   - orphan：已停止实例的残留进程（runtime/<实例目录>，进程仍在但实例未运行）
//   - orphan：插件子进程残留（命令行含 --provider-serve，宿主 opencode2api 已退出）
//   - 运行中实例的进程、管理器本身、统一网关、宿主存活的插件子进程 → 不列出（使用中/保留）
//
// 用户在前端勾选后按 PID 一键清除；后端仅允许杀扫描结果内的进程（防误杀）。
package manager

import (
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// procLine 系统进程枚举结果的一行。
type procLine struct {
	PID  int    `json:"pid"`
	PPID int    `json:"ppid"` // 父进程 PID（插件子进程判定宿主存活用；Windows=ParentProcessId）
	Name string `json:"name"` // 进程名（opencode2api.exe / sing-box.exe / 插件入口）
	Cmd  string `json:"cmd"`  // 命令行
}

// OrphanProcess 一条残留进程（前端展示 + 勾选）。
type OrphanProcess struct {
	PID      int    `json:"pid"`
	Name     string `json:"name"`               // 进程名
	Category string `json:"category"`           // probe | orphan
	Instance string `json:"instance,omitempty"` // 孤儿归属的实例名
	Port     uint16 `json:"port,omitempty"`     // opencode2api 的 -port（探测到才填）
	Detail   string `json:"detail"`             // 人类可读说明
}

// OrphanScan 探测结果汇总。
type OrphanScan struct {
	Total  int             `json:"total"`
	Probe  int             `json:"probe"`
	Orphan int             `json:"orphan"`
	Items  []OrphanProcess `json:"items"`
}

// OrphanKillResult 一键清除结果。
type OrphanKillResult struct {
	Killed []int             `json:"killed"`
	Errors map[string]string `json:"errors"`
}

var portFlagRe = regexp.MustCompile(`-port\s+(\d+)`)

// providerDirRe 从插件子进程命令行提取插件 id（.../providers/<id>/<entry> 路径段）。
var providerDirRe = regexp.MustCompile(`(?i)[\\/]providers[\\/]([^\\/"]+)`)

// portFromCmd 从命令行提取 -port 值（无则 0）。
func portFromCmd(cmd string) uint16 {
	m := portFlagRe.FindStringSubmatch(cmd)
	if len(m) != 2 {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n < 1 || n > 65535 {
		return 0
	}
	return uint16(n)
}

// pluginIDFromCmd 从插件子进程命令行提取插件 id（.../providers/<id>/<entry> 路径段）。
func pluginIDFromCmd(cmd string) string {
	m := providerDirRe.FindStringSubmatch(cmd)
	if len(m) != 2 {
		return ""
	}
	return m[1]
}

// classifyOrphans 纯函数：按命令行分类残留进程（便于单测）。
// runtimeDir 为实例运行目录（runtime/）；instances 为当前实例列表。
func classifyOrphans(procs []procLine, runtimeDir string, instances []Instance) OrphanScan {
	scan := OrphanScan{Items: []OrphanProcess{}}
	running := map[string]bool{}
	for _, inst := range instances {
		if inst.Status.State == "Running" {
			running[inst.Name] = true
		}
	}
	runtimeLow := strings.ToLower(runtimeDir)
	dirSep := string(os.PathSeparator)

	// 宿主动进程集合：插件子进程的宿主 = opencode2api 主进程（按 PID 匹配）。
	hosts := map[int]bool{}
	for _, p := range procs {
		if strings.Contains(strings.ToLower(p.Name), "opencode2api") {
			hosts[p.PID] = true
		}
	}

	for _, p := range procs {
		if p.Cmd == "" {
			continue
		}
		low := strings.ToLower(p.Cmd)

		// 探针扫描残留（runtime/_probe/worker-*）。
		if strings.Contains(low, "_probe") {
			scan.Items = append(scan.Items, OrphanProcess{
				PID: p.PID, Name: p.Name, Category: "probe",
				Detail: "探针扫描残留（sing-box 探测进程）",
			})
			continue
		}
		// 统一网关子进程：保留（随实例池自动管理）。
		if strings.Contains(low, "_unified-gateway") {
			continue
		}
		// 实例目录进程：归属已知实例。
		matched := ""
		port := uint16(0)
		for _, inst := range instances {
			instDir := runtimeLow + dirSep + strings.ToLower(inst.Name) + dirSep
			if strings.Contains(low, instDir) {
				matched = inst.Name
				port = portFromCmd(p.Cmd)
				break
			}
		}
		if matched != "" {
			if running[matched] {
				continue // 实例运行中 = 使用中，保留
			}
			scan.Items = append(scan.Items, OrphanProcess{
				PID: p.PID, Name: p.Name, Category: "orphan",
				Instance: matched, Port: port,
				Detail: "实例已停止但进程残留",
			})
			continue
		}
		// 管理器自身（config.json 在数据目录，不在 runtime/）：保留。
		if strings.Contains(low, "config.json") && !strings.Contains(low, runtimeLow) {
			continue
		}
		// 插件子进程（--provider-serve）：宿主存活 = 运行中，保留；
		// 宿主退出（PPID 不在宿主动进程集合 / 无 PPID 信息）= 残留，按孤儿处理。
		if strings.Contains(low, "--provider-serve") {
			if p.PPID > 0 && hosts[p.PPID] {
				continue // 宿主 opencode2api 存活，由插件管理器自行管理
			}
			scan.Items = append(scan.Items, OrphanProcess{
				PID: p.PID, Name: p.Name, Category: "orphan",
				Instance: pluginIDFromCmd(p.Cmd),
				Detail:   "插件子进程残留（宿主 opencode2api 已退出）",
			})
			continue
		}
		// runtime/ 下未识别归属的进程（罕见历史残留）。
		if strings.Contains(low, runtimeLow) {
			scan.Items = append(scan.Items, OrphanProcess{
				PID: p.PID, Name: p.Name, Category: "orphan",
				Port: portFromCmd(p.Cmd), Detail: "未识别实例残留",
			})
		}
	}

	scan.Total = len(scan.Items)
	for _, it := range scan.Items {
		if it.Category == "probe" {
			scan.Probe++
		} else {
			scan.Orphan++
		}
	}
	return scan
}

// ScanOrphans 枚举系统进程并分类（当前机器上本应用的残留进程）。
func (m *Manager) ScanOrphans() OrphanScan {
	procs, err := listAppProcesses()
	if err != nil {
		return OrphanScan{Items: []OrphanProcess{}}
	}
	return classifyOrphans(procs, m.paths.RuntimeDir, m.ListInstances())
}

// KillOrphans 按 PID 清除残留进程；仅允许杀本次扫描结果内的进程。
func (m *Manager) KillOrphans(pids []int) OrphanKillResult {
	result := OrphanKillResult{Killed: []int{}, Errors: map[string]string{}}
	if len(pids) == 0 {
		return result
	}
	// 复核：只杀当前系统里属于本应用的进程（防误杀任意 PID）。
	valid := map[int]bool{}
	if procs, err := listAppProcesses(); err == nil {
		for _, p := range procs {
			valid[p.PID] = true
		}
	}
	for _, pid := range pids {
		if !valid[pid] {
			result.Errors[strconv.Itoa(pid)] = "不是本应用进程，已跳过"
			continue
		}
		if err := killProcess(pid); err != nil {
			result.Errors[strconv.Itoa(pid)] = err.Error()
		} else {
			result.Killed = append(result.Killed, pid)
		}
	}
	return result
}

// OrphanScanHandler GET 探测残留进程。
func (m *Manager) OrphanScanHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodOK(w, r, http.MethodGet) {
			return
		}
		writeJSON(w, m.ScanOrphans())
	}
}

// OrphanKillHandler POST 一键清除选中的残留进程（body: {"pids":[1,2,3]}）。
func (m *Manager) OrphanKillHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodOK(w, r, http.MethodPost) {
			return
		}
		var req struct {
			PIDs []int `json:"pids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "请求格式错误: "+err.Error())
			return
		}
		writeJSON(w, m.KillOrphans(req.PIDs))
	}
}
