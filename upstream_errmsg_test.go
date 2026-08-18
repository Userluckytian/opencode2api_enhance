package main

import (
	"errors"
	"strings"
	"testing"
)

func TestUpstreamErrMsg(t *testing.T) {
	cases := []struct {
		name   string
		status int
		err    error
		body   []byte
		want   string
	}{
		{
			name:   "nil err 空 body（历史格式退化）",
			status: 400,
			err:    nil,
			body:   nil,
			want:   "upstream status 400: <nil>",
		},
		{
			name:   "带错误信息",
			status: 429,
			err:    errors.New("rate limited"),
			body:   nil,
			want:   "upstream status 429: rate limited",
		},
		{
			name:   "响应体 JSON 入日志",
			status: 400,
			err:    nil,
			body:   []byte(`{"type":"error","error":{"type":"ModelError","message":"Model mimo-v2.5 is not supported"}}`),
			want:   `upstream status 400: <nil> body={"type":"error","error":{"type":"ModelError","message":"Model mimo-v2.5 is not supported"}}`,
		},
		{
			name:   "响应体空白时忽略",
			status: 400,
			err:    nil,
			body:   []byte("   \n  "),
			want:   "upstream status 400: <nil>",
		},
		{
			name:   "超长响应体截断",
			status: 400,
			err:    nil,
			body:   []byte(strings.Repeat("x", upstreamErrBodyMax+100)),
			want:   "upstream status 400: <nil> body=" + strings.Repeat("x", upstreamErrBodyMax) + "…",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := upstreamErrMsg(c.status, c.err, c.body)
			if got != c.want {
				t.Fatalf("upstreamErrMsg() = %q, want %q", got, c.want)
			}
		})
	}
}
