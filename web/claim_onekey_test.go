package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 领取策略开关 ClaimRule.OneKeyPerIP 的行为验证。
//
// 历史缺陷：该开关此前只在管理 API 与页面里存在，领取逻辑从未读取，
// 无论怎么改都恒为「一 IP 一 Key」。这里钉死两个方向。

func newOneKeyApp(t *testing.T, oneKeyPerIP bool) *App {
	t.Helper()
	app := newTestApp(t)
	app.Claims = NewClaimPolicy(t.TempDir(), ClaimRule{
		AllowCIDRs:  []string{"0.0.0.0/0", "::/0"}, // 测试来源为 192.0.2.1
		OneKeyPerIP: oneKeyPerIP,
	})
	return app
}

func claimOnceForTest(t *testing.T, app *App) (string, bool) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/keys", bytes.NewBufferString(`{}`))
	req.RemoteAddr = "192.0.2.1:1234"
	rec := httptest.NewRecorder()
	app.HandleCreateKey(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Key   string `json:"key"`
		IsNew bool   `json:"is_new"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Key == "" {
		t.Fatal("key 为空")
	}
	return out.Key, out.IsNew
}

// 开关为 true：同一 IP 重复领取拿到同一个 Key。
func TestClaimOneKeyPerIPEnabled(t *testing.T) {
	app := newOneKeyApp(t, true)

	k1, isNew1 := claimOnceForTest(t, app)
	if !isNew1 {
		t.Fatal("首次领取 is_new 应为 true")
	}
	k2, isNew2 := claimOnceForTest(t, app)
	if isNew2 {
		t.Fatal("二次领取 is_new 应为 false（拿回原 Key）")
	}
	if k1 != k2 {
		t.Fatalf("同一 IP 应拿回同一个 Key: %q vs %q", k1, k2)
	}
	if n := len(app.Store.List()); n != 1 {
		t.Fatalf("应只有 1 个 Key, got %d", n)
	}
}

// 开关为 false：同一 IP 每次领取都得到新 Key。
func TestClaimOneKeyPerIPDisabled(t *testing.T) {
	app := newOneKeyApp(t, false)

	k1, isNew1 := claimOnceForTest(t, app)
	k2, isNew2 := claimOnceForTest(t, app)
	if !isNew1 || !isNew2 {
		t.Fatalf("每次领取都应 is_new=true, got %v %v", isNew1, isNew2)
	}
	if k1 == k2 {
		t.Fatalf("关闭开关后应生成不同 Key, 两次都是 %q", k1)
	}
	if n := len(app.Store.List()); n != 2 {
		t.Fatalf("应有 2 个 Key, got %d", n)
	}
	// 两个 Key 都能正常鉴权
	for _, kk := range []string{k1, k2} {
		if _, ok := app.Store.SnapshotByKey(kk); !ok {
			t.Fatalf("Key %q 未入库", kk)
		}
	}
}

// 切换开关后行为随之改变（不需要重启）：先关闭拿两个，再开启拿回同一个。
func TestClaimPolicyToggleTakesEffect(t *testing.T) {
	app := newOneKeyApp(t, false)
	k1, _ := claimOnceForTest(t, app)
	k2, _ := claimOnceForTest(t, app)
	if k1 == k2 {
		t.Fatal("关闭时应拿到不同 Key")
	}

	// 运行时切到 true：同一 IP 会拿回「已存在的那一个」
	app.Claims.SetRule(ClaimRule{
		AllowCIDRs:  []string{"0.0.0.0/0", "::/0"},
		OneKeyPerIP: true,
	})
	k3, isNew3 := claimOnceForTest(t, app)
	if isNew3 {
		t.Fatal("开启后不应再新建 Key")
	}
	if k3 != k1 && k3 != k2 {
		t.Fatalf("开启后应返回已有 Key 之一, got %q", k3)
	}
	if n := len(app.Store.List()); n != 2 {
		t.Fatalf("不应新增 Key, got %d", n)
	}
}

// 未配置 Claims（nil）时默认按「一 IP 一 Key」处理，保持旧行为。
func TestClaimDefaultsToOneKeyPerIPWithoutPolicy(t *testing.T) {
	app := newTestApp(t) // 无 Claims
	k1, _ := claimOnceForTest(t, app)
	k2, _ := claimOnceForTest(t, app)
	if k1 != k2 {
		t.Fatalf("无策略时应退化为幂等领取: %q vs %q", k1, k2)
	}
}
