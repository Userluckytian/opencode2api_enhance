// 自定义模型源向既有子进程传播：把 manager 配置里的 providers（含 custom）补写进
// runtime 下每个实例与统一网关的 opencode2api.json。
// 运行中的子进程靠自身 3s 配置监视热重载（providers 变化 → 原地重建厂商集合 →
// /v1/models 立即出现自定义模型）；停着的实例下次启动时配置会整体重新生成，
// 此处顺带保持磁盘一致。网关配置会在成员变化时整体重建，来源同为 manager 配置，不冲突。
package manager

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
)

// PropagateCustomProviders 把当前 manager 配置的 providers 传播到全部子进程配置文件
// （runtime/*/opencode2api.json：实例 + 统一网关）。custom 条目替换、其余保留、
// 原 providers 缺失时物化内建（与 SyncCustomProviders 同语义）。仅写有变化的文件。
func (m *Manager) PropagateCustomProviders() error {
	appCfg := m.loadConfig()
	entries, err := os.ReadDir(m.paths.RuntimeDir)
	if err != nil {
		return nil // runtime 目录不存在 = 尚无实例/网关，跳过
	}
	var firstErr error
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		cfgPath := filepath.Join(m.paths.RuntimeDir, e.Name(), "opencode2api.json")
		if !fileExists(cfgPath) {
			continue
		}
		if err := patchProvidersFile(cfgPath, appCfg.Providers); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// patchProvidersFile 替换子进程配置文件 providers 中的 custom 条目（其余保留）。
// 文件缺失/损坏跳过；内容未变化不写（避免触发子进程无谓的热重载）。
func patchProvidersFile(path string, managerProviders []map[string]any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil // 损坏文件不碰（下次启动会整体重新生成）
	}
	existing, _ := cfg["providers"].([]any)
	existingMaps := make([]map[string]any, 0, len(existing))
	for _, p := range existing {
		if m, ok := p.(map[string]any); ok {
			existingMaps = append(existingMaps, m)
		}
	}
	// customs 取自 manager 配置（type=custom 的条目）。
	var customs []map[string]any
	for _, p := range managerProviders {
		if t, _ := p["type"].(string); t == "custom" {
			customs = append(customs, p)
		}
	}
	merged := mergeProviderEntries(existingMaps, customs)
	if len(customs) == 0 && len(existingMaps) == 0 {
		slog.Info("patchProvidersFile: skip (both sides empty)", "path", path)
		return nil // 两边都空：保持「无 providers = 自动注册内建」语义，不写
	}
	// 与现文件等价则跳过（map 顺序不稳定，按数量+逐条比对）。
	if providersEquivalent(existingMaps, merged) {
		slog.Info("patchProvidersFile: skip (equivalent)", "path", path, "count", len(merged))
		return nil
	}
	// 诊断日志：记录传播前后差异
	existingIDs := make([]string, 0, len(existingMaps))
	for _, m := range existingMaps {
		if id, _ := m["id"].(string); id != "" {
			existingIDs = append(existingIDs, id)
		}
	}
	customIDs := make([]string, 0, len(customs))
	for _, m := range customs {
		if id, _ := m["id"].(string); id != "" {
			customIDs = append(customIDs, id)
		}
	}
	mergedIDs := make([]string, 0, len(merged))
	for _, m := range merged {
		if id, _ := m["id"].(string); id != "" {
			mergedIDs = append(mergedIDs, id)
		}
	}
	slog.Info("patchProvidersFile: patching",
		"path", path,
		"existing_ids", existingIDs,
		"customs_from_manager", customIDs,
		"merged_ids", mergedIDs,
	)
	cfg["providers"] = merged
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(path, out, 0o644)
}

// providersEquivalent 粗判两个 providers 列表是否等价（长度相同且逐条 JSON 相等，不计顺序差异
// 之外的细微变化；用于跳过无变化的写盘）。
func providersEquivalent(a, b []map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	used := make([]bool, len(b))
	for _, x := range a {
		xj, _ := json.Marshal(x)
		matched := false
		for i, y := range b {
			if used[i] {
				continue
			}
			yj, _ := json.Marshal(y)
			if string(xj) == string(yj) {
				used[i] = true
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}
