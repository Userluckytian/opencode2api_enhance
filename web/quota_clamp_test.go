package main

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 额度越界（MaxInt64 脏值）修复的回归用例集合。
//
// 背景（finding 823 / fact 822）：
//   - 旧实现 token_quota 无上界，9223372036854775807(MaxInt64) 能被写进 keys.json
//     并下发到每个 Key；gateway.go「TokenUsed >= Quota」与 store.go「TokenUsed+n > Quota」
//     对 MaxInt64 恒不成立，该 Key 当天可无限调用上游。
//   - MaxIssuedQuota 若把该脏值当成「已发放的最大额度」，管理员任何下调
//     token_quota 的操作都会被 400 拒绝（活锁）。
// 修复：启动期 ClampOutOfRangeQuota 把越界额度钳制为 min(默认额度, MaxTokenQuota) 并落盘；
// MaxIssuedQuota 只统计 1..MaxTokenQuota 内的合法额度，管理员因此不会被脏值活锁住额度调整。
//
// 语义（方案 A）：额度变更对存量 Key 立即生效 —— 管理员下调 token_quota 时由
// handlePutSettings 调用 ApplyQuotaDown 当场收紧所有在用 Key 的额度（只降不升）。

// ① 存量 MaxInt64 脏 Key 经启动期钳制后不得再阻止管理员降低 token_quota：
//
//	钳制把脏值压回默认额度，MaxIssuedQuota 随之回落，PUT /api/admin/settings 应 200。
//	注意：本用例里脏值已先行钳制（模拟 main.go 启动序列 ClampOutOfRangeQuota 已跑过）。
func TestQuotaClampDirtyKeyDoesNotBlockAdminDowngrade(t *testing.T) {
	app, srv := newAdminApp(t)
	defer srv.Close()
	tok := adminToken(t, srv)

	// 直接注入一条 quota=MaxInt64 的越界存量 Key（模拟线上脏数据），再跑一次钳制。
	app.Store.mu.Lock()
	app.Store.keys["dirty"] = &Key{
		ID: "dirty", Key: "sk-dirty", Model: "glm-4",
		TokenUsed: 0, Quota: 9223372036854775807,
	}
	_ = app.Store.writeLocked()
	app.Store.mu.Unlock()
	if _, err := app.Store.ClampOutOfRangeQuota(1000000); err != nil {
		t.Fatal(err)
	}

	// 钳制后脏值回落到 1000000，MaxIssuedQuota 随之回落到 1000000；
	// 管理员把 token_quota 下调到 1000000（等于当前已发放最大值，不触发闸门）—— 修复前会被 400 活锁。
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/admin/settings",
		bytes.NewBufferString(`{"token_quota":1000000}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("钳制后越界脏 Key 不应阻止下调 token_quota，期望 200 实际 %d", resp.StatusCode)
	}
	if app.Cfg.TokenQuota != 1000000 {
		t.Fatalf("token_quota 未生效: %d", app.Cfg.TokenQuota)
	}
}

// ② ClampOutOfRangeQuota 把越界额度钳到 min(默认额度, MaxTokenQuota) 并落盘 keys.json：
//
//	重新用 NewStore 读回文件，quota 不得再是 MaxInt64。
func TestQuotaClampPersistsToDisk(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.keys["dirty"] = &Key{
		ID: "dirty", Key: "sk-dirty", Model: "glm-4",
		TokenUsed: 0, Quota: 9223372036854775807,
	}
	_ = s.writeLocked()
	s.mu.Unlock()

	res, err := s.ClampOutOfRangeQuota(10000000)
	if err != nil {
		t.Fatal(err)
	}
	if res.Clamped != 1 {
		t.Fatalf("应钳制 1 个 Key，实际 %d: %+v", res.Clamped, res.Details)
	}
	// 直接读 keys.json 原文确认越界值已被覆盖。
	raw, err := os.ReadFile(filepath.Join(dir, "keys.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("9223372036854775807")) {
		t.Fatalf("keys.json 仍含越界额度: %s", string(raw))
	}
	// 重新加载 Store，确认钳制值持久化。
	s2, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	snap, ok := s2.Snapshot("dirty")
	if !ok {
		t.Fatal("钳制后 Key 丢失")
	}
	// 方案 A：钳制上界是 min(defaultQuota, MaxTokenQuota)。defaultQuota=1000W 小于
	// MaxTokenQuota=1e12，故钳制到 defaultQuota=10000000。
	// 上界是 MaxTokenQuota（1e12）而非 defaultQuota —— 钳制只针对真正越界的值，
	// 正常存量额度（哪怕高于当前默认额度）不受影响（额度变更次日 08:00 才生效）。
	if snap.Quota != MaxTokenQuota {
		t.Fatalf("钳制后应持久化为 MaxTokenQuota=%d，实际 %d", MaxTokenQuota, snap.Quota)
	}
}

// ③ 方案 A：token_quota 不得低于「已发放 Key 的最大额度」，否则 400。
//
//	这是「一次误操作砍掉所有在用 Key 额度」的保护闸门 —— 与 ApplyQuotaDown
//	立即收紧配套：先拦住明显误配，合法下调才走收紧路径。
func TestQuotaDowngradeBelowIssuedMaxIsAllowed(t *testing.T) {
	app, srv := newAdminApp(t)
	defer srv.Close()
	tok := adminToken(t, srv)

	if _, err := app.Store.CreateKey("glm-4", 7, 900000, time.Now()); err != nil {
		t.Fatal(err)
	}
	// 900000 > 1000：允许下调 —— 额度变更对存量 Key 是「次日 08:00 生效」，
	// 当天不砍在用 Key 的额度，故不再拦（否则管理员永远调不低额度）。
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/admin/settings",
		bytes.NewBufferString(`{"token_quota":1000}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("下调额度应放行（次日生效），实际 %d", resp.StatusCode)
	}
	if app.Cfg.TokenQuota != 1000 {
		t.Fatalf("token_quota 未生效: %d", app.Cfg.TokenQuota)
	}
}

// ④ 方案 A：下调额度立即收紧所有在用 Key 的额度（ApplyQuotaDown 只降不升）。
//
//	handlePutSettings 在合法下调时调用 ApplyQuotaDown，存量 Key 额度当场降到新值，
//	不存在「次日才生效」的越界窗口。
func TestAdminDowngradeAppliesImmediately(t *testing.T) {
	app, srv := newAdminApp(t)
	defer srv.Close()
	tok := adminToken(t, srv)

	app.Store.SetDefaultQuota(1000000)
	k, _, err := app.Store.CreateOrGetForIP("192.168.12.50", "glm-4", 0, 1000000, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// 先把存量 Key 额度调低到 500000，再下调全局额度到 500000 → 合法（不被闸门拦）。
	if err := app.Store.SetQuota(k.ID, 500000); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/admin/settings",
		bytes.NewBufferString(`{"token_quota":500000}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("合法下调应 200，实际 %d", resp.StatusCode)
	}
	if app.Cfg.TokenQuota != 500000 {
		t.Fatalf("token_quota 未生效: %d", app.Cfg.TokenQuota)
	}
	// ApplyQuotaDown 立即生效：存量 Key 额度不得高于新默认额度。
	snap, _ := app.Store.Snapshot(k.ID)
	if snap.Quota > 500000 {
		t.Fatalf("下调后存量 Key 额度应被立即收紧到 <=500000，实际 %d", snap.Quota)
	}
}

// ⑤ MaxIssuedQuota 忽略越界额度：只统计 1..MaxTokenQuota 内的合法值。
func TestMaxIssuedQuotaIgnoresOutOfRange(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.keys["dirty"] = &Key{ID: "dirty", Key: "sk-dirty", Model: "glm-4", Quota: 9223372036854775807}
	s.keys["legal"] = &Key{ID: "legal", Key: "sk-legal", Model: "glm-4", Quota: 500000}
	s.keys["toobig"] = &Key{ID: "toobig", Key: "sk-toobig", Model: "glm-4", Quota: MaxTokenQuota + 1}
	s.mu.Unlock()

	if got := s.MaxIssuedQuota(); got != 500000 {
		t.Fatalf("MaxIssuedQuota 应只统计合法额度 500000，实际 %d", got)
	}
}

// ⑥ ApplyQuotaDown 只降不升：不高于 newQuota 的 Key 不被改动，避免中途凭空抬高额度。
func TestApplyQuotaDownOnlyLowers(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.keys["high"] = &Key{ID: "high", Key: "sk-high", Model: "glm-4", Quota: 1000000}
	s.keys["low"] = &Key{ID: "low", Key: "sk-low", Model: "glm-4", Quota: 100000}
	s.mu.Unlock()

	n := s.ApplyQuotaDown(500000)
	if n != 1 {
		t.Fatalf("应只收紧 1 个（high），实际 %d", n)
	}
	hi, _ := s.Snapshot("high")
	if hi.Quota != 500000 {
		t.Fatalf("high 应被收紧到 500000，实际 %d", hi.Quota)
	}
	lo, _ := s.Snapshot("low")
	if lo.Quota != 100000 {
		t.Fatalf("low 额度更低，不应被抬高，实际 %d", lo.Quota)
	}
}
