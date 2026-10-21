package main

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"time"
	"unicode/utf8"
)

// estimateTokens 粗略估算文本 token：ASCII 约 4 字符/token，非 ASCII（中文等）约 1 字符/token。
func estimateTokens(s string) int {
	if s == "" {
		return 0
	}
	var n float64
	for _, r := range s {
		if r < utf8.RuneSelf {
			n += 0.25
		} else {
			n++
		}
	}
	if n < 1 {
		n = 1
	}
	return int(math.Ceil(n))
}

// outputThrottle 是单请求的输出 token 桶：容量 1 秒，速率 tps token/秒。
type outputThrottle struct {
	tps    float64
	tokens float64
	last   time.Time
	ctx    context.Context
}

func newOutputThrottle(tps int, ctx context.Context) *outputThrottle {
	if tps <= 0 {
		return nil
	}
	return &outputThrottle{tps: float64(tps), tokens: float64(tps), last: time.Now(), ctx: ctx}
}

// wait 在需要时阻塞，使平均输出速度不超过 tps；上下文取消时立即返回。
func (t *outputThrottle) wait(n int) {
	if t == nil || n <= 0 {
		return
	}
	now := time.Now()
	elapsed := now.Sub(t.last).Seconds()
	t.tokens = math.Min(t.tps, t.tokens+elapsed*t.tps)
	t.last = now
	t.tokens -= float64(n)
	if t.tokens >= 0 {
		return
	}
	d := time.Duration(-t.tokens / t.tps * float64(time.Second))
	t.tokens = 0
	t.last = now.Add(d)
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-t.ctx.Done():
	}
}

// sseDeltaText 从单条 SSE data 中提取新增正文，用于限速。
// 兼容 OpenAI chat delta、Anthropic content_block_delta 与 Responses output_text.delta。
func sseDeltaText(data []byte) string {
	var obj struct {
		Type  string `json:"type"`
		Delta any    `json:"delta"`
		Text  string `json:"text"`
		Choices []struct {
			Delta struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &obj); err != nil {
		return ""
	}
	var b strings.Builder
	switch d := obj.Delta.(type) {
	case string:
		b.WriteString(d)
	case map[string]any:
		if s, ok := d["text"].(string); ok {
			b.WriteString(s)
		}
		if s, ok := d["content"].(string); ok {
			b.WriteString(s)
		}
	}
	if obj.Type == "content_block_delta" || obj.Type == "" {
		b.WriteString(obj.Text)
	}
	for _, c := range obj.Choices {
		b.WriteString(c.Delta.Content)
		b.WriteString(c.Delta.ReasoningContent)
	}
	return b.String()
}

// responseText 提取非流式响应中的正文，用于输出限速。
func responseText(body []byte) string {
	var obj struct {
		Choices []struct {
			Message struct {
				Content any `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Content any `json:"content"`
		Output  []struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(body, &obj); err != nil {
		return ""
	}
	var b strings.Builder
	appendContent := func(v any) {
		switch c := v.(type) {
		case string:
			b.WriteString(c)
		case []any:
			for _, part := range c {
				if m, ok := part.(map[string]any); ok {
					if s, ok := m["text"].(string); ok {
						b.WriteString(s)
					}
				}
			}
		}
	}
	for _, c := range obj.Choices {
		appendContent(c.Message.Content)
	}
	appendContent(obj.Content)
	for _, o := range obj.Output {
		for _, c := range o.Content {
			b.WriteString(c.Text)
		}
	}
	return b.String()
}
