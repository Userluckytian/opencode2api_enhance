// 链路质量探测改造回归测试（2026-08-20，问题 1）：
// 探测改经实例 API 端口（带 key）用标准 net/http 请求 /v1/models，
// 2xx 判通、非 2xx/不可达透传 LastError——旧实现直拨 sing-box SOCKS
// 且吞错误，UI 只能看到黑盒 0 分（与「测试按钮成功但质量不可用」矛盾）。
package manager

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestProbeInstanceOnceViaAPI(t *testing.T) {
	key := "sk-test"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+key {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"m1"}]}`))
	}))
	defer srv.Close()
	port := uint16(srv.Listener.Addr().(*net.TCPAddr).Port)

	// 200 + 正确 key → 通，无错误
	s := probeInstanceOnce(Instance{Port: port, Password: key}, 3*time.Second)
	if !s.OK || s.LastError != "" {
		t.Fatalf("200 应判通, got ok=%v err=%q", s.OK, s.LastError)
	}
	// key 错误 → 401 → 失败 + LastError 含状态码
	s2 := probeInstanceOnce(Instance{Port: port, Password: "wrong"}, 3*time.Second)
	if s2.OK || !strings.Contains(s2.LastError, "401") {
		t.Fatalf("401 应失败并透传状态码, got ok=%v err=%q", s2.OK, s2.LastError)
	}
	// 5xx → 失败 + LastError
	srv5 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv5.Close()
	s3 := probeInstanceOnce(Instance{Port: uint16(srv5.Listener.Addr().(*net.TCPAddr).Port), Password: ""}, 3*time.Second)
	if s3.OK || !strings.Contains(s3.LastError, "503") {
		t.Fatalf("503 应失败并透传, got ok=%v err=%q", s3.OK, s3.LastError)
	}
	// 无监听端口 → 失败 + LastError「不可达」
	s4 := probeInstanceOnce(Instance{Port: 1, Password: key}, 500*time.Millisecond)
	if s4.OK || !strings.Contains(s4.LastError, "不可达") {
		t.Fatalf("不可达应失败并透传, got ok=%v err=%q", s4.OK, s4.LastError)
	}
	// 未配置端口 → 失败 + 明确原因
	s5 := probeInstanceOnce(Instance{}, time.Second)
	if s5.OK || s5.LastError == "" {
		t.Fatalf("空配置应失败, got ok=%v err=%q", s5.OK, s5.LastError)
	}
}

// TestComputeQualityLastError 质量汇总透传最新失败原因（全成功清空）。
func TestComputeQualityLastError(t *testing.T) {
	rec := &QualityRecord{}
	now := int64(10000)
	computeQuality(rec, []ProbeSample{
		{OK: true, TS: 10001},
		{OK: false, TS: 10002, LastError: "实例 /v1/models 返回 503"},
	}, now, 600)
	if rec.LastError != "实例 /v1/models 返回 503" {
		t.Fatalf("未透传失败原因, got %q", rec.LastError)
	}
	computeQuality(rec, []ProbeSample{
		{OK: true, TS: 20001},
		{OK: true, TS: 20002},
	}, now+10000, 600)
	if rec.LastError != "" {
		t.Fatalf("全成功应清空失败原因, got %q", rec.LastError)
	}
}
