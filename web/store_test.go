package main

import (
	"testing"
	"time"
)

func TestGenerateKeyFormat(t *testing.T) {
	k := GenerateKey()
	if len(k) != 3+32 {
		t.Fatalf("len = %d, want 35", len(k))
	}
	if k[:3] != "sk-" {
		t.Fatalf("prefix = %q, want sk-", k[:3])
	}
}

func TestCreateAndGetByKey(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	k, err := s.CreateKey("glm-4", 7, 1000000, now)
	if err != nil {
		t.Fatal(err)
	}
	if k.Model != "glm-4" {
		t.Fatalf("model = %q", k.Model)
	}
	if !k.ExpiresAt.After(now.Add(6 * 24 * time.Hour)) {
		t.Fatalf("expires too early: %v", k.ExpiresAt)
	}
	got, ok := s.GetByKey(k.Key)
	if !ok || got.ID != k.ID {
		t.Fatalf("GetByKey failed: ok=%v got=%+v", ok, got)
	}
}

func TestAddUsageAndQuota(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	now := time.Now()
	k, _ := s.CreateKey("m", 7, 1000, now)
	s.AddUsage(k.ID, 400)
	if got, _ := s.GetByID(k.ID); got.TokenUsed != 400 {
		t.Fatalf("TokenUsed = %d, want 400", got.TokenUsed)
	}
	s.AddUsage(k.ID, 100)
	if got, _ := s.GetByID(k.ID); got.TokenUsed != 500 {
		t.Fatalf("TokenUsed = %d, want 500", got.TokenUsed)
	}
}

func TestConsumeAtomicCheckAndAct(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	now := time.Now()
	k, _ := s.CreateKey("m", 7, 10, now) // quota=10

	if !s.Consume(k.ID, 6) {
		t.Fatal("first consume (6/10) should succeed")
	}
	if s.Consume(k.ID, 6) {
		t.Fatal("second consume (6/10) should fail: 6+6 > 10")
	}
	if got, _ := s.GetByID(k.ID); got.TokenUsed != 6 {
		t.Fatalf("TokenUsed = %d, want 6 (failed consume must not change)", got.TokenUsed)
	}

	if err := s.SetDisabled(k.ID, true); err != nil {
		t.Fatal(err)
	}
	if s.Consume(k.ID, 1) {
		t.Fatal("consume on disabled key should fail")
	}
	if got, _ := s.GetByID(k.ID); got.TokenUsed != 6 {
		t.Fatalf("TokenUsed after disabled consume = %d, want 6", got.TokenUsed)
	}

	if s.Consume("sk-nosuch", 1) {
		t.Fatal("consume on missing key should fail")
	}
}

func TestSnapshotIsCopy(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	now := time.Now()
	k, _ := s.CreateKey("m", 7, 10, now)
	snap, ok := s.Snapshot(k.ID)
	if !ok {
		t.Fatal("snapshot failed")
	}
	snap.TokenUsed = 999
	if got, _ := s.GetByID(k.ID); got.TokenUsed != 0 {
		t.Fatalf("snapshot must be a copy, internal TokenUsed = %d", got.TokenUsed)
	}
}

func TestSetDisabledAndDelete(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	now := time.Now()
	k, _ := s.CreateKey("m", 7, 1000, now)
	if err := s.SetDisabled(k.ID, true); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetByID(k.ID); !got.Disabled {
		t.Fatal("expected disabled")
	}
	if err := s.SetDisabled(k.ID, false); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetByID(k.ID); got.Disabled {
		t.Fatal("expected enabled")
	}
	if err := s.Delete(k.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.GetByID(k.ID); ok {
		t.Fatal("expected deleted")
	}
}

func TestStorePersists(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewStore(dir)
	now := time.Now()
	k, _ := s.CreateKey("m", 7, 1000, now)
	s.AddUsage(k.ID, 123) // 触发写盘
	s2, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := s2.GetByKey(k.Key)
	if !ok {
		t.Fatal("key not persisted")
	}
	if got.TokenUsed != 123 {
		t.Fatalf("TokenUsed not persisted: %d", got.TokenUsed)
	}
}
