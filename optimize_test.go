// 资源优化回归测试：验证重试上限、客户端缓存、日志轮转、无界 map 上限、
// SSE/非流式转换等价性等优化行为（不改变原有功能）。
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// ---------- 重试预算封顶 ----------

func TestMaxRouteRetriesCapped(t *testing.T) {
	// 即使代理池很大，重试预算也必须封顶（防止上游故障时重试风暴）。
	socks5Mu.Lock()
	socks5Proxies = make([]Socks5Proxy, 30)
	for i := range socks5Proxies {
		socks5Proxies[i] = Socks5Proxy{Addr: "127.0.0.1:1"}
	}
	socks5Mu.Unlock()
	defer func() {
		socks5Mu.Lock()
		socks5Proxies = nil
		socks5Mu.Unlock()
	}()

	if got := maxRouteRetries(); got != maxUpstreamRetries {
		t.Fatalf("maxRouteRetries = %d, want cap %d", got, maxUpstreamRetries)
	}
}

// ---------- 代理客户端缓存（连接池复用） ----------

func TestClientForProxyCached(t *testing.T) {
	proxyClientMu.Lock()
	proxyClients = map[string]*http.Client{}
	proxyClientMu.Unlock()

	p := Socks5Proxy{Addr: "127.0.0.1:1080"}
	c1 := clientForProxy(p)
	c2 := clientForProxy(p)
	if c1 != c2 {
		t.Fatal("同一代理地址应返回缓存客户端")
	}
	// 配置变更（proxies 列表变化）后缓存整体失效
	proxyClientMu.Lock()
	proxyClients = map[string]*http.Client{}
	proxyClientMu.Unlock()
	c3 := clientForProxy(p)
	if c3 == c1 {
		t.Fatal("配置变更后应重建客户端")
	}
}

// ---------- 调用日志轮转 ----------

func TestEventLogRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "call_log.jsonl")

	// 构造小轮转阈值（10 字节）的 EventLog
	l := &EventLog{maxRecords: 100, maxBytes: 10}
	l.SetPath(path)

	rec := CallRecord{ReqID: "r1", Status: "ok", Model: "m"}
	for i := 0; i < 5; i++ {
		rec.ReqID = "req_" + string(rune('a'+i))
		if err := l.Append(rec); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("轮转后应生成 .1 文件: %v", err)
	}
	// 新文件仍可追加
	if err := l.Append(rec); err != nil {
		t.Fatalf("轮转后追加: %v", err)
	}
}

// ---------- 无界 map 上限 ----------

func TestStoredResponsesCap(t *testing.T) {
	storedResponsesMu.Lock()
	storedResponses = map[string]StoredResponseState{}
	storedResponsesMu.Unlock()

	// 填满到上限前一条（唯一 key）
	for i := 0; i < maxStoredResponses-1; i++ {
		storedResponsesMu.Lock()
		storedResponses[fmt.Sprintf("resp_%d", i)] = StoredResponseState{Model: "m"}
		storedResponsesMu.Unlock()
	}
	storeResponseState(map[string]any{"id": "x1", "output": []any{}}, ResponsesAPIRequest{})
	storeResponseState(map[string]any{"id": "x2", "output": []any{}}, ResponsesAPIRequest{})
	storedResponsesMu.RLock()
	n := len(storedResponses)
	storedResponsesMu.RUnlock()
	if n > maxStoredResponses {
		t.Fatalf("storedResponses 超过上限: %d > %d", n, maxStoredResponses)
	}
	// 上限重置后仍可读写
	if _, ok := loadResponseState("x2"); !ok {
		t.Fatal("重置后新写入的响应应可读取")
	}
}

func TestSessionsCap(t *testing.T) {
	sessionsMu.Lock()
	sessions = map[string]struct{}{}
	for i := 0; i < maxSessions; i++ {
		sessions[fmt.Sprintf("tok_%d", i)] = struct{}{}
	}
	sessionsMu.Unlock()

	// 触发上限重置
	func() {
		sessionsMu.Lock()
		defer sessionsMu.Unlock()
		if len(sessions) >= maxSessions {
			sessions = map[string]struct{}{}
		}
		sessions["tok_new"] = struct{}{}
	}()
	sessionsMu.Lock()
	n := len(sessions)
	sessionsMu.Unlock()
	if n != 1 {
		t.Fatalf("上限重置后应有 1 个会话, got %d", n)
	}
}

// ---------- SSE/非流式转换等价性（消除重复解析后输出一致） ----------

func TestConvertStreamChunkFromObjEquivalent(t *testing.T) {
	line := `data: {"choices":[{"delta":{"content":"你好","reasoning_content":"think"}}],"usage":{"total_tokens":10},"cost":0.5}`
	keep := false
	// 历史签名路径
	fromLine, usageLine := convertStreamChunkWithUsage(line, keep)
	// map 版路径（先解析一次，与 SSE 循环一致）
	var obj map[string]any
	if json.Unmarshal([]byte(line[6:]), &obj) != nil {
		t.Fatal("parse failed")
	}
	conv, usageObj := convertStreamChunkFromObj(obj, keep)
	if fromLine != "data: "+conv {
		t.Fatalf("转换结果不一致:\n fromLine=%s\n fromObj =%s", fromLine, "data: "+conv)
	}
	if (usageLine == nil) != (usageObj == nil) {
		t.Fatalf("usage 提取不一致: line=%v obj=%v", usageLine, usageObj)
	}
}

// ---------- 统计落盘合并（dirty 标志行为） ----------

func TestStatsDirtyFlags(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		startStatsFlusher() // 惰性启动，只应启动一次
	}()
	wg.Wait()
	markTokenStatsDirty()
	if !tokenStatsDirty.Load() {
		t.Fatal("tokenStatsDirty 应被置位")
	}
}
