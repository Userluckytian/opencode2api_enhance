package manager

// 阶段 M / Task 1：虚拟模型配置层单测。

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func newVirtualStore(t *testing.T) *kiloVirtualStore {
	t.Helper()
	m := New(t.TempDir())
	return newKiloVirtualStore(m)
}

func TestKiloVirtualCRUD(t *testing.T) {
	s := newVirtualStore(t)
	vm := VirtualModel{
		Name: "fast-code", Strategy: KiloVirtualStrategyFallback, Enabled: true,
		Models: []string{"nvidia/a:free", "cohere/b:free"},
	}
	if err := s.Upsert(vm); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, ok := s.Get("fast-code")
	if !ok || got.Strategy != KiloVirtualStrategyFallback || len(got.Models) != 2 {
		t.Fatalf("Get 错误: %+v ok=%v", got, ok)
	}
	// 更新（同名覆盖，不新增）
	vm.Strategy = KiloVirtualStrategyRoundRobin
	if err := s.Upsert(vm); err != nil {
		t.Fatalf("Upsert update: %v", err)
	}
	if all := s.List(); len(all) != 1 || all[0].Strategy != KiloVirtualStrategyRoundRobin {
		t.Fatalf("更新后应只有 1 条且策略已改: %+v", all)
	}
	// 删除
	if err := s.Delete("fast-code"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(s.List()) != 0 {
		t.Fatal("删除后应为空")
	}
}

func TestKiloVirtualValidation(t *testing.T) {
	s := newVirtualStore(t)
	base := VirtualModel{Name: "ok", Strategy: KiloVirtualStrategyFallback, Models: []string{"a"}}
	cases := []struct {
		name string
		vm   VirtualModel
		want string
	}{
		{"bad name", VirtualModel{Name: "bad/name", Strategy: "fallback", Models: []string{"a"}}, "名称不合法"},
		{"empty models", VirtualModel{Name: "ok", Strategy: "fallback"}, "候选模型不能为空"},
		{"too many", VirtualModel{Name: "ok", Strategy: "race", Models: make([]string, 13)}, "最多 12"},
		{"bad strategy", VirtualModel{Name: "ok", Strategy: "magic", Models: []string{"a"}}, "策略必须是"},
		{"duplicate", VirtualModel{Name: "ok", Strategy: "fallback", Models: []string{"a", "kilo/a"}}, "重复"},
		{"empty id", VirtualModel{Name: "ok", Strategy: "fallback", Models: []string{"  "}}, "不能为空"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := s.Upsert(c.vm)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("期望错误含 %q，实际 %v", c.want, err)
			}
		})
	}
	if err := s.Upsert(base); err != nil {
		t.Fatalf("合法配置应通过: %v", err)
	}
	// 默认策略
	def := VirtualModel{Name: "defaulted", Models: []string{"x"}}
	if err := s.Upsert(def); err != nil {
		t.Fatalf("空策略应默认 fallback: %v", err)
	}
	if got, _ := s.Get("defaulted"); got.Strategy != KiloVirtualStrategyFallback {
		t.Fatalf("默认策略 = %q", got.Strategy)
	}
}

func TestKiloVirtualPersistAndRoundRobin(t *testing.T) {
	dir := t.TempDir()
	m := New(dir)
	s := newKiloVirtualStore(m)
	if err := s.Upsert(VirtualModel{Name: "rr", Strategy: KiloVirtualStrategyRoundRobin, Enabled: true, Models: []string{"a", "b", "c"}}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	// 文件存在
	if _, err := os.Stat(filepath.Join(dir, "runtime", "kilo", kiloVirtualFileName)); err != nil {
		t.Fatalf("配置文件未落盘: %v", err)
	}
	// 重启（新建 Manager 指向同目录）仍读到
	m2 := New(dir)
	s2 := newKiloVirtualStore(m2)
	if got, ok := s2.Get("rr"); !ok || len(got.Models) != 3 {
		t.Fatalf("重启后配置丢失: %+v ok=%v", got, ok)
	}
	// 轮询起点 0,1,2,0...
	for i, want := range []int{0, 1, 2, 0, 1} {
		if got := s2.nextStart("rr", 3); got != want {
			t.Fatalf("第 %d 次起点 = %d, want %d", i, got, want)
		}
	}
	// 改候选后游标重置
	if err := s2.Upsert(VirtualModel{Name: "rr", Strategy: KiloVirtualStrategyRoundRobin, Enabled: true, Models: []string{"a", "b"}}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if got := s2.nextStart("rr", 2); got != 0 {
		t.Fatalf("改候选后游标应重置，实际 %d", got)
	}
}

func TestKiloVirtualConcurrent(t *testing.T) {
	s := newVirtualStore(t)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = s.Upsert(VirtualModel{Name: "vm" + string(rune('a'+i)), Strategy: KiloVirtualStrategyFallback, Models: []string{"x"}})
			_ = s.List()
			_ = s.nextStart("vm-a", 1)
		}(i)
	}
	wg.Wait()
	if len(s.List()) != 20 {
		t.Fatalf("并发 Upsert 后应有 20 条，实际 %d", len(s.List()))
	}
}
