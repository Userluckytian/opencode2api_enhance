package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

type Key struct {
	ID        string    `json:"id"`
	Key       string    `json:"key"`
	Model     string    `json:"model"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	TokenUsed int       `json:"token_used"`
	Quota     int       `json:"quota"`
	Disabled  bool      `json:"disabled"`
	// IP 是领取该 Key 的来源 IP（每个 IP 固定一个 Key）。
	IP string `json:"ip,omitempty"`
	// QuotaDate 是本次额度的所属额度日（08:00 边界）。跨到新的额度日时，
	// TokenUsed 归零、Quota 恢复为默认额度 —— 用户无需每天重新领取。
	QuotaDate string `json:"quota_date,omitempty"`
}

// isExpired 统一过期语义：now >= ExpiresAt 即视为过期。
// ExpiresAt 为零值表示**永不过期**（由每日额度重置来控制用量，而不是失效）。
func isExpired(k Key, now time.Time) bool {
	if k.ExpiresAt.IsZero() {
		return false
	}
	return !now.Before(k.ExpiresAt)
}

func GenerateKey() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return "sk-" + hex.EncodeToString(buf)
}

type Store struct {
	mu   sync.Mutex
	dir  string
	keys map[string]*Key // by ID
	// defaultQuota 是「每日重置后」恢复到的额度（取自配置 token_quota）。
	defaultQuota int
}

func NewStore(dir string) (*Store, error) {
	s := &Store{dir: dir, keys: map[string]*Key{}}
	data, err := os.ReadFile(filepath.Join(dir, "keys.json"))
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var list []*Key
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	for _, k := range list {
		s.keys[k.ID] = k
	}
	return s, nil
}

func (s *Store) filePath() string { return filepath.Join(s.dir, "keys.json") }

// SetDefaultQuota 设置「每日 08:00 重置后」恢复到的额度。
func (s *Store) SetDefaultQuota(n int) {
	s.mu.Lock()
	s.defaultQuota = n
	s.mu.Unlock()
}

func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeLocked()
}

func (s *Store) writeLocked() error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	list := make([]*Key, 0, len(s.keys))
	for _, k := range s.keys {
		list = append(list, k)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt.Before(list[j].CreatedAt) })
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.filePath(), data, 0o644)
}

func (s *Store) CreateKey(model string, ttlDays, quota int, now time.Time) (*Key, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := &Key{
		ID:        GenerateKey(),
		Key:       GenerateKey(),
		Model:     model,
		CreatedAt: now,
		ExpiresAt: keyExpiry(now, ttlDays),
		TokenUsed: 0,
		Quota:     quota,
		Disabled:  false,
		QuotaDate: dailyResetDate(now),
	}
	s.keys[k.ID] = k
	return k, s.writeLocked()
}

// CreateOrGetForIP 返回该 IP 的 Key；已存在则直接返回旧的（幂等领取）。
//
// 语义：每个 IP 固定一个 Key，额度每日 08:00 自动重置，**用户无需重复领取**。
// 重复点击「获取 Key」拿到的是同一个 Key，不会报错、不会多占额度。
func (s *Store) CreateOrGetForIP(ip, model string, ttlDays, quota int, now time.Time) (*Key, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ip = normalizeIP(ip)
	for _, k := range s.keys {
		if k.IP != "" && k.IP == ip {
			// 额度跨日自动重置：这里顺带把额度恢复到默认值。
			s.rollQuotaLocked(k, quota, now)
			return k, false, s.writeLocked()
		}
	}
	k := &Key{
		ID:        GenerateKey(),
		Key:       GenerateKey(),
		Model:     model,
		CreatedAt: now,
		ExpiresAt: keyExpiry(now, ttlDays),
		TokenUsed: 0,
		Quota:     quota,
		Disabled:  false,
		IP:        ip,
		QuotaDate: dailyResetDate(now),
	}
	s.keys[k.ID] = k
	return k, true, s.writeLocked()
}

// rollQuotaLocked 跨额度日时把用量归零、额度恢复默认。
func (s *Store) rollQuotaLocked(k *Key, defaultQuota int, now time.Time) bool {
	date := dailyResetDate(now)
	if k.QuotaDate == date {
		return false
	}
	k.QuotaDate = date
	k.TokenUsed = 0
	if defaultQuota > 0 {
		k.Quota = defaultQuota
	} else if s.defaultQuota > 0 {
		k.Quota = s.defaultQuota
	}
	return true
}

// RollQuota 对所有 Key 执行跨日额度重置（启动时与后台查询时调用）。
func (s *Store) RollQuota(defaultQuota int, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	for _, k := range s.keys {
		if s.rollQuotaLocked(k, defaultQuota, now) {
			changed = true
		}
	}
	if changed {
		_ = s.writeLocked()
	}
	return changed
}

// ApplyTTL 把「Key 有效期」这个全局策略应用到所有 Key。
//
// ttlDays=0 → 全部改为**永不过期**（用量由每日 08:00 自动重置的额度控制）。
// ttlDays>0 → 全部从此刻起算，到「下一个 08:00 边界 + (ttlDays-1) 天」到期。
func (s *Store) ApplyTTL(ttlDays int, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	for _, k := range s.keys {
		want := keyExpiry(now, ttlDays)
		if !k.ExpiresAt.Equal(want) {
			k.ExpiresAt = want
			changed = true
		}
	}
	if changed {
		_ = s.writeLocked()
	}
	return changed
}

// FindByIP 按 IP 查 Key。
func (s *Store) FindByIP(ip string) (*Key, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ip = normalizeIP(ip)
	for _, k := range s.keys {
		if k.IP == ip {
			return k, true
		}
	}
	return nil, false
}

// keyExpiry 计算 Key 的到期时间：对齐到每日 8:00 的额度日边界。
//
// ttlDays=1 → 今天领取就在「下一个 8:00」到期（早上领的人能用一整个白天，
// 晚上领的人到次日 8:00 失效，两种情况都符合「一天一个额度日」的直觉）。
//
// ttlDays=0 → **永不过期**：Key 长期有效，用量由「每日 08:00 自动重置的额度」控制。
// 这是「每个 IP 固定一个 Key、用完明天自动恢复」的推荐配置。
func keyExpiry(now time.Time, ttlDays int) time.Time {
	if ttlDays <= 0 {
		return time.Time{} // 零值 = 永不过期
	}
	base := nextDailyReset(now)
	return base.AddDate(0, 0, ttlDays-1)
}

func (s *Store) GetByKey(key string) (*Key, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range s.keys {
		if k.Key == key {
			return k, true
		}
	}
	return nil, false
}

func (s *Store) GetByID(id string) (*Key, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.keys[id]
	return k, ok
}

// Snapshot 返回 id 对应 Key 的深拷贝，锁内完成，不暴露内部指针。
func (s *Store) Snapshot(id string) (Key, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.keys[id]
	if !ok {
		return Key{}, false
	}
	return *k, true
}

// SnapshotByKey 按 Key 字符串（sk-...）快照，语义同 Snapshot。
func (s *Store) SnapshotByKey(key string) (Key, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range s.keys {
		if k.Key == key {
			return *k, true
		}
	}
	return Key{}, false
}

// Consume 锁内原子 check+act：key 存在且未禁用且 token_used+n <= quota 时
// 累加并写盘返回 true；否则（超额/禁用/不存在）返回 false 且不写。
//
// 扣减前先做跨日额度重置：这样「每日 08:00 自动恢复额度」无需用户重新领取。
func (s *Store) Consume(id string, n int) bool {
	return s.ConsumeAt(id, n, time.Now())
}

// ConsumeAt 是 Consume 的显式时间版本（便于测试与回放）。
func (s *Store) ConsumeAt(id string, n int, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.keys[id]
	if !ok || k.Disabled {
		return false
	}
	if s.rollQuotaLocked(k, 0, now) {
		_ = s.writeLocked()
	}
	if k.TokenUsed+n > k.Quota {
		return false
	}
	k.TokenUsed += n
	_ = s.writeLocked()
	return true
}

func (s *Store) AddUsage(id string, tokens int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if k, ok := s.keys[id]; ok {
		k.TokenUsed += tokens
		_ = s.writeLocked()
	}
}

// SetQuota 调整单个 Key 的额度（并写盘）。
func (s *Store) SetQuota(id string, quota int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.keys[id]
	if !ok {
		return os.ErrNotExist
	}
	k.Quota = quota
	return s.writeLocked()
}

func (s *Store) SetDisabled(id string, disabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.keys[id]
	if !ok {
		return os.ErrNotExist
	}
	k.Disabled = disabled
	return s.writeLocked()
}

func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.keys[id]; !ok {
		return os.ErrNotExist
	}
	delete(s.keys, id)
	return s.writeLocked()
}

// MaxIssuedQuota 返回当前所有 Key 中最大的额度（只统计**合法上界内**的额度）。
//
// 用于 PUT /api/admin/settings 的合理性校验：token_quota 一旦被下调到低于存量
// Key 的额度，所有在用 Key 的额度都会被砍，属于高危误配置。
//
// 修复（finding 823 / fact 822）：旧实现无条件取全部存量 Key 的 quota 最大值，
// 于是线上那条 quota=9223372036854775807(MaxInt64) 的脏 Key 会让返回值恒为 MaxInt64，
// 管理员任何下调 token_quota 的操作都被 400 拒绝（活锁，管理员无法收紧额度）。
// 现在只统计 1..MaxTokenQuota 之间的额度：越界脏值不参与比较（启动期已由
// ClampOutOfRangeQuota 钳制回合法值），既保留「不要误砍在用 Key 额度」的保护，
// 又不会被单条异常数据永久锁死。
//
// 刻意不把 s.defaultQuota 计入：启动时它等于当前的 config.TokenQuota，
// 一旦计入就等于「额度只升不降」，管理员合法下调会被永久拒绝。
// 这里只关心「已经发出去、正在被使用的 Key」。
func (s *Store) MaxIssuedQuota() int {
    s.mu.Lock()
    defer s.mu.Unlock()
    max := 0
    for _, k := range s.keys {
        if k.Quota <= 0 || k.Quota > MaxTokenQuota {
            // 越界（或非法）额度不参与统计：它不是合法发放过的额度。
            continue
        }
        if k.Quota > max {
            max = k.Quota
        }
    }
    return max
}

// QuotaClampResult 描述一次存量额度钳制的结果，供启动告警与回归测试使用。
type QuotaClampResult struct {
    Clamped int      // 被钳制的 Key 数量
    Max     int      // 钳制后生效的额度上界
    Details []string // 每个被钳制 Key 的明细（id: 原值 -> 新值）
}

// ClampOutOfRangeQuota 把存量 Key 中**超出合法上界**的额度钳制回合法值并写盘。
//
// 背景（finding 823 / fact 822）：旧版本对 token_quota 没有上界，
// 9223372036854775807(MaxInt64) 这种额度能被写进 keys.json 并长期生效：
// gateway.go 的「used >= quota」「used + n > quota」判断对 MaxInt64 恒不成立，
// 该 Key 在当前额度日内可无限量调用上游，绕过计费与额度保护。
//
// 现在：启动/加载期把越界额度钳制为 limit（默认额度与 MaxTokenQuota 取小），
// 立即写回 keys.json，使脏数据不再永久生效。
// quota <= 0 的 Key 不处理：那是「已耗尽」的 fail-closed 状态，钳制反而会白送额度。
func (s *Store) ClampOutOfRangeQuota(defaultQuota int) (QuotaClampResult, error) {
    s.mu.Lock()
    defer s.mu.Unlock()
    // 上界**只用 MaxTokenQuota，不能用 defaultQuota**。
    //
    // defaultQuota 是「当前配置的默认额度」，而存量 Key 的额度是它被发放那
    // 一刻的值。管理员把默认额度从 1000W 调到 200W 后，存量 Key 的额度仍应
    // 是 1000W（额度变更次日 08:00 才生效）。若拿 defaultQuota 当上限，启动时
    // 会把所有额度高于新默认值的正常 Key 全砍成 200W —— 既让当天在用的 Key
    // 当场超额，又把「次日生效」变成「重启即生效」。
    //
    // 这里只清理**真正越界**的值（> MaxTokenQuota，如 MaxInt64 脏 Key）。
    limit := MaxTokenQuota
    res := QuotaClampResult{Max: limit}
    for _, k := range s.keys {
        if k.Quota <= limit {
            continue
        }
        old := k.Quota
        k.Quota = limit
        res.Clamped++
        res.Details = append(res.Details, fmt.Sprintf("%s: %d -> %d", k.ID, old, k.Quota))
    }
    if res.Clamped > 0 {
        if err := s.writeLocked(); err != nil {
            return res, err
        }
    }
    return res, nil
}

// ApplyQuotaDown 把所有在用 Key 的额度收紧到 newQuota（**只降不升**）。
//
// 用于管理员下调 token_quota 后立即生效：额度每日 08:00 会自动重置为默认额度，
// 因此收紧是可逆的（不存在旧注释所说「不可逆、只能重新领取」）。
// 只降不升：避免在额度日中途把 Key 的额度抬高，凭空赠送用量。
// 返回被收紧的 Key 数量。
func (s *Store) ApplyQuotaDown(newQuota int) int {
    if newQuota <= 0 {
        return 0
    }
    s.mu.Lock()
    defer s.mu.Unlock()
    changed := 0
    for _, k := range s.keys {
        if k.Quota > newQuota {
            k.Quota = newQuota
            changed++
        }
    }
    if changed > 0 {
        _ = s.writeLocked()
    }
    return changed
}

func (s *Store) List() []Key {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Key, 0, len(s.keys))
	for _, k := range s.keys {
		out = append(out, *k)
	}
	return out
}
