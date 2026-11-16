//go:build windows

package manager

import (
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// netstatRunner Windows 真实现。
//
// 注意参数：Windows netstat 的协议过滤是 `-p TCP`，**不是** `-tcp`。
// 早先写成 `-tcp` 时命令只回表头（exit code 非 0），listPortPids 永远返回空，
// 于是「按端口认领引擎进程」永远失败——这正是 P0 修复实测时才暴露出来的坑。
func netstatRunner() *exec.Cmd {
	return exec.Command("netstat", "-ano", "-p", "TCP")
}

// netstatLineRe 匹配一行 TCP 连接：
//
//	  TCP    127.0.0.1:40180    0.0.0.0:0    LISTENING    57312
//
// 只取本地地址里的端口、状态与 PID；本地地址允许回环/通配/IPv6（表头是
// 本地化文案，因此不按列名解析）。
var netstatLineRe = regexp.MustCompile(`^\s*TCP\s+(\S+)\s+(\S+)\s+(LISTENING)\s+(\d+)\s*$`)

// listPortPids Windows：解析 netstat 输出中占用给定端口的 PID 集合。
func listPortPids(port uint16) []int {
	seen := map[int]bool{}
	var pids []int
	for _, line := range runNetstat() {
		m := netstatLineRe.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		local := m[1]
		i := strings.LastIndex(local, ":")
		if i < 0 {
			continue
		}
		p, _ := strconv.Atoi(local[i+1:])
		if uint16(p) != port {
			continue
		}
		pid, _ := strconv.Atoi(m[4])
		if pid <= 0 || seen[pid] {
			continue
		}
		seen[pid] = true
		pids = append(pids, pid)
	}
	return pids
}
