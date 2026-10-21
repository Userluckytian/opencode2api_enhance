package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

/* ---------- 8:00 额度日边界 ---------- */

func TestNextDailyResetAlwaysAt8(t *testing.T) {
	loc := time.Local
	cases := []struct {
		name string
		now  time.Time
		want time.Time
	}{
		{"凌晨3点→当天8点", time.Date(2026, 10, 21, 3, 0, 0, 0, loc), time.Date(2026, 10, 21, 8, 0, 0, 0, loc)},
		{"上午9点→次日8点", time.Date(2026, 10, 21, 9, 0, 0, 0, loc), time.Date(2026, 10, 22, 8, 0, 0, 0, loc)},
		{"正好8点→次日8点", time.Date(2026, 10, 21, 8, 0, 0, 0, loc), time.Date(2026, 10, 22, 8, 0, 0, 0, loc)},
		{"晚上23点→次日8点", time.Date(2026, 10, 21, 23, 0, 0, 0, loc), time.Date(2026, 10, 22, 8, 0, 0, 0, loc)},
	}
	for _, c := range cases {
		if got := nextDailyReset(c.now); !got.Equal(c.want) {
			t.Fatalf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestDailyResetDateGroupsBefore8WithPreviousDay(t *testing.T) {
	loc := time.Local
	// 8:00 之前属于「前一天的额度日」
	if got := dailyResetDate(time.Date(2026, 10, 21, 7, 59, 0, 0, loc)); got != "2026-10-20" {
		t.Fatalf("got %s want 2026-10-20", got)
	}
	// 8:00 之后属于「当天」
	if got := dailyResetDate(time.Date(2026, 10, 21, 8, 0, 0, 0, loc)); got != "2026-10-21" {
		t.Fatalf("got %s want 2026-10-21", got)
	}
}

func TestAlignToGrid(t *testing.T) {
	loc := time.Local
	// 一天窗口：8:00 → 次日 8:00
	start, end := alignToGrid(time.Date(2026, 10, 21, 14, 0, 0, 0, loc), 1440)
	if !start.Equal(time.Date(2026, 10, 21, 8, 0, 0, 0, loc)) || !end.Equal(time.Date(2026, 10, 22, 8, 0, 0, 0, loc)) {
		t.Fatalf("一天窗口 got %v → %v", start, end)
	}
	// 凌晨 3 点属于前一天开始的窗口
	start2, end2 := alignToGrid(time.Date(2026, 10, 21, 3, 0, 0, 0, loc), 1440)
	if !start2.Equal(time.Date(2026, 10, 20, 8, 0, 0, 0, loc)) || !end2.Equal(time.Date(2026, 10, 21, 8, 0, 0, 0, loc)) {
		t.Fatalf("凌晨窗口 got %v → %v", start2, end2)
	}
	// 两小时窗口：8:00→10:00、10:00→12:00
	s3, e3 := alignToGrid(time.Date(2026, 10, 21, 11, 0, 0, 0, loc), 120)
	if !s3.Equal(time.Date(2026, 10, 21, 10, 0, 0, 0, loc)) || !e3.Equal(time.Date(2026, 10, 21, 12, 0, 0, 0, loc)) {
		t.Fatalf("两小时窗口 got %v → %v", s3, e3)
	}
}

// Key 到期时间应落在 8:00 边界。
func TestKeyExpiryAlignedTo8(t *testing.T) {
	loc := time.Local
	// 上午 10 点领 → 次日 8:00 到期
	exp := keyExpiry(time.Date(2026, 10, 21, 10, 0, 0, 0, loc), 1)
	if !exp.Equal(time.Date(2026, 10, 22, 8, 0, 0, 0, loc)) {
		t.Fatalf("got %v", exp)
	}
	// 凌晨 3 点领 → 当天 8:00 到期
	exp2 := keyExpiry(time.Date(2026, 10, 21, 3, 0, 0, 0, loc), 1)
	if !exp2.Equal(time.Date(2026, 10, 21, 8, 0, 0, 0, loc)) {
		t.Fatalf("got %v", exp2)
	}
}

/* ---------- 领取网段白名单 ---------- */

func TestClaimPolicyIPAllowed(t *testing.T) {
	p := NewClaimPolicy(t.TempDir(), ClaimRule{
		AllowCIDRs:  []string{"192.168.12.0/24"},
		OneKeyPerIP: true,
	})
	if !p.IPAllowed("192.168.12.17") {
		t.Fatal("192.168.12.17 应被放行")
	}
	if p.IPAllowed("192.168.13.5") {
		t.Fatal("192.168.13.5 不应被放行")
	}
	if p.IPAllowed("8.8.8.8") {
		t.Fatal("公网 IP 不应被放行")
	}
	if p.IPAllowed("not-an-ip") {
		t.Fatal("非法 IP 不应被放行")
	}
}

// 修复 finding 566：白名单为空时必须 fail-closed（旧语义是「空=不限」，
// 与 ParseCIDRs 静默丢弃非法项叠加后，会把「收紧白名单」变成「对全网开放」）。
func TestClaimPolicyEmptyAllowListRejectsAll(t *testing.T) {
	p := NewClaimPolicy(t.TempDir(), ClaimRule{OneKeyPerIP: true})
	if p.IPAllowed("8.8.8.8") {
		t.Fatal("白名单为空时应一律拒绝（fail-closed）")
	}
	if p.IPAllowed("192.168.12.17") {
		t.Fatal("白名单为空时内网 IP 也应拒绝")
	}
	// 要真正放开必须显式写 0.0.0.0/0 与 ::/0。
	p2 := NewClaimPolicy(t.TempDir(), ClaimRule{AllowCIDRs: []string{"0.0.0.0/0", "::/0"}})
	if !p2.IPAllowed("8.8.8.8") {
		t.Fatal("显式配置 0.0.0.0/0 后应放行")
	}
}

// ParseCIDRsStrict：任何一项非法都必须整体报错，且错误信息点名具体项。
func TestParseCIDRsStrictRejectsIllegal(t *testing.T) {
	if _, err := ParseCIDRsStrict([]string{"192.168.12.0/24", "192.168.12.*"}); err == nil {
		t.Fatal("含非法项时应返回错误")
	} else if !strings.Contains(err.Error(), "192.168.12.*") {
		t.Fatalf("错误信息应点名非法项: %v", err)
	}
	got, err := ParseCIDRsStrict([]string{"192.168.12.0/24", "10.1.2.3"})
	if err != nil {
		t.Fatalf("合法输入不应报错: %v", err)
	}
	if len(got) != 2 || got[1] != "10.1.2.3/32" {
		t.Fatalf("裸 IP 应补成 /32: %v", got)
	}
}

// PUT /api/admin/claim-policy：非法网段必须 400 且白名单保持原值（不再落盘成空）。
func TestPutClaimPolicyRejectsIllegalCIDR(t *testing.T) {
	dir := t.TempDir()
	app := &App{Claims: NewClaimPolicy(dir, ClaimRule{AllowCIDRs: []string{"192.168.12.0/24"}, OneKeyPerIP: true})}
	body := strings.NewReader(`{"allow_cidrs":["192.168.12.*"]}`)
	rec := httptest.NewRecorder()
	app.handlePutClaimPolicy(rec, httptest.NewRequest(http.MethodPut, "/api/admin/claim-policy", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法网段应 400，实际 %d: %s", rec.Code, rec.Body.String())
	}
	rule := app.Claims.Rule()
	if len(rule.AllowCIDRs) != 1 || rule.AllowCIDRs[0] != "192.168.12.0/24" {
		t.Fatalf("被拒绝后白名单必须保持原值: %v", rule.AllowCIDRs)
	}
	if !app.Claims.IPAllowed("192.168.12.17") {
		t.Fatal("原白名单应仍然生效")
	}
}

// 未提交 allow_cidrs 时应保持原值（合并语义）。
func TestPutClaimPolicyOmittingCIDRsKeepsExisting(t *testing.T) {
	dir := t.TempDir()
	app := &App{Claims: NewClaimPolicy(dir, ClaimRule{AllowCIDRs: []string{"192.168.12.0/24"}, OneKeyPerIP: true})}
	rec := httptest.NewRecorder()
	app.handlePutClaimPolicy(rec, httptest.NewRequest(http.MethodPut, "/api/admin/claim-policy",
		strings.NewReader(`{"one_key_per_ip":false}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	rule := app.Claims.Rule()
	if len(rule.AllowCIDRs) != 1 || rule.AllowCIDRs[0] != "192.168.12.0/24" {
		t.Fatalf("未提交 allow_cidrs 时不应改白名单: %v", rule.AllowCIDRs)
	}
	if rule.OneKeyPerIP {
		t.Fatal("提交的 one_key_per_ip 应生效")
	}
}

// 显式提交空数组 = 全拒绝（不是「不限」）。
func TestPutClaimPolicyExplicitEmptyCIDRsDeniesAll(t *testing.T) {
	dir := t.TempDir()
	app := &App{Claims: NewClaimPolicy(dir, ClaimRule{AllowCIDRs: []string{"192.168.12.0/24"}, OneKeyPerIP: true})}
	rec := httptest.NewRecorder()
	app.handlePutClaimPolicy(rec, httptest.NewRequest(http.MethodPut, "/api/admin/claim-policy",
		strings.NewReader(`{"allow_cidrs":[]}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	if app.Claims.IPAllowed("192.168.12.17") {
		t.Fatal("显式清空白名单后应全拒绝")
	}
}

func TestClaimPolicyBareIPAndPrefix(t *testing.T) {
	p := NewClaimPolicy(t.TempDir(), ClaimRule{AllowCIDRs: ParseCIDRs([]string{"192.168.12.0/24", "10.1.2.3"})})
	if !p.IPAllowed("192.168.12.99") {
		t.Fatal("网段应放行")
	}
	if !p.IPAllowed("10.1.2.3") {
		t.Fatal("单个 IP 应放行")
	}
	if p.IPAllowed("10.1.2.4") {
		t.Fatal("其它 IP 不应放行")
	}
}

func TestDefaultAllowedCIDRCoversPrivateRanges(t *testing.T) {
	p := NewClaimPolicy(t.TempDir(), ClaimRule{AllowCIDRs: DefaultAllowedCIDR()})
	for _, ip := range []string{"192.168.12.17", "10.0.0.5", "172.16.3.4", "100.64.1.1", "127.0.0.1"} {
		if !p.IPAllowed(ip) {
			t.Fatalf("%s 应被默认白名单放行", ip)
		}
	}
	for _, ip := range []string{"8.8.8.8", "1.1.1.1", "203.0.113.7"} {
		if p.IPAllowed(ip) {
			t.Fatalf("%s 不应被默认白名单放行", ip)
		}
	}
}

/* ---------- 同一 IP 限领一个 ---------- */

func TestClaimPolicyOneKeyPerIP(t *testing.T) {
	dir := t.TempDir()
	p := NewClaimPolicy(dir, ClaimRule{OneKeyPerIP: true})
	now := time.Date(2026, 10, 21, 10, 0, 0, 0, time.Local)
	if p.ClaimedToday("192.168.12.17", now) {
		t.Fatal("未领取过不应算已领取")
	}
	p.RecordClaim("192.168.12.17", "k1", "glm-4", now)
	if !p.ClaimedToday("192.168.12.17", now) {
		t.Fatal("领取后应算已领取")
	}
	if p.ClaimedToday("192.168.12.18", now) {
		t.Fatal("其它 IP 不受影响")
	}
	// 跨到次日 8:00 之后应重置
	after := time.Date(2026, 10, 22, 8, 0, 0, 0, time.Local)
	if p.ClaimedToday("192.168.12.17", after) {
		t.Fatal("次日 8:00 后应可再次领取")
	}
}

func TestClaimPolicyPersists(t *testing.T) {
	dir := t.TempDir()
	p := NewClaimPolicy(dir, ClaimRule{OneKeyPerIP: true})
	now := time.Now()
	p.RecordClaim("192.168.12.17", "k1", "glm-4", now)

	p2 := NewClaimPolicy(dir, ClaimRule{OneKeyPerIP: true})
	if !p2.ClaimedToday("192.168.12.17", now) {
		t.Fatal("重启后领取记录应保留")
	}
}

/* ---------- 领 Key 接口 ---------- */

func newClaimApp(t *testing.T, rule ClaimRule) (*App, *httptest.Server) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.AllowedModels = []string{"glm-4"}
	// finding 548（fail-closed）：base_url 只来自 public_base_url，缺失即 503 拒绝下发。
	cfg.PublicBaseURL = "http://example.com:23081"
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	app := &App{
		Cfg: cfg, Store: store, Rl: NewRateLimiter(1000), Sessions: NewSessionStore(),
		Logger: NewLogger(dir), Client: http.DefaultClient, ConfigPath: dir,
		Claims: NewClaimPolicy(dir, rule),
	}
	return app, httptest.NewServer(http.HandlerFunc(app.HandleCreateKey))
}

// claimKey 直接调用 handler（不走网络，避免 httptest 请求的 RequestURI 限制）。
func claimKey(t *testing.T, app *App, remoteAddr string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/keys", strings.NewReader(`{"model":"glm-4"}`))
	req.RemoteAddr = remoteAddr
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.HandleCreateKey(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if e, ok := out["error"]; ok {
		return rec.Code, e.(string)
	}
	return rec.Code, ""
}

func TestCreateKeyRejectsDisallowedNetwork(t *testing.T) {
	app, _ := newClaimApp(t, ClaimRule{AllowCIDRs: []string{"192.168.12.0/24"}, OneKeyPerIP: true})
	code, errMsg := claimKey(t, app, "8.8.8.8:1234")
	if code != http.StatusForbidden {
		t.Fatalf("外网 IP 应被拒绝，code=%d", code)
	}
	if !strings.Contains(errMsg, "不允许领取") {
		t.Fatalf("错误信息不对: %s", errMsg)
	}
}

func TestCreateKeyAllowsInternalNetwork(t *testing.T) {
	app, _ := newClaimApp(t, ClaimRule{AllowCIDRs: []string{"192.168.12.0/24"}, OneKeyPerIP: true})
	code, errMsg := claimKey(t, app, "192.168.12.17:1234")
	if code != http.StatusOK {
		t.Fatalf("内网 IP 应放行，code=%d err=%s", code, errMsg)
	}
}

// 同一 IP 重复领取：拿到的是同一个 Key（幂等），不报错、不多占额度。
// 额度每日 08:00 自动重置，使用者不需要每天重新领取。
func TestCreateKeyIsIdempotentPerIP(t *testing.T) {
	app, _ := newClaimApp(t, ClaimRule{AllowCIDRs: []string{"192.168.12.0/24"}, OneKeyPerIP: true})
	rec1 := httptest.NewRecorder()
	req1 := httptest.NewRequest(http.MethodPost, "/api/keys", strings.NewReader(`{"model":"glm-4"}`))
	req1.RemoteAddr = "192.168.12.17:1234"
	app.HandleCreateKey(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("首次应成功 code=%d", rec1.Code)
	}
	var out1 struct {
		Key   string `json:"key"`
		IsNew bool   `json:"is_new"`
	}
	_ = json.Unmarshal(rec1.Body.Bytes(), &out1)
	if !out1.IsNew {
		t.Fatal("首次应标记为新建")
	}

	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/api/keys", strings.NewReader(`{"model":"glm-4"}`))
	req2.RemoteAddr = "192.168.12.17:1234"
	app.HandleCreateKey(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("同 IP 重复领取应返回原 Key，code=%d body=%s", rec2.Code, rec2.Body.String())
	}
	var out2 struct {
		Key   string `json:"key"`
		IsNew bool   `json:"is_new"`
	}
	_ = json.Unmarshal(rec2.Body.Bytes(), &out2)
	if out2.IsNew {
		t.Fatal("重复领取不应标记为新建")
	}
	if out2.Key != out1.Key {
		t.Fatalf("重复领取应拿回同一个 Key: %q vs %q", out2.Key, out1.Key)
	}

	// 其它 IP 拿到不同的 Key
	rec3 := httptest.NewRecorder()
	req3 := httptest.NewRequest(http.MethodPost, "/api/keys", strings.NewReader(`{"model":"glm-4"}`))
	req3.RemoteAddr = "192.168.12.18:1234"
	app.HandleCreateKey(rec3, req3)
	var out3 struct {
		Key string `json:"key"`
	}
	_ = json.Unmarshal(rec3.Body.Bytes(), &out3)
	if out3.Key == out1.Key {
		t.Fatal("不同 IP 应拿到不同 Key")
	}
}

func TestCreateKeyAllowsRepeatWhenLimitDisabled(t *testing.T) {
	app, _ := newClaimApp(t, ClaimRule{AllowCIDRs: []string{"192.168.12.0/24"}, OneKeyPerIP: false})
	if code, _ := claimKey(t, app, "192.168.12.17:1234"); code != http.StatusOK {
		t.Fatalf("首次 code=%d", code)
	}
	if code, _ := claimKey(t, app, "192.168.12.17:1234"); code != http.StatusOK {
		t.Fatalf("关闭限领后应可重复领取 code=%d", code)
	}
}

/* ---------- 公开规则接口 ---------- */

func TestClaimRulesEndpoint(t *testing.T) {
	cfg := DefaultConfig()
	cfg.TokenQuota = 30000000
	cfg.TTLDays = 1
	app := &App{Cfg: cfg, Claims: NewClaimPolicy(t.TempDir(), ClaimRule{
		AllowCIDRs: []string{"192.168.12.0/24"}, OneKeyPerIP: true,
	})}
	rec := httptest.NewRecorder()
	app.handleClaimRules(rec, httptest.NewRequest(http.MethodGet, "/api/claim-rules", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["one_key_per_ip"] != true {
		t.Fatalf("应返回限领规则: %v", out)
	}
	// reset_hour 已移除（该配置从未生效），重置时刻统一为 08:00。
	if _, ok := out["reset_hour"]; ok {
		t.Fatalf("不应再返回 reset_hour: %v", out)
	}
	if !strings.Contains(out["reset_at"].(string), "08:00") {
		t.Fatalf("重置时刻应为 08:00: %v", out)
	}
	// 公开接口不能泄露白名单网段
	if strings.Contains(rec.Body.String(), "192.168.12.0/24") {
		t.Fatalf("公开规则不应暴露网段白名单: %s", rec.Body.String())
	}
}

/* ---------- 额度唯一数据源 ---------- */

// 额度只能来自 config.token_quota；claim_policy.json 里不得再存副本。
func TestQuotaHasSingleSourceOfTruth(t *testing.T) {
	dir := t.TempDir()
	p := NewClaimPolicy(dir, ClaimRule{AllowCIDRs: []string{"192.168.12.0/24"}, OneKeyPerIP: true})
	// Rule() 返回的规则里不应再带额度/有效期字段（已从 ClaimRule 移除，
	// 编译期即保证 —— 这里再确认落盘 JSON 里也没有）。
	_ = p.Rule()
	p.SetRule(p.Rule()) // 触发落盘
	// 落盘的 JSON 里也不应出现 token_quota / ttl_days
	raw, err := os.ReadFile(filepath.Join(dir, "claim_policy.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, banned := range []string{"token_quota", "ttl_days"} {
		if strings.Contains(s, banned) {
			t.Fatalf("claim_policy.json 不应存 %s 副本: %s", banned, s)
		}
	}
}

// 公开规则接口不得下发额度数值（额度属内部配置）。
func TestPublicRulesDoNotExposeQuotaNumber(t *testing.T) {
	cfg := DefaultConfig()
	cfg.TokenQuota = 12345678
	cfg.TTLDays = 0
	app := &App{Cfg: cfg, Claims: NewClaimPolicy(t.TempDir(), ClaimRule{OneKeyPerIP: true})}
	rec := httptest.NewRecorder()
	app.handleClaimRules(rec, httptest.NewRequest(http.MethodGet, "/api/claim-rules", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "12345678") {
		t.Fatalf("公开规则不应下发额度数值: %s", body)
	}
	if strings.Contains(body, "token_quota") {
		t.Fatalf("公开规则不应包含 token_quota 字段: %s", body)
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["never_expires"] != true {
		t.Fatalf("ttl=0 应告知长期有效: %v", out)
	}
}
