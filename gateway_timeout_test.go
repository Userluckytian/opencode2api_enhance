package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTimeoutConfigDefaults(t *testing.T) {
	cfg := DefaultTimeoutConfig()
	// 默认模式：首字超时固定 10s、静默固定 5s（区间 min=max，不再随机）
	if cfg.RandomTTFT() != 10*time.Second {
		t.Fatalf("TTFT default should be 10s, got %v", cfg.RandomTTFT())
	}
	if cfg.RandomSilence() != 5*time.Second {
		t.Fatalf("silence default should be 5s, got %v", cfg.RandomSilence())
	}
	// 探测数区间
	for i := 0; i < 50; i++ {
		n := cfg.RandomProbeN()
		if n < cfg.ProbeRange[0] || n > cfg.ProbeRange[1] {
			t.Fatalf("probe %d out of range", n)
		}
	}
}

func TestCallStatusText(t *testing.T) {
	if got := CallStatusText(CallRecord{Status: "ok"}); got != "【成功】" {
		t.Fatalf("got %q", got)
	}
	if got := CallStatusText(CallRecord{Status: "fail"}); got != "【失败】" {
		t.Fatalf("got %q", got)
	}
}

func TestCallLogRingBuffer(t *testing.T) {
	l := NewEventLog(3)
	for i := 0; i < 5; i++ {
		l.Append(CallRecord{ReqID: string(rune('a' + i)), Status: "ok"})
	}
	recs := l.ReadAll()
	if len(recs) != 3 {
		t.Fatalf("expected 3, got %d", len(recs))
	}
	if recs[0].ReqID != "c" {
		t.Fatalf("expected oldest dropped, first %q", recs[0].ReqID)
	}
}

func TestCallLogJSONLFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "call_log.jsonl")
	l := NewEventLog(10)
	l.SetPath(path)
	defer l.Stop() // 停掉后台写者，避免测试泄漏 ticker goroutine
	l.Append(CallRecord{ReqID: "r1", Status: "ok", Model: "m1", Events: []CallEvent{{Type: "switch", Node: "a", Detail: "ttft", At: time.Now()}}})
	l.Append(CallRecord{ReqID: "r2", Status: "fail", ErrMsg: "boom"})
	l.Flush() // 异步单写者：读文件前同步排空待写缓冲
	restored, err := LoadCallLogFromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	recs := restored.ReadAll()
	if len(recs) != 2 {
		t.Fatalf("expected 2 after load, got %d", len(recs))
	}
	if recs[1].Status != "fail" || recs[1].ErrMsg != "boom" {
		t.Fatalf("fields lost: %+v", recs[1])
	}
	if len(recs[0].Events) != 1 || recs[0].Events[0].Type != "switch" {
		t.Fatalf("events lost: %+v", recs[0])
	}
}

func TestSetTimeoutConfigFromApp(t *testing.T) {
	// 先重置为默认
	timeoutCfg = DefaultTimeoutConfig()
	cfg := AppConfig{
		TTFTMinMS:    1000,
		TTFTMaxMS:    2000,
		SilenceMinMS: 3000,
		SilenceMaxMS: 5000,
		ProbeMin:     1,
		ProbeMax:     2,
		CallLogMax:   100,
	}
	setTimeoutConfigFromApp(cfg)
	if timeoutCfg.TTFTRange[0] != 1000*time.Millisecond || timeoutCfg.TTFTRange[1] != 2000*time.Millisecond {
		t.Fatalf("TTFT range not applied: %v", timeoutCfg.TTFTRange)
	}
	if timeoutCfg.SilenceRange[0] != 3000*time.Millisecond || timeoutCfg.SilenceRange[1] != 5000*time.Millisecond {
		t.Fatalf("silence range not applied: %v", timeoutCfg.SilenceRange)
	}
	if timeoutCfg.ProbeRange != [2]int{1, 2} {
		t.Fatalf("probe range not applied: %v", timeoutCfg.ProbeRange)
	}
	// 非法区间（min>max）应被忽略，保持旧值
	setTimeoutConfigFromApp(AppConfig{TTFTMinMS: 5000, TTFTMaxMS: 1000})
	if timeoutCfg.TTFTRange[0] != 1000*time.Millisecond {
		t.Fatalf("invalid range should be ignored, got %v", timeoutCfg.TTFTRange)
	}
	// CallLogMax 重置环形上限
	setTimeoutConfigFromApp(AppConfig{CallLogMax: 5})
	if callLog.MaxRecords() != 5 {
		t.Fatalf("callLogMax not applied: %d", callLog.MaxRecords())
	}
	// 恢复，避免影响其他测试
	timeoutCfg = DefaultTimeoutConfig()
	oldLog := callLog
	callLog = NewEventLog(DefaultCallLogMax)
	oldLog.Stop() // 停掉残留后台写者
	_ = os.Remove("call_log.jsonl")
}

func TestBuildResumeBody(t *testing.T) {
	body := []byte(`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	resumed := buildResumeBody(body, "已生成内容ABC")
	var m map[string]any
	if err := json.Unmarshal(resumed, &m); err != nil {
		t.Fatal(err)
	}
	msgs, _ := m["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}
	last := msgs[2].(map[string]any)
	if last["role"] != "user" || !strings.Contains(last["content"].(string), "请继续") {
		t.Fatalf("resume tail malformed: %v", last)
	}
	asst := msgs[1].(map[string]any)
	if asst["role"] != "assistant" || asst["content"] != "已生成内容ABC" {
		t.Fatalf("assistant context missing: %v", asst)
	}
	// 原始 body 未被修改
	if string(body) != `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}` {
		t.Fatalf("original body mutated")
	}
}

// streamWithResume：正常上游（立即吐 [DONE]）应成功完成，不触发切换
func TestStreamWithResumeNormal(t *testing.T) {
	// 保存并缩短超时
	orig := timeoutCfg
	timeoutCfg = TimeoutConfig{
		TTFTRange:    [2]time.Duration{500 * time.Millisecond, 600 * time.Millisecond},
		SilenceRange: [2]time.Duration{300 * time.Millisecond, 400 * time.Millisecond},
		ProbeRange:   [2]int{2, 3},
	}
	defer func() { timeoutCfg = orig }()

	// 开启节点前缀展示（默认关闭，此处显式开启验证前缀逻辑）
	configMu.Lock()
	showNodePrefix = true
	configMu.Unlock()
	defer func() {
		configMu.Lock()
		showNodePrefix = false
		configMu.Unlock()
	}()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fl, _ := w.(http.Flusher)
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")
		fl.Flush()
		fmt.Fprintf(w, "data: [DONE]\n\n")
		fl.Flush()
	}))
	defer srv.Close()

	// 用真实 client 请求 mock 上游获得 SSE body
	body := []byte(`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	// 直接构造 initial: 手动请求 mock 服务器
	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	callRec := &CallRecord{ReqID: "test-1", Model: "m"}
	res := streamWithResume(rr, req, body, "m", UpstreamAuth{Mode: AuthRoutePublic}, resp.Body, "127.0.0.1:28100", false, callRec)
	if !res.OK {
		t.Fatalf("expected OK, got %+v", res)
	}
	if res.Switched {
		t.Fatalf("unexpected switch")
	}
	if !strings.Contains(rr.Body.String(), "hello") {
		t.Fatalf("expected hello in output, got %s", rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "[DONE]") {
		t.Fatalf("expected [DONE] in output")
	}
	// SSE 事件必须以 \n\n 分隔（严格的 OpenAI 兼容客户端要求事件间空行）
	outStr := rr.Body.String()
	if !strings.Contains(outStr, "\n\n") {
		t.Fatalf("SSE events must be separated by \\n\\n, got raw: %q", outStr)
	}
	// data 行后必须紧跟空行：data: {...}\n\n
	if !strings.Contains(outStr, "}\n\n") {
		t.Fatalf("expected '}\\n\\n' between events, got: %q", outStr)
	}
	// 节点/模型标识前缀：首个内容 chunk 前应插入 🤖 节点 · 模型
	if !strings.Contains(outStr, "🤖") {
		t.Fatalf("expected node/model label 🤖 in output, got: %q", outStr)
	}
	if !strings.Contains(outStr, "· m") {
		t.Fatalf("expected model name in label, got: %q", outStr)
	}
	// 首次连接的前缀应显示实际节点地址（而非"未知节点"）
	if !strings.Contains(outStr, "127.0.0.1:28100") {
		t.Fatalf("expected node addr in label (not 未知节点), got: %q", outStr)
	}
	if strings.Contains(outStr, "未知节点") {
		t.Fatalf("should not show 未知节点 when addr known, got: %q", outStr)
	}
}

// streamWithResume：开关默认关闭（OFF）时，首个内容 chunk 不应插入 🤖 前缀
func TestStreamWithResumePrefixOffByDefault(t *testing.T) {
	// 确保开关为默认 false（不手动开启）
	configMu.Lock()
	showNodePrefix = false
	configMu.Unlock()
	defer func() {
		configMu.Lock()
		showNodePrefix = false
		configMu.Unlock()
	}()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fl, _ := w.(http.Flusher)
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")
		fl.Flush()
		fmt.Fprintf(w, "data: [DONE]\n\n")
		fl.Flush()
	}))
	defer srv.Close()

	body := []byte(`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	callRec := &CallRecord{ReqID: "test-off", Model: "m"}
	res := streamWithResume(rr, req, body, "m", UpstreamAuth{Mode: AuthRoutePublic}, resp.Body, "127.0.0.1:28100", false, callRec)
	if !res.OK {
		t.Fatalf("expected OK, got %+v", res)
	}
	outStr := rr.Body.String()
	if !strings.Contains(outStr, "hello") {
		t.Fatalf("expected hello in output, got %s", outStr)
	}
	// 默认关闭：不得出现节点/模型前缀
	if strings.Contains(outStr, "🤖") {
		t.Fatalf("prefix should be suppressed when show_node_prefix is off, got: %q", outStr)
	}
}

// buildResumeBody 对非法 JSON 应原样返回
func TestBuildResumeBodyInvalidJSON(t *testing.T) {
	bad := []byte(`not-json`)
	if got := buildResumeBody(bad, "x"); string(got) != string(bad) {
		t.Fatalf("invalid json should pass through")
	}
}

// streamWithResume：上游中断（EOF 无 [DONE]）→ 续写重连 → 成功
// 验证 switch 事件记录与续写 body 构造
func TestStreamWithResumeSwitchOnInterrupt(t *testing.T) {
	transport := installFakeOpenCodeClient(t, []fakeUpstreamResponse{
		// 第一次：吐一句后 EOF 无 [DONE] → 触发中断（有 accumulated 内容）
		{status: http.StatusOK, body: "data: {\"choices\":[{\"delta\":{\"content\":\"第一句\"}}]}\n\n", header: http.Header{"Content-Type": {"text/event-stream"}}},
		// 第二次：正常 SSE（含续写后内容）
		{status: http.StatusOK, body: "data: {\"choices\":[{\"delta\":{\"content\":\"续写内容\"}}]}\n\ndata: [DONE]\n\n", header: http.Header{"Content-Type": {"text/event-stream"}}},
	})

	// 保存并缩短超时
	orig := timeoutCfg
	timeoutCfg = TimeoutConfig{
		TTFTRange:    [2]time.Duration{500 * time.Millisecond, 600 * time.Millisecond},
		SilenceRange: [2]time.Duration{300 * time.Millisecond, 400 * time.Millisecond},
		ProbeRange:   [2]int{2, 3},
	}
	defer func() { timeoutCfg = orig }()

	body := []byte(`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	callRec := &CallRecord{ReqID: "test-2", Model: "m"}

	// initial 为 nil → 直接走第一次 callOpenCodeAPIStream
	res := streamWithResume(rr, req, body, "m", UpstreamAuth{Mode: AuthRoutePublic}, nil, "", false, callRec)
	if !res.OK {
		t.Fatalf("expected OK after resume, got %+v", res)
	}
	if !res.Switched {
		t.Fatalf("expected switch flag")
	}
	if !strings.Contains(rr.Body.String(), "续写内容") {
		t.Fatalf("expected resumed content, got: %s", rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "第一句") {
		t.Fatalf("expected first-part content forwarded, got: %s", rr.Body.String())
	}
	// 事件应有 stream_interrupt 和 switch
	foundInterrupt := false
	foundSwitch := false
	for _, ev := range callRec.Events {
		if ev.Type == "stream_interrupt" {
			foundInterrupt = true
		}
		if ev.Type == "switch" {
			foundSwitch = true
		}
	}
	if !foundInterrupt || !foundSwitch {
		t.Fatalf("expected stream_interrupt + switch events, got %+v", callRec.Events)
	}
	// 第二次请求的 payload 应包含续写消息（assistant 历史 + 请继续）
	if len(transport.requestPayloads) < 2 {
		t.Fatalf("expected 2 upstream requests, got %d", len(transport.requestPayloads))
	}
	msgs, _ := transport.requestPayloads[1]["messages"].([]any)
	if len(msgs) < 3 {
		t.Fatalf("expected resume messages, got %v", transport.requestPayloads[1]["messages"])
	}
}

// 问题7：上游在流中途以 SSE error 事件报告故障（免费额度用尽）+ [DONE]，
// 网关应识别为中断、切节点续写，而不是当作成功完成直接结束。
func TestStreamWithResumeSwitchOnSSEError(t *testing.T) {
	transport := installFakeOpenCodeClient(t, []fakeUpstreamResponse{
		// 第一次：200 建立，吐一句内容后发 SSE error（额度用尽）+ [DONE]
		{status: http.StatusOK, body: "data: {\"choices\":[{\"delta\":{\"content\":\"开头内容\"}}]}\n\ndata: {\"error\":{\"message\":\"免费额度已用尽（Rate limit exceeded）\",\"code\":\"rate_limit_exceeded\"}}\n\ndata: [DONE]\n\n", header: http.Header{"Content-Type": {"text/event-stream"}}},
		// 第二次：正常 SSE（切节点后续写）
		{status: http.StatusOK, body: "data: {\"choices\":[{\"delta\":{\"content\":\"续写内容\"}}]}\n\ndata: [DONE]\n\n", header: http.Header{"Content-Type": {"text/event-stream"}}},
	})

	orig := timeoutCfg
	timeoutCfg = TimeoutConfig{
		TTFTRange:    [2]time.Duration{500 * time.Millisecond, 600 * time.Millisecond},
		SilenceRange: [2]time.Duration{300 * time.Millisecond, 400 * time.Millisecond},
		ProbeRange:   [2]int{2, 3},
	}
	defer func() { timeoutCfg = orig }()

	body := []byte(`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	callRec := &CallRecord{ReqID: "test-sse-err", Model: "m"}

	res := streamWithResume(rr, req, body, "m", UpstreamAuth{Mode: AuthRoutePublic}, nil, "", false, callRec)
	if !res.OK {
		t.Fatalf("expected OK after switch on SSE error, got %+v", res)
	}
	if !res.Switched {
		t.Fatalf("expected switch flag on SSE error")
	}
	if !strings.Contains(rr.Body.String(), "续写内容") {
		t.Fatalf("expected resumed content, got: %s", rr.Body.String())
	}
	// 事件应有 stream_error 和 switch
	foundErr := false
	foundSwitch := false
	for _, ev := range callRec.Events {
		if ev.Type == "stream_error" {
			foundErr = true
		}
		if ev.Type == "switch" {
			foundSwitch = true
		}
	}
	if !foundErr || !foundSwitch {
		t.Fatalf("expected stream_error + switch events, got %+v", callRec.Events)
	}
	// 第二次请求的 payload 应包含续写消息
	if len(transport.requestPayloads) < 2 {
		t.Fatalf("expected 2 upstream requests, got %d", len(transport.requestPayloads))
	}
	msgs, _ := transport.requestPayloads[1]["messages"].([]any)
	if len(msgs) < 3 {
		t.Fatalf("expected resume messages, got %v", transport.requestPayloads[1]["messages"])
	}
}

// 坏状态码：429 连续 3 次 → 节点进坏池（badReason 非空），pickHealthyProxy 跳过
func TestBadStatusCodeEntersBadPool(t *testing.T) {
	// 重置健康表
	socks5HealthMu.Lock()
	socks5Health = map[string]socks5HealthState{}
	socks5HealthMu.Unlock()

	// 第一次 429：badCount=1，未进坏池，临时冷却 45s
	markSocks5Result("127.0.0.1:28100", http.StatusTooManyRequests, nil)
	markSocks5Result("127.0.0.1:28100", http.StatusTooManyRequests, nil)
	socks5HealthMu.Lock()
	st := socks5Health["127.0.0.1:28100"]
	socks5HealthMu.Unlock()
	if st.badReason != "" {
		t.Fatalf("should not be in bad pool yet, got %q", st.badReason)
	}
	if st.badCount != 2 {
		t.Fatalf("badCount = %d, want 2", st.badCount)
	}

	// 第三次 429：进坏池
	markSocks5Result("127.0.0.1:28100", http.StatusTooManyRequests, nil)
	socks5HealthMu.Lock()
	st = socks5Health["127.0.0.1:28100"]
	socks5HealthMu.Unlock()
	if st.badReason == "" {
		t.Fatal("expected bad pool after 3rd 429")
	}
	if !strings.Contains(st.badReason, "429") {
		t.Fatalf("badReason should mention 429, got %q", st.badReason)
	}

	// pickHealthyProxy 应跳过坏池节点，选择另一个健康节点
	proxies := []Socks5Proxy{
		{Addr: "127.0.0.1:28100"},
		{Addr: "127.0.0.1:28101"},
	}
	pick := pickHealthyProxy(proxies, 0)
	if pick.Addr != "127.0.0.1:28101" {
		t.Fatalf("expected skip bad pool node, got %s", pick.Addr)
	}

	// 清理
	socks5HealthMu.Lock()
	delete(socks5Health, "127.0.0.1:28100")
	socks5HealthMu.Unlock()
}

// 401 也进坏池
func TestBadStatusCode401EntersBadPool(t *testing.T) {
	socks5HealthMu.Lock()
	socks5Health = map[string]socks5HealthState{}
	socks5HealthMu.Unlock()

	for i := 0; i < 3; i++ {
		markSocks5Result("127.0.0.1:28102", http.StatusUnauthorized, nil)
	}
	socks5HealthMu.Lock()
	st := socks5Health["127.0.0.1:28102"]
	socks5HealthMu.Unlock()
	if st.badReason == "" || !strings.Contains(st.badReason, "401") {
		t.Fatalf("expected 401 bad pool, got %q", st.badReason)
	}
	socks5HealthMu.Lock()
	delete(socks5Health, "127.0.0.1:28102")
	socks5HealthMu.Unlock()
}

// 正常状态（2xx）应清除坏池标记（节点恢复）
func TestSuccessClearsBadPool(t *testing.T) {
	socks5HealthMu.Lock()
	socks5Health = map[string]socks5HealthState{}
	socks5HealthMu.Unlock()

	for i := 0; i < 3; i++ {
		markSocks5Result("127.0.0.1:28103", http.StatusTooManyRequests, nil)
	}
	socks5HealthMu.Lock()
	st := socks5Health["127.0.0.1:28103"]
	socks5HealthMu.Unlock()
	if st.badReason == "" {
		t.Fatal("expected bad pool first")
	}

	// 2xx 成功应清除（手动恢复场景：实例重启后重新标记）
	markSocks5Result("127.0.0.1:28103", http.StatusOK, nil)
	socks5HealthMu.Lock()
	st = socks5Health["127.0.0.1:28103"]
	socks5HealthMu.Unlock()
	if st.badReason != "" {
		t.Fatalf("2xx should clear bad pool, got %q", st.badReason)
	}
}