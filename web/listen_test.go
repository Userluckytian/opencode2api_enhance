package main

import "testing"

// 监听地址收敛（finding 547 / fact 589、592）：
// 默认必须只监听回环；显式 0.0.0.0 才对外；非回环 + -require-loopback 拒绝启动。

func TestResolveListenAddrPrecedence(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Listen = "10.0.0.5"

	// 1) 默认（无 flag、无环境变量、config 有值）→ 取 config
	t.Setenv(EnvListen, "")
	addr, src := resolveListenAddr("", cfg)
	if addr != "10.0.0.5" || src != listenSourceConfig {
		t.Fatalf("config source: addr=%q src=%q", addr, src)
	}

	// 2) 环境变量优先于 config
	t.Setenv(EnvListen, "192.168.1.9")
	addr, src = resolveListenAddr("", cfg)
	if addr != "192.168.1.9" || src != listenSourceEnv {
		t.Fatalf("env source: addr=%q src=%q", addr, src)
	}

	// 3) 命令行优先于环境变量
	addr, src = resolveListenAddr("0.0.0.0", cfg)
	if addr != "0.0.0.0" || src != listenSourceFlag {
		t.Fatalf("flag source: addr=%q src=%q", addr, src)
	}

	// 4) 都没配 → 回环默认
	empty := &Config{}
	t.Setenv(EnvListen, "")
	addr, src = resolveListenAddr("", empty)
	if addr != DefaultListenAddress || src != listenSourceDefault {
		t.Fatalf("default source: addr=%q src=%q", addr, src)
	}
}

func TestNormalizeListenHost(t *testing.T) {
	cases := map[string]string{
		"0.0.0.0":         "0.0.0.0",
		"0.0.0.0:8080":    "0.0.0.0",
		"127.0.0.1:23099": "127.0.0.1",
		" [::1] ":         "::1",
		"::1":             "::1",
		"":                DefaultListenAddress,
		"   ":             DefaultListenAddress,
		"example.com:80":  "example.com",
	}
	for in, want := range cases {
		if got := normalizeListenHost(in); got != want {
			t.Fatalf("normalizeListenHost(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsLoopbackListen(t *testing.T) {
	loop := []string{"127.0.0.1", "127.0.0.2", "::1", "[::1]", "localhost"}
	loop = append(loop, "")
	for _, a := range loop {
		if !isLoopbackListen(a) {
			t.Fatalf("%q should be loopback", a)
		}
	}
	// 通配地址 = 对外，不能当成回环
	// 注意：空串会被规整为默认回环（与 resolveListenAddr 的默认语义一致）。
	public := []string{"0.0.0.0", "::", "192.168.12.17", "example.com", "10.0.0.5"}
	for _, a := range public {
		if isLoopbackListen(a) {
			t.Fatalf("%q must NOT be treated as loopback", a)
		}
	}
}

func TestCheckListenPolicy(t *testing.T) {
	// 回环：两种模式都放行
	if err := checkListenPolicy("127.0.0.1", true); err != nil {
		t.Fatalf("loopback should pass: %v", err)
	}
	if err := checkListenPolicy("127.0.0.1", false); err != nil {
		t.Fatalf("loopback should pass: %v", err)
	}
	// 非回环 + require-loopback → 拒绝启动
	if err := checkListenPolicy("0.0.0.0", true); err == nil {
		t.Fatal("expected error for non-loopback with requireLoopback")
	}
	// 非回环 + 正式模式 → 不阻断（由 nginx/防火墙兜底）
	if err := checkListenPolicy("0.0.0.0", false); err != nil {
		t.Fatalf("non-loopback prod mode should not block: %v", err)
	}
}

func TestDefaultConfigListensOnLoopback(t *testing.T) {
	c := DefaultConfig()
	if c.Listen != DefaultListenAddress {
		t.Fatalf("default listen = %q, want %q", c.Listen, DefaultListenAddress)
	}
}
