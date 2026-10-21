package main

import (
	"testing"
	"time"
)

func TestRateLimitBlocksAfterLimit(t *testing.T) {
	rl := NewRateLimiter(3)
	now := time.Now()
	for i := 0; i < 3; i++ {
		if !rl.Allow("key1", now) {
			t.Fatalf("allow %d should pass", i)
		}
	}
	if rl.Allow("key1", now) {
		t.Fatal("4th request should be blocked")
	}
}

func TestRateLimitIsolationPerKey(t *testing.T) {
	rl := NewRateLimiter(2)
	now := time.Now()
	rl.Allow("a", now)
	rl.Allow("a", now)
	if rl.Allow("a", now) {
		t.Fatal("a should be blocked")
	}
	if !rl.Allow("b", now) {
		t.Fatal("b should be allowed independently")
	}
}

func TestRateLimitWindowReset(t *testing.T) {
	rl := NewRateLimiter(2)
	now := time.Now()
	rl.Allow("k", now)
	rl.Allow("k", now)
	if rl.Allow("k", now) {
		t.Fatal("blocked within window")
	}
	// 下一窗口
	if !rl.Allow("k", now.Add(61*time.Second)) {
		t.Fatal("should reset in next window")
	}
}

func TestRateLimitZeroMeansUnlimited(t *testing.T) {
	rl := NewRateLimiter(0)
	now := time.Now()
	for i := 0; i < 1000; i++ {
		if !rl.Allow("k", now) {
			t.Fatal("0 means unlimited")
		}
	}
}
