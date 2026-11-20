// 端口无法绑定时的原因分类。
//
// 2026-11-20 事故：30080 被 Hyper-V/WinNAT 划进「动态保留端口段」，bind 返回
// WSAEACCES（访问权限不允许），而旧代码一律报「端口已被其他进程占用」。
// 用户按「占用」排查了半天（netstat 查不到任何监听者），实际根本没有进程占用。
// 这里把「真被占用」和「被系统保留」分开，并在报错里给出排查命令。
package manager

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"syscall"
)

// portBindBlock 端口绑定失败的原因类别。
type portBindBlock int

const (
	// portBindOK 端口可绑定。
	portBindOK portBindBlock = iota
	// portBindInUse 端口确实被其他进程监听（WSAEADDRINUSE）。
	portBindInUse
	// portBindReserved 端口被系统保留/权限拒绝（WSAEACCES），典型是 Hyper-V/
	// WinNAT 动态保留端口段，netstat 里看不到任何监听者。
	portBindReserved
	// portBindOther 其它原因（地址不可用、防火墙驱动等），原样带走。
	portBindOther
)

// probePortBind 尝试在 127.0.0.1:port 上 bind 一次，返回失败类别。
func probePortBind(port uint16) (portBindBlock, error) {
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))))
	if err == nil {
		_ = ln.Close()
		return portBindOK, nil
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		switch uintptr(errno) {
		case 10048: // WSAEADDRINUSE
			return portBindInUse, err
		case 10013: // WSAEACCES
			return portBindReserved, err
		}
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "in use"), strings.Contains(msg, "只允许使用一次"):
		return portBindInUse, err
	case strings.Contains(msg, "access"), strings.Contains(msg, "权限不允许"):
		return portBindReserved, err
	}
	return portBindOther, err
}

// describePortBlock 生成面向用户的报错文案（区分占用/保留/其它）。
func describePortBlock(port uint16, kind portBindBlock, bindErr error) string {
	switch kind {
	case portBindInUse:
		return fmt.Sprintf("端口 %d 已被其他进程占用，供应商服务无法启动", port)
	case portBindReserved:
		return fmt.Sprintf("端口 %d 无法绑定：被 Windows 保留端口段占用（Hyper-V/WinNAT 动态分配，netstat 看不到监听者）。"+
			"查看：netsh int ipv4 show excludedportrange protocol=tcp；"+
			"修复：netsh int ipv4 add excludedportrange protocol=tcp startport=%d numberofports=1 store=persistent（需管理员，执行前先停本服务）",
			port, port)
	default:
		if bindErr == nil {
			return fmt.Sprintf("端口 %d 无法绑定", port)
		}
		return fmt.Sprintf("端口 %d 无法绑定：%v", port, bindErr)
	}
}
