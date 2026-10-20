//go:build windows

package manager

// Windows 上用 shell32!ShellExecuteW 把 URL 交给 Shell，由默认关联的程序（浏览器）打开。
//
// 为什么不再走 `cmd /c start`（2026-10-14 实测踩坑）：
//  1. 按名字 exec "cmd" 只查 PATH，而宿主进程的 PATH 不保证包含
//     %SystemRoot%\system32（本机实测直接 `exec: "cmd": executable file not found in
//     %PATH%`），插件面板入口于是恒定失败，前端只能弹「无法自动打开面板」；
//  2. 即便找得到 cmd，URL 里的 & 也会被 cmd 当命令分隔符，
//     gateway/plugin/key/models 参数会被整段截断丢失。
//
// ShellExecute 是 API 调用，不做命令行拼接，这两个坑都不存在。

import (
	"fmt"
	"syscall"
	"unsafe"
)

var (
	modShell32        = syscall.NewLazyDLL("shell32.dll")
	procShellExecuteW = modShell32.NewProc("ShellExecuteW")
)

func openWithShellExecute(target string) error {
	verb, err := syscall.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	arg, err := syscall.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	const swShowNormal = 1
	// 返回值 <= 32 表示失败（Win32 约定，见 ShellExecuteW 文档）。
	ret, _, callErr := procShellExecuteW.Call(
		0, // hwnd
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(arg)),
		0, // lpParameters
		0, // lpDirectory
		swShowNormal,
	)
	if ret <= 32 {
		if callErr != nil && callErr != syscall.Errno(0) {
			return callErr
		}
		return fmt.Errorf("ShellExecute 返回错误码 %d", ret)
	}
	return nil
}
