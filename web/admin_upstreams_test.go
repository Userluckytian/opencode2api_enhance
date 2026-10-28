package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func doJSON(t *testing.T, srv *httptest.Server, tok, method, path, body string) (int, string) {
	t.Helper()
	var rdr *bytes.Buffer
	if body == "" {
		rdr = bytes.NewBufferString("")
	} else {
		rdr = bytes.NewBufferString(body)
	}
	req, _ := http.NewRequest(method, srv.URL+path, rdr)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out bytes.Buffer
	_, _ = out.ReadFrom(resp.Body)
	return resp.StatusCode, out.String()
}

// 新增上游：出现在列表、密钥脱敏、自动成为默认（首个启用项）。
func TestAdminCreateUpstream(t *testing.T) {
	app, srv := newAdminApp(t)
	defer srv.Close()
	tok := adminToken(t, srv)

	code, body := doJSON(t, srv, tok, http.MethodPost, "/api/admin/upstreams",
		`{"id":"up1","name":"上游一","base_url":"http://1.2.3.4:18080/v1","api_key":"sk-abcdef1234","enabled":true}`)
	if code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", code, body)
	}
	u, ok := FindUpstream(app.Cfg.Upstreams, "up1")
	if !ok {
		t.Fatalf("上游未写入: %+v", app.Cfg.Upstreams)
	}
	if u.Name != "上游一" || u.BaseURL != "http://1.2.3.4:18080/v1" || u.APIKey != "sk-abcdef1234" {
		t.Fatalf("字段不正确: %+v", u)
	}
	// newAdminApp 的默认配置带旧单上游字段，实体化后它成为 default 并保持默认身份；
	// 新加的上游不应抢走默认位（存量行为不能被一次「新增」改变）。
	if app.Cfg.DefaultUpstreamID != LegacyUpstreamID {
		t.Fatalf("默认上游不应被新增操作改变, got %q", app.Cfg.DefaultUpstreamID)
	}

	code, body = doJSON(t, srv, tok, http.MethodGet, "/api/admin/upstreams", "")
	if code != http.StatusOK {
		t.Fatalf("list code = %d", code)
	}
	if !bytes.Contains([]byte(body), []byte("****1234")) {
		t.Fatalf("密钥未脱敏: %s", body)
	}
	if bytes.Contains([]byte(body), []byte("sk-abcdef1234")) {
		t.Fatalf("泄露明文密钥: %s", body)
	}
}

// clearLegacyUpstream 清掉 newAdminApp 默认配置里的旧单上游字段，
// 让用例从「无上游」的干净状态开始（否则会被实体化成 default 条目）。
func clearLegacyUpstream(app *App) {
	app.Mu.Lock()
	app.Cfg.UpstreamBaseURL = ""
	app.Cfg.UpstreamAPIKey = ""
	app.Cfg.Upstreams = nil
	app.Cfg.DefaultUpstreamID = ""
	app.Mu.Unlock()
}

// ID 重复 → 409；非法 ID → 400。
func TestAdminCreateUpstreamValidation(t *testing.T) {
	app, srv := newAdminApp(t)
	defer srv.Close()
	tok := adminToken(t, srv)
	clearLegacyUpstream(app)

	if code, _ := doJSON(t, srv, tok, http.MethodPost, "/api/admin/upstreams",
		`{"id":"dup","base_url":"http://1.2.3.4/v1","enabled":true}`); code != http.StatusOK {
		t.Fatalf("首次创建失败: %d", code)
	}
	if code, _ := doJSON(t, srv, tok, http.MethodPost, "/api/admin/upstreams",
		`{"id":"dup","base_url":"http://5.6.7.8/v1","enabled":true}`); code != http.StatusConflict {
		t.Fatalf("重复 ID 应 409, got %d", code)
	}
	for _, bad := range []string{"", "a/b", "中文", "-x"} {
		body := `{"id":"` + bad + `","base_url":"http://1.2.3.4/v1"}`
		if code, _ := doJSON(t, srv, tok, http.MethodPost, "/api/admin/upstreams", body); code != http.StatusBadRequest {
			t.Fatalf("非法 ID %q 应 400, got %d", bad, code)
		}
	}
	if len(app.Cfg.Upstreams) != 1 {
		t.Fatalf("非法请求不应写入: %+v", app.Cfg.Upstreams)
	}
}

// 删除默认上游 → 400；删除最后一个启用上游 → 400。
func TestAdminDeleteUpstreamGuards(t *testing.T) {
	app, srv := newAdminApp(t)
	defer srv.Close()
	tok := adminToken(t, srv)
	clearLegacyUpstream(app)

	doJSON(t, srv, tok, http.MethodPost, "/api/admin/upstreams",
		`{"id":"a","base_url":"http://1.2.3.4/v1","enabled":true}`)

	// a 是默认上游
	if code, body := doJSON(t, srv, tok, http.MethodDelete, "/api/admin/upstreams/a", ""); code != http.StatusBadRequest {
		t.Fatalf("删除默认上游应 400, got %d body=%s", code, body)
	}

	// 新增 b 并把默认改为 b
	doJSON(t, srv, tok, http.MethodPost, "/api/admin/upstreams",
		`{"id":"b","base_url":"http://5.6.7.8/v1","enabled":true}`)
	app.Mu.Lock()
	app.Cfg.DefaultUpstreamID = "b"
	app.Mu.Unlock()

	// 现在 a 可删
	if code, body := doJSON(t, srv, tok, http.MethodDelete, "/api/admin/upstreams/a", ""); code != http.StatusOK {
		t.Fatalf("删除非默认上游应 200, got %d body=%s", code, body)
	}
	// b 是默认且是最后一个启用 → 拒绝
	if code, _ := doJSON(t, srv, tok, http.MethodDelete, "/api/admin/upstreams/b", ""); code != http.StatusBadRequest {
		t.Fatalf("删除最后一个启用上游应 400, got %d", code)
	}
	if len(app.Cfg.Upstreams) != 1 {
		t.Fatalf("应剩 1 条: %+v", app.Cfg.Upstreams)
	}
}

// 停用最后一个启用上游 → 400；停用非最后一个 → 200 且默认上游自动改指。
func TestAdminDisableUpstreamGuards(t *testing.T) {
	app, srv := newAdminApp(t)
	defer srv.Close()
	tok := adminToken(t, srv)
	clearLegacyUpstream(app)

	doJSON(t, srv, tok, http.MethodPost, "/api/admin/upstreams",
		`{"id":"a","base_url":"http://1.2.3.4/v1","enabled":true}`)
	if code, body := doJSON(t, srv, tok, http.MethodPut, "/api/admin/upstreams/a",
		`{"enabled":false}`); code != http.StatusBadRequest {
		t.Fatalf("停用最后一个启用上游应 400, got %d body=%s", code, body)
	}

	doJSON(t, srv, tok, http.MethodPost, "/api/admin/upstreams",
		`{"id":"b","base_url":"http://5.6.7.8/v1","enabled":true}`)
	// 停用 a（默认）→ 应成功且默认改指 b
	if code, body := doJSON(t, srv, tok, http.MethodPut, "/api/admin/upstreams/a",
		`{"enabled":false}`); code != http.StatusOK {
		t.Fatalf("停用非最后一个应 200, got %d body=%s", code, body)
	}
	ua, _ := FindUpstream(app.Cfg.Upstreams, "a")
	if ua.Enabled {
		t.Fatal("a 应已停用")
	}
	if app.Cfg.DefaultUpstreamID != "b" {
		t.Fatalf("默认上游应改指 b, got %q", app.Cfg.DefaultUpstreamID)
	}
}

// 上游不存在 → 404（更新与删除）。
func TestAdminUpstreamNotFound(t *testing.T) {
	_, srv := newAdminApp(t)
	defer srv.Close()
	tok := adminToken(t, srv)

	if code, _ := doJSON(t, srv, tok, http.MethodPut, "/api/admin/upstreams/nope", `{"name":"x"}`); code != http.StatusNotFound {
		t.Fatalf("update 不存在应 404, got %d", code)
	}
	if code, _ := doJSON(t, srv, tok, http.MethodDelete, "/api/admin/upstreams/nope", ""); code != http.StatusNotFound {
		t.Fatalf("delete 不存在应 404, got %d", code)
	}
	if code, _ := doJSON(t, srv, tok, http.MethodPost, "/api/admin/upstreams/nope/test", ""); code != http.StatusNotFound {
		t.Fatalf("test 不存在应 404, got %d", code)
	}
}

// 首次通过 API 加上游时，旧单上游字段必须被实体化，不能凭空消失。
func TestAdminCreateMaterializesLegacy(t *testing.T) {
	app, srv := newAdminApp(t)
	defer srv.Close()
	tok := adminToken(t, srv)

	// newAdminApp 的默认配置只有旧字段 upstream_base_url
	if len(app.Cfg.Upstreams) != 0 {
		t.Fatalf("前置条件不满足: %+v", app.Cfg.Upstreams)
	}
	if code, body := doJSON(t, srv, tok, http.MethodPost, "/api/admin/upstreams",
		`{"id":"newone","base_url":"http://1.2.3.4/v1","enabled":true}`); code != http.StatusOK {
		t.Fatalf("create code=%d body=%s", code, body)
	}
	if _, ok := FindUpstream(app.Cfg.Upstreams, LegacyUpstreamID); !ok {
		t.Fatalf("旧单上游未实体化，已丢失: %+v", app.Cfg.Upstreams)
	}
	if _, ok := FindUpstream(app.Cfg.Upstreams, "newone"); !ok {
		t.Fatalf("新上游未写入: %+v", app.Cfg.Upstreams)
	}
}

// /api/admin/models 按上游分组；单个上游失败只影响自己那一组。
func TestAdminModelsGroupedByUpstream(t *testing.T) {
	ok1 := httptest.NewServer(&recordingUpstream{name: "g1"})
	defer ok1.Close()

	app, srv := newAdminApp(t)
	defer srv.Close()
	tok := adminToken(t, srv)
	app.Client = ok1.Client()

	app.Mu.Lock()
	app.Cfg.Upstreams = []Upstream{
		{ID: "good", Name: "好的", BaseURL: ok1.URL + "/v1", Enabled: true},
		{ID: "bad", Name: "坏的", BaseURL: "http://127.0.0.1:1/v1", Enabled: true},
		{ID: "off", Name: "停用的", BaseURL: "http://127.0.0.1:2/v1", Enabled: false},
	}
	app.Cfg.DefaultUpstreamID = "good"
	app.Mu.Unlock()

	code, body := doJSON(t, srv, tok, http.MethodGet, "/api/admin/models", "")
	if code != http.StatusOK {
		t.Fatalf("code=%d body=%s", code, body)
	}
	var out struct {
		Upstreams []struct {
			ID      string   `json:"id"`
			Models  []string `json:"models"`
			Warning string   `json:"warning"`
			Prefix  string   `json:"prefix"`
		} `json:"upstreams"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Upstreams) != 3 {
		t.Fatalf("应返回 3 组, got %d: %s", len(out.Upstreams), body)
	}
	byID := map[string]int{}
	for i, g := range out.Upstreams {
		byID[g.ID] = i
	}
	if got := out.Upstreams[byID["good"]]; len(got.Models) == 0 || got.Warning != "" {
		t.Fatalf("good 组应有模型且无告警: %+v", got)
	}
	if got := out.Upstreams[byID["bad"]]; got.Warning == "" {
		t.Fatalf("bad 组应有告警: %+v", got)
	}
	if got := out.Upstreams[byID["off"]]; got.Warning == "" {
		t.Fatalf("停用组应标注: %+v", got)
	}
	// 默认上游无前缀，其它有
	if got := out.Upstreams[byID["good"]].Prefix; got != "" {
		t.Fatalf("默认上游不应有前缀, got %q", got)
	}
	if got := out.Upstreams[byID["bad"]].Prefix; got != "bad/" {
		t.Fatalf("非默认上游前缀 = %q, want bad/", got)
	}
}

// 上游连通性测试：可达 → ok=true；不可达 → ok=false 且有 warning。
func TestAdminTestUpstream(t *testing.T) {
	up := httptest.NewServer(&recordingUpstream{name: "t"})
	defer up.Close()

	app, srv := newAdminApp(t)
	defer srv.Close()
	tok := adminToken(t, srv)
	app.Client = up.Client()

	app.Mu.Lock()
	app.Cfg.Upstreams = []Upstream{
		{ID: "ok", BaseURL: up.URL + "/v1", Enabled: true},
		{ID: "dead", BaseURL: "http://127.0.0.1:1/v1", Enabled: true},
	}
	app.Mu.Unlock()

	code, body := doJSON(t, srv, tok, http.MethodPost, "/api/admin/upstreams/ok/test", "")
	if code != http.StatusOK {
		t.Fatalf("code=%d body=%s", code, body)
	}
	var good struct {
		OK         bool `json:"ok"`
		ModelCount int  `json:"model_count"`
	}
	if err := json.Unmarshal([]byte(body), &good); err != nil {
		t.Fatal(err)
	}
	if !good.OK || good.ModelCount == 0 {
		t.Fatalf("可达上游应 ok=true 且有模型: %s", body)
	}

	code, body = doJSON(t, srv, tok, http.MethodPost, "/api/admin/upstreams/dead/test", "")
	if code != http.StatusOK {
		t.Fatalf("不可达也应 200（结果在体内）: %d", code)
	}
	var bad struct {
		OK      bool   `json:"ok"`
		Warning string `json:"warning"`
	}
	if err := json.Unmarshal([]byte(body), &bad); err != nil {
		t.Fatal(err)
	}
	if bad.OK || bad.Warning == "" {
		t.Fatalf("不可达上游应 ok=false 且有 warning: %s", body)
	}
}
