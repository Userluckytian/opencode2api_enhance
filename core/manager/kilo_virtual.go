package manager

// Kilo 虚拟模型配置层（阶段 M / Task 1）。
//
// 参考 9router 的 Combo 概念（{id, name, models[]} 的命名组合），但配置存
// 独立 JSON 而非 config.json —— 避免与 main 包 AppConfig 的 MergeConfigJSON
// 双结构互相覆盖，也便于原子整表保存。全部逻辑在 core/manager 包内，
// 零 main 包依赖（延续阶段 K 独立性红线）。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

const (
	kiloVirtualFileName = "virtual_models.json"
	// KiloVirtualStrategyFallback 按序试，2xx 即返回；失败自动换下一个（默认，9router 式）
	KiloVirtualStrategyFallback = "fallback"
	// KiloVirtualStrategyRoundRobin 每次轮换起点，后续仍按序 fallback
	KiloVirtualStrategyRoundRobin = "round_robin"
	// KiloVirtualStrategyRace 并发竞速，第一个有效 2xx 胜出（用户显式开启，代价自担）
	KiloVirtualStrategyRace = "race"
)

var kiloVirtualNameRE = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,48}$`)

// VirtualModel 一个命名虚拟模型。
type VirtualModel struct {
	Name     string   `json:"name"`
	Models   []string `json:"models"`
	Strategy string   `json:"strategy"`
	Enabled  bool     `json:"enabled"`
}

type virtualModelFile struct {
	Version int            `json:"version"`
	Models  []VirtualModel `json:"models"`
}

// kiloVirtualStore 虚拟模型存储（读写独立 JSON + 轮询游标）。
type kiloVirtualStore struct {
	mgr  *Manager
	mu   sync.RWMutex
	list []VirtualModel
	// 轮询游标：name → 下一个起点索引（atomic 累加，读时不持 mu）
	cursors sync.Map // map[string]*atomic.Uint64
}

func (m *Manager) KiloVirtualPath() string {
	return filepath.Join(m.Paths().RuntimeDir, "kilo", kiloVirtualFileName)
}

func newKiloVirtualStore(m *Manager) *kiloVirtualStore {
	s := &kiloVirtualStore{mgr: m}
	s.load()
	return s
}

func (s *kiloVirtualStore) load() {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := os.ReadFile(s.mgr.KiloVirtualPath())
	if err != nil {
		s.list = []VirtualModel{}
		return
	}
	var f virtualModelFile
	if json.Unmarshal(b, &f) != nil {
		s.list = []VirtualModel{}
		return
	}
	s.list = f.Models
	if s.list == nil {
		s.list = []VirtualModel{}
	}
}

func (s *kiloVirtualStore) save() error {
	s.mu.RLock()
	f := virtualModelFile{Version: 1, Models: append([]VirtualModel(nil), s.list...)}
	s.mu.RUnlock()
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	path := s.mgr.KiloVirtualPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return writeFileAtomic(path, b)
}

// List 返回已启用的虚拟模型（顺序稳定：按名称排序）。
func (s *kiloVirtualStore) List() []VirtualModel {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]VirtualModel, 0, len(s.list))
	for _, vm := range s.list {
		out = append(out, VirtualModel{
			Name: vm.Name, Models: append([]string(nil), vm.Models...),
			Strategy: vm.Strategy, Enabled: vm.Enabled,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ListAll 含未启用的（管理端点用）。
func (s *kiloVirtualStore) ListAll() []VirtualModel { return s.List() }

func (s *kiloVirtualStore) Get(name string) (VirtualModel, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, vm := range s.list {
		if vm.Name == name {
			return VirtualModel{Name: vm.Name, Models: append([]string(nil), vm.Models...), Strategy: vm.Strategy, Enabled: vm.Enabled}, true
		}
	}
	return VirtualModel{}, false
}

func validKiloVirtualStrategy(s string) bool {
	switch s {
	case KiloVirtualStrategyFallback, KiloVirtualStrategyRoundRobin, KiloVirtualStrategyRace:
		return true
	}
	return false
}

// validate 校验一个虚拟模型（名称/候选/策略/自引用）。
func (s *kiloVirtualStore) validate(vm VirtualModel) error {
	if !kiloVirtualNameRE.MatchString(vm.Name) {
		return fmt.Errorf("名称不合法（只允许字母数字 - _，1~48 字符）")
	}
	if len(vm.Models) == 0 {
		return fmt.Errorf("候选模型不能为空")
	}
	if len(vm.Models) > 12 {
		return fmt.Errorf("候选模型最多 12 个（race 会放大上游调用）")
	}
	if !validKiloVirtualStrategy(vm.Strategy) {
		return fmt.Errorf("策略必须是 fallback / round_robin / race")
	}
	seen := map[string]bool{}
	for _, m := range vm.Models {
		id := strings.TrimSpace(m)
		if id == "" {
			return fmt.Errorf("候选模型 ID 不能为空")
		}
		if strings.HasPrefix(id, "kilo/") {
			id = strings.TrimPrefix(id, "kilo/")
		}
		if seen[id] {
			return fmt.Errorf("候选模型重复: %s", id)
		}
		seen[id] = true
	}
	return nil
}

// Upsert 新增或按名称更新（整表保存）。
func (s *kiloVirtualStore) Upsert(vm VirtualModel) error {
	vm.Name = strings.TrimSpace(vm.Name)
	vm.Strategy = strings.TrimSpace(vm.Strategy)
	if vm.Strategy == "" {
		vm.Strategy = KiloVirtualStrategyFallback
	}
	for i := range vm.Models {
		vm.Models[i] = strings.TrimSpace(vm.Models[i])
	}
	if err := s.validate(vm); err != nil {
		return err
	}
	s.mu.Lock()
	found := false
	for i := range s.list {
		if s.list[i].Name == vm.Name {
			s.list[i] = vm
			found = true
			break
		}
	}
	if !found {
		s.list = append(s.list, vm)
	}
	s.mu.Unlock()
	// 改名/改候选后重置轮询游标（抄 9router resetComboRotation 思路）
	s.cursors.Delete(vm.Name)
	return s.save()
}

// Delete 按名称删除。
func (s *kiloVirtualStore) Delete(name string) error {
	s.mu.Lock()
	out := make([]VirtualModel, 0, len(s.list))
	for _, vm := range s.list {
		if vm.Name != name {
			out = append(out, vm)
		}
	}
	s.list = out
	s.mu.Unlock()
	s.cursors.Delete(name)
	return s.save()
}

// nextStart 返回 round_robin 的本次起点索引。
func (s *kiloVirtualStore) nextStart(name string, n int) int {
	if n <= 0 {
		return 0
	}
	v, _ := s.cursors.LoadOrStore(name, &atomic.Uint64{})
	c := v.(*atomic.Uint64)
	return int((c.Add(1) - 1) % uint64(n))
}
