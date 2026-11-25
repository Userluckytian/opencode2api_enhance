package manager

// SOCKS5 UDP 中继探测。
//
// 今天的故障证明「TCP 可达」不等于「UDP 中继可用」：订阅节点注册（HTTPS/TCP）
// 3/3 全成功，但 WireGuard 握手包从节点出去后没有任何回包。mihomo 的 delay
// API 只测 TCP，发现不了这种节点。
//
// 这里做一次真实的 SOCKS5 UDP ASSOCIATE：握手 → 向 1.1.1.1:53 发 DNS 查询 →
// 等任意回包。能收到回包，才说明这条中继能承载 WireGuard 的 UDP。

import (
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// socks5UDPAssociate 建立 UDP ASSOCIATE 并返回中继地址。
func socks5UDPAssociate(conn net.Conn) (string, error) {
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		return "", err
	}
	buf := make([]byte, 2)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return "", err
	}
	if buf[0] != 0x05 || buf[1] != 0x00 {
		return "", fmt.Errorf("SOCKS5 认证失败: %v", buf)
	}
	// VER REP RSV ATYP DST.ADDR DST.PORT（0.0.0.0:0 = 由代理解析）
	if _, err := conn.Write([]byte{0x05, 0x03, 0x00, 0x01, 0, 0, 0, 0, 0, 0}); err != nil {
		return "", err
	}
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(conn, hdr); err != nil {
		return "", err
	}
	if hdr[0] != 0x05 || hdr[1] != 0x00 {
		return "", fmt.Errorf("UDP ASSOCIATE 被拒绝: rep=%d", hdr[1])
	}
	var host string
	switch hdr[3] {
	case 0x01:
		a := make([]byte, 4)
		if _, err := io.ReadFull(conn, a); err != nil {
			return "", err
		}
		host = net.IP(a).String()
	case 0x04:
		a := make([]byte, 16)
		if _, err := io.ReadFull(conn, a); err != nil {
			return "", err
		}
		host = net.IP(a).String()
	case 0x03:
		l := make([]byte, 1)
		if _, err := io.ReadFull(conn, l); err != nil {
			return "", err
		}
		a := make([]byte, int(l[0]))
		if _, err := io.ReadFull(conn, a); err != nil {
			return "", err
		}
		host = string(a)
	default:
		return "", fmt.Errorf("未知 ATYP=%d", hdr[3])
	}
	pb := make([]byte, 2)
	if _, err := io.ReadFull(conn, pb); err != nil {
		return "", err
	}
	port := int(pb[0])<<8 | int(pb[1])
	if host == "0.0.0.0" || host == "" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, strconv.Itoa(port)), nil
}

// buildDNSQuery 构造一个最小的 A 记录查询（www.cloudflare.com）。
func buildDNSQuery() []byte {
	q := []byte{0x12, 0x34, 0x01, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	for _, label := range strings.Split("www.cloudflare.com", ".") {
		q = append(q, byte(len(label)))
		q = append(q, label...)
	}
	return append(q, 0x00, 0x00, 0x01, 0x00, 0x01)
}

// probeSocks5UDP 验证一个 SOCKS5 中继能否真正转发 UDP。
// addr 形如 host:port（也接受 socks5://host:port）。
func probeSocks5UDP(addr string, timeout time.Duration) error {
	addr = strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(addr), "socks5h://"), "socks5://")
	addr = strings.TrimSuffix(addr, "/")
	if addr == "" {
		return fmt.Errorf("空地址")
	}
	tcp, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return fmt.Errorf("连接中继失败: %w", err)
	}
	defer func() { _ = tcp.Close() }()
	_ = tcp.SetDeadline(time.Now().Add(timeout))
	relay, err := socks5UDPAssociate(tcp)
	if err != nil {
		return err
	}
	udp, err := net.DialTimeout("udp", relay, timeout)
	if err != nil {
		return fmt.Errorf("连接 UDP 中继失败: %w", err)
	}
	defer func() { _ = udp.Close() }()
	_ = udp.SetDeadline(time.Now().Add(timeout))
	pkt := append([]byte{0x00, 0x00, 0x00, 0x01, 1, 1, 1, 1, 0, 53}, buildDNSQuery()...)
	if _, err := udp.Write(pkt); err != nil {
		return fmt.Errorf("UDP 发送失败: %w", err)
	}
	resp := make([]byte, 1500)
	n, err := udp.Read(resp)
	if err != nil {
		return fmt.Errorf("UDP 无回包: %w", err)
	}
	if n < 12 {
		return fmt.Errorf("UDP 回包过短(%d 字节)", n)
	}
	return nil
}
