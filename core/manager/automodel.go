// auto 虚拟模型：配置类型 + 管理端读写 + 子进程传播。
// auto 是实例池的聚合路由入口：请求 model:"auto" 时，网关在「用户权重 × 模型实测反馈」
// 打分出的候选链里选模型（实例维度由既有 smart/failover 池逻辑负责），失败沿链降级。
// 权重按模型粒度配置（同模型跨实例同权重，2026-08-17 决策）；上下文护栏见根 auto_model.go。
package manager

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

// AutoModelCfg auto 虚拟模型配置（config.json 的 auto_model 键；与 show_node_prefix /
// providers 等键同款「根 AppConfig 与本 Config 双结构共用」生存策略，任一写者重写文件都不丢）。
type AutoModelCfg struct {
	Enabled bool `json:"enabled"`
	// Name 虚拟模型对外名称（默认 "auto"；可自定义避免与上游/其它 API 的模型名冲突）。
	Name string `json:"name,omitempty"`
	// Strategy 选择策略：balanced（默认，平滑加权轮询）/ speed（权重≥5 中选最快）/ quality（按权重锁定，失败才降）。
	Strategy string `json:"strategy,omitempty"`
	// Models 已勾选参与 auto 的模型展示名白名单（空 = 无候选，调用返回明确错误「请先配置」）。
	Models []string `json:"models,omitempty"`
	// Weights 模型展示名（/v1/models 可见名）→ 权重 0~10；缺省 5；0 = 永不参与 auto。
	Weights map[string]int `json:"weights,omitempty"`
	// ContextWindows 模型展示名 → 上下文上限 token；未配置的模型按保守默认处理
	//（见根 auto_model.go 的 defaultAutoContextTokens）。
	ContextWindows map[string]int `json:"context_windows,omitempty"`
}

const (
	AutoStrategyBalanced = "balanced"
	AutoStrategySpeed    = "speed"
	AutoStrategyQuality  = "quality"
)

// Normalize 就地规范：策略白名单（空/非法回 balanced）、名称去空白、白名单去空去重、
// 权重钳 0~10、非正上下文删除。
func (c *AutoModelCfg) Normalize() {
	if c == nil {
		return
	}
	c.Name = strings.TrimSpace(c.Name)
	switch c.Strategy {
	case AutoStrategySpeed, AutoStrategyQuality:
	default:
		c.Strategy = AutoStrategyBalanced
	}
	if len(c.Models) > 0 {
		seen := map[string]bool{}
		ms := c.Models[:0]
		for _, m := range c.Models {
			m = strings.TrimSpace(m)
			if m == "" || seen[m] {
				continue
			}
			seen[m] = true
			ms = append(ms, m)
		}
		c.Models = ms
	}
	for k, w := range c.Weights {
		if w < 0 {
			c.Weights[k] = 0
		} else if w > 10 {
			c.Weights[k] = 10
		}
	}
	for k, n := range c.ContextWindows {
		if n <= 0 {
			delete(c.ContextWindows, k)
		}
	}
}

// isEmpty 判定「全空配置」（关闭 + 默认名称 + 默认策略 + 无白名单 + 无权重 + 无上下文）。
func (c *AutoModelCfg) isEmpty() bool {
	return c == nil || (!c.Enabled && c.Name == "" && c.Strategy == AutoStrategyBalanced &&
		len(c.Models) == 0 && len(c.Weights) == 0 && len(c.ContextWindows) == 0)
}

// AutoModel 返回当前 auto 配置（未配置 = 关闭 + 默认策略的零值）。
func (m *Manager) AutoModel() AutoModelCfg {
	if c := m.loadConfig().AutoModel; c != nil {
		out := *c
		out.Normalize()
		return out
	}
	return AutoModelCfg{Strategy: AutoStrategyBalanced}
}

// SetAutoModel 保存 auto 配置并传播到全部子进程配置文件（运行中子进程 3s 热重载生效；
// 停着的实例下次启动经 buildOpenCodeCfg/buildRouterCfg 自动带上）。
func (m *Manager) SetAutoModel(cfg AutoModelCfg) error {
	cfg.Normalize()
	cur := m.loadConfig()
	if cfg.isEmpty() {
		cur.AutoModel = nil // 全空 = 未配置语义，不留空壳键
	} else {
		c := cfg
		cur.AutoModel = &c
	}
	if err := m.saveConfig(cur); err != nil {
		return err
	}
	return m.propagateAutoModel()
}

// propagateAutoModel 把 auto_model 键补写进 runtime 下每个子进程配置
// （实例 + 统一网关；仿 PropagateCustomProviders：内容未变不写，避免无谓热重载）。
func (m *Manager) propagateAutoModel() error {
	val := m.loadConfig().AutoModel
	entries, err := os.ReadDir(m.paths.RuntimeDir)
	if err != nil {
		return nil // runtime 目录不存在 = 尚无子进程，跳过
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
		if err := patchAutoModelFile(cfgPath, val); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// patchAutoModelFile 替换子进程配置的 auto_model 键（其余键保留）。
// 文件缺失/损坏跳过（下次启动会整体重新生成）；内容未变化不写（防触发子进程热重载）。
// 相等判定经 map 归一化（json.Unmarshal 到 map 再 DeepEqual），规避结构体字段序与
// map 键序差异导致的「语义相同却字节不同 → 每次保存都重写」。
func patchAutoModelFile(path string, cfg *AutoModelCfg) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var doc map[string]any
	if json.Unmarshal(data, &doc) != nil {
		return nil // 损坏文件不碰
	}
	if cfg == nil {
		if _, exists := doc["auto_model"]; !exists {
			return nil
		}
		delete(doc, "auto_model")
	} else {
		want, err := json.Marshal(cfg)
		if err != nil {
			return err
		}
		var wantMap map[string]any
		if err := json.Unmarshal(want, &wantMap); err != nil {
			return err
		}
		if reflect.DeepEqual(doc["auto_model"], wantMap) {
			return nil
		}
		doc["auto_model"] = wantMap
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(path, out, 0o644)
}
