package main

import (
	"testing"
	"time"
)

// 额度每日 08:00 自动重置：跨边界后 TokenUsed 归零、Quota 恢复默认。
func TestKeyQuotaResetsAt8AM(t *testing.T) {
	loc := time.Local
	s := &Store{keys: map[string]*Key{}, defaultQuota: 1000, dir: t.TempDir()}
	now := time.Date(2026, 10, 21, 10, 0, 0, 0, loc) // 上午 10 点领
	k, _, err := s.CreateOrGetForIP("192.168.12.17", "glm-4", 1, 1000, now)
	if err != nil {
		t.Fatal(err)
	}
	s.Consume(k.ID, 600)
	snap, _ := s.Snapshot(k.ID)
	if snap.TokenUsed != 600 {
		t.Fatalf("用量应累计: %+v", snap)
	}
	if snap.Quota != 1000 {
		t.Fatalf("额度不对: %+v", snap)
	}

	// 当天 23:00 仍未跨额度日
	s.RollQuota(1000, time.Date(2026, 10, 21, 23, 0, 0, 0, loc))
	snap2, _ := s.Snapshot(k.ID)
	if snap2.TokenUsed != 600 {
		t.Fatalf("同额度日内不应重置: %+v", snap2)
	}

	// 次日 08:00 之后 → 用量归零、额度恢复默认
	s.RollQuota(1000, time.Date(2026, 10, 22, 8, 0, 0, 0, loc))
	snap3, _ := s.Snapshot(k.ID)
	if snap3.TokenUsed != 0 {
		t.Fatalf("跨 08:00 应重置用量: %+v", snap3)
	}
	if snap3.Quota != 1000 {
		t.Fatalf("重置后额度应恢复默认: %+v", snap3)
	}
}

// 08:00 之前属于前一个额度日，不应重置。
func TestKeyQuotaNotResetBefore8AM(t *testing.T) {
	loc := time.Local
	s := &Store{keys: map[string]*Key{}, defaultQuota: 1000, dir: t.TempDir()}
	now := time.Date(2026, 10, 21, 10, 0, 0, 0, loc)
	k, _, _ := s.CreateOrGetForIP("192.168.12.17", "glm-4", 1, 1000, now)
	s.Consume(k.ID, 600)
	// 次日 07:59 —— 仍在同一额度日内
	s.RollQuota(1000, time.Date(2026, 10, 22, 7, 59, 0, 0, loc))
	snap, _ := s.Snapshot(k.ID)
	if snap.TokenUsed != 600 {
		t.Fatalf("08:00 前不应重置: %+v", snap)
	}
}

// 扣减前应自动重置：用户不重新领取也能继续用。
func TestConsumeTriggersDailyReset(t *testing.T) {
	loc := time.Local
	s := &Store{keys: map[string]*Key{}, defaultQuota: 1000, dir: t.TempDir()}
	now := time.Date(2026, 10, 21, 10, 0, 0, 0, loc)
	k, _, _ := s.CreateOrGetForIP("192.168.12.17", "glm-4", 1, 1000, now)
	s.Consume(k.ID, 600)

	// 把窗口日期改到前一天，模拟跨过 08:00
	s.mu.Lock()
	s.keys[k.ID].QuotaDate = dailyResetDate(time.Date(2026, 10, 21, 10, 0, 0, 0, loc))
	s.mu.Unlock()

	ok := s.ConsumeAt(k.ID, 10, time.Date(2026, 10, 22, 8, 0, 0, 0, loc))
	if !ok {
		t.Fatal("跨日后应可继续使用")
	}
	snap, _ := s.Snapshot(k.ID)
	if snap.TokenUsed != 10 {
		t.Fatalf("跨日后用量应从 0 重新累计: %+v", snap)
	}
}

// 超额后扣减失败；跨日后又能用。
func TestKeyQuotaExhaustedThenRecoversNextDay(t *testing.T) {
	loc := time.Local
	s := &Store{keys: map[string]*Key{}, defaultQuota: 100, dir: t.TempDir()}
	now := time.Date(2026, 10, 21, 10, 0, 0, 0, loc)
	k, _, _ := s.CreateOrGetForIP("192.168.12.17", "glm-4", 1, 100, now)
	if !s.Consume(k.ID, 100) {
		t.Fatal("首次扣减应成功")
	}
	if s.Consume(k.ID, 1) {
		t.Fatal("超额后扣减应失败")
	}
	// 跨额度日
	s.RollQuota(100, time.Date(2026, 10, 22, 8, 0, 0, 0, loc))
	if !s.Consume(k.ID, 50) {
		t.Fatal("跨日后应恢复可用")
	}
}

// 同一 IP 始终是同一个 Key（幂等），不会因为重复领取而多出 Key。
func TestOneKeyPerIPPersists(t *testing.T) {
	s := &Store{keys: map[string]*Key{}, defaultQuota: 100, dir: t.TempDir()}
	now := time.Now()
	k1, isNew1, _ := s.CreateOrGetForIP("192.168.12.17", "glm-4", 1, 100, now)
	k2, isNew2, _ := s.CreateOrGetForIP("192.168.12.17", "glm-4", 1, 100, now)
	if !isNew1 || isNew2 {
		t.Fatalf("首次新建、再次应复用: %v %v", isNew1, isNew2)
	}
	if k1.Key != k2.Key || k1.ID != k2.ID {
		t.Fatal("同一 IP 应复用同一个 Key")
	}
	if len(s.List()) != 1 {
		t.Fatalf("不应产生多个 Key: %d", len(s.List()))
	}
}

/* ---------- 有效期 0 = 永不过期 ---------- */

func TestKeyNeverExpiresWhenTTLZero(t *testing.T) {
	s := &Store{keys: map[string]*Key{}, defaultQuota: 100, dir: t.TempDir()}
	now := time.Now()
	k, _, err := s.CreateOrGetForIP("192.168.12.17", "glm-4", 0, 100, now)
	if err != nil {
		t.Fatal(err)
	}
	if !k.ExpiresAt.IsZero() {
		t.Fatalf("ttl=0 时期到期时间应为零值（永不过期），got %v", k.ExpiresAt)
	}
	if isExpired(*k, now.AddDate(5, 0, 0)) {
		t.Fatal("ttl=0 的 Key 不应过期")
	}
}

func TestKeyExpiresAfterTTLWhenPositive(t *testing.T) {
	s := &Store{keys: map[string]*Key{}, defaultQuota: 100, dir: t.TempDir()}
	now := time.Now()
	k, _, _ := s.CreateOrGetForIP("192.168.12.17", "glm-4", 1, 100, now)
	if k.ExpiresAt.IsZero() {
		t.Fatal("ttl=1 时应有过期时间")
	}
	if !isExpired(*k, k.ExpiresAt.Add(time.Second)) {
		t.Fatal("超过到期时间应判定为过期")
	}
}

// 全局策略切换到 0 时，已有 Key 也应变为永不过期。
func TestApplyTTLZeroMakesExistingKeysNeverExpire(t *testing.T) {
	s := &Store{keys: map[string]*Key{}, defaultQuota: 100, dir: t.TempDir()}
	now := time.Now()
	k, _, _ := s.CreateOrGetForIP("192.168.12.17", "glm-4", 1, 100, now)
	if k.ExpiresAt.IsZero() {
		t.Fatal("初始应是有限期")
	}
	if !s.ApplyTTL(0, now) {
		t.Fatal("应发生变更")
	}
	snap, _ := s.Snapshot(k.ID)
	if !snap.ExpiresAt.IsZero() {
		t.Fatalf("ApplyTTL(0) 后应永不过期，got %v", snap.ExpiresAt)
	}
	if isExpired(snap, now.AddDate(30, 0, 0)) {
		t.Fatal("ApplyTTL(0) 后不应过期")
	}
}
