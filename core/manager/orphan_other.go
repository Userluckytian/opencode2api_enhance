//go:build !windows

package manager

import (
	"os/exec"
	"strconv"
	"strings"
)

// listAppProcesses 非 Windows：经 ps 枚举本应用进程（opencode2api / sing-box /
// 命令行含 --provider-serve 的插件子进程）。
func listAppProcesses() ([]procLine, error) {
	out, err := exec.Command("ps", "-eo", "pid=,ppid=,comm=,args=").Output()
	if err != nil {
		return nil, err
	}
	var procs []procLine
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		comm := fields[2]
		args := strings.Join(fields[3:], " ")
		if comm != "opencode2api" && comm != "sing-box" &&
			!strings.HasSuffix(comm, "opencode2api") && !strings.HasSuffix(comm, "sing-box") &&
			!strings.Contains(args, "--provider-serve") {
			continue
		}
		procs = append(procs, procLine{PID: pid, PPID: ppid, Name: comm, Cmd: args})
	}
	return procs, nil
}
