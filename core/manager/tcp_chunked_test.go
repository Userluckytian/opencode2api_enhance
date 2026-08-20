package manager

import (
	"bytes"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// addChunkedServer 裸 TCP 服务端：按请求路径返回 chunked 或 Content-Length 响应。
// 用于验证 httpRequest 的 Transfer-Encoding: chunked 解码（实例测试误报根因）。
func addChunkedServer(t *testing.T) (uint16, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				// 完整读完请求头（直到空行）再响应，规避 Windows 部分读竞态（与 rawHTTPServer 同策略）。
				_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
				var reqBytes []byte
				tmp := make([]byte, 1024)
				for !bytes.Contains(reqBytes, []byte("\r\n\r\n")) && len(reqBytes) < 16*1024 {
					n, err := c.Read(tmp)
					if n > 0 {
						reqBytes = append(reqBytes, tmp[:n]...)
					}
					if err != nil {
						break
					}
				}
				req := string(reqBytes)
				writeChunk := func(sizeLine, data string) {
					_, _ = c.Write([]byte(sizeLine + "\r\n" + data + "\r\n"))
				}
				switch {
				case strings.HasPrefix(req, "GET /chunked "):
					// 多段 chunk（3 段 + 0 终止），验证跨块拼接还原完整 JSON。
					parts := []string{
						`{"data":[`,
						`{"id":"big-pickle"},`,
						`{"id":"hy3"}]}`,
					}
					_, _ = c.Write([]byte("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nTransfer-Encoding: chunked\r\n\r\n"))
					for _, p := range parts {
						writeChunk(fmt.Sprintf("%x", len(p)), p)
					}
					_, _ = c.Write([]byte("0\r\n\r\n"))
				case strings.HasPrefix(req, "GET /chunked-ext "):
					// chunk 大小行带扩展参数（如 "4;name=ext"），须剥离扩展后解析。
					_, _ = c.Write([]byte("HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n"))
					writeChunk("4;name=ext", "WXYZ")
					_, _ = c.Write([]byte("0\r\n\r\n"))
				case strings.HasPrefix(req, "GET /plain "):
					// 对照：Content-Length 路径仍正常。
					_, _ = c.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nOK"))
				default:
					_, _ = c.Write([]byte("HTTP/1.1 404 Not Found\r\nContent-Length: 7\r\n\r\nnope"))
				}
			}(conn)
		}
	}()
	return uint16(ln.Addr().(*net.TCPAddr).Port), func() { ln.Close() }
}

func TestHTTPRequestChunked(t *testing.T) {
	port, stop := addChunkedServer(t)
	defer stop()

	// 多段 chunk：解码后应还原完整 JSON 体。
	// retryHTTP 容忍本机回环偶发慢速（Windows 安全软件/负载），与 TestHTTPRequestRaw 同策略。
	status, body, err := retryHTTP(t, func() (int, []byte, error) { return httpGetJSON(port, "/chunked", 8, "") })
	if err != nil || status != http.StatusOK {
		t.Fatalf("chunked case: status=%d err=%v", status, err)
	}
	want := `{"data":[{"id":"big-pickle"},{"id":"hy3"}]}`
	if string(body) != want {
		t.Fatalf("chunked body = %q, want %q", string(body), want)
	}

	// chunk 大小行带扩展参数。
	status, body, err = retryHTTP(t, func() (int, []byte, error) { return httpGetJSON(port, "/chunked-ext", 8, "") })
	if err != nil || status != http.StatusOK || string(body) != "WXYZ" {
		t.Fatalf("chunked-ext case: status=%d body=%q err=%v", status, string(body), err)
	}

	// Content-Length 路径回归。
	status, body, err = retryHTTP(t, func() (int, []byte, error) { return httpGetJSON(port, "/plain", 8, "") })
	if err != nil || status != http.StatusOK || string(body) != "OK" {
		t.Fatalf("plain case: status=%d body=%q err=%v", status, string(body), err)
	}
}