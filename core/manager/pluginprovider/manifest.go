// provider.json 契约解析（设计定稿 docs/PLUGIN-PROVIDERS.md §三）。
// 主进程只读七个顶层保留键（id/name/version/api_version/entry/expose_all/exposed_models）；
// provider_private_configs 整体不解析（不透明）——Go json.Unmarshal 天然忽略未声明
// 字段，密钥从结构上就进不了主进程内存。provider.json 全文保留（面板回显/编辑回填，
// 与 /api/admin/custom-providers api_keys 明文回显同款取舍，见设计文档 §九）。
package pluginprovider

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// supportedAPIVersion 主进程支持的线协议契约版本（设计文档 §四）。
const supportedAPIVersion = 1

// Manifest 顶层保留键（字段规则见设计文档 §3.2）。
type Manifest struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Version    string `json:"version"`
	APIVersion int    `json:"api_version"`
	Entry      string `json:"entry"`
	// 模型暴露白名单（设计文档 §3.3）：ExposeAll 缺省 = true（全量透传）；
	// ExposeAll=false 时仅暴露 ExposedModels 内的模型 ID（对齐自定义源
	// allowed_models 语义，主进程侧过滤，插件零改动获得暴露控制）。
	ExposeAll     *bool    `json:"expose_all,omitempty"`
	ExposedModels []string `json:"exposed_models,omitempty"`
}

// parseManifest 仅做 JSON 解析（不含目录一致 / 文件存在等上下文校验）。
func parseManifest(raw []byte) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return Manifest{}, fmt.Errorf("provider.json 不是合法 JSON: %w", err)
	}
	if m.ID == "" {
		return Manifest{}, fmt.Errorf("缺少顶层字段 id")
	}
	return m, nil
}

// validateManifest 加载期校验：目录名一致、api_version 兼容、entry 安全且在目录内存在。
// 任一不满足 → 拒绝加载并在面板告警（不静默坏掉）。
func validateManifest(m Manifest, id, dir string) error {
	if m.ID != id {
		return fmt.Errorf("清单 id %q 与目录名 %q 不一致", m.ID, id)
	}
	if m.APIVersion != supportedAPIVersion {
		return fmt.Errorf("api_version %d 不兼容（当前契约版本 %d）", m.APIVersion, supportedAPIVersion)
	}
	entryPath, err := safeJoin(dir, m.Entry)
	if err != nil {
		return fmt.Errorf("entry 非法: %w", err)
	}
	fi, err := os.Stat(entryPath)
	if err != nil || fi.IsDir() {
		return fmt.Errorf("entry %q 指向的文件不存在", m.Entry)
	}
	return nil
}

// validateSave 保存期校验（面板编辑保存，设计文档 §六）：JSON 合法、id 与目录名一致、
// entry 指向存在的文件。api_version 兼容性是加载期决策，不在此拦截（保存后由面板告警）。
func validateSave(m Manifest, id, dir string) error {
	if m.ID != id {
		return fmt.Errorf("清单 id %q 与目录名 %q 不一致", m.ID, id)
	}
	if m.Entry == "" {
		return fmt.Errorf("缺少顶层字段 entry")
	}
	entryPath, err := safeJoin(dir, m.Entry)
	if err != nil {
		return fmt.Errorf("entry 非法: %w", err)
	}
	fi, err := os.Stat(entryPath)
	if err != nil || fi.IsDir() {
		return fmt.Errorf("entry %q 指向的文件不存在", m.Entry)
	}
	return nil
}

// safeJoin 将相对路径安全并入 dir（拒绝绝对路径与 .. 逃逸，防目录穿越）。
func safeJoin(dir, rel string) (string, error) {
	if rel == "" || filepath.IsAbs(rel) {
		return "", fmt.Errorf("必须是相对路径")
	}
	joined := filepath.Join(dir, rel)
	base := filepath.Clean(dir)
	clean := filepath.Clean(joined)
	if clean != base && !strings.HasPrefix(clean, base+string(filepath.Separator)) {
		return "", fmt.Errorf("路径越界")
	}
	return clean, nil
}

// validPluginID 校验插件 id 是普通目录名（无分隔符/无穿越）。
func validPluginID(id string) error {
	if id == "" || id == "." || id == ".." || filepath.Base(id) != id || strings.ContainsAny(id, `/\`) {
		return fmt.Errorf("非法插件 id %q", id)
	}
	return nil
}
