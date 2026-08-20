// 裸 TCP HTTP 客户端与端口工具（Rust instance.rs http_get_json / wait_for_port /
// is_port_free / pids_on_port 移植）。探针、统计重置、网关检查共用。
package manager

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// unifiedGatewayKey 统一网关本地 API 密钥（Rust gateway.rs 同值）。
const unifiedGatewayKey = "sk-unified-local"

// unifiedGatewayPort 统一网关进程默认端口（与槽位表一致：release 40080；
// dev/便携/web-dev 槽位由壳层按 OPCODE2API_GATEWAY_PORT 注入覆盖）。
const unifiedGatewayPort = 40080

// httpStatusLine 解析 HTTP 响应首行得出状态码。
func httpStatusLine(line string) (int, error) {
	parts := strings.Fields(line)
	if len(parts) < 2 {
		return 0, fmt.Errorf("malformed status line: %s", line)
	}
	code, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, err
	}
	return code, nil
}

// httpRequest 裸 TCP 发一次 HTTP 请求（GET/POST/DELETE），返回状态码与响应体。
// 语义与 Rust instance::http_*_json 一致：connect 127.0.0.1:port，读超时 readTimeout。
func httpRequest(method, path string, port uint16, readTimeout time.Duration, authToken string, body []byte, writeTimeout time.Duration) (int, []byte, error) {
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port)))
	if writeTimeout <= 0 {
		writeTimeout = 5 * time.Second
	}
	conn, err := net.DialTimeout("tcp", addr, writeTimeout)
	if err != nil {
		return 0, nil, err
	}
	defer conn.Close()
	if readTimeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(readTimeout))
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "%s %s HTTP/1.1\r\n", method, path)
	fmt.Fprintf(&sb, "Host: 127.0.0.1:%d\r\n", port)
	fmt.Fprintf(&sb, "Connection: close\r\n")
	fmt.Fprintf(&sb, "Accept: application/json\r\n")
	fmt.Fprintf(&sb, "User-Agent: opencode2api-manager/0.1\r\n")
	if authToken != "" {
		fmt.Fprintf(&sb, "Authorization: Bearer %s\r\n", authToken)
	}
	if body != nil {
		fmt.Fprintf(&sb, "Content-Type: application/json\r\n")
		fmt.Fprintf(&sb, "Content-Length: %d\r\n", len(body))
	}
	sb.WriteString("\r\n")
	if body != nil {
		sb.Write(body)
	}
	if _, err := conn.Write([]byte(sb.String())); err != nil {
		return 0, nil, err
	}

	reader := bufio.NewReader(conn)
	status := 0
	contentLength := -1
	chunked := false
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return 0, nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if status == 0 {
			if code, err := httpStatusLine(line); err != nil {
				return 0, nil, err
			} else {
				status = code
			}
		}
		if k, v, ok := strings.Cut(line, ":"); ok {
			key := strings.TrimSpace(k)
			if strings.EqualFold(key, "Content-Length") {
				if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
					contentLength = n
				}
			}
			if strings.EqualFold(key, "Transfer-Encoding") && strings.Contains(strings.ToLower(v), "chunked") {
				chunked = true
			}
		}
	}
	var rest []byte
	if chunked {
		// Transfer-Encoding: chunked：按 chunk 解码（大小行 → 数据块 → CRLF → 0 块结束）。
		// Go 服务端 json.NewEncoder(w).Encode 未预置 Content-Length 时大响应走 chunked，
		// 裸读会保留 chunk 元数据（如 "80e\r\n"）导致 json.Unmarshal 失败（实例测试误报）。
		for {
			sizeLine, err := reader.ReadString('\n')
			if err != nil {
				return 0, nil, err
			}
			sizeStr := strings.TrimSpace(strings.SplitN(sizeLine, ";", 2)[0]) // 去扩展参数（如 ";ext"）
			size, err := strconv.ParseUint(sizeStr, 16, 32)
			if err != nil {
				return 0, nil, fmt.Errorf("chunked size parse %q: %w", sizeStr, err)
			}
			if size == 0 {
				// 尾部 trailer（可选）直到空行后结束。
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						return 0, nil, err
					}
					if strings.TrimRight(line, "\r\n") == "" {
						break
					}
				}
				break
			}
			chunk := make([]byte, size)
			if _, err := io.ReadFull(reader, chunk); err != nil {
				return 0, nil, err
			}
			rest = append(rest, chunk...)
			// 块后 CRLF。
			var crlf [2]byte
			if _, err := io.ReadFull(reader, crlf[:]); err != nil {
				return 0, nil, err
			}
		}
	} else if contentLength >= 0 {
		// 按 Content-Length 精确读取（避免依赖连接关闭；Windows 关闭延迟会触发超时）
		rest = make([]byte, contentLength)
		if _, err := io.ReadFull(reader, rest); err != nil {
			return status, rest, err
		}
	} else {
		b, err := io.ReadAll(reader)
		if err != nil {
			return status, b, err
		}
		rest = b
	}
	return status, rest, nil
}

// httpGetJSON GET 请求（authToken 可空）。
func httpGetJSON(port uint16, path string, timeout time.Duration, authToken string) (int, []byte, error) {
	return httpRequest("GET", path, port, timeout, authToken, nil, 0)
}

// httpDeleteJSON DELETE 请求。
func httpDeleteJSON(port uint16, path string, timeout time.Duration, authToken string) (int, []byte, error) {
	return httpRequest("DELETE", path, port, timeout, authToken, nil, 0)
}

// httpPostJSON POST 请求。
func httpPostJSON(port uint16, path string, timeout time.Duration, authToken string, body []byte) (int, []byte, error) {
	return httpRequest("POST", path, port, timeout, authToken, body, 0)
}

// waitForPort 轮询探测端口可连接（每 200ms 一次，直到超时）。
func waitForPort(port uint16, timeout time.Duration) error {
	return waitForPortAbort(port, timeout, nil)
}

// waitForPortAbort 同 waitForPort；abort 非空时每轮先检查（返回 true → 立即中止）。
// 扫描停止时探针进程被 Kill，探测端口不再就绪——此检查让阻塞的轮询快速退出，
// 否则停止要干等到 waitForPort 超时（8~20s）。
func waitForPortAbort(port uint16, timeout time.Duration, abort func() bool) error {
	deadline := time.Now().Add(timeout)
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port)))
	for {
		if abort != nil && abort() {
			return fmt.Errorf("已中止")
		}
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("端口 %d 未在 %v 内就绪", port, timeout)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// isPortFree 探测端口是否未被监听（一次性 bind 判断）。
func isPortFree(port uint16) bool {
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

// --------------------------------- 端口占用查询（平台无关壳） ---------------------------------

// pidsOnPort 返回占用指定端口的 PID 集合（平台实现：Windows netstat / 非 Windows lsof）。
func pidsOnPort(port uint16) []int {
	return listPortPids(port)
}

// parseLsofPIDOutput 解析 lsof -t 输出（每行一个十进制 PID，非 PID 行容错跳过）；平台无关便于单测。
func parseLsofPIDOutput(out string) []int {
	seen := map[int]bool{}
	var pids []int
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		pid, err := strconv.Atoi(line)
		if err != nil || pid <= 0 || seen[pid] {
			continue
		}
		seen[pid] = true
		pids = append(pids, pid)
	}
	return pids
}

// runNetstat（Windows）执行 netstat -ano -tcp；非 Windows 返回空（桩，端口清理走 lsof）。
func runNetstat() []string {
	out, err := netstatCmd()
	if err != nil {
		return nil
	}
	return strings.Split(string(out), "\n")
}

// netstatCmd 抽象命令执行（测试可注入 fake）。
var netstatCmd = func() ([]byte, error) {
	c := netstatRunner()
	if c == nil {
		return nil, fmt.Errorf("netstat unavailable on this platform")
	}
	return c.Output()
}
