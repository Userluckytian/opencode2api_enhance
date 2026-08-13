// TMaily 临时邮箱客户端（移植 tmaily.rs）。
// API：GET /domains、GET /generate?force&domain&prefix、GET /emails?address=。
package windsurf

import (
	"context"
	crand "crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"time"
)

var tmailyBase = "https://tmaily.com"

// maxGenerateRecursion session_not_found 递归重试上限（防无限递归 + 外部请求风暴）。
const maxGenerateRecursion = 3

var tmailyUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/150.0.0.0 Safari/537.36"

var tmailyFallbackDomains = []string{
	"hqpdf.com", "2048unblocked.com", "watersoftenersystemcost.com",
	"imgcompress.io", "10timer.com", "manglgih.com", "manglit.it.com",
}

var (
	codeRe = regexp.MustCompile(`\b(\d{6})\b`)
	hintRe = regexp.MustCompile(`(?i)devin|cognition`)
)

// tmailyMailbox 是 MailboxProvider 的 TMaily 实现。
type tmailyMailbox struct {
	c *http.Client
}

// NewTMailyMailbox 构造 TMaily 邮箱提供者。
func NewTMailyMailbox(hc *http.Client) MailboxProvider {
	if hc == nil {
		hc = &http.Client{Timeout: 45 * time.Second}
	}
	if hc.Jar == nil {
		if jar, err := cookiejar.New(nil); err == nil {
			hc.Jar = jar
		}
	}
	return &tmailyMailbox{c: hc}
}

func tmailyHeaders(req *http.Request) {
	req.Header.Set("User-Agent", tmailyUA)
	req.Header.Set("Origin", tmailyBase)
	req.Header.Set("Referer", tmailyBase+"/")
	req.Header.Set("Accept", "*/*")
}

func (t *tmailyMailbox) getJSON(ctx context.Context, u string) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	tmailyHeaders(req)
	resp, err := t.c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return body, fmt.Errorf("tmaily HTTP %d: %s", resp.StatusCode, truncateBody(body))
	}
	return body, nil
}

func truncateBody(b []byte) string {
	s := string(b)
	if len(s) > 240 {
		return s[:240]
	}
	return s
}

// listDomains 拉取可用域名（失败/空 → 兜底列表）。
func (t *tmailyMailbox) listDomains(ctx context.Context) []string {
	raw, err := t.getJSON(ctx, tmailyBase+"/domains")
	if err != nil {
		return tmailyFallbackDomains
	}
	var v struct {
		Domains []string `json:"domains"`
	}
	if json.Unmarshal(raw, &v) != nil || len(v.Domains) == 0 {
		return tmailyFallbackDomains
	}
	return v.Domains
}

// Create 实现 MailboxProvider：申请一个新邮箱地址。
func (t *tmailyMailbox) Create(ctx context.Context) (string, error) {
	domains := t.listDomains(ctx)
	domain := domains[rand.Intn(len(domains))]
	local := "wsf" + randomHex(8)
	return t.generate(ctx, true, domain, local)
}

func (t *tmailyMailbox) generate(ctx context.Context, force bool, domain, prefix string) (string, error) {
	return t.generateDepth(ctx, force, domain, prefix, 0)
}

// generateDepth 为 generate 的实现；depth 限制 session_not_found 递归重试次数，
// 防止服务端持续返回该错误时无限递归 + 重复外部请求。
func (t *tmailyMailbox) generateDepth(ctx context.Context, force bool, domain, prefix string, depth int) (string, error) {
	q := url.Values{}
	if force {
		q.Set("force", "true")
	}
	if domain != "" {
		q.Set("domain", domain)
	}
	if prefix != "" {
		q.Set("prefix", prefix)
	}
	u := tmailyBase + "/generate"
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	raw, err := t.getJSON(ctx, u)
	if err != nil {
		return "", err
	}
	var v struct {
		Address string `json:"address"`
		Error   string `json:"error"`
	}
	_ = json.Unmarshal(raw, &v)
	if v.Address != "" {
		return v.Address, nil
	}
	if v.Error == "turnstile_required" {
		// 重试（force=true 仅一次）
		raw2, err2 := t.getJSON(ctx, tmailyBase+"/generate?force=true")
		if err2 == nil {
			var v2 struct {
				Address string `json:"address"`
			}
			if json.Unmarshal(raw2, &v2) == nil && v2.Address != "" {
				return v2.Address, nil
			}
		}
		return "", errors.New("tmaily: turnstile_required and retry failed")
	}
	if v.Error == "session_not_found" {
		// 递归重试加深度上限，防止无限递归 + 重复外部请求。
		if depth >= maxGenerateRecursion {
			return "", fmt.Errorf("tmaily: session_not_found after %d retries", maxGenerateRecursion)
		}
		return t.generateDepth(ctx, true, domain, prefix, depth+1)
	}
	return "", fmt.Errorf("tmaily generate rejected: %s", truncateBody(raw))
}

// WaitCode 实现 MailboxProvider：轮询验证码。
func (t *tmailyMailbox) WaitCode(ctx context.Context, address, hint string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	var lastCount int
	for time.Now().Before(deadline) {
		emails, err := t.fetchEmails(ctx, address)
		if err == nil {
			lastCount = len(emails)
		}
		for _, em := range emails {
			subj := em["subject"]
			from := em["from"]
			text := em["text"]
			html := em["html"]
			blob := subj + "\n" + from + "\n" + text + "\n" + html
			if cap := codeRe.FindStringSubmatch(blob); len(cap) > 1 {
				if len(emails) > 1 && !hintRe.MatchString(blob) {
					continue
				}
				return cap[1], nil
			}
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(1500 * time.Millisecond):
		}
	}
	return "", fmt.Errorf("mail code timeout after %v (inbox=%d)", timeout, lastCount)
}

// fetchEmails 拉取邮箱收件列表（[{subject,from,text,html}, ...]）。
func (t *tmailyMailbox) fetchEmails(ctx context.Context, address string) ([]map[string]string, error) {
	u := tmailyBase + "/emails?address=" + url.QueryEscape(address)
	raw, err := t.getJSON(ctx, u)
	if err != nil {
		return nil, err
	}
	var arr []map[string]any
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil, err
	}
	out := make([]map[string]string, 0, len(arr))
	for _, em := range arr {
		m := make(map[string]string, 4)
		for _, k := range []string{"subject", "from", "text", "html"} {
			if s, ok := em[k].(string); ok {
				m[k] = s
			}
		}
		out = append(out, m)
	}
	return out, nil
}

var _ MailboxProvider = (*tmailyMailbox)(nil)

// randomHex 生成 n 位十六进制（供邮箱本地名）。
func randomHex(n int) string {
	const hex = "0123456789abcdef"
	b := make([]byte, n)
	crand.Read(b)
	for i := range b {
		b[i] = hex[b[i]%byte(len(hex))]
	}
	return string(b)
}
