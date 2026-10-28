package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

/* ------------------------------------------------------------------ */
/* 一、IP 策略上界 + 旧脏数据钳制（finding 565 / fact 591）             */
/* ------------------------------------------------------------------ */

// 超大的 window_minutes 必须被 SetDefaults 拒绝；它旧版本会导致
// reset.go alignToGrid 溢出 → 每请求 panic → nginx 502。
func TestIPSDefaultsRejectsHugeWindowMinutes(t *testing.T) {
	dir := t.TempDir()
	s, err := NewIPPolicyStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	bad := DefaultIPPolicy()
	bad.WindowMinutes = 9007199254740992
	if err := s.SetDefaults(bad); err == nil {
		t.Fatal("超大 window_minutes 应被拒绝")
	}
	if got := s.Defaults().WindowMinutes; got != DefaultIPPolicy().WindowMinutes {
		t.Fatalf("被拒绝后默认值不应变化，got=%d", got)
	}
}

// 四个字段都要有上界。
func TestIPSDefaultsRejectsAllOverLimitFields(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewIPPolicyStore(dir)
	cases := map[string]func(d *IPDefaults){
		"window_minutes":     func(d *IPDefaults) { d.WindowMinutes = MaxWindowMinutes + 1 },
		"window_tokens":      func(d *IPDefaults) { d.WindowTokens = MaxWindowTokens + 1 },
		"max_active_minutes": func(d *IPDefaults) { d.MaxActiveMinutes = MaxActiveMinutes + 1 },
		"output_tps":         func(d *IPDefaults) { d.OutputTPS = MaxOutputTPS + 1 },
	}
	for name, mutate := range cases {
		d := DefaultIPPolicy()
		mutate(&d)
		if err := s.SetDefaults(d); err == nil {
			t.Fatalf("%s 超上界应被拒绝", name)
		}
	}
	// 边界值本身合法。
	ok := IPDefaults{WindowTokens: MaxWindowTokens, WindowMinutes: MaxWindowMinutes,
		MaxActiveMinutes: MaxActiveMinutes, OutputTPS: MaxOutputTPS}
	if err := s.SetDefaults(ok); err != nil {
		t.Fatalf("边界值应被接受: %v", err)
	}
}

// SetOverride 同样要有上界。
func TestIPOverrideRejectsHugeValues(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewIPPolicyStore(dir)
	big := 9007199254740992
	if err := s.SetOverride("1.2.3.4", IPOverride{WindowMinutes: &big}); err == nil {
		t.Fatal("override 超大 window_minutes 应被拒绝")
	}
	if _, ok := s.Override("1.2.3.4"); ok {
		t.Fatal("被拒绝的 override 不应落库")
	}
}

// 磁盘上已有的越界值必须在载入时钳制回默认（否则旧脏数据持续 502）。
func TestIPPolicyStoreClampsDirtyFileOnLoad(t *testing.T) {
	dir := t.TempDir()
	dirty := `{"defaults":{"window_tokens":0,"window_minutes":9007199254740992,"max_active_minutes":120,"output_tps":60},
"overrides":[{"ip":"1.2.3.4","window_minutes":9007199254740992,"output_tps":30},
{"ip":"not-an-ip","window_tokens":5}]}`
	if err := os.WriteFile(filepath.Join(dir, "ip_policies.json"), []byte(dirty), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := NewIPPolicyStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Defaults().WindowMinutes; got != DefaultIPPolicy().WindowMinutes {
		t.Fatalf("脏默认值应被钳制回默认，got=%d", got)
	}
	eff := s.Effective("1.2.3.4")
	if eff.WindowMinutes != DefaultIPPolicy().WindowMinutes {
		t.Fatalf("越界 override 字段应置 nil 继承默认，got=%d", eff.WindowMinutes)
	}
	if eff.OutputTPS != 30 {
		t.Fatalf("合法 override 字段应保留，got=%d", eff.OutputTPS)
	}
	if _, ok := s.Override("not-an-ip"); ok {
		t.Fatal("非法 IP 的 override 应被丢弃")
	}
	// 迁移结果应已回写，磁盘上不再有越界值。
	raw, _ := os.ReadFile(filepath.Join(dir, "ip_policies.json"))
	if strings.Contains(string(raw), "9007199254740992") || strings.Contains(string(raw), "not-an-ip") {
		t.Fatalf("脏数据未回写清理: %s", raw)
	}
}

// alignToGrid 在超大窗口下也不能 panic（配置层被绕过的最后兜底）。
func TestAlignToGridHandlesHugeWindowWithoutPanic(t *testing.T) {
	for _, m := range []int{9007199254740992, 1 << 40, MaxWindowMinutes + 1} {
		start, end := alignToGrid(time.Now(), m)
		if start.IsZero() || end.IsZero() {
			t.Fatalf("window=%d 应返回有效区间", m)
		}
		if !end.After(start) {
			t.Fatalf("window=%d 区间非法: %v ~ %v", m, start, end)
		}
	}
}

// 端到端：PUT defaults 传超大值必须 400，且服务仍能正常出窗口（不 panic）。
func TestAdminPutIPDefaultsRejectsHugeWindowMinutes(t *testing.T) {
	app, srv := newIPAdminServer(t)
	defer srv.Close()
	tok := adminToken(t, srv)

	code, out := ipAdminRequest(t, srv, tok, http.MethodPut, "/api/admin/ip-policies/defaults",
		map[string]any{"window_minutes": 9007199254740992})
	if code != http.StatusBadRequest {
		t.Fatalf("超大 window_minutes 应 400，实际 %d: %v", code, out)
	}
	if got := app.IPs.Policies().Defaults().WindowMinutes; got != DefaultIPPolicy().WindowMinutes {
		t.Fatalf("被拒后默认值不应改变: %d", got)
	}
	// 后续 Snapshot 不应 panic。
	_ = app.IPs.Snapshot(time.Now())
}

// 部分 PUT defaults 未提交的字段必须保持原值（旧版会把它们清零 = 静默放开限速）。
func TestAdminPutIPDefaultsIsMergeNotReplace(t *testing.T) {
	app, srv := newIPAdminServer(t)
	defer srv.Close()
	tok := adminToken(t, srv)

	code, _ := ipAdminRequest(t, srv, tok, http.MethodPut, "/api/admin/ip-policies/defaults",
		IPDefaults{WindowTokens: 500000, WindowMinutes: 120, MaxActiveMinutes: 90, OutputTPS: 40})
	if code != http.StatusOK {
		t.Fatalf("初始 PUT code=%d", code)
	}
	code, _ = ipAdminRequest(t, srv, tok, http.MethodPut, "/api/admin/ip-policies/defaults",
		map[string]any{"output_tps": 55})
	if code != http.StatusOK {
		t.Fatalf("部分 PUT code=%d", code)
	}
	d := app.IPs.Policies().Defaults()
	if d.OutputTPS != 55 {
		t.Fatalf("提交字段应生效: %d", d.OutputTPS)
	}
	if d.WindowTokens != 500000 || d.WindowMinutes != 120 || d.MaxActiveMinutes != 90 {
		t.Fatalf("未提交字段被清零了: %+v", d)
	}
}

// 非法 IP 路径参数必须 400，且不能写入 overrides。
func TestAdminIPPolicyRejectsInvalidIPPath(t *testing.T) {
	app, srv := newIPAdminServer(t)
	defer srv.Close()
	tok := adminToken(t, srv)

	for _, bad := range []string{"not-an-ip", "999.999.999.999", "192.168.12.300"} {
		code, out := ipAdminRequest(t, srv, tok, http.MethodPut, "/api/admin/ip-policies/"+bad,
			map[string]any{"output_tps": 10})
		if code != http.StatusBadRequest {
			t.Fatalf("PUT %s 应 400，实际 %d: %v", bad, code, out)
		}
		if _, ok := app.IPs.Policies().Override(bad); ok {
			t.Fatalf("非法 IP %s 不应写入 overrides", bad)
		}
	}
}

/* ------------------------------------------------------------------ */
/* 二、claim policy 空列表 fail-closed + 严格解析（finding 566）        */
/* ------------------------------------------------------------------ */

// 空列表在重启后仍须保持「全拒绝」（旧 load 会把它当未配置、回落到默认白名单）。
func TestClaimPolicyEmptyListSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	p := NewClaimPolicy(dir, ClaimRule{AllowCIDRs: DefaultAllowedCIDR(), OneKeyPerIP: true})
	p.SetRule(ClaimRule{AllowCIDRs: nil, OneKeyPerIP: true})
	if p.IPAllowed("192.168.12.17") {
		t.Fatal("清空后应立即拒绝")
	}
	p2 := NewClaimPolicy(dir, ClaimRule{AllowCIDRs: DefaultAllowedCIDR(), OneKeyPerIP: true})
	if p2.IPAllowed("192.168.12.17") {
		t.Fatal("重启后显式清空的白名单不应被启动默认值覆盖（fail-closed 持久化）")
	}
}

/* ------------------------------------------------------------------ */
/* 三、settings 部分更新 + token_quota 上界（finding 564 / fact 591）   */
/* ------------------------------------------------------------------ */

// 只 PUT token_quota 时，其余字段（尤其 rate_limit_per_min / ttl_days）
// 必须保持原值；旧实现会把它们清零（限速被关掉、Key 变永不过期）。
func TestAdminPutSettingsPartialUpdateKeepsOthers(t *testing.T) {
	_, srv := newAdminApp(t)
	defer srv.Close()
	tok := adminToken(t, srv)

	// 先落一组合法配置（上游已拆到 /api/admin/upstreams，这里只设其余字段）。
	body := `{"ttl_days":7,"token_quota":500000,"rate_limit_per_min":17}`
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/admin/settings", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("初始化 settings code=%d", resp.StatusCode)
	}

	// 只提交 token_quota。
	req2, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/admin/settings",
		bytes.NewBufferString(`{"token_quota":600000}`))
	req2.Header.Set("Authorization", "Bearer "+tok)
	resp2, err := srv.Client().Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("部分更新 code=%d", resp2.StatusCode)
	}

	var got struct {
		RateLimitPerMin int `json:"rate_limit_per_min"`
		TTLDays         int `json:"ttl_days"`
		TokenQuota      int `json:"token_quota"`
	}
	req3, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/admin/settings", nil)
	req3.Header.Set("Authorization", "Bearer "+tok)
	resp3, err := srv.Client().Do(req3)
	if err != nil {
		t.Fatal(err)
	}
	defer resp3.Body.Close()
	if err := json.NewDecoder(resp3.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.RateLimitPerMin != 17 {
		t.Fatalf("rate_limit_per_min 被清零了: %d", got.RateLimitPerMin)
	}
	if got.TTLDays != 7 {
		t.Fatalf("ttl_days 被清零了: %d", got.TTLDays)
	}
	if got.TokenQuota != 600000 {
		t.Fatalf("提交的 token_quota 未生效: %d", got.TokenQuota)
	}
}

// token_quota 必须有上界（旧实现允许 9223372036854775807 并下发到新 Key）。
func TestAdminPutSettingsRejectsHugeTokenQuota(t *testing.T) {
	app, srv := newAdminApp(t)
	defer srv.Close()
	tok := adminToken(t, srv)
	before := app.Cfg.TokenQuota

	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/admin/settings",
		bytes.NewBufferString(`{"token_quota":1000000000001}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("超上界 token_quota 应 400，实际 %d", resp.StatusCode)
	}
	if app.Cfg.TokenQuota != before {
		t.Fatal("被拒后 token_quota 不应变化")
	}
}

// 不得把 token_quota 下调到低于存量 Key 的额度（一次误操作会砍掉所有在用 Key）。
// 方案 A：额度变更对存量 Key 立即生效 —— 下调本就是高危操作，必须先用
// POST /api/admin/keys/{id}/quota 单独处理存量 Key 或完成启动期钳制，否则 400。
func TestAdminPutSettingsAllowsQuotaBelowIssued(t *testing.T) {
	app, srv := newAdminApp(t)
	defer srv.Close()
	tok := adminToken(t, srv)

	if _, err := app.Store.CreateKey("glm-4", 7, 900000, time.Now()); err != nil {
		t.Fatal(err)
	}
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
