package main

import (
	"errors"
	"testing"
)

func testUpstreams() []Upstream {
	return []Upstream{
		{ID: "main", Name: "主上游", BaseURL: "http://10.0.0.1:18080/v1", Enabled: true},
		{ID: "backup", Name: "备用", BaseURL: "http://10.0.0.2:18080/v1", Enabled: true},
		{ID: "off", Name: "已停用", BaseURL: "http://10.0.0.3:18080/v1", Enabled: false},
	}
}

func TestResolveUpstreamPrefix(t *testing.T) {
	tests := []struct {
		name      string
		model     string
		defaultID string
		wantID    string
		wantModel string
		wantErr   error
	}{
		{
			name:      "命中已启用上游则剥离前缀",
			model:     "backup/gpt-4o",
			defaultID: "main",
			wantID:    "backup",
			wantModel: "gpt-4o",
		},
		{
			name:      "命中默认上游前缀同样剥离",
			model:     "main/gpt-4o",
			defaultID: "main",
			wantID:    "main",
			wantModel: "gpt-4o",
		},
		{
			// 真实模型名含斜杠：第一段 "or" 不是上游 ID，必须整体原样走默认上游
			name:      "真实模型名含斜杠不误剥",
			model:     "or/minimax/minimax-m3.1-flash:free",
			defaultID: "main",
			wantID:    "main",
			wantModel: "or/minimax/minimax-m3.1-flash:free",
		},
		{
			name:      "无斜杠走默认上游",
			model:     "gpt-4o",
			defaultID: "main",
			wantID:    "main",
			wantModel: "gpt-4o",
		},
		{
			// 停用的上游不参与匹配：既不路由过去，也不剥离前缀
			name:      "停用上游不参与路由",
			model:     "off/gpt-4o",
			defaultID: "main",
			wantID:    "main",
			wantModel: "off/gpt-4o",
		},
		{
			// 大小写敏感：Backup != backup
			name:      "大小写不匹配视为普通模型名",
			model:     "Backup/gpt-4o",
			defaultID: "main",
			wantID:    "main",
			wantModel: "Backup/gpt-4o",
		},
		{
			name:      "前缀后为空报错",
			model:     "backup/",
			defaultID: "main",
			wantErr:   errEmptyModelAfterPrefix,
		},
		{
			name:      "前缀后仅空白报错",
			model:     "backup/   ",
			defaultID: "main",
			wantErr:   errEmptyModelAfterPrefix,
		},
		{
			// 默认上游 ID 失效（不存在）时退化为第一个 enabled
			name:      "默认ID不存在则取第一个启用项",
			model:     "gpt-4o",
			defaultID: "does-not-exist",
			wantID:    "main",
			wantModel: "gpt-4o",
		},
		{
			// 默认上游被停用同样退化
			name:      "默认上游被停用则取第一个启用项",
			model:     "gpt-4o",
			defaultID: "off",
			wantID:    "main",
			wantModel: "gpt-4o",
		},
		{
			// 空 model 且无斜杠：交给默认上游，由上层白名单判定
			name:      "空模型名走默认上游",
			model:     "",
			defaultID: "main",
			wantID:    "main",
			wantModel: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			u, model, err := resolveUpstream(testUpstreams(), tc.defaultID, tc.model)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if u.ID != tc.wantID {
				t.Fatalf("upstream = %q, want %q", u.ID, tc.wantID)
			}
			if model != tc.wantModel {
				t.Fatalf("model = %q, want %q", model, tc.wantModel)
			}
		})
	}
}

func TestResolveNoUpstream(t *testing.T) {
	cases := []struct {
		name string
		list []Upstream
	}{
		{"空列表", nil},
		{"全部停用", []Upstream{{ID: "a", Enabled: false}, {ID: "b", Enabled: false}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := resolveUpstream(tc.list, "", "gpt-4o"); !errors.Is(err, errNoUpstream) {
				t.Fatalf("err = %v, want errNoUpstream", err)
			}
		})
	}
}

// 前缀命中但该上游被停用、且没有其它启用上游 → 无可用上游（而不是误用停用项）。
func TestResolveDisabledPrefixOnly(t *testing.T) {
	list := []Upstream{{ID: "off", BaseURL: "http://x/v1", Enabled: false}}
	if _, _, err := resolveUpstream(list, "off", "off/gpt-4o"); !errors.Is(err, errNoUpstream) {
		t.Fatalf("err = %v, want errNoUpstream", err)
	}
}

func TestValidUpstreamID(t *testing.T) {
	// 32 字符（1 + 31）是规则上限
	valid := []string{"a", "main", "backup-2", "A_b-9", "01234567890123456789012345678901"}
	invalid := []string{"", "-a", "_a", "a/b", "a b", "a.b", "中文", "012345678901234567890123456789012"} // 33 字符
	for _, id := range valid {
		if !validUpstreamID(id) {
			t.Fatalf("validUpstreamID(%q) = false, want true", id)
		}
	}
	for _, id := range invalid {
		if validUpstreamID(id) {
			t.Fatalf("validUpstreamID(%q) = true, want false", id)
		}
	}
}

func TestUpstreamPrefix(t *testing.T) {
	if got := upstreamPrefix("main", "main"); got != "" {
		t.Fatalf("default upstream should have no prefix, got %q", got)
	}
	if got := upstreamPrefix("backup", "main"); got != "backup/" {
		t.Fatalf("non-default prefix = %q, want %q", got, "backup/")
	}
}

func TestFindUpstreamCaseSensitive(t *testing.T) {
	if _, ok := FindUpstream(testUpstreams(), "Backup"); ok {
		t.Fatal("FindUpstream must be case-sensitive")
	}
	if _, ok := FindUpstream(testUpstreams(), "backup"); !ok {
		t.Fatal("FindUpstream should find exact id")
	}
}
