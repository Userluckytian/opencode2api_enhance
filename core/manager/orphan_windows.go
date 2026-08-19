//go:build windows

package manager

import (
	"encoding/json"
	"os/exec"
)

// listAppProcesses Windows：经 PowerShell Get-CimInstance 枚举本应用的全部进程
// （opencode2api.exe / sing-box.exe / 插件子进程，后两者按命令行含 --provider-serve
// 识别），输出 PID / 父 PID / 进程名 / 命令行。
func listAppProcesses() ([]procLine, error) {
	ps := "Get-CimInstance Win32_Process -Filter \"Name='opencode2api.exe' OR Name='sing-box.exe' OR CommandLine LIKE '%--provider-serve%'\" | ForEach-Object { [PSCustomObject]@{ pid=$_.ProcessId; ppid=$_.ParentProcessId; name=$_.Name; cmd=$_.CommandLine } } | ConvertTo-Json -Compress"
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", ps)
	applyNoWindow(cmd, true)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var procs []procLine
	if err := json.Unmarshal(out, &procs); err == nil {
		return procs, nil
	}
	// 单条命中时 ConvertTo-Json 输出对象而非数组。
	var one procLine
	if err := json.Unmarshal(out, &one); err == nil && one.PID > 0 {
		return []procLine{one}, nil
	}
	return nil, json.Unmarshal(out, &procs)
}
