package main

// 阶段 8：复现重放工具（debug_replay）。
//
// 两种模式：
//   - 生成：`debug_replay -from-bundle <bundle> -out <fixture>` 从阶段5现场包一键生成脱敏 fixture；
//   - 重放：`debug_replay -fixture <fixture> -target <baseURL>` 用当前构建重放，携带同一 X-Trace-ID 以便日志对照。
// 真实上游端到端属真机验证；本层用可注入 client 支持本地链路重放与单测。纯标准库。

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ReplayFixture 一次可重放的脱敏请求 + 期望行为。
type ReplayFixture struct {
	TraceID            string            `json:"trace_id"`
	Method             string            `json:"method"`
	Path               string            `json:"path"`
	Model              string            `json:"model"`
	Stream             bool              `json:"stream"`
	Headers            map[string]string `json:"headers,omitempty"`
	ExpectStatus       int               `json:"expect_status,omitempty"`
	ExpectRouteVerdict string            `json:"expect_route_verdict,omitempty"`
	Source             string            `json:"source,omitempty"`
}

// guessStatus 从 bundle 的错误文本里提取代表性 HTTP 状态码（无则 0 = 不校验具体码）。
func guessStatus(b PostmortemBundle) int {
	blob := b.ErrMsg
	for _, e := range b.Events {
		blob += " " + e.Detail
	}
	for _, code := range []int{408, 401, 403, 429, 500, 502, 503} {
		if strings.Contains(blob, strconv.Itoa(code)) {
			return code
		}
	}
	return 0
}

// BundleToFixture 从失败现场包生成重放 fixture。
func BundleToFixture(b PostmortemBundle, source string) ReplayFixture {
	method := http.MethodPost
	if b.Path == "/v1/models" {
		method = http.MethodGet
	}
	return ReplayFixture{
		TraceID:            b.TraceID,
		Method:             method,
		Path:               b.Path,
		Model:              b.Model,
		Stream:             b.Stream,
		ExpectStatus:       guessStatus(b),
		ExpectRouteVerdict: b.RouteVerdict,
		Source:             source,
	}
}

// SaveFixture / LoadFixture fixture 落盘与读取。
func SaveFixture(path string, f ReplayFixture) error {
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

func LoadFixture(path string) (ReplayFixture, error) {
	var f ReplayFixture
	b, err := os.ReadFile(path)
	if err != nil {
		return f, err
	}
	err = json.Unmarshal(b, &f)
	return f, err
}

// ReplayRequest 向 targetBase 重放一次 fixture 请求，携带同一 X-Trace-ID。
// 返回 HTTP 状态码与响应回显的 trace_id；client 可注入（测试用 mock server）。
func ReplayRequest(f ReplayFixture, targetBase string, client *http.Client) (int, string, error) {
	url := strings.TrimRight(targetBase, "/") + f.Path
	var body io.Reader
	if f.Method == http.MethodPost {
		payload := map[string]any{
			"model":    f.Model,
			"messages": []map[string]string{{"role": "user", "content": "replay-probe"}},
			"stream":   f.Stream,
		}
		if b, err := json.Marshal(payload); err == nil {
			body = bytes.NewReader(b)
		}
	}
	req, err := http.NewRequest(f.Method, url, body)
	if err != nil {
		return 0, "", err
	}
	if f.TraceID != "" {
		req.Header.Set("X-Trace-ID", f.TraceID)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range f.Headers {
		req.Header.Set(k, v)
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, resp.Header.Get("X-Trace-ID"), nil
}

// replayMatches 判定重放结果是否符合期望：ExpectStatus=0 时仅要求失败(>=400)。
func replayMatches(f ReplayFixture, status int) bool {
	if f.ExpectStatus == 0 {
		return status >= 400
	}
	return status == f.ExpectStatus
}

func orNA(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

// genFixtureFromBundle 从 postmortem bundle 生成 fixture；outPath 空则默认 <bundle>.fixture.json。
func genFixtureFromBundle(bundlePath, outPath string) int {
	raw, err := os.ReadFile(bundlePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "read bundle failed:", err)
		return 2
	}
	var b PostmortemBundle
	if err := json.Unmarshal(raw, &b); err != nil {
		fmt.Fprintln(os.Stderr, "parse bundle failed:", err)
		return 2
	}
	if outPath == "" {
		outPath = bundlePath + ".fixture.json"
	}
	f := BundleToFixture(b, filepath.Base(bundlePath))
	if err := SaveFixture(outPath, f); err != nil {
		fmt.Fprintln(os.Stderr, "save fixture failed:", err)
		return 2
	}
	fmt.Printf("fixture generated: %s (trace=%s expect=%d)\n", outPath, orNA(f.TraceID), f.ExpectStatus)
	return 0
}

// runReplay debug_replay 子命令入口：生成模式（-from-bundle）或重放模式（-fixture -target）。
func runReplay(args []string) int {
	fs := flag.NewFlagSet("debug_replay", flag.ContinueOnError)
	fxPath := fs.String("fixture", "", "fixture json 路径（重放模式）")
	target := fs.String("target", "", "重放目标 base URL，如 http://127.0.0.1:40080")
	fromBundle := fs.String("from-bundle", "", "从 postmortem bundle 生成 fixture")
	out := fs.String("out", "", "生成的 fixture 输出路径（配合 -from-bundle）")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	// 生成模式
	if *fromBundle != "" {
		return genFixtureFromBundle(*fromBundle, *out)
	}
	// 重放模式
	if *fxPath == "" || *target == "" {
		fmt.Fprintln(os.Stderr, "usage: opencode2api debug_replay -from-bundle <bundle> [-out <fixture>]")
		fmt.Fprintln(os.Stderr, "   or: opencode2api debug_replay -fixture <fixture> -target <baseURL>")
		return 2
	}
	f, err := LoadFixture(*fxPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "load fixture failed:", err)
		return 2
	}
	status, gotTrace, err := ReplayRequest(f, *target, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "replay request failed:", err)
		return 2
	}
	matched := replayMatches(f, status)
	fmt.Printf("replay %s %s → HTTP %d (expect %d) trace=%s matched=%v\n",
		f.Method, f.Path, status, f.ExpectStatus, orNA(gotTrace), matched)
	if matched {
		return 0
	}
	return 1
}
