package manager

import (
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

func TestConfigGatewayPortSetAndValidate(t *testing.T) {
	m := New(t.TempDir())
	// 默认回退
	if p := m.managerGatewayPort(); p != unifiedGatewayPort {
		t.Fatalf("default port = %d", p)
	}
	// 非法端口报错
	if err := m.ConfigSet("gateway_port", "70000"); err == nil {
		t.Fatal("port > 65535 should error")
	}
	if err := m.ConfigSet("gateway_port", "abc"); err == nil {
		t.Fatal("non-numeric port should error")
	}
	// 合法设置
	if err := m.ConfigSet("gateway_port", "50123"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if cfg := m.loadConfig(); cfg.GatewayPort != 50123 {
		t.Fatalf("persisted port = %d", cfg.GatewayPort)
	}
	v, err := m.ConfigGet("gateway_port")
	if err != nil || v != "50123" {
		t.Fatalf("ConfigGet port = %q err=%v", v, err)
	}
	// 空串重置默认
	if err := m.ConfigSet("gateway_port", ""); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if cfg := m.loadConfig(); cfg.GatewayPort != 0 {
		t.Fatalf("after reset = %d", cfg.GatewayPort)
	}
}

func TestNewGatewayUsesConfiguredPort(t *testing.T) {
	m := New(t.TempDir())
	_ = m.ConfigSet("gateway_port", "50123")
	gw := NewGateway(m, 0)
	if gw.Port() != 50123 {
		t.Fatalf("gateway port = %d", gw.Port())
	}
}

// T5: 设置端口后网关内存端口立即更新（未运行时静默成功；下次启动用新端口）。
func TestApplyPortUpdatesMemoryPort(t *testing.T) {
	m := New(t.TempDir())
	run := &fakeRunner{}
	gw := NewGateway(m, 0)
	if err := gw.ApplyPort(50123, run); err != nil {
		t.Fatalf("apply when not running: %v", err)
	}
	if gw.Port() != 50123 {
		t.Fatalf("port = %d, want 50123", gw.Port())
	}
	if len(run.starts) != 0 {
		t.Fatalf("must not start when not running, got %+v", run.starts)
	}
	// 经 ConfigSet 保存后，再次构造网关应使用新端口（落盘生效）
	_ = m.ConfigSet("gateway_port", "50123")
	if cfg := m.loadConfig(); cfg.GatewayPort != 50123 {
		t.Fatalf("persisted port = %d", cfg.GatewayPort)
	}
}

// T5: 网关运行中 ApplyPort 必须热重启子进程：旧进程被停、新进程带新端口参数。
// （portRunner 的 Start 返回测试进程自身 pid，pidAlive 恒真 → 可模拟运行中状态。）
func TestApplyPortHotRestartsRunningGateway(t *testing.T) {
	m := newTestManager(t)
	run := &fakeRunner{}
	ln1, ln2 := occupyPort(t, 29901), occupyPort(t, 29901+singboxPortOffset)
	defer ln1.Close()
	defer ln2.Close()
	runningInstanceHeld(t, m, run, "p1", 29901, true, ln1, ln2)

	gw := NewGateway(m, 40080)
	rec := &portRunner{}

	// 网关先进入运行中（Status 自动拉起 = 第 1 次 spawn）
	gw.Status(rec)
	rec.mu.Lock()
	pre := rec.gateway
	rec.mu.Unlock()
	if pre != 1 {
		t.Fatalf("initial gateway spawns = %d, want 1", pre)
	}

	// 运行中改端口 → 必须 stop 旧进程 + 以新端口重启
	if err := gw.ApplyPort(50234, rec); err != nil {
		t.Fatalf("ApplyPort: %v", err)
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.gateway != pre+1 {
		t.Fatalf("gateway spawns = %d, want %d (热重启 1 次)", rec.gateway, pre+1)
	}
	if len(rec.kills) != 1 {
		t.Fatalf("kills = %v, want exactly 1 (替换的旧子进程)", rec.kills)
	}
	for _, k := range rec.kills {
		if k != os.Getpid() {
			t.Fatalf("killed unexpected pid %d", k)
		}
	}
	if gw.Port() != 50234 {
		t.Fatalf("port = %d, want 50234", gw.Port())
	}
	// 最后一次网关 spawn 的 -port 参数必须是新端口
	if last, ok := rec.lastGatewaySpec(); !ok || !specPortIs(last, 50234) {
		t.Fatalf("last gateway spawn lacks -port 50234: %+v", rec.specs)
	}
}

// portRunner 记录网关 Start/Kill 及 ExecSpec；Start 返回自身 pid（pidAlive 恒真）。
type portRunner struct {
	mu      sync.Mutex
	starts  int
	gateway int
	kills   []int
	specs   []ExecSpec
}

func (r *portRunner) Start(spec ExecSpec) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.starts++
	if hasGatewayStart([]ExecSpec{spec}) {
		r.gateway++
	}
	r.specs = append(r.specs, spec)
	return os.Getpid(), nil
}

func (r *portRunner) Kill(pid int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.kills = append(r.kills, pid)
	return nil
}

// lastGatewaySpec 返回最近一次网关 spawn 的 ExecSpec。
func (r *portRunner) lastGatewaySpec() (ExecSpec, bool) {
	for i := len(r.specs) - 1; i >= 0; i-- {
		if hasGatewayStart([]ExecSpec{r.specs[i]}) {
			return r.specs[i], true
		}
	}
	return ExecSpec{}, false
}

// specPortIs 检查 ExecSpec 是否带指定 -port 参数。
func specPortIs(spec ExecSpec, want uint16) bool {
	for i, a := range spec.Args {
		if a == "-port" && i+1 < len(spec.Args) && spec.Args[i+1] == itoa(want) {
			return true
		}
	}
	return false
}

func TestGatewayPortHandlerHTTP(t *testing.T) {
	m := New(t.TempDir())
	h := m.ConfigSetHandler()
	// 非法端口 → 400
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/admin/config/set", strings.NewReader(`{"key":"gateway_port","value":"99999"}`))
	h(rec, req)
	if rec.Code != 400 {
		t.Fatalf("invalid port code=%d body=%s", rec.Code, rec.Body.String())
	}
	// 合法 → 200
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("POST", "/api/admin/config/set", strings.NewReader(`{"key":"gateway_port","value":"50123"}`))
	h(rec2, req2)
	if rec2.Code != 200 {
		t.Fatalf("set code=%d body=%s", rec2.Code, rec2.Body.String())
	}
	// ConfigView：gateway_port 回显数值
	gh := m.ConfigGetHandler()
	rec3 := httptest.NewRecorder()
	req3 := httptest.NewRequest("GET", "/api/admin/config", nil)
	gh(rec3, req3)
	body := rec3.Body.String()
	if !strings.Contains(body, `"gateway_port":50123`) {
		t.Fatalf("view body = %s", body)
	}
}
