package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// IPDefaults 是全局策略。0 表示不限；WindowMinutes=0 表示窗口不自动重置。
type IPDefaults struct {
	WindowTokens     int `json:"window_tokens"`      // 每窗口 token 上限
	WindowMinutes    int `json:"window_minutes"`     // 授权窗口长度
	MaxActiveMinutes int `json:"max_active_minutes"` // 每窗口累计活跃时长上限
	OutputTPS        int `json:"output_tps"`         // 输出限速 token/秒
}

// DefaultIPPolicy 是 IP 层面的默认防滥用策略。
//
// 注意：WindowTokens 默认为 0（不限）。额度控制由「参数设置」里的默认额度
// （每个 Key 的 token_quota）统一承担，IP 层面不再重复设一道更紧的额度，
// 否则会出现「Key 层面给 1000W、IP 层面只给 100W」的错配，导致额度在
// IP 层被提前拦截。IP 层面只保留与防滥用直接相关的两项：
//   - MaxActiveMinutes：累计请求耗时上限（防慢速占位）
//   - OutputTPS：输出限速（防单人打满上游）
func DefaultIPPolicy() IPDefaults {
	return IPDefaults{
		WindowTokens:     0, // 不限：额度统一由「默认额度（每个 Key）」控制
		WindowMinutes:    1440,
		MaxActiveMinutes: 120,
		OutputTPS:        60,
	}
}

// IPOverride 按 IP 覆盖策略；nil 字段表示继承全局。
type IPOverride struct {
	IP               string    `json:"ip"`
	WindowTokens     *int      `json:"window_tokens"`
	WindowMinutes    *int      `json:"window_minutes"`
	MaxActiveMinutes *int      `json:"max_active_minutes"`
	OutputTPS        *int      `json:"output_tps"`
	Disabled         bool      `json:"disabled"`
	Note             string    `json:"note"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// EffectiveIPPolicy 是合并后的最终策略。
type EffectiveIPPolicy struct {
	WindowTokens     int    `json:"window_tokens"`
	WindowMinutes    int    `json:"window_minutes"`
	MaxActiveMinutes int    `json:"max_active_minutes"`
	OutputTPS        int    `json:"output_tps"`
	Disabled         bool   `json:"disabled"`
	Source           string `json:"source"` // default / override
}

// IP 策略字段上界。
//
// 为什么必须有上界（fact 591 / finding 565）：旧实现只判 <0，管理员（或拿到会话的
// 攻击者）可以把 window_minutes 写成 9007199254740992 这类超大值，它会一路流到
// reset.go alignToGrid 的 `time.Duration(windowMinutes) * time.Minute`，在 int64
// 纳秒上溢出成负的 step / 0，导致除零或索引算出荒谬值——此后每一次 /v1 请求
// （Check）都可能 panic，nginx 侧统一 502，接口被永久打挂。
//
// 取值同时兼顾业务合理性：窗口最长一周（10080 分钟）、额度上限 1e12 token、
// 累计活跃时长同样一周、输出限速 10000 token/s。
const (
	MaxWindowMinutes = 10080
	MaxWindowTokens  = 1000000000000 // 1e12
	MaxActiveMinutes = 10080
	MaxOutputTPS     = 10000
)

// checkPolicyRange 校验单个策略字段：不允许负数，也不允许超过上界。
func checkPolicyRange(name string, v, max int) error {
	if v < 0 {
		return fmt.Errorf("%s 不能为负数", name)
	}
	if v > max {
		return fmt.Errorf("%s 超过上限 %d", name, max)
	}
	return nil
}

func (d IPDefaults) validate() error {
	if err := checkPolicyRange("window_tokens", d.WindowTokens, MaxWindowTokens); err != nil {
		return err
	}
	if err := checkPolicyRange("window_minutes", d.WindowMinutes, MaxWindowMinutes); err != nil {
		return err
	}
	if err := checkPolicyRange("max_active_minutes", d.MaxActiveMinutes, MaxActiveMinutes); err != nil {
		return err
	}
	return checkPolicyRange("output_tps", d.OutputTPS, MaxOutputTPS)
}

// clampDefaults 把从磁盘读到的默认策略钳制回合法区间。
// 旧版本写进 ip_policies.json 的越界值不会被当时的 SetDefaults 拦下（那时还没有上界），
// 载入时必须兜住，否则脏数据会让服务持续 panic / 502。
// 返回值第二项表示是否发生了修改（用于决定是否回写文件）。
func clampDefaults(d IPDefaults) (IPDefaults, bool) {
	def := DefaultIPPolicy()
	changed := false
	clamp := func(v, upper, fallback int) int {
		if v < 0 || v > upper {
			changed = true
			return fallback
		}
		return v
	}
	d.WindowTokens = clamp(d.WindowTokens, MaxWindowTokens, def.WindowTokens)
	d.WindowMinutes = clamp(d.WindowMinutes, MaxWindowMinutes, def.WindowMinutes)
	d.MaxActiveMinutes = clamp(d.MaxActiveMinutes, MaxActiveMinutes, def.MaxActiveMinutes)
	d.OutputTPS = clamp(d.OutputTPS, MaxOutputTPS, def.OutputTPS)
	return d, changed
}

// clampOverride 把磁盘上的单 IP 覆盖钳制回合法区间；越界字段置 nil（继承全局）。
// 非法 IP 由调用方先剔除。
func clampOverride(o IPOverride) (IPOverride, bool) {
	changed := false
	fix := func(p **int, upper int) {
		if *p == nil {
			return
		}
		if **p < 0 || **p > upper {
			*p = nil
			changed = true
		}
	}
	fix(&o.WindowTokens, MaxWindowTokens)
	fix(&o.WindowMinutes, MaxWindowMinutes)
	fix(&o.MaxActiveMinutes, MaxActiveMinutes)
	fix(&o.OutputTPS, MaxOutputTPS)
	return o, changed
}

type ipPolicyFile struct {
	Defaults  IPDefaults   `json:"defaults"`
	Overrides []IPOverride `json:"overrides"`
}

type IPPolicyStore struct {
	mu        sync.RWMutex
	dir       string
	defaults  IPDefaults
	overrides map[string]IPOverride
}

func NewIPPolicyStore(dir string) (*IPPolicyStore, error) {
	s := &IPPolicyStore{dir: dir, defaults: DefaultIPPolicy(), overrides: map[string]IPOverride{}}
	data, err := os.ReadFile(filepath.Join(dir, "ip_policies.json"))
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var f ipPolicyFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, err
	}
	if f.Defaults.WindowMinutes == 0 && f.Defaults.WindowTokens == 0 && f.Defaults.OutputTPS == 0 && f.Defaults.MaxActiveMinutes == 0 {
		// 旧文件/空文件：保留默认值，避免把「未配置」误读为「全部不限」。
		f.Defaults = DefaultIPPolicy()
	}
	// 旧版本没有上界校验，磁盘里可能躺着 window_minutes=9007199254740992 这类
	// 会把 alignToGrid 打爆的值；载入时统一钳制，避免脏数据继续造成 502。
	dirty := false
	if clamped, changed := clampDefaults(f.Defaults); changed {
		f.Defaults = clamped
		dirty = true
	}
	s.defaults = f.Defaults
	for _, o := range f.Overrides {
		ip := normalizeIP(o.IP)
		if _, err := netip.ParseAddr(ip); err != nil {
			// 非法 IP 的覆盖项直接丢弃（旧版本会把 "not-an-ip" 之类原样落盘）。
			dirty = true
			continue
		}
		o.IP = ip
		if clamped, changed := clampOverride(o); changed {
			o = clamped
			dirty = true
		}
		s.overrides[o.IP] = o
	}
	if dirty {
		// 回写迁移结果，让磁盘上的脏数据一次性被清理干净。
		_ = s.saveLocked()
	}
	return s, nil
}

func (s *IPPolicyStore) filePath() string { return filepath.Join(s.dir, "ip_policies.json") }

func (s *IPPolicyStore) saveLocked() error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	list := make([]IPOverride, 0, len(s.overrides))
	for _, o := range s.overrides {
		list = append(list, o)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].IP < list[j].IP })
	data, err := json.MarshalIndent(ipPolicyFile{Defaults: s.defaults, Overrides: list}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.filePath(), data, 0o644)
}

func (s *IPPolicyStore) Defaults() IPDefaults {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.defaults
}

func (s *IPPolicyStore) SetDefaults(d IPDefaults) error {
	if err := d.validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.defaults = d
	return s.saveLocked()
}

// Effective 合并全局与单 IP 覆盖。
func (s *IPPolicyStore) Effective(ip string) EffectiveIPPolicy {
	ip = normalizeIP(ip)
	s.mu.RLock()
	defer s.mu.RUnlock()
	e := EffectiveIPPolicy{
		WindowTokens:     s.defaults.WindowTokens,
		WindowMinutes:    s.defaults.WindowMinutes,
		MaxActiveMinutes: s.defaults.MaxActiveMinutes,
		OutputTPS:        s.defaults.OutputTPS,
		Source:           "default",
	}
	if o, ok := s.overrides[ip]; ok {
		e.Source = "override"
		e.Disabled = o.Disabled
		if o.WindowTokens != nil {
			e.WindowTokens = *o.WindowTokens
		}
		if o.WindowMinutes != nil {
			e.WindowMinutes = *o.WindowMinutes
		}
		if o.MaxActiveMinutes != nil {
			e.MaxActiveMinutes = *o.MaxActiveMinutes
		}
		if o.OutputTPS != nil {
			e.OutputTPS = *o.OutputTPS
		}
	}
	return e
}

// Override 返回单 IP 覆盖的副本。
func (s *IPPolicyStore) Override(ip string) (IPOverride, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	o, ok := s.overrides[normalizeIP(ip)]
	return o, ok
}

func (s *IPPolicyStore) ListOverrides() []IPOverride {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]IPOverride, 0, len(s.overrides))
	for _, o := range s.overrides {
		out = append(out, o)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].IP < out[j].IP })
	return out
}

func (s *IPPolicyStore) SetOverride(ip string, o IPOverride) error {
	ip = normalizeIP(ip)
	if ip == "" {
		return errors.New("缺少 IP")
	}
	// normalizeIP 解析失败时会原样返回，因此这里必须再用 netip 做一次真正的校验，
	// 否则 PUT /api/admin/ip-policies/not-an-ip 会把垃圾 key 写进 overrides。
	if _, err := netip.ParseAddr(ip); err != nil {
		return fmt.Errorf("非法 IP：%q", ip)
	}
	for _, f := range []struct {
		name  string
		p     *int
		upper int
	}{
		{"window_tokens", o.WindowTokens, MaxWindowTokens},
		{"window_minutes", o.WindowMinutes, MaxWindowMinutes},
		{"max_active_minutes", o.MaxActiveMinutes, MaxActiveMinutes},
		{"output_tps", o.OutputTPS, MaxOutputTPS},
	} {
		if f.p == nil {
			continue
		}
		if err := checkPolicyRange(f.name, *f.p, f.upper); err != nil {
			return err
		}
	}
	o.IP = ip
	o.UpdatedAt = time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.overrides[ip] = o
	return s.saveLocked()
}

func (s *IPPolicyStore) DeleteOverride(ip string) error {
	ip = normalizeIP(ip)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.overrides[ip]; !ok {
		return os.ErrNotExist
	}
	delete(s.overrides, ip)
	return s.saveLocked()
}
