package main

import (
	"sync"
	"time"
)

type window struct {
	start int64 // 窗口起始 Unix 秒
	count int
}

type RateLimiter struct {
	mu      sync.Mutex
	perMin  int
	buckets map[string]*window
}

func NewRateLimiter(perMin int) *RateLimiter {
	return &RateLimiter{perMin: perMin, buckets: map[string]*window{}}
}

func (r *RateLimiter) Allow(key string, now time.Time) bool {
	if r.perMin <= 0 {
		return true
	}
	sec := now.Unix()
	win := sec / 60
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.buckets[key]
	if !ok || b.start != win {
		r.buckets[key] = &window{start: win, count: 1}
		return true
	}
	if b.count >= r.perMin {
		return false
	}
	b.count++
	return true
}
