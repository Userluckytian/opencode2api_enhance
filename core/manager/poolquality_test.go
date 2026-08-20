package manager

import (
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

// mustURLPort 取 httptest server 监听端口（问题 1 改造后探测经实例 API 端口）。
func mustURLPort(t *testing.T, srv *httptest.Server) int {
	t.Helper()
	return srv.Listener.Addr().(*net.TCPAddr).Port
}

// ---- 滑动窗口评分 ----

func TestComputeQualityEmptyWindow(t *testing.T) {
	var rec QualityRecord
	computeQuality(&rec, nil, 1000, 600)
	// S1：空窗口（无探活样本）→ unknown，不参与竞速；分数保持乐观 100（单发可用）。
	if rec.Score != 100 || rec.Level != qualityUnknown {
		t.Fatalf("empty window: score=%d level=%s, want 100/unknown", rec.Score, rec.Level)
	}
	if len(rec.Samples) != 0 {
		t.Fatalf("samples should be empty, got %d", len(rec.Samples))
	}
}

// S3: unknown 计数正式化——空窗口节点计入 PoolQualitySummary.Unknown（UI 显示「探测中」）。
func TestSummarizeQualityUnknownCount(t *testing.T) {
	recs := []QualityRecord{
		{Name: "a", Level: qualityUnknown, Samples: nil},
		{Name: "b", Level: qualityHealthy, Samples: []ProbeSample{{OK: true, TS: 1000}}},
		{Name: "c", Level: qualityUnknown, Samples: nil},
		{Name: "d", Level: qualityDown, Samples: []ProbeSample{{OK: false, TS: 1000}}},
	}
	s := summarizeQuality(recs, 1000)
	if s.Total != 4 || s.Probed != 2 {
		t.Fatalf("total=%d probed=%d, want 4/2", s.Total, s.Probed)
	}
	if s.Healthy != 1 || s.Down != 1 || s.Unknown != 2 {
		t.Fatalf("healthy=%d down=%d unknown=%d, want 1/1/2", s.Healthy, s.Down, s.Unknown)
	}
	if s.Degraded != 0 || s.Flaky != 0 {
		t.Fatalf("degraded=%d flaky=%d, want 0/0", s.Degraded, s.Flaky)
	}
}

func TestComputeQualityAllOK(t *testing.T) {
	samples := []ProbeSample{
		{OK: true, LatencyMS: 100, TS: 1000},
		{OK: true, LatencyMS: 200, TS: 1001},
		{OK: true, LatencyMS: 150, TS: 1002},
	}
	var rec QualityRecord
	computeQuality(&rec, samples, 1000, 600)
	if rec.Score != 100 || rec.Level != qualityHealthy {
		t.Fatalf("all ok: score=%d level=%s, want 100/healthy", rec.Score, rec.Level)
	}
	if rec.SuccessRate != 1.0 || rec.ConsecutiveFailures != 0 {
		t.Fatalf("rate=%v cf=%d, want 1.0/0", rec.SuccessRate, rec.ConsecutiveFailures)
	}
	if rec.AvgLatencyMS != 150 {
		t.Fatalf("avg latency=%d, want 150", rec.AvgLatencyMS)
	}
}

func TestComputeQualityAllFail(t *testing.T) {
	samples := []ProbeSample{
		{OK: false, LatencyMS: 3000, TS: 1000},
		{OK: false, LatencyMS: 3000, TS: 1001},
	}
	var rec QualityRecord
	computeQuality(&rec, samples, 1000, 600)
	if rec.Score != 0 || rec.Level != qualityDown {
		t.Fatalf("all fail: score=%d level=%s, want 0/down", rec.Score, rec.Level)
	}
	if rec.ConsecutiveFailures != 2 {
		t.Fatalf("cf=%d, want 2", rec.ConsecutiveFailures)
	}
}

// 单次失败（窗口内唯一样本）不应判 down，应为 flaky（分数被打到 0 以下）。
func TestComputeQualitySingleFail(t *testing.T) {
	samples := []ProbeSample{{OK: false, LatencyMS: 3000, TS: 1000}}
	var rec QualityRecord
	computeQuality(&rec, samples, 1000, 600)
	if rec.Level != qualityFlaky {
		t.Fatalf("single fail: level=%s, want flaky", rec.Level)
	}
}

// 窗口滑出：旧样本不参与评分，且不留在 Samples 中。
func TestComputeQualityWindowSlide(t *testing.T) {
	samples := []ProbeSample{
		{OK: false, LatencyMS: 3000, TS: 100}, // 旧失败（窗口外）
		{OK: true, LatencyMS: 200, TS: 900},   // 新成功（窗口内）
	}
	var rec QualityRecord
	computeQuality(&rec, samples, 1000, 600) // cutoff = 400
	if len(rec.Samples) != 1 {
		t.Fatalf("samples=%d, want 1 (old slid out)", len(rec.Samples))
	}
	if rec.Score != 100 || rec.Level != qualityHealthy {
		t.Fatalf("after slide: score=%d level=%s, want 100/healthy", rec.Score, rec.Level)
	}
}

// 延迟分档：高延迟（avg>8000）把满分压到 30。
func TestComputeQualityLatencyTier(t *testing.T) {
	samples := []ProbeSample{
		{OK: true, LatencyMS: 9000, TS: 1000},
		{OK: true, LatencyMS: 9000, TS: 1001},
	}
	var rec QualityRecord
	computeQuality(&rec, samples, 1000, 600)
	if rec.Score != 30 {
		t.Fatalf("high latency score=%d, want 30", rec.Score)
	}
	if rec.Level != qualityFlaky {
		t.Fatalf("level=%s, want flaky", rec.Level)
	}

	// 中等延迟（avg 2000，>1000）压 0.8 倍 → 80 → degraded。
	samples2 := []ProbeSample{
		{OK: true, LatencyMS: 2000, TS: 1000},
		{OK: true, LatencyMS: 2000, TS: 1001},
	}
	var rec2 QualityRecord
	computeQuality(&rec2, samples2, 1000, 600)
	if rec2.Score != 80 || rec2.Level != qualityDegraded {
		t.Fatalf("mid latency: score=%d level=%s, want 80/degraded", rec2.Score, rec2.Level)
	}
}

// 连续失败计数：末尾 1 次失败 + 前面成功 → 成功率 0.5、扣 15 分 → flaky。
func TestComputeQualityConsecutiveFailures(t *testing.T) {
	samples := []ProbeSample{
		{OK: true, LatencyMS: 100, TS: 1000},
		{OK: false, LatencyMS: 3000, TS: 1001},
	}
	var rec QualityRecord
	computeQuality(&rec, samples, 1000, 600)
	if rec.ConsecutiveFailures != 1 {
		t.Fatalf("cf=%d, want 1", rec.ConsecutiveFailures)
	}
	// rate=0.5 → 50；avg=1550 触发延迟分档 ×0.8 → 40；连续失败扣 15 → 25；score<50 → flaky。
	if rec.Score != 25 || rec.Level != qualityFlaky {
		t.Fatalf("score=%d level=%s, want 25/flaky", rec.Score, rec.Level)
	}

	// 连续 3 次失败 → down。
	three := []ProbeSample{
		{OK: true, LatencyMS: 100, TS: 1000},
		{OK: false, LatencyMS: 3000, TS: 1001},
		{OK: false, LatencyMS: 3000, TS: 1002},
		{OK: false, LatencyMS: 3000, TS: 1003},
	}
	var rec3 QualityRecord
	computeQuality(&rec3, three, 1000, 600)
	if rec3.ConsecutiveFailures != 3 || rec3.Level != qualityDown {
		t.Fatalf("three fails: cf=%d level=%s, want 3/down", rec3.ConsecutiveFailures, rec3.Level)
	}
}

// 成功率 < 0.9 → degraded（即便无连续失败）。
func TestComputeQualityLowRate(t *testing.T) {
	samples := []ProbeSample{
		{OK: false, LatencyMS: 100, TS: 1000},
		{OK: false, LatencyMS: 100, TS: 1001},
		{OK: true, LatencyMS: 100, TS: 1002},
		{OK: true, LatencyMS: 100, TS: 1003},
		{OK: true, LatencyMS: 100, TS: 1004},
		{OK: true, LatencyMS: 100, TS: 1005},
		{OK: true, LatencyMS: 100, TS: 1006},
		{OK: true, LatencyMS: 100, TS: 1007},
		{OK: true, LatencyMS: 100, TS: 1008},
		{OK: true, LatencyMS: 100, TS: 1009},
	}
	var rec QualityRecord
	computeQuality(&rec, samples, 1000, 600)
	if rec.SuccessRate != 0.8 {
		t.Fatalf("rate=%v, want 0.8", rec.SuccessRate)
	}
	if rec.Level != qualityDegraded {
		t.Fatalf("level=%s, want degraded", rec.Level)
	}
}

// ---- 配置生效值 ----

func TestPoolProbeConfigDefaults(t *testing.T) {
	cfg := Config{}
	if got := poolProbeInterval(cfg); got != 45 {
		t.Fatalf("interval=%d, want 45", got)
	}
	if got := poolProbeTimeout(cfg); got != 3*time.Second {
		t.Fatalf("timeout=%v, want 3s", got)
	}
	if got := poolQualityWindowSec(cfg); got != 600 {
		t.Fatalf("window=%d, want 600", got)
	}
	if !poolProbeEnabled(cfg) {
		t.Fatal("enabled default should be true")
	}

	cfg2 := Config{PoolProbeIntervalSec: 10, PoolProbeTimeoutSec: 5, PoolQualityWindowMin: 20}
	if got := poolProbeInterval(cfg2); got != 10 {
		t.Fatalf("interval=%d, want 10", got)
	}
	if got := poolProbeTimeout(cfg2); got != 5*time.Second {
		t.Fatalf("timeout=%v, want 5s", got)
	}
	if got := poolQualityWindowSec(cfg2); got != 1200 {
		t.Fatalf("window=%d, want 1200", got)
	}

	disabled := false
	cfg3 := Config{PoolProbeEnabled: &disabled}
	if poolProbeEnabled(cfg3) {
		t.Fatal("explicit false should disable")
	}
}

// ---- 配置解析（ConfigSet / ConfigGet / ConfigViewOf） ----

func TestPoolProbeConfigSetGet(t *testing.T) {
	m := New(t.TempDir())

	if err := m.ConfigSet("pool_probe_interval_sec", "60"); err != nil {
		t.Fatalf("set interval: %v", err)
	}
	if got, _ := m.ConfigGet("pool_probe_interval_sec"); got != "60" {
		t.Fatalf("get interval=%s, want 60", got)
	}
	if err := m.ConfigSet("pool_probe_timeout_sec", "7"); err != nil {
		t.Fatalf("set timeout: %v", err)
	}
	if err := m.ConfigSet("pool_quality_window_min", "15"); err != nil {
		t.Fatalf("set window: %v", err)
	}
	if err := m.ConfigSet("pool_probe_enabled", "false"); err != nil {
		t.Fatalf("set enabled: %v", err)
	}
	if got, _ := m.ConfigGet("pool_probe_enabled"); got != "false" {
		t.Fatalf("get enabled=%s, want false", got)
	}

	// 非法值回退（保持原值并报错）。
	if err := m.ConfigSet("pool_probe_interval_sec", "-1"); err == nil {
		t.Fatal("negative interval should error")
	}
	if got, _ := m.ConfigGet("pool_probe_interval_sec"); got != "60" {
		t.Fatalf("interval after invalid set=%s, want 60", got)
	}
	if err := m.ConfigSet("pool_probe_enabled", "notabool"); err == nil {
		t.Fatal("invalid bool should error")
	}

	// ConfigViewOf 反映生效值。
	v := m.ConfigViewOf()
	if v.PoolProbeIntervalSec != 60 || v.PoolProbeTimeoutSec != 7 || v.PoolQualityWindowMin != 15 {
		t.Fatalf("view=%d/%d/%d, want 60/7/15", v.PoolProbeIntervalSec, v.PoolProbeTimeoutSec, v.PoolQualityWindowMin)
	}
	if v.PoolProbeEnabled {
		t.Fatal("view enabled should be false")
	}
}

// ---- S1：race_budget_ms 配置 ----

func TestRaceBudgetConfigSetGet(t *testing.T) {
	m := New(t.TempDir())

	// 默认 10000。
	if got := poolRaceBudgetMSOf(m.loadConfig()); got != 10000 {
		t.Fatalf("default budget=%d, want 10000", got)
	}

	if err := m.ConfigSet("race_budget_ms", "5000"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if got, _ := m.ConfigGet("race_budget_ms"); got != "5000" {
		t.Fatalf("get=%s, want 5000", got)
	}
	if got := poolRaceBudgetMSOf(m.loadConfig()); got != 5000 {
		t.Fatalf("effective=%d, want 5000", got)
	}
	// 非法值回退（保持原值并报错）。
	if err := m.ConfigSet("race_budget_ms", "-1"); err == nil {
		t.Fatal("negative should error")
	}
	if got, _ := m.ConfigGet("race_budget_ms"); got != "5000" {
		t.Fatalf("after invalid set=%s, want 5000", got)
	}
	if err := m.ConfigSet("race_budget_ms", "abc"); err == nil {
		t.Fatal("non-integer should error")
	}
	// ConfigViewOf 反映生效值。
	if v := m.ConfigViewOf(); v.RaceBudgetMS != 5000 {
		t.Fatalf("view budget=%d, want 5000", v.RaceBudgetMS)
	}
}

// ---- S5：压力阈值与副本上限配置 ----

func TestRacePressureConfigSetGet(t *testing.T) {
	m := New(t.TempDir())

	// 默认 0.5 / 1.0。
	if got := poolRacePressureLowOf(m.loadConfig()); got != 0.5 {
		t.Fatalf("default low=%v, want 0.5", got)
	}
	if got := poolRacePressureHighOf(m.loadConfig()); got != 1.0 {
		t.Fatalf("default high=%v, want 1.0", got)
	}

	if err := m.ConfigSet("pool_race_pressure_low", "0.3"); err != nil {
		t.Fatalf("set low: %v", err)
	}
	if err := m.ConfigSet("pool_race_pressure_high", "1.5"); err != nil {
		t.Fatalf("set high: %v", err)
	}
	if got, _ := m.ConfigGet("pool_race_pressure_low"); got != "0.3" {
		t.Fatalf("get low=%s, want 0.3", got)
	}
	if got, _ := m.ConfigGet("pool_race_pressure_high"); got != "1.5" {
		t.Fatalf("get high=%s, want 1.5", got)
	}
	// 非法值回退（保持原值并报错）。
	if err := m.ConfigSet("pool_race_pressure_low", "abc"); err == nil {
		t.Fatal("non-float should error")
	}
	if err := m.ConfigSet("pool_race_pressure_low", "-0.1"); err == nil {
		t.Fatal("negative should error")
	}
	if got, _ := m.ConfigGet("pool_race_pressure_low"); got != "0.3" {
		t.Fatalf("after invalid set=%s, want 0.3", got)
	}
	// ConfigViewOf 反映生效值。
	if v := m.ConfigViewOf(); v.PoolRacePressureLow != 0.3 || v.PoolRacePressureHigh != 1.5 {
		t.Fatalf("view low=%v high=%v, want 0.3/1.5", v.PoolRacePressureLow, v.PoolRacePressureHigh)
	}
}

// TestRaceCopiesRangeValidation pool_race_copies 值域校验 1~4（1 = 关闭竞速）。
func TestRaceCopiesRangeValidation(t *testing.T) {
	m := New(t.TempDir())

	for _, bad := range []string{"0", "5", "-1", "abc"} {
		if err := m.ConfigSet("pool_race_copies", bad); err == nil {
			t.Fatalf("copies=%s should error (range 1~4)", bad)
		}
	}
	if err := m.ConfigSet("pool_race_copies", "3"); err != nil {
		t.Fatalf("copies=3 should be ok: %v", err)
	}
	if got, _ := m.ConfigGet("pool_race_copies"); got != "3" {
		t.Fatalf("get=%s, want 3", got)
	}
}

// ---- S2：429 感知配置 ----

func TestRateLimitConfigSetGet(t *testing.T) {
	m := New(t.TempDir())

	// 默认 30 / 1000 / 30000。
	if got := rateLimitCooldownSecOf(m.loadConfig()); got != 30 {
		t.Fatalf("default cooldown=%d, want 30", got)
	}
	if got := rateLimitBackoffBaseMSOf(m.loadConfig()); got != 1000 {
		t.Fatalf("default base=%d, want 1000", got)
	}
	if got := rateLimitBackoffCapMSOf(m.loadConfig()); got != 30000 {
		t.Fatalf("default cap=%d, want 30000", got)
	}

	if err := m.ConfigSet("rate_limit_cooldown_sec", "15"); err != nil {
		t.Fatalf("set cooldown: %v", err)
	}
	if err := m.ConfigSet("rate_limit_backoff_base_ms", "2000"); err != nil {
		t.Fatalf("set base: %v", err)
	}
	if err := m.ConfigSet("rate_limit_backoff_cap_ms", "60000"); err != nil {
		t.Fatalf("set cap: %v", err)
	}
	if got, _ := m.ConfigGet("rate_limit_cooldown_sec"); got != "15" {
		t.Fatalf("get cooldown=%s, want 15", got)
	}
	if got, _ := m.ConfigGet("rate_limit_backoff_base_ms"); got != "2000" {
		t.Fatalf("get base=%s, want 2000", got)
	}
	if got, _ := m.ConfigGet("rate_limit_backoff_cap_ms"); got != "60000" {
		t.Fatalf("get cap=%s, want 60000", got)
	}

	// 非法值回退（-1/abc）：保持原值并报错。
	for _, bad := range []string{"-1", "abc"} {
		if err := m.ConfigSet("rate_limit_cooldown_sec", bad); err == nil {
			t.Fatalf("invalid %q should error", bad)
		}
		if err := m.ConfigSet("rate_limit_backoff_base_ms", bad); err == nil {
			t.Fatalf("invalid %q should error", bad)
		}
		if err := m.ConfigSet("rate_limit_backoff_cap_ms", bad); err == nil {
			t.Fatalf("invalid %q should error", bad)
		}
	}
	if got, _ := m.ConfigGet("rate_limit_cooldown_sec"); got != "15" {
		t.Fatalf("after invalid set cooldown=%s, want 15", got)
	}

	// ConfigViewOf 反映生效值。
	v := m.ConfigViewOf()
	if v.RateLimitCooldownSec != 15 || v.RateLimitBackoffBaseMS != 2000 || v.RateLimitBackoffCapMS != 60000 {
		t.Fatalf("view=%d/%d/%d, want 15/2000/60000", v.RateLimitCooldownSec, v.RateLimitBackoffBaseMS, v.RateLimitBackoffCapMS)
	}
}

// ---- 持久化 ----

func TestPoolQualityFileRoundtrip(t *testing.T) {
	m := New(t.TempDir())
	recs := []QualityRecord{
		{Name: "n1", Port: 14400, SingboxPort: 16400, Score: 100, Level: qualityHealthy,
			Samples: []ProbeSample{{OK: true, LatencyMS: 100, TS: 1000}}},
		{Name: "n2", Port: 14401, SingboxPort: 16401, Score: 0, Level: qualityDown,
			ConsecutiveFailures: 3},
	}
	m.savePoolQuality(recs)
	loaded := m.loadPoolQuality()
	if len(loaded) != 2 || loaded[0].Name != "n1" || loaded[1].Level != qualityDown {
		t.Fatalf("loaded = %+v", loaded)
	}
	if loaded[0].Samples[0].LatencyMS != 100 {
		t.Fatalf("sample lost: %+v", loaded[0].Samples)
	}
}

func TestPoolQualityFileCorrupt(t *testing.T) {
	m := New(t.TempDir())
	if err := os.MkdirAll(m.paths.RuntimeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.paths.RuntimeDir, "pool_quality.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := m.loadPoolQuality(); got != nil {
		t.Fatalf("corrupt file should load nil, got %+v", got)
	}
}

// ---- 测试用最小 SOCKS5 代理 ----

type testSocksMode int

const (
	socksModeProxy  testSocksMode = iota // CONNECT 后双向转发到真实目标
	socksModeReject                      // CONNECT 一律拒绝
	socksModeHang                        // 握手后不响应（模拟卡死）
)

// startTestSocks5 起一个无鉴权的最小 SOCKS5 代理，返回监听地址。
func startTestSocks5(t *testing.T, mode testSocksMode) (addr string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen socks5: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go handleTestSocksConn(conn, mode)
		}
	}()
	return ln.Addr().(*net.TCPAddr).String()
}

func handleTestSocksConn(conn net.Conn, mode testSocksMode) {
	defer conn.Close()
	buf := make([]byte, 3)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return
	}
	if _, err := conn.Write([]byte{0x05, 0x00}); err != nil { // 无鉴权
		return
	}
	head := make([]byte, 4)
	if _, err := io.ReadFull(conn, head); err != nil {
		return
	}
	if head[3] != 0x03 { // 仅支持域名类型
		return
	}
	lb := make([]byte, 1)
	if _, err := io.ReadFull(conn, lb); err != nil {
		return
	}
	host := make([]byte, lb[0])
	if _, err := io.ReadFull(conn, host); err != nil {
		return
	}
	portB := make([]byte, 2)
	if _, err := io.ReadFull(conn, portB); err != nil {
		return
	}
	target := net.JoinHostPort(string(host), strconv.Itoa(int(binary.BigEndian.Uint16(portB))))

	switch mode {
	case socksModeReject:
		_, _ = conn.Write([]byte{0x05, 0x05, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	case socksModeHang:
		return // 不响应 CONNECT
	}

	up, err := net.Dial("tcp", target)
	if err != nil {
		_, _ = conn.Write([]byte{0x05, 0x05, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	defer up.Close()
	if _, err := conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0}); err != nil {
		return
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = io.Copy(up, conn) }()
	go func() { defer wg.Done(); _, _ = io.Copy(conn, up) }()
	wg.Wait()
}

// socksPort 从 "127.0.0.1:port" 解析端口。
func socksPort(t *testing.T, addr string) uint16 {
	t.Helper()
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	p, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}
	return uint16(p)
}

// ---- 链路探测三态 ----

func TestHTTPGetViaSocksSuccess(t *testing.T) {
	back := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("path=%s, want /v1/models", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer back.Close()

	socksAddr := startTestSocks5(t, socksModeProxy)
	ok, err := httpGetViaSocks(socksPort(t, socksAddr), back.URL+"/v1/models", 2*time.Second)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v, want true/nil", ok, err)
	}
}

func TestHTTPGetViaSocksReject(t *testing.T) {
	socksAddr := startTestSocks5(t, socksModeReject)
	ok, err := httpGetViaSocks(socksPort(t, socksAddr), "http://127.0.0.1:1/v1/models", 2*time.Second)
	if err == nil || ok {
		t.Fatalf("reject: ok=%v err=%v, want false+err", ok, err)
	}
}

func TestHTTPGetViaSocksTimeout(t *testing.T) {
	socksAddr := startTestSocks5(t, socksModeHang)
	ok, err := httpGetViaSocks(socksPort(t, socksAddr), "http://127.0.0.1:1/v1/models", 200*time.Millisecond)
	if err == nil || ok {
		t.Fatalf("hang: ok=%v err=%v, want false+err", ok, err)
	}
}

// 5xx 视为链路失败。
func TestHTTPGetViaSocksServerError(t *testing.T) {
	back := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer back.Close()
	socksAddr := startTestSocks5(t, socksModeProxy)
	ok, err := httpGetViaSocks(socksPort(t, socksAddr), back.URL+"/v1/models", 2*time.Second)
	if ok || err == nil {
		t.Fatalf("5xx: ok=%v err=%v, want false+err", ok, err)
	}
}

// ---- 探活调度（RunPoolQualityOnce） ----

func TestRunPoolQualityOnce(t *testing.T) {
	m := New(t.TempDir())

	// 探测改经实例 API 端口（2026-08-20 问题 1 修复）：good=/v1/models 2xx（通）、
	// bad=5xx（失败）；stopped 不探测。
	goodSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer goodSrv.Close()
	badSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer badSrv.Close()

	_ = m.AddInstance(Instance{Name: "good", Port: uint16(mustURLPort(t, goodSrv)), Node: "n1", Password: "sk"})
	_ = m.AddInstance(Instance{Name: "bad", Port: uint16(mustURLPort(t, badSrv)), Node: "n2", Password: "sk"})
	_ = m.AddInstance(Instance{Name: "stopped", Port: uint16(mustURLPort(t, goodSrv)), Node: "n3", Password: "sk"})
	for _, inst := range m.ListInstances() {
		inst.Status = StatusRunning()
		if inst.Name == "stopped" {
			inst.Status = StatusStopped()
		}
		_ = m.UpdateInstance(inst)
	}

	_ = m.ConfigSet("pool_probe_timeout_sec", "2")

	// 第 1 轮：good 健康，bad 单次失败 → flaky（down 需连续 3 次失败）。
	summary := m.RunPoolQualityOnce(&fakeRunner{})
	if summary.Total != 2 {
		t.Fatalf("total=%d, want 2 (stopped excluded)", summary.Total)
	}
	if summary.Probed != 2 {
		t.Fatalf("probed=%d, want 2", summary.Probed)
	}
	if summary.Healthy != 1 || summary.Flaky != 1 {
		t.Fatalf("round1 healthy=%d flaky=%d, want 1/1", summary.Healthy, summary.Flaky)
	}
	for _, rec := range summary.Records {
		if rec.Name == "good" && rec.Level != qualityHealthy {
			t.Fatalf("good level=%s, want healthy", rec.Level)
		}
		if rec.Name == "bad" && rec.Level != qualityFlaky {
			t.Fatalf("bad level=%s, want flaky", rec.Level)
		}
	}

	// 连跑 3 轮：bad 连续失败累计到 3 → down；good 始终 healthy（恢复路径）。
	for i := 0; i < 2; i++ {
		m.RunPoolQualityOnce(&fakeRunner{})
	}
	round3 := m.RunPoolQualityOnce(&fakeRunner{})
	if round3.Down != 1 || round3.Healthy != 1 {
		t.Fatalf("round3 healthy=%d down=%d, want 1/1", round3.Healthy, round3.Down)
	}
	for _, rec := range round3.Records {
		if rec.Name == "bad" && rec.Level != qualityDown {
			t.Fatalf("bad round3 level=%s, want down", rec.Level)
		}
		if rec.Name == "good" && rec.Level != qualityHealthy {
			t.Fatalf("good round3 level=%s, want healthy", rec.Level)
		}
	}

	// 持久化已落盘且含 2 条。
	loaded := m.loadPoolQuality()
	if len(loaded) != 2 {
		t.Fatalf("persisted %d, want 2", len(loaded))
	}
	for _, rec := range loaded {
		if rec.Name == "bad" && rec.ConsecutiveFailures < 3 {
			t.Fatalf("bad cf=%d, want >=3", rec.ConsecutiveFailures)
		}
	}

	// GET 视图：非 Running 的陈旧记录被过滤。
	_ = m.UpdateInstance(Instance{Name: "good", Port: uint16(mustURLPort(t, goodSrv)), Node: "n1", Password: "sk", Status: StatusStopped()})
	view := m.poolQualityView()
	if view.Total != 1 {
		t.Fatalf("view total=%d, want 1", view.Total)
	}
}

// G9：并发执行 RunPoolQualityOnce（模拟后台探活轮与手动触发同时触发）——
// 探活互斥保证轮次串行，pool_quality.json 不撕裂、样本不丢失（-race 验证无数据竞争）。
func TestRunPoolQualityOnceConcurrent(t *testing.T) {
	m := New(t.TempDir())

	// 探测经实例 API 端口 2xx（通过）。
	goodSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer goodSrv.Close()

	_ = m.AddInstance(Instance{Name: "good", Port: uint16(mustURLPort(t, goodSrv)), Node: "n1", Password: "sk"})
	for _, inst := range m.ListInstances() {
		inst.Status = StatusRunning()
		_ = m.UpdateInstance(inst)
	}
	_ = m.ConfigSet("pool_probe_timeout_sec", "2")

	// 多 goroutine 同时触发探活轮。
	const rounds = 4
	results := make([]PoolQualitySummary, rounds)
	var wg sync.WaitGroup
	for i := 0; i < rounds; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = m.RunPoolQualityOnce(&fakeRunner{})
		}(i)
	}
	wg.Wait()

	// 每轮结果完整（互斥串行，不撕裂）。
	for i, s := range results {
		if s.Total != 1 || s.Healthy != 1 {
			t.Fatalf("round %d total=%d healthy=%d, want 1/1", i, s.Total, s.Healthy)
		}
	}
	// 文件是完整 JSON 且每轮样本都保留（并发不丢失/回退）。
	loaded := m.loadPoolQuality()
	if len(loaded) != 1 {
		t.Fatalf("persisted %d records, want 1", len(loaded))
	}
	if got := len(loaded[0].Samples); got != rounds {
		t.Fatalf("samples=%d, want %d (concurrent rounds must not lose samples)", got, rounds)
	}
}

// ---- G22：后台循环停止句柄 ----

// TestLoopStopHandles G22：StartPoolQualityLoop / StartHealthLoop 返回停止函数，
// 停止后循环退出——后台轮不再写 runtime 文件（文件 mtime 保持静止）。
func TestLoopStopHandles(t *testing.T) {
	t.Run("pool quality", func(t *testing.T) {
		m := newTestManager(t)
		if err := m.saveConfig(Config{PoolProbeIntervalSec: 1}); err != nil {
			t.Fatalf("saveConfig: %v", err)
		}
		stop := m.StartPoolQualityLoop()
		waitLoopWrite(t, m.poolQualityFilePath(), "pool quality probe")
		stop()
		assertLoopStopped(t, m.poolQualityFilePath(), "pool quality loop")
	})
	t.Run("health", func(t *testing.T) {
		m := newTestManager(t)
		if err := m.saveConfig(Config{HealthCheckIntervalSec: 1}); err != nil {
			t.Fatalf("saveConfig: %v", err)
		}
		stop := m.StartHealthLoop()
		waitLoopWrite(t, m.healthFilePath(), "health check")
		stop()
		assertLoopStopped(t, m.healthFilePath(), "health loop")
	})
}

// waitLoopWrite 等到后台轮把结果文件写出来（循环确认在运行）。
func waitLoopWrite(t *testing.T, path, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not write %s", what, path)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// assertLoopStopped 停止后等待超过一轮间隔，文件 mtime 不再变 → 循环已退出。
func assertLoopStopped(t *testing.T, path, what string) {
	t.Helper()
	// 等取消收尾（若恰在轮次中，让该轮落定后再取基准）。
	time.Sleep(300 * time.Millisecond)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	time.Sleep(1600 * time.Millisecond)
	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s after: %v", path, err)
	}
	if after.ModTime().After(before.ModTime()) {
		t.Fatalf("%s still running after stop: mtime %v -> %v", what, before.ModTime(), after.ModTime())
	}
}
