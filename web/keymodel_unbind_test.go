package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// 2026-10-28 策略变更：一个 Key 可用全部白名单模型，且跨上游同样适用。
// 本文件钉死这条语义，防止后续把「Key 绑定单模型」的旧行为改回来。

// 同一 Key 依次请求多个不同上游前缀的白名单模型，全部应放行并各自路由正确。
func TestOneKeyUsesAllWhitelistedModelsAcrossUpstreams(t *testing.T) {
	app, mainUp, backupUp, cleanup := newMultiUpstreamApp(t, true)
	defer cleanup()

	// 白名单横跨两个上游
	app.Cfg.AllowedModels = []string{"glm-4", "backup/glm-4"}

	// 领取时不再指定模型（新前端行为）
	k := mustCreateKey(t, app, "")
	if k.Model != "" {
		t.Fatalf("新 Key 不应绑定模型, got %q", k.Model)
	}

	cases := []struct {
		model      string
		wantUp     *recordingUpstream
		wantModel  string
		wantStatus int
	}{
		{"glm-4", mainUp, "glm-4", http.StatusOK},
		{"backup/glm-4", backupUp, "glm-4", http.StatusOK},
		{"glm-4", mainUp, "glm-4", http.StatusOK}, // 回到默认上游仍可用
	}
	for i, c := range cases {
		before := map[string]int{}
		for name, up := range map[string]*recordingUpstream{"main": mainUp, "backup": backupUp} {
			h, _, _, _ := up.snapshot()
			before[name] = h
		}
		rec := callV1(t, app, k.Key, "/v1/chat/completions",
			`{"model":"`+c.model+`","messages":[{"role":"user","content":"hi"}]}`)
		if rec.Code != c.wantStatus {
			t.Fatalf("case %d model=%q: code=%d body=%s", i, c.model, rec.Code, rec.Body.String())
		}
		// 命中的上游应 +1，另一个不变
		for name, up := range map[string]*recordingUpstream{"main": mainUp, "backup": backupUp} {
			h, _, _, _ := up.snapshot()
			want := before[name]
			if up == c.wantUp {
				want++
			}
			if h != want {
				t.Fatalf("case %d model=%q: 上游 %s 命中 %d, want %d", i, c.model, name, h, want)
			}
		}
		// 上游收到的 model 应是剥离前缀后的
		_, body, _, _ := c.wantUp.snapshot()
		var got struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		if got.Model != c.wantModel {
			t.Fatalf("case %d: 上游收到 model=%q, want %q", i, got.Model, c.wantModel)
		}
	}
}

// 一个 Key 请求白名单外的模型仍应 403（放开的是「全部白名单」，不是「全部模型」）。
func TestOneKeyStillRejectsNonWhitelistedModel(t *testing.T) {
	app, _, _, cleanup := newMultiUpstreamApp(t, true)
	defer cleanup()
	app.Cfg.AllowedModels = []string{"glm-4"}

	k := mustCreateKey(t, app, "")
	for _, model := range []string{"big-pickle", "backup/not-in-list", "other/gpt-4o"} {
		rec := callV1(t, app, k.Key, "/v1/chat/completions", `{"model":"`+model+`","messages":[]}`)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("model=%q: code=%d, want 403", model, rec.Code)
		}
	}
}

// /v1/models 应返回全部白名单（不再是单个绑定模型）。
func TestOneKeyV1ModelsReturnsFullWhitelist(t *testing.T) {
	app, _, _, cleanup := newMultiUpstreamApp(t, true)
	defer cleanup()
	app.Cfg.AllowedModels = []string{"glm-4", "backup/glm-4"}

	k := mustCreateKey(t, app, "")
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+k.Key)
	rec := httptest.NewRecorder()
	app.HandleV1(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, d := range out.Data {
		if id, ok := d["id"].(string); ok {
			ids[id] = true
		}
	}
	if len(ids) != 2 || !ids["glm-4"] || !ids["backup/glm-4"] {
		t.Fatalf("/v1/models = %v, want 全部白名单", ids)
	}
}

// 白名单为空时不允许领取（fail-closed），避免发出去一个什么都用不了的 Key。
func TestCreateKeyFailsClosedWhenWhitelistEmpty(t *testing.T) {
	app, _, _, cleanup := newMultiUpstreamApp(t, true)
	defer cleanup()
	app.Cfg.AllowedModels = nil

	req := httptest.NewRequest(http.MethodPost, "/api/keys", bytes.NewBufferString(`{}`))
	rec := httptest.NewRecorder()
	app.HandleCreateKey(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d, want 503", rec.Code)
	}
}

// 存量 Key（Model 有值）在启动迁移后应可用全部白名单模型。
func TestUnbindKeyModelsMigratesLegacyKeys(t *testing.T) {
	app, mainUp, backupUp, cleanup := newMultiUpstreamApp(t, true)
	defer cleanup()
	app.Cfg.AllowedModels = []string{"glm-4", "backup/glm-4"}

	// 模拟存量 Key：绑定在 glm-4 上
	legacy := mustCreateKey(t, app, "glm-4")
	if legacy.Model != "glm-4" {
		t.Fatal("前置条件不满足")
	}
	// 迁移前：只能用 glm-4
	rec := callV1(t, app, legacy.Key, "/v1/chat/completions", `{"model":"backup/glm-4","messages":[]}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("迁移前应 403, got %d", rec.Code)
	}

	// 执行迁移
	n, err := app.Store.UnbindKeyModels()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("应解除 1 个 Key, got %d", n)
	}

	// 迁移后：两个上游的模型都能用
	for _, model := range []string{"glm-4", "backup/glm-4"} {
		rec := callV1(t, app, legacy.Key, "/v1/chat/completions", `{"model":"`+model+`","messages":[]}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("迁移后 model=%q 应 200, got %d body=%s", model, rec.Code, rec.Body.String())
		}
	}
	_ = mainUp
	_ = backupUp

	// 幂等：再次调用不应有改动
	if n2, err := app.Store.UnbindKeyModels(); err != nil || n2 != 0 {
		t.Fatalf("二次迁移应无改动: n=%d err=%v", n2, err)
	}
}

// UnbindKeyModels 不得改动额度、有效期、IP、禁用状态与用量。
func TestUnbindKeyModelsPreservesOtherFields(t *testing.T) {
	app, _, _, cleanup := newMultiUpstreamApp(t, true)
	defer cleanup()

	now := time.Now()
	k, err := app.Store.CreateKey("glm-4", 7, 12345, now)
	if err != nil {
		t.Fatal(err)
	}
	app.Store.ConsumeAt(k.ID, 100, now)
	if err := app.Store.SetDisabled(k.ID, false); err != nil {
		t.Fatal(err)
	}
	before, _ := app.Store.Snapshot(k.ID)

	if _, err := app.Store.UnbindKeyModels(); err != nil {
		t.Fatal(err)
	}
	after, _ := app.Store.Snapshot(k.ID)

	if after.Model != "" {
		t.Fatalf("Model 应被清空, got %q", after.Model)
	}
	if after.Quota != before.Quota || after.TokenUsed != before.TokenUsed {
		t.Fatalf("额度/用量被改动: before=%+v after=%+v", before, after)
	}
	if !after.ExpiresAt.Equal(before.ExpiresAt) {
		t.Fatalf("有效期被改动: %v → %v", before.ExpiresAt, after.ExpiresAt)
	}
	if after.Disabled != before.Disabled {
		t.Fatal("禁用状态被改动")
	}
	if after.IP != before.IP {
		t.Fatalf("IP 被改动: %q → %q", before.IP, after.IP)
	}
}

// 管理后台 Key 列表应标出 all_models（前端据此显示「全部模型」）。
func TestAdminKeyListMarksAllModels(t *testing.T) {
	app, srv := newAdminApp(t)
	defer srv.Close()
	tok := adminToken(t, srv)

	// 一个未绑定（新语义）与一个仍绑定（模拟未迁移的历史数据）
	unbound := mustCreateKey(t, app, "")
	bound := mustCreateKey(t, app, "glm-4")

	code, body := doJSON(t, srv, tok, http.MethodGet, "/api/admin/keys", "")
	if code != http.StatusOK {
		t.Fatalf("code=%d body=%s", code, body)
	}
	var out struct {
		Keys []struct {
			ID        string `json:"id"`
			Model     string `json:"model"`
			AllModels bool   `json:"all_models"`
		} `json:"keys"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	byID := map[string]bool{}
	models := map[string]string{}
	for _, k := range out.Keys {
		byID[k.ID] = k.AllModels
		models[k.ID] = k.Model
	}
	if !byID[unbound.ID] {
		t.Fatalf("未绑定 Key 应 all_models=true: %+v", out.Keys)
	}
	if byID[bound.ID] {
		t.Fatalf("绑定 Key 应 all_models=false: %+v", out.Keys)
	}
	if models[bound.ID] != "glm-4" {
		t.Fatalf("绑定 Key 的 model 应保留: %q", models[bound.ID])
	}
}
