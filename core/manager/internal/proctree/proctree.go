// Package proctree 提供按父子关系（PPID）分析进程树的纯逻辑，以及孤儿判定
// 共用的命令行/路径归一函数。
//
// 独立 internal 小包的原因：core/manager（M1 孤儿扫描/清理端点）与
// core/manager/pluginprovider（M3 reapOrphans 自动回收）都需要“找出某进程的后代”
// 这一判定。pluginprovider 实际可以 import core/manager（manager.go 已这样做），
// 但后代判定与命令行 token 判定都依赖 manager 的未导出实现，跨包复用只能导出
// 内部细节；把共享纯逻辑下沉到 internal 小包，两处调用同一实现，避免逻辑漂移。
//
// 本包不依赖平台库、不枚举进程、不杀进程：输入是全量进程枚举行，输出是纯数据。
// 具体枚举/杀进程由调用方按平台实现（Windows taskkill /T、非 Windows SIGKILL）。
package proctree

import (
	"regexp"
	"sort"
	"strings"
)

// Process 全量进程枚举的一行（pid/ppid/cmd）。JSON tag 供 Windows CIM
// ConvertTo-Json 输出直接反序列化（键名 pid/ppid/cmd）。
type Process struct {
	PID  int    `json:"pid"`
	PPID int    `json:"ppid"`
	Cmd  string `json:"cmd"`
}

// providerServeRe --provider-serve 特征判定（M2 P2-1 token 边界）。
// 裸子串 Contains 会被 --provider-server / --provider-services 等无关参数命中。
// 边界字符集 = 空白 + 双引号：真实 spawn 契约（pluginprovider manager.go）是
// `... --provider-serve --port 0`（两侧空白）；命令行里的引号出现在路径两端
// （如 "C:\a b\x.exe" --provider-serve），若参数被包装进程带引号下发（形如
// "--provider-serve"），引号即边界——两种形态都有，单测锁定。
var providerServeRe = regexp.MustCompile(`(^|[\s"])--provider-serve([\s"]|$)`)

// HasProviderServeToken 判断命令行是否含独立的 --provider-serve 参数。
// core/manager 的孤儿分类与 pluginprovider 的 reapOrphans 共用（M3 复核 P2-2），
// 避免两处正则各写一份后漂移。
func HasProviderServeToken(cmd string) bool {
	return providerServeRe.MatchString(cmd)
}

// NormalizePath 进程目录比对前的路径归一：`\` 统一为 `/` 再小写。
// Windows 路径大小写不敏感且 `\`/`/` 混用等价（`D:/deploy/providers` 与
// `D:\deploy\providers` 指同一目录），不归一会把「本目录孤儿」当外来目录列出。
// 比对两侧（命令行与 ProvidersDir）都过本函数；core/manager 与 pluginprovider
// 共用（M3 复核 P2-2）。
func NormalizePath(s string) string {
	return strings.ToLower(strings.ReplaceAll(s, `\`, `/`))
}

// FindDescendants 返回 procs 中所有“祖先链在 maxDepth 层内到达 roots”的进程。
//
// 语义（调用方与单测共同遵守）：
//   - 深度从 1 起算：1 = 直接子进程，2 = 孙进程；maxDepth <= 0 返回 nil。
//   - roots 自身永不返回（即使环状引用把它们挂到彼此后代上）。
//   - 结果按深度**从深到浅**排列（同深度保持 procs 原始顺序），可直接用于
//     “先杀后代、再杀父进程”的回收顺序；非 Windows 上父进程死后 PPID 关联会
//     失效，顺序不可颠倒。
//   - 环防护：visited 以 PID 去重，BFS 保证每个 PID 只按最短深度出现一次。
//   - PID <= 0 的枚举行（Windows 的 Idle/System、异常工具输出）永不返回：
//     Unix `kill(0, SIGKILL)` 会杀调用者所在进程组，必须从源头拦住（P2-1）。
//   - 只按 PPID 关联，不匹配命令行/进程名——这是“不误杀其它项目同名进程
//     （如 ARTEX 的 mihomo）”的安全边界，调用方不得加名字过滤放宽。
//
// PID 复用残余风险（M3 C8，本阶段**显式接受**）：判定不校验进程创建时间。
// 调用方（reapOrphans / 孤儿端点）都是在枚举后立即回收，窗口 = 枚举→杀之间
// （极小）；完整消除需要带创建时间的运行期快照（路线 B），不在 M3 范围。
// 详见 M3 计划 §3.4 / §五 C8 与 issue-log 2026-11-10 #7。
func FindDescendants(procs []Process, roots []int, maxDepth int) []Process {
	if maxDepth <= 0 || len(roots) == 0 || len(procs) == 0 {
		return nil
	}
	rootSet := make(map[int]struct{}, len(roots))
	frontier := make(map[int]struct{}, len(roots))
	for _, r := range roots {
		if r <= 0 {
			continue
		}
		rootSet[r] = struct{}{}
		frontier[r] = struct{}{}
	}
	if len(frontier) == 0 {
		return nil
	}

	type hit struct {
		p     Process
		depth int
	}
	var found []hit
	// visited 含 roots（深度 0）：既防环，也保证 roots 不被当作后代返回。
	visited := make(map[int]struct{}, len(procs)+len(roots))
	for r := range rootSet {
		visited[r] = struct{}{}
	}

	for depth := 1; depth <= maxDepth && len(frontier) > 0; depth++ {
		next := make(map[int]struct{})
		for _, p := range procs {
			if p.PID <= 0 {
				continue // 无效 PID：绝不能进入回收集合（Unix kill(0,SIGKILL) 会杀进程组）
			}
			if _, ok := frontier[p.PPID]; !ok {
				continue // 父进程不在上一层集合内 → 本层不是它的后代
			}
			if _, ok := visited[p.PID]; ok {
				continue // 已按更浅深度命中 / 是 root / 环
			}
			visited[p.PID] = struct{}{}
			found = append(found, hit{p: p, depth: depth})
			next[p.PID] = struct{}{}
		}
		frontier = next
	}
	if len(found) == 0 {
		return nil
	}
	// 深→浅；同深度保持输入顺序（BFS 追加顺序即输入顺序）。
	sort.SliceStable(found, func(i, j int) bool { return found[i].depth > found[j].depth })
	out := make([]Process, len(found))
	for i, h := range found {
		out[i] = h.p
	}
	return out
}
