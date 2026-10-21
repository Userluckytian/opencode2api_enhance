package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

/* ---------- 口令强度策略单元测试 ---------- */

// 4 位口令（现网口令 0099 就是 4 位）必须被拒；8 位以上、含 3 类字符的必须通过。
func TestValidateAdminPasswordMinLength(t *testing.T) {
	cases := []struct {
		name    string
		pw      string
		wantErr bool
	}{
		{"现网 4 位纯数字口令", "0099", true},
		{"4 位字母", "abcd", true},
		{"7 位", "Ab1!efg", true},
		{"空口令", "", true},
		{"8 位含 3 类", "Ab1!efg7", false},
		{"12 位含 3 类", "Abcdef1!ghij", false},
	}
	for _, c := range cases {
		err := ValidateAdminPassword(c.pw, CurrentPassword{})
		if c.wantErr && err == nil {
			t.Fatalf("%s: 期望被拒，实际通过", c.name)
		}
		if !c.wantErr && err != nil {
			t.Fatalf("%s: 期望通过，实际被拒: %v", c.name, err)
		}
	}
}

// 常见弱口令（全数字/纯字母/纯重复字符/字典口令）必须被拒。
func TestValidateAdminPasswordRejectsWeak(t *testing.T) {
	for _, pw := range []string{
		"12345678",    // 纯数字
		"abcdefgh",    // 纯字母
		"aaaaaaaa",    // 同一字符重复
		"Password1",   // 字典口令
		"Admin@123",   // 字典口令
		"Passw0rd!",   // 字典口令
		"Qwerty123!",  // 字典口令
		"Changeme1!",  // 字典口令
		"Letmein123",  // 字典口令
		"tokenbuffet", // 字典口令
		"Ab1!",        // 太短
		"ab1!efg",     // 只有 3 类字符
	} {
		if err := ValidateAdminPassword(pw, CurrentPassword{}); err == nil {
			t.Fatalf("弱口令 %q 应当被拒", pw)
		}
	}
}

// 与当前口令完全相同必须被拒——用哈希校验，不依赖明文（进程重启后依然有效）。
func TestValidateAdminPasswordRejectsSameAsCurrent(t *testing.T) {
	cur := "Adm1n!Passw0rd"
	hash := HashPassword(cur)
	cp := CurrentPassword{Verify: func(candidate string) bool { return VerifyPassword(hash, candidate) }}
	if err := ValidateAdminPassword(cur, cp); err == nil {
		t.Fatal("与当前口令相同必须被拒")
	}
	// 真正不同的强口令应通过。
	if err := ValidateAdminPassword("Zx9!Kw3mLp7Q", cp); err != nil {
		t.Fatalf("不同的强口令应通过: %v", err)
	}
	// 大小写变体：有明文基准时被 passwordSimilar 拒；无明文基准时属降级放过。
	cpWithPlain := CurrentPassword{
		Plain:  cur,
		Verify: func(candidate string) bool { return VerifyPassword(hash, candidate) },
	}
	if err := ValidateAdminPassword(strings.ToUpper(cur), cpWithPlain); err == nil {
		t.Fatal("与当前口令相同（仅大小写不同）必须有明文基准时必须被拒")
	}
}

// 与当前口令高度相似（只差一两个字符 / 前缀追加）必须被拒。
func TestValidateAdminPasswordRejectsSimilarToCurrent(t *testing.T) {
	cur := "Adm1n!Passw0rd"
	cp := CurrentPassword{Plain: cur}
	for _, pw := range []string{
		"Adm1n!Passw0rX",  // 改了一个字符
		"Adm1n!Passw0rdX", // 末尾追加
		"XAdm1n!Passw0rd", // 开头追加
	} {
		if err := ValidateAdminPassword(pw, cp); err == nil {
			t.Fatalf("与当前口令相似的 %q 应当被拒", pw)
		}
	}
}

// 没有明文基准时（进程重启后），相似性检查降级但相同性检查仍有效。
func TestValidateAdminPasswordDegradesWithoutPlain(t *testing.T) {
	cur := "Adm1n!Passw0rd"
	hash := HashPassword(cur)
	cp := CurrentPassword{Verify: func(candidate string) bool { return VerifyPassword(hash, candidate) }}
	if err := ValidateAdminPassword(cur, cp); err == nil {
		t.Fatal("无明文基准时，相同口令仍必须被拒")
	}
	// 无明文基准 + 相似口令 → 放行（降级语义：宁可放过，不可误杀）
	if err := ValidateAdminPassword("Adm1n!Passw0rX", cp); err != nil {
		t.Fatalf("无明文基准时不应因相似性被拒: %v", err)
	}
}

// 首次启动生成的初始口令（web/main.go:87 「admin-」+ 随机 16 字节）必须通过强度策略。
func TestBootstrapAdminPasswordPassesPolicy(t *testing.T) {
	for i := 0; i < 20; i++ {
		pw := "admin-" + GenerateKey()[3:]
		if err := ValidateAdminPassword(pw, CurrentPassword{}); err != nil {
			t.Fatalf("首次启动初始口令未通过强度策略: %v", err)
		}
	}
}

/* ---------- HTTP 端到端：PUT /api/admin/password ---------- */

func newPasswordApp(t *testing.T) (*App, *httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.AdminPasswordHash = HashPassword("Adm1n!Passw0rd")
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{
		Cfg:        cfg,
		Store:      store,
		Rl:         NewRateLimiter(cfg.RateLimitPerMin),
		Sessions:   NewSessionStore(),
		Logger:     NewLogger(dir),
		Client:     http.DefaultClient,
		ConfigPath: dir,
		Audit:      NewAuditLog(dir),
		Login:      NewLoginLimiter(LoginMaxAttempts, LoginWindow, LoginLockDuration),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/admin/login", app.handleLogin)
	mux.Handle("PUT /api/admin/password", app.adminAuth(http.HandlerFunc(app.handlePutPassword)))
	srv := httptest.NewServer(mux)
	// 登录取得会话 token
	resp, err := srv.Client().Post(srv.URL+"/api/admin/login", "application/json",
		bytes.NewBufferString(`{"password":"Adm1n!Passw0rd"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Token == "" {
		t.Fatal("未取得 token")
	}
	return app, srv, body.Token
}

func putPassword(t *testing.T, srv *httptest.Server, tok, pw string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/admin/password",
		bytes.NewBufferString(`{"password":"`+pw+`"}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

// 4 位口令必须被拒（400），且**不落盘**（配置里的哈希保持不变），且审计有记录。
func TestPutPasswordRejectsShortAndDoesNotPersist(t *testing.T) {
	app, srv, tok := newPasswordApp(t)
	defer srv.Close()

	before := app.Cfg.AdminPasswordHash
	code, body := putPassword(t, srv, tok, "0099")
	if code != http.StatusBadRequest {
		t.Fatalf("4 位口令应返回 400，实际 %d body=%s", code, body)
	}
	if !strings.Contains(body, "password rejected") {
		t.Fatalf("响应体应说明被拒原因: %s", body)
	}
	// 不落盘：哈希未变，且旧口令仍能登录。
	if app.Cfg.AdminPasswordHash != before {
		t.Fatal("被拒的口令不得写入配置")
	}
	if !VerifyPassword(app.Cfg.AdminPasswordHash, "Adm1n!Passw0rd") {
		t.Fatal("被拒后原口令必须仍然有效")
	}
	// 审计有记录（denied）。
	entries := app.Audit.Read(time.Now().Format("2006-01-02"))
	found := false
	for _, e := range entries {
		if e.Action == "update_password" && e.Result == "denied" {
			found = true
		}
	}
	if !found {
		t.Fatalf("审计里应有 update_password/denied 记录，实际: %+v", entries)
	}
	// 磁盘上也不能出现被拒口令的痕迹（配置里只有哈希）。
	raw, err := os.ReadFile(filepath.Join(app.ConfigPath, "config.json"))
	if err == nil && strings.Contains(string(raw), "0099") {
		t.Fatal("被拒口令不得落盘到 config.json")
	}
}

// 8 位以上的强口令必须通过，且新口令生效、旧口令失效。
func TestPutPasswordAcceptsStrongPassword(t *testing.T) {
	app, srv, tok := newPasswordApp(t)
	defer srv.Close()

	code, body := putPassword(t, srv, tok, "Zx9!Kw3mLp7Q")
	if code != http.StatusOK {
		t.Fatalf("强口令应返回 200，实际 %d body=%s", code, body)
	}
	if !VerifyPassword(app.Cfg.AdminPasswordHash, "Zx9!Kw3mLp7Q") {
		t.Fatal("新口令应已生效")
	}
	if VerifyPassword(app.Cfg.AdminPasswordHash, "Adm1n!Passw0rd") {
		t.Fatal("旧口令应已失效")
	}
	// 审计有 ok 记录。
	entries := app.Audit.Read(time.Now().Format("2006-01-02"))
	ok := false
	for _, e := range entries {
		if e.Action == "update_password" && e.Result == "ok" {
			ok = true
		}
	}
	if !ok {
		t.Fatalf("审计里应有 update_password/ok 记录，实际: %+v", entries)
	}
}

// 与当前口令相同必须被拒，且不落盘。
func TestPutPasswordRejectsSameAsCurrent(t *testing.T) {
	app, srv, tok := newPasswordApp(t)
	defer srv.Close()

	before := app.Cfg.AdminPasswordHash
	code, body := putPassword(t, srv, tok, "Adm1n!Passw0rd")
	if code != http.StatusBadRequest {
		t.Fatalf("与当前口令相同应返回 400，实际 %d body=%s", code, body)
	}
	if app.Cfg.AdminPasswordHash != before {
		t.Fatal("被拒的口令不得写入配置")
	}
}

// 登录限流阈值收紧到 5 次/10 分钟后，并发齐射也只能拿到 5 次真实校验机会。
// （finding 544 的原子记账语义必须保持：判定与计数在同一把锁内。）
func TestLoginLimiterConcurrentAttemptsAreCapped(t *testing.T) {
	l := NewLoginLimiter(LoginMaxAttempts, LoginWindow, LoginLockDuration)
	now := time.Now()
	allowed := 0
	for i := 0; i < 30; i++ {
		if ok, _ := l.TryAttempt("1.2.3.4", now); ok {
			allowed++
		}
	}
	if allowed > LoginMaxAttempts {
		t.Fatalf("并发 30 次齐射放行了 %d 次，期望 ≤ %d", allowed, LoginMaxAttempts)
	}
	if allowed < 1 {
		t.Fatal("至少应放行第 1 次尝试")
	}
}
