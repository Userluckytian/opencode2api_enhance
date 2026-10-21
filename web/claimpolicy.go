package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ClaimRule 是领取页面向使用者公开的规则。
type ClaimRule struct {
	// AllowCIDRs 是允许领取的网段白名单。
	//
	// 语义（修复 finding 566）：空列表 = 全部拒绝（fail-closed）。
	// 旧版本把空列表当「不限制」，而 ParseCIDRs 又会静默丢弃无法解析的项，
	// 于是管理员把白名单写成 "192.168.12.*"（全都不合法）后，落盘得到一个空列表，
	// 领取口反而从「仅内网」变成「对全网开放」。现在改成：非法项直接 400，
	// 空列表则一个人都领不到；确实要放开必须显式写 0.0.0.0/0 与 ::/0。
	AllowCIDRs []string `json:"allow_cidrs"`
	// OneKeyPerIP 是否限制同一 IP 固定一个 Key。
	OneKeyPerIP bool `json:"one_key_per_ip"`
}

// 注意：额度（token_quota）与 Key 有效期（ttl_days）**不在这里**。
// 它们是全局配置项（config.json 的 token_quota / ttl_days），唯一数据源，
// 避免出现两份副本各自漂移。

// ClaimPolicy 保存领取规则与「IP → 已领取」记录。
type ClaimPolicy struct {
	mu     sync.Mutex
	dir    string
	rule   ClaimRule
	issued map[string]claimIssue // 按 IP 记录已领取
}

type claimIssue struct {
	Date  string `json:"date"` // 额度日（8:00 边界）
	Time  string `json:"time"`
	KeyID string `json:"key_id"`
	Model string `json:"model"`
	IP    string `json:"ip"`
	Count int    `json:"count"`
}

type claimPolicyFile struct {
	// Rule 用指针：文件里「有 rule 键」与「没有 rule 键」是两种语义。
	// 有键（哪怕是 {"allow_cidrs":[]}）= 管理员显式配置过，必须原样生效；
	// 没有键 = 旧结构残留，沿用默认规则。
	Rule   *ClaimRule            `json:"rule"`
	Issued map[string]claimIssue `json:"issued"`
}

// DefaultClaimRule 返回默认领取规则。
func DefaultClaimRule() ClaimRule {
	return ClaimRule{
		AllowCIDRs:  nil,
		OneKeyPerIP: true,
	}
}

func NewClaimPolicy(dir string, rule ClaimRule) *ClaimPolicy {
	p := &ClaimPolicy{dir: dir, rule: rule, issued: map[string]claimIssue{}}
	p.load()
	return p
}

func (p *ClaimPolicy) filePath() string { return filepath.Join(p.dir, "claim_policy.json") }

func (p *ClaimPolicy) load() {
	data, err := os.ReadFile(p.filePath())
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		return
	}
	var f claimPolicyFile
	if json.Unmarshal(data, &f) != nil {
		return
	}
	// 修复 finding 566 的 restart 侧缺口：旧实现只在 len(AllowCIDRs)>0 ||
	// OneKeyPerIP 时才载入规则，于是「显式清空白名单（全拒绝）」在重启后会
	// 被启动参数里的默认白名单覆盖，静默变回 fail-open。现在只要文件里存在
	// rule 键就一律以磁盘为准。
	if f.Rule != nil {
		p.rule = *f.Rule
	}
	for ip, iss := range f.Issued {
		p.issued[ip] = iss
	}
	// 旧版本曾在 rule 里存 token_quota / ttl_days / reset_hour 副本（会与全局
	// 配置漂移且从不生效）。落盘时按当前结构重写，自动清掉这些历史残留。
	p.saveLocked()
}

// sameCIDRs 比较两个网段列表是否一致。
func sameCIDRs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (p *ClaimPolicy) saveLocked() {
	if err := os.MkdirAll(p.dir, 0o755); err != nil {
		return
	}
	rule := p.rule
	data, err := json.MarshalIndent(claimPolicyFile{Rule: &rule, Issued: p.issued}, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(p.filePath(), data, 0o644)
}

// Rule 返回当前规则副本。
func (p *ClaimPolicy) Rule() ClaimRule {
	p.mu.Lock()
	defer p.mu.Unlock()
	r := p.rule
	r.AllowCIDRs = append([]string(nil), p.rule.AllowCIDRs...)
	return r
}

// SetRule 更新规则并落盘。
func (p *ClaimPolicy) SetRule(r ClaimRule) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rule = r
	p.saveLocked()
}

// IPAllowed 判断来源 IP 是否在允许网段内。
//
// fail-closed（finding 566）：白名单为空时一律拒绝，来源必须显式命中某个网段。
// 只有在配置里明确写了 0.0.0.0/0 / ::/0 才算放开。
func (p *ClaimPolicy) IPAllowed(ip string) bool {
	p.mu.Lock()
	cidrs := append([]string(nil), p.rule.AllowCIDRs...)
	p.mu.Unlock()
	if len(cidrs) == 0 {
		return false
	}
	addr, err := netip.ParseAddr(normalizeIP(ip))
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	for _, c := range cidrs {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if !strings.Contains(c, "/") {
			// 允许写单个 IP 或裸网段（192.168.12. 视为 /24）
			if strings.HasSuffix(c, ".") {
				c += "0/24"
			} else {
				c += "/32"
			}
		}
		pfx, err := netip.ParsePrefix(c)
		if err != nil {
			continue
		}
		if pfx.Contains(addr) {
			return true
		}
	}
	return false
}

// ClaimedToday 返回该 IP 在当前额度日是否已领过 Key。
func (p *ClaimPolicy) ClaimedToday(ip string, now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	iss, ok := p.issued[normalizeIP(ip)]
	if !ok {
		return false
	}
	return iss.Date == dailyResetDate(now)
}

// RecordClaim 记录一次领取。
func (p *ClaimPolicy) RecordClaim(ip, keyID, model string, now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	ip = normalizeIP(ip)
	date := dailyResetDate(now)
	iss, ok := p.issued[ip]
	if !ok || iss.Date != date {
		iss = claimIssue{Date: date}
		p.issued[ip] = iss
	}
	iss.Time = now.Format(time.RFC3339)
	iss.KeyID = keyID
	iss.Model = model
	iss.IP = ip
	iss.Count++
	p.issued[ip] = iss
	p.saveLocked()
}

// ClaimedList 返回当前有领取记录的 IP（用于后台展示）。
func (p *ClaimPolicy) ClaimedList(now time.Time) []claimIssue {
	p.mu.Lock()
	defer p.mu.Unlock()
	date := dailyResetDate(now)
	out := make([]claimIssue, 0, len(p.issued))
	for _, iss := range p.issued {
		if iss.Date == date {
			out = append(out, iss)
		}
	}
	return out
}

// DefaultAllowedCIDR 是默认放行的网段。
// 用户团队在 192.168.12.x，但硬编码具体网段不通用——这里默认放行全部
// 私有网段（RFC1918 + CGNAT）以及回环地址。管理员可在后台按需收窄成
// 具体的 192.168.12.0/24。
// 回环地址一并放行：那是「就在服务器上的人」在访问，不属于「走代理进来」。
func DefaultAllowedCIDR() []string {
	return []string{
		"127.0.0.0/8",
		"::1/128",
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"100.64.0.0/10",
	}
}

// isPrivateOrCGNAT 判断地址是否属于私有网段或运营商 NAT。
func isPrivateOrCGNAT(addr netip.Addr) bool {
	addr = addr.Unmap()
	if addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() {
		return true
	}
	b := addr.As4()
	return b[0] == 100 && b[1] >= 64 && b[1] <= 127
}

// normalizeCIDRItem 把用户输入的单项网段规范成 netip 可解析的形式。
// 返回规范后的串与错误；错误里带原始输入，便于前端/日志点名是哪一项非法。
func normalizeCIDRItem(raw string) (string, error) {
	c := strings.TrimSpace(raw)
	if c == "" {
		return "", errors.New("空的网段项")
	}
	// 裸 IP 或「192.168.12.」这种简写补成前缀形式。
	if !strings.Contains(c, "/") {
		if strings.HasSuffix(c, ".") {
			c += "0/24"
		} else {
			c += "/32"
		}
	}
	if _, err := netip.ParsePrefix(c); err != nil {
		return "", fmt.Errorf("非法的网段 %q", raw)
	}
	return c, nil
}

// ParseCIDRsStrict 严格解析网段列表：任何一项非法都整体报错，绝不静默丢弃。
//
// 修复 finding 566 —— 旧 ParseCIDRs 对非法项 `continue`，管理员以为收紧了白名单，
// 实际因为「全部非法 → 结果为空 → IPAllowed 放行」而把领取口彻底打开。
// 需要在错误信息里点名具体哪一项非法（调用方拼进 400 响应）。
func ParseCIDRsStrict(list []string) ([]string, error) {
	out := make([]string, 0, len(list))
	for _, raw := range list {
		c, err := normalizeCIDRItem(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// ParseCIDRs 解析网段列表，忽略非法项。
//
// 仅保留给「受信配置」使用（如 config.json 的 trusted_proxies / 默认白名单）。
// 任何用户可控输入（后台 PUT /api/admin/claim-policy）都必须走 ParseCIDRsStrict。
func ParseCIDRs(list []string) []string {
	out, err := ParseCIDRsStrict(list)
	if err != nil {
		return nil
	}
	return out
}

var _ = net.ParseIP
