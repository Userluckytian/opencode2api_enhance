// OpenCode 模型目录磁盘缓存（stale-while-revalidate）：
// ListModels 成功时把 zen+go 合并清单写入 <dataDir>/opencode_models/<id>.json；
// 拉取失败或空目录（重启后上游未就绪/官方站点抖动）时按 内存 → 磁盘 顺序兜底，
// 保证进程重启后聚合器首次 Refresh 即带上上一代 OpenCode 模型，后台刷新再修正。
// 同环境多进程（面板/各实例）共享 dataDir，互为缓存（原子写，后写覆盖无害）。
package opencode

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/6Kmfi6HP/opencode2api/core/contract"
)

// cacheDir 返回缓存目录：优先 OPCODE2API_DATA_DIR（壳按环境注入，实例子进程继承），
// 否则用户配置目录下 opencode2api-manager（与 custom / windsurf 同规则）。
func cacheDir() string {
	dir := os.Getenv("OPCODE2API_DATA_DIR")
	if dir == "" {
		if base, err := os.UserConfigDir(); err == nil && base != "" {
			dir = filepath.Join(base, "opencode2api-manager")
		} else {
			dir = "."
		}
	}
	return filepath.Join(dir, "opencode_models")
}

var unsafeFilename = regexp.MustCompile(`[^A-Za-z0-9_-]`)

// cachePath 本厂商缓存文件路径（id 兜底替换不安全字符）。
func (v *Vendor) cachePath() string {
	id := unsafeFilename.ReplaceAllString(v.cfg.ID, "_")
	if id == "" {
		id = "opencode"
	}
	return filepath.Join(cacheDir(), id+".json")
}

// modelsCacheFile 磁盘缓存结构。
type modelsCacheFile struct {
	SavedAt time.Time        `json:"saved_at"`
	Models  []contract.Model `json:"models"`
}

// saveModelsCache 原子写缓存（tmp + rename，读侧不会读到半截文件）。尽力而为，失败仅告警。
func (v *Vendor) saveModelsCache(models []contract.Model) {
	if len(models) == 0 {
		return
	}
	path := v.cachePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	data, err := json.Marshal(modelsCacheFile{SavedAt: time.Now(), Models: models})
	if err != nil {
		return
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // 失败路径清理；成功 Rename 后该名已不存在
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return
	}
	if err := tmp.Close(); err != nil {
		return
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return
	}
	if err := os.Rename(tmpName, path); err != nil {
		slog.Debug("opencode: rename models cache failed", "id", v.cfg.ID, "error", err)
	}
}

// loadModelsCache 读磁盘缓存（损坏/缺失/无本源模型返回 nil）。
func (v *Vendor) loadModelsCache() []contract.Model {
	data, err := os.ReadFile(v.cachePath())
	if err != nil {
		return nil
	}
	var f modelsCacheFile
	if json.Unmarshal(data, &f) != nil || len(f.Models) == 0 {
		return nil
	}
	out := make([]contract.Model, 0, len(f.Models))
	for _, m := range f.Models {
		// 防手工篡改：必须仍是本厂商标识，且模型 ID 非空。
		if m.ID == "" || m.Provider != v.cfg.ID {
			continue
		}
		out = append(out, m)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// fallbackModels 上游失败或空目录时的兜底：内存缓存 → 磁盘缓存。
// 都没有则返回 nil（与历史「空列表 + nil 错误」一致，聚合器按从未成功处理）。
func (v *Vendor) fallbackModels() []contract.Model {
	v.modelMu.RLock()
	cached := append([]contract.Model(nil), v.cacheAll...)
	v.modelMu.RUnlock()
	if len(cached) > 0 {
		slog.Info("opencode: using cached model catalog", "id", v.cfg.ID, "count", len(cached), "source", "memory")
		return cached
	}
	disk := v.loadModelsCache()
	if len(disk) == 0 {
		return nil
	}
	v.modelMu.Lock()
	v.cacheAll = disk
	v.modelMu.Unlock()
	slog.Info("opencode: using cached model catalog", "id", v.cfg.ID, "count", len(disk), "source", "disk")
	return disk
}
