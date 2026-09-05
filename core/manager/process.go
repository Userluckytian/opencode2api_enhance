// 进程执行抽象（P4-2）：Runner 接口让测试以 fake 注入；
// 生产实现 = os/exec + CREATE_NO_WINDOW（Windows）/ 常规（其它平台）。
package manager

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ExecSpec 描述一次进程启动。
type ExecSpec struct {
	Bin      string   // 二进制路径
	Args     []string // 参数（不含 Bin）
	Dir      string   // 工作目录（空 = 继承）
	LogOut   string   // stdout 重定向文件（空 = 丢弃）
	LogErr   string   // stderr 重定向文件（空 = 丢弃）
	NoWindow bool     // Windows CREATE_NO_WINDOW
	Env      []string // 追加到子进程环境（形如 KEY=VALUE；空 = 仅继承父进程 env）
}

// Runner 抽象进程生命周期。
type Runner interface {
	// Start 启动进程，返回 pid。
	Start(spec ExecSpec) (int, error)
	// Kill 强制终止（Windows taskkill /F，其它平台 SIGKILL 语义）。
	Kill(pid int) error
}

// realRunner 生产端 Runner。
type realRunner struct{}

// NewRealRunner 构造生产 Runner。
func NewRealRunner() Runner { return &realRunner{} }

// traceEnvKV 阶段 3：若父进程携带 OPENCODE2API_TRACE，则透传给子进程，形成进程树
// trace 链——子进程无入站 X-Trace-ID 头时以此为进程级默认 trace（如启动期日志）。
// 父进程未设置时返回 nil，不改变子进程环境。
func traceEnvKV() []string {
	if v := strings.TrimSpace(os.Getenv("OPENCODE2API_TRACE")); v != "" {
		return []string{"OPENCODE2API_TRACE=" + v}
	}
	return nil
}

// Start 实现 Runner。
func (r *realRunner) Start(spec ExecSpec) (int, error) {
	cmd := exec.Command(spec.Bin, spec.Args...)
	cmd.Dir = spec.Dir
	applyNoWindow(cmd, spec.NoWindow)
	if len(spec.Env) > 0 {
		cmd.Env = append(os.Environ(), spec.Env...)
	}
	if spec.LogOut != "" {
		if f, err := os.OpenFile(spec.LogOut, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
			cmd.Stdout = f
		}
	}
	if spec.LogErr != "" {
		if f, err := os.OpenFile(spec.LogErr, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
			cmd.Stderr = f
		}
	}
	if spec.LogOut == "" && spec.LogErr == "" {
		// 无人读取的日志丢弃（避免句柄堆积）
		cmd.Stdout, cmd.Stderr = nil, nil
	}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	return cmd.Process.Pid, nil
}

// Kill 实现 Runner。
func (r *realRunner) Kill(pid int) error {
	if pid <= 0 {
		return nil
	}
	return killProcess(pid)
}

// binPath 在 bin 目录解析可执行文件（优先 <name>.exe）。
func (m *Manager) binPath(name string) string {
	for _, p := range []string{name + ".exe", name} {
		full := filepath.Join(m.paths.BinDir, p)
		if _, err := os.Stat(full); err == nil {
			return full
		}
	}
	return filepath.Join(m.paths.BinDir, name+".exe")
}

// resolveBin 给默认 Runner 用（真实启动实例/网关/探针）。
func (m *Manager) resolveBin(name string) string {
	return m.binPath(name)
}

// core/manager/process.go 依赖的平台工厂（netstat_*.go / process_windows|other.go 提供）。
