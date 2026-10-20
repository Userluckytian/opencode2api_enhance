//go:build !windows

package manager

// 非 Windows 平台沿用系统自带的 open/xdg-open，先解析成绝对路径再启动，
// 避免调用方的 PATH 不完整导致找不到可执行文件。

import (
	"os/exec"
	"runtime"
)

func openWithShellExecute(target string) error {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return err
	}
	return exec.Command(path, target).Start()
}
