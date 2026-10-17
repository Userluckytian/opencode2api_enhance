// 本地代理文件导入：读取用户本机已有的 Clash 配置文件（单文件 / 目录），
// 解析其中的 proxies 并入节点池。
//
// 为什么需要它：节点池原本只有三条来源——Clash 外部控制 API、Clash Verge 的固定
// profiles 目录、本管理器的订阅缓存。用户「已经导入 Clash 的本地代理文件」若不在
// Clash Verge 的默认目录（Clash for Windows / mihomo party / 便携版 / 自建目录），
// 前两条都读不到。本文件补上「用户显式指定路径」这条来源。
//
// 设计取舍：
//   - 只读，不写用户文件；不监听文件变化（用户点「重载」即刷新，YAGNI）。
//   - 目录只取 1 层 *.yaml/*.yml/*.txt，不递归（避免误扫整个磁盘）。
//   - 节点落订阅缓存（复用分组机制），因此删除源即可清节点、与订阅节点同源。
package manager

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// localProfileMaxFileSize 单文件读取上限（与订阅拉取一致，防误选超大文件卡死）。
const localProfileMaxFileSize = 32 << 20

// LocalProfileSource 一条本地代理文件源（持久化 dataDir/local_profiles.json）。
type LocalProfileSource struct {
	Path       string `json:"path"`                  // 用户指定的绝对路径（文件或目录）
	Group      string `json:"group"`                 // 导入分组名（用户指定，或取文件名/目录名）
	Kind       string `json:"kind"`                  // "file" | "dir"
	NodeCount  int    `json:"node_count"`            // 最近一次成功导入的节点数
	LastImport string `json:"last_import,omitempty"` // RFC3339
	LastError  string `json:"last_error,omitempty"`  // 最近一次失败原因（成功后清空）
}

// localProfilesPath 本地代理文件源列表路径。
func (m *Manager) localProfilesPath() string {
	return filepath.Join(m.paths.DataDir, "local_profiles.json")
}

// 本地文件源读写全程持 localProfileMu：load→merge→save 必须成对在同一临界区内
// 完成（否则并发重载会基于旧快照互相覆盖，把对方刚登记的源抹掉）。
// 对外只暴露 loadLocalProfiles / saveLocalProfiles（自带锁）；带 Locked 后缀的仅供
// 已持锁的内部流程调用——与 subscribe.go 的 subscriptionCacheMu 模式一致。

// loadLocalProfiles 读取本地文件源列表（不存在/损坏返回空）。
func (m *Manager) loadLocalProfiles() []LocalProfileSource {
	m.localProfileMu.Lock()
	defer m.localProfileMu.Unlock()
	return m.loadLocalProfilesLocked()
}

// loadLocalProfilesLocked 读取本地文件源列表（锁内调用）。
func (m *Manager) loadLocalProfilesLocked() []LocalProfileSource {
	data, err := os.ReadFile(m.localProfilesPath())
	if err != nil {
		return nil
	}
	var list []LocalProfileSource
	if json.Unmarshal(data, &list) != nil {
		return nil
	}
	return list
}

// saveLocalProfiles 持久化本地文件源列表（原子写）。
func (m *Manager) saveLocalProfiles(list []LocalProfileSource) error {
	m.localProfileMu.Lock()
	defer m.localProfileMu.Unlock()
	return m.saveLocalProfilesLocked(list)
}

// saveLocalProfilesLocked 持久化本地文件源列表（锁内调用）。
func (m *Manager) saveLocalProfilesLocked(list []LocalProfileSource) error {
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化本地代理文件源失败: %v", err)
	}
	return writeFileAtomic(m.localProfilesPath(), data)
}

// defaultGroupForPath 路径缺省分组名：文件取去扩展名的文件名，目录取目录名。
func defaultGroupForPath(path string, isDir bool) string {
	base := filepath.Base(path)
	if isDir {
		return base
	}
	base = strings.TrimSuffix(base, filepath.Ext(base))
	if strings.TrimSpace(base) == "" {
		return "本地文件"
	}
	return base
}

// collectProfileFiles 把路径展开为待解析的文件列表：
// 单文件 → 自身；目录 → 其下 1 层的 *.yaml/*.yml/*.txt（按名排序，保证结果稳定）。
func collectProfileFiles(path string) (files []string, isDir bool, err error) {
	fi, statErr := os.Stat(path)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			return nil, false, fmt.Errorf("路径不存在: %s", path)
		}
		return nil, false, fmt.Errorf("无法访问路径 %s: %v", path, statErr)
	}
	if !fi.IsDir() {
		return []string{path}, false, nil
	}
	entries, readErr := os.ReadDir(path)
	if readErr != nil {
		return nil, true, fmt.Errorf("无法读取目录 %s: %v", path, readErr)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue // 只取 1 层，不递归
		}
		switch strings.ToLower(filepath.Ext(e.Name())) {
		case ".yaml", ".yml", ".txt":
			files = append(files, filepath.Join(path, e.Name()))
		}
	}
	sort.Strings(files)
	if len(files) == 0 {
		return nil, true, fmt.Errorf("目录下没有 .yaml/.yml/.txt 配置文件: %s", path)
	}
	return files, true, nil
}

// parseProfileFile 解析单个配置文件：
// 先按 Clash YAML 的 proxies 解析（主路径），无节点时回退 parseSubscription
// （兼容用户把「明文链接列表 / base64 订阅文本」存成 .txt 的情况）。
func parseProfileFile(file string) ([]SubscribeNode, error) {
	fi, err := os.Stat(file)
	if err != nil {
		return nil, fmt.Errorf("无法访问文件 %s: %v", file, err)
	}
	if fi.Size() > localProfileMaxFileSize {
		return nil, fmt.Errorf("文件过大（%d 字节，上限 %d）: %s", fi.Size(), localProfileMaxFileSize, file)
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("读取文件失败 %s: %v", file, err)
	}
	content := strings.TrimSpace(strings.TrimPrefix(string(raw), "\uFEFF"))
	if content == "" {
		return nil, fmt.Errorf("文件内容为空: %s", file)
	}

	if clash, clashErr := parseClashYAML(content); clashErr == nil && len(clash) > 0 {
		out := make([]SubscribeNode, 0, len(clash))
		for i := range clash {
			out = append(out, subscribeFromClash(clash[i]))
		}
		return out, nil
	}
	// 回退：明文链接 / base64 / sing-box JSON
	nodes, subErr := parseSubscription(content)
	if subErr != nil {
		return nil, fmt.Errorf("无法解析为 Clash 配置或代理链接: %s", file)
	}
	return nodes, nil
}

// loadLocalProfileNodes 解析一个本地源（文件或目录）→ SubscribeNode 列表。
// 返回：节点、展开的文件数、错误。目录内单个文件失败不阻断其他文件（错误合并返回）。
func loadLocalProfileNodes(path string) (nodes []SubscribeNode, fileCount int, err error) {
	files, _, err := collectProfileFiles(path)
	if err != nil {
		return nil, 0, err
	}
	var errs []string
	seen := map[string]bool{}
	for _, f := range files {
		parsed, parseErr := parseProfileFile(f)
		if parseErr != nil {
			errs = append(errs, parseErr.Error())
			continue
		}
		// 目录多文件时，节点缺分组用各自文件名（用户未指定分组才有意义，调用方会覆盖）
		fileGroup := defaultGroupForPath(f, false)
		for _, n := range parsed {
			if isPseudoNode(n.Name) || seen[n.Name] {
				continue
			}
			seen[n.Name] = true
			if n.Group == "" {
				n.Group = fileGroup
			}
			nodes = append(nodes, n)
		}
	}
	if len(nodes) == 0 {
		if len(errs) > 0 {
			return nil, len(files), errors.New(strings.Join(errs, "；"))
		}
		return nil, len(files), fmt.Errorf("未解析到任何可用节点: %s", path)
	}
	return nodes, len(files), nil
}

// ImportLocalProfile 导入（或重载）一个本地代理文件源：解析 → 打分组 → 落订阅缓存 → 登记源。
// group 为空时取默认分组名（文件名/目录名）。返回导入节点数与展开文件数。
func (m *Manager) ImportLocalProfile(path, group string) (imported int, fileCount int, groupName string, err error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return 0, 0, "", fmt.Errorf("路径不能为空")
	}
	if abs, absErr := filepath.Abs(path); absErr == nil {
		path = abs
	}
	_, isDir, statErr := collectProfileFiles(path)
	if statErr != nil {
		return 0, 0, "", statErr
	}
	nodes, fileCount, err := loadLocalProfileNodes(path)
	if err != nil {
		_ = m.upsertLocalProfileSource(LocalProfileSource{Path: path, Kind: kindOf(isDir), LastError: err.Error()}, false)
		return 0, fileCount, "", err
	}

	previous, existed := m.localProfileSourceByPath(path)
	groupName = strings.TrimSpace(group)
	if groupName == "" {
		// 已有源记录的分组名优先（重载时保持原分组，不因文件名变化跳组）
		if existed && previous.Group != "" {
			groupName = previous.Group
		} else {
			groupName = defaultGroupForPath(path, isDir)
		}
	}
	if owner := m.groupOwner(groupName, path, ""); owner != "" {
		return 0, fileCount, groupName, fmt.Errorf("分组 %q 已被%s使用，请换一个分组名", groupName, owner)
	}
	for i := range nodes {
		nodes[i].Group = groupName
	}
	source := LocalProfileSource{
		Path:       path,
		Group:      groupName,
		Kind:       kindOf(isDir),
		NodeCount:  len(nodes),
		LastImport: time.Now().Format(time.RFC3339),
	}
	if err := m.upsertLocalProfileSource(source, true); err != nil {
		return 0, fileCount, groupName, err
	}
	if err := m.saveSubscriptionCacheGrouped(nodes); err != nil {
		if existed {
			_ = m.upsertLocalProfileSource(previous, true)
		} else {
			_ = m.removeLocalProfileSourceOnly(path)
		}
		return 0, fileCount, groupName, err
	}
	if existed && previous.Group != "" && previous.Group != groupName && m.groupOwner(previous.Group, path, "") == "" {
		_, _ = m.RemoveSubscriptionGroupNodes(previous.Group)
	}
	return len(nodes), fileCount, groupName, nil
}

// kindOf 目录/文件标识。
func kindOf(isDir bool) string {
	if isDir {
		return "dir"
	}
	return "file"
}

// localProfileSourceByPath 按路径查源记录。
func (m *Manager) localProfileSourceByPath(path string) (LocalProfileSource, bool) {
	for _, s := range m.loadLocalProfiles() {
		if s.Path == path {
			return s, true
		}
	}
	return LocalProfileSource{}, false
}

// upsertLocalProfileSource 写入/更新源记录（path 为键）。
// replace=true 表示本次是成功导入（覆盖 NodeCount/LastImport 并清空 LastError）；
// false 表示仅记录失败（保留已有成功信息，只更新 LastError）。
// 整个 load→改→save 持 localProfileMu（并发重载时不会互相覆盖丢源）。
func (m *Manager) upsertLocalProfileSource(src LocalProfileSource, replace bool) error {
	m.localProfileMu.Lock()
	defer m.localProfileMu.Unlock()
	list := m.loadLocalProfilesLocked()
	for i := range list {
		if list[i].Path != src.Path {
			continue
		}
		if replace {
			list[i] = src
		} else {
			list[i].LastError = src.LastError
		}
		return m.saveLocalProfilesLocked(list)
	}
	list = append(list, src)
	return m.saveLocalProfilesLocked(list)
}

func (m *Manager) removeLocalProfileSourceOnly(path string) error {
	m.localProfileMu.Lock()
	defer m.localProfileMu.Unlock()
	list := m.loadLocalProfilesLocked()
	kept := list[:0]
	for _, source := range list {
		if source.Path != path {
			kept = append(kept, source)
		}
	}
	return m.saveLocalProfilesLocked(kept)
}

// RemoveLocalProfile 移除本地文件源并清理其分组节点。
// 返回清理的节点数；源不存在时返回 (0, false, nil)。
func (m *Manager) RemoveLocalProfile(path string) (removedNodes int, removed bool, err error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return 0, false, fmt.Errorf("路径不能为空")
	}
	if abs, absErr := filepath.Abs(path); absErr == nil {
		path = abs
	}

	// 锁内删源记录（load→filter→save 必须成对，防并发重载覆盖）；
	// 清节点在锁外进行——走订阅缓存锁，两把锁不嵌套。
	group, found, shared, err := m.removeLocalProfileSource(path)
	if err != nil {
		return 0, true, err
	}
	if !found {
		return 0, false, nil
	}
	if group != "" && !shared && m.groupOwner(group, "", "") == "" {
		n, err := m.RemoveSubscriptionGroupNodes(group)
		if err != nil {
			return 0, true, err
		}
		removedNodes = n
	}
	return removedNodes, true, nil
}

// removeLocalProfileSource 在锁内删除源记录。
// 返回：该源的分组名、源是否存在过、该分组是否仍被其它源引用。
func (m *Manager) removeLocalProfileSource(path string) (group string, found, shared bool, err error) {
	m.localProfileMu.Lock()
	defer m.localProfileMu.Unlock()
	list := m.loadLocalProfilesLocked()
	kept := make([]LocalProfileSource, 0, len(list))
	for _, s := range list {
		if s.Path == path {
			found = true
			group = s.Group
			continue
		}
		kept = append(kept, s)
	}
	if !found {
		return "", false, false, nil
	}
	if err := m.saveLocalProfilesLocked(kept); err != nil {
		return group, true, false, err
	}
	// 同一分组可能还被其他源/订阅共用：仅当没有其它源指向该分组时才清节点。
	if group != "" {
		for _, s := range kept {
			if s.Group == group {
				shared = true
				break
			}
		}
	}
	return group, true, shared, nil
}

// 说明：本地文件源导入的节点直接写入订阅缓存（与订阅节点同源），
// 因此 ListNodesWithGroup 的「订阅缓存并入」一步已自动带上它们，
// 无需在合并链里再加一条来源——避免同一批节点被两条路径重复收集。

// ---------- HTTP handlers ----------

// LocalProfilesListHandler GET → 全部本地文件源。
func (m *Manager) LocalProfilesListHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodOK(w, r, http.MethodGet) {
			return
		}
		list := m.loadLocalProfiles()
		if list == nil {
			list = []LocalProfileSource{}
		}
		writeJSON(w, map[string]any{"sources": list})
	}
}

// LocalProfilesAddHandler POST {path, group} → 导入本地代理文件/目录并登记为源。
func (m *Manager) LocalProfilesAddHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodOK(w, r, http.MethodPost) {
			return
		}
		var req struct {
			Path  string `json:"path"`
			Group string `json:"group"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			writeErr(w, http.StatusBadRequest, "请求体解析失败")
			return
		}
		if strings.TrimSpace(req.Path) == "" {
			writeErr(w, http.StatusBadRequest, "path 必填")
			return
		}
		imported, files, group, err := m.ImportLocalProfile(req.Path, req.Group)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, map[string]any{
			"status":   "ok",
			"path":     req.Path,
			"group":    group,
			"imported": imported,
			"files":    files,
		})
	}
}

// LocalProfilesRemoveHandler POST {path} → 移除源并清理其分组节点。
func (m *Manager) LocalProfilesRemoveHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodOK(w, r, http.MethodPost) {
			return
		}
		var req struct {
			Path string `json:"path"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil || strings.TrimSpace(req.Path) == "" {
			writeErr(w, http.StatusBadRequest, "path 必填")
			return
		}
		removedNodes, removed, err := m.RemoveLocalProfile(req.Path)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, map[string]any{"status": "ok", "removed": removed, "removed_nodes": removedNodes})
	}
}

// LocalProfilesReloadHandler POST {path} → 重载一个源；path 为空时重载全部。
func (m *Manager) LocalProfilesReloadHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodOK(w, r, http.MethodPost) {
			return
		}
		var req struct {
			Path string `json:"path"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		path := strings.TrimSpace(req.Path)
		if path != "" {
			imported, files, group, err := m.ImportLocalProfile(path, "")
			if err != nil {
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
			writeJSON(w, map[string]any{"status": "ok", "imported": imported, "files": files, "group": group, "reloaded": 1})
			return
		}
		// 全部重载：逐源执行，单个失败不阻断（结果里带逐源状态）
		sources := m.loadLocalProfiles()
		results := make([]map[string]any, 0, len(sources))
		total, okCount := 0, 0
		for _, s := range sources {
			imported, files, group, err := m.ImportLocalProfile(s.Path, s.Group)
			item := map[string]any{"path": s.Path, "group": group, "imported": imported, "files": files}
			if err != nil {
				item["error"] = err.Error()
			} else {
				okCount++
				total += imported
			}
			results = append(results, item)
		}
		writeJSON(w, map[string]any{
			"status":   "ok",
			"reloaded": okCount,
			"total":    len(sources),
			"imported": total,
			"results":  results,
		})
	}
}
