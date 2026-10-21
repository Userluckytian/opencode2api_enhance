package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// IPWindow 是单 IP 在一个授权窗口内的用量。
type IPWindow struct {
	Start    time.Time `json:"start"`
	End      time.Time `json:"end"` // 窗口结束时间（对齐 8:00 网格）
	Tokens   int       `json:"tokens"`
	ActiveMs int64     `json:"active_ms"`
	Requests int       `json:"requests"`
	Blocked  int       `json:"blocked"`
	LastSeen time.Time `json:"last_seen"`
}

type IPUsage struct {
	IP               string               `json:"ip"`
	WindowStart      string               `json:"window_start"`
	ResetAt          string               `json:"reset_at"`
	Tokens           int                  `json:"tokens"`
	WindowTokens     int                  `json:"window_tokens"`
	ActiveMs         int64                `json:"active_ms"`
	MaxActiveMinutes int                  `json:"max_active_minutes"`
	Requests         int                  `json:"requests"`
	Blocked          int                  `json:"blocked"`
	LastSeen         string               `json:"last_seen"`
	Policy           EffectiveIPPolicy    `json:"policy"`
	Overridden       bool                 `json:"overridden"`
	Note             string               `json:"note,omitempty"`
	RemainingTokens  int                  `json:"remaining_tokens"`
	RemainingSeconds int64                `json:"remaining_seconds"`
}

type ipUsageFile struct {
	Windows map[string]*IPWindow `json:"windows"`
}

// IPTracker 维护窗口用量并执行「token 或活跃时长先到先拦」。
// 策略读取 IPPolicyStore；用量持久化到 ip_usage.json，进程重启不重置窗口。
type IPTracker struct {
	mu       sync.Mutex
	dir      string
	policies *IPPolicyStore
	windows  map[string]*IPWindow
}

func NewIPTracker(dir string, policies *IPPolicyStore) (*IPTracker, error) {
	t := &IPTracker{dir: dir, policies: policies, windows: map[string]*IPWindow{}}
	data, err := os.ReadFile(filepath.Join(dir, "ip_usage.json"))
	if os.IsNotExist(err) {
		return t, nil
	}
	if err != nil {
		return nil, err
	}
	var f ipUsageFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, err
	}
	for ip, w := range f.Windows {
		if w == nil {
			continue
		}
		if ip = normalizeIP(ip); ip != "" {
			t.windows[ip] = w
		}
	}
	return t, nil
}

// Policies 返回策略存储，供网关读取输出限速等配置。
func (t *IPTracker) Policies() *IPPolicyStore { return t.policies }

func (t *IPTracker) saveLocked() error {	if err := os.MkdirAll(t.dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(ipUsageFile{Windows: t.windows}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(t.dir, "ip_usage.json"), data, 0o644)
}

// resetAtFor 返回当前窗口的结束时间。
// 窗口按「以每日 8:00 为原点的固定网格」对齐，使用者看到的重置时间是可预期的
// （例如 24 小时窗口即 08:00 → 次日 08:00），而不是从首次使用起算的滚动时间。
//
// 必须用窗口自身的 End：早期版本用 Start+窗口长度，算出来的是「首次使用时刻
// +24h」（如 11:17 → 次日 11:17），与网格对齐的 08:00 不符。
func resetAtFor(w *IPWindow, eff EffectiveIPPolicy) time.Time {
	if w == nil || w.Start.IsZero() || eff.WindowMinutes <= 0 {
		return time.Time{}
	}
	if !w.End.IsZero() {
		return w.End
	}
	return w.Start.Add(time.Duration(eff.WindowMinutes) * time.Minute)
}

// windowLocked 返回当前窗口；窗口不存在或已到期时自动开新窗口。
func (t *IPTracker) windowLocked(ip string, now time.Time, eff EffectiveIPPolicy) (*IPWindow, bool) {
	w := t.windows[ip]
	if w == nil {
		start, end := alignToGrid(now, eff.WindowMinutes)
		w = &IPWindow{Start: start, End: end}
		t.windows[ip] = w
		return w, true
	}
	if !w.Start.IsZero() && eff.WindowMinutes > 0 {
		if !now.Before(w.Start.Add(time.Duration(eff.WindowMinutes) * time.Minute)) {
			start, end := alignToGrid(now, eff.WindowMinutes)
			*w = IPWindow{Start: start, End: end}
			return w, true
		}
		// 存量窗口（早期版本创建，没有 End 字段）按网格补齐起点与终点，
		// 否则重置时间会显示成「首次使用时刻 + 窗口长度」而非网格上的 08:00。
		if w.End.IsZero() {
			start, end := alignToGrid(w.Start, eff.WindowMinutes)
			w.Start, w.End = start, end
			return w, true
		}
	}
	return w, false
}

// Check 判断当前请求是否放行；不放行时返回原因与窗口重置时间。
func (t *IPTracker) Check(ip string, now time.Time) (bool, string, time.Time) {
	ip = normalizeIP(ip)
	eff := t.policies.Effective(ip)
	if eff.Disabled {
		t.mu.Lock()
		w, _ := t.windowLocked(ip, now, eff)
		w.Blocked++
		w.LastSeen = now
		_ = t.saveLocked()
		t.mu.Unlock()
		return false, "该 IP 已被管理员封禁，请联系管理员", time.Time{}
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	w, changed := t.windowLocked(ip, now, eff)
	resetAt := resetAtFor(w, eff)
	resetText := "管理员重置后"
	if !resetAt.IsZero() {
		resetText = resetAt.Local().Format("2006-01-02 15:04") + " 重置"
	}
	if eff.WindowTokens > 0 && w.Tokens >= eff.WindowTokens {
		w.Blocked++
		w.LastSeen = now
		_ = t.saveLocked()
		return false, fmt.Sprintf("IP 窗口额度已用尽：已使用 %d token（上限 %d），%s", w.Tokens, eff.WindowTokens, resetText), resetAt
	}
	if eff.MaxActiveMinutes > 0 && w.ActiveMs >= int64(eff.MaxActiveMinutes)*60_000 {
		w.Blocked++
		w.LastSeen = now
		_ = t.saveLocked()
		return false, fmt.Sprintf("IP 窗口时长已用尽：累计活跃 %d 分钟（上限 %d 分钟），%s", w.ActiveMs/60_000, eff.MaxActiveMinutes, resetText), resetAt
	}
	w.LastSeen = now
	if changed {
		_ = t.saveLocked()
	}
	return true, "", resetAt
}

// Record 记录一次成功转发后的实际用量。
func (t *IPTracker) Record(ip string, tokens int, active time.Duration, now time.Time) {
	ip = normalizeIP(ip)
	if ip == "" {
		return
	}
	eff := t.policies.Effective(ip)
	t.mu.Lock()
	defer t.mu.Unlock()
	w, _ := t.windowLocked(ip, now, eff)
	if tokens < 0 {
		tokens = 0
	}
	w.Tokens += tokens
	if active > 0 {
		w.ActiveMs += active.Milliseconds()
	}
	w.Requests++
	w.LastSeen = now
	_ = t.saveLocked()
}

// Reset 立即清零单个 IP 的窗口用量（从当前时刻重新计时）。
func (t *IPTracker) Reset(ip string, now time.Time) error {
	ip = normalizeIP(ip)
	if ip == "" {
		return os.ErrNotExist
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.windows[ip] = &IPWindow{Start: now, End: nextDailyReset(now)}
	return t.saveLocked()
}

// Snapshot 返回所有出现过的 IP 的窗口用量（到期窗口按空窗口展示，不改写磁盘）。
func (t *IPTracker) Snapshot(now time.Time) []IPUsage {
	t.mu.Lock()
	defer t.mu.Unlock()
	ips := make([]string, 0, len(t.windows))
	for ip := range t.windows {
		ips = append(ips, ip)
	}
	sort.Strings(ips)
	out := make([]IPUsage, 0, len(ips))
	for _, ip := range ips {
		w := *t.windows[ip]
		eff := t.policies.Effective(ip)
		if !w.Start.IsZero() && eff.WindowMinutes > 0 {
			if !now.Before(w.Start.Add(time.Duration(eff.WindowMinutes)*time.Minute)) {
				start, end := alignToGrid(now, eff.WindowMinutes)
				w = IPWindow{Start: start, End: end}
			} else if w.End.IsZero() {
				// 存量窗口没有 End 字段，按网格补齐，保证展示的重置时间是 08:00 对齐的。
				start, end := alignToGrid(w.Start, eff.WindowMinutes)
				w.Start, w.End = start, end
			}
		}
		resetAt := resetAtFor(&w, eff)
		u := IPUsage{
			IP:               ip,
			WindowStart:      w.Start.Format(time.RFC3339),
			Tokens:           w.Tokens,
			WindowTokens:     eff.WindowTokens,
			ActiveMs:         w.ActiveMs,
			MaxActiveMinutes: eff.MaxActiveMinutes,
			Requests:         w.Requests,
			Blocked:          w.Blocked,
			Policy:           eff,
			RemainingTokens:  remainingTokens(w.Tokens, eff.WindowTokens),
			RemainingSeconds: remainingSeconds(w.ActiveMs, eff.MaxActiveMinutes),
			LastSeen:         w.LastSeen.Format(time.RFC3339),
		}
		if !resetAt.IsZero() {
			u.ResetAt = resetAt.Format(time.RFC3339)
		}
		if o, ok := t.policies.Override(ip); ok {
			u.Overridden = true
			u.Note = o.Note
		}
		out = append(out, u)
	}
	return out
}

func remainingTokens(used, limit int) int {
	if limit <= 0 {
		return 0
	}
	if r := limit - used; r > 0 {
		return r
	}
	return 0
}

func remainingSeconds(activeMs int64, maxMinutes int) int64 {
	if maxMinutes <= 0 {
		return 0
	}
	r := int64(maxMinutes)*60 - activeMs/1000
	if r > 0 {
		return r
	}
	return 0
}
