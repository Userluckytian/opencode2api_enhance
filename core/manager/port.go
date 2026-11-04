// 端口工具（Rust commands.rs port_suggest / port_check / force_free_port 移植）。
//
// v2.0.0：实例池移除后不再有「实例端口段」，端口建议改为在 41000~43000 段内
// 找一个空闲端口（用于自定义源测试或临时调试）。
package manager

import (
	"errors"
	"time"
)

// randSeed 与 Rust LCG 一致的种子（时间）。
func randSeed() uint64 {
	return uint64(time.Now().UnixNano() & 0x7fffffff)
}

// lcgNext 线性同余迭代（Rust 同参）。
func lcgNext(seed *uint64) uint64 {
	*seed = *seed*6364136223846793005 + 1442695040888963407
	return *seed
}

// suggestFreePorts 从 start 起找 count 个连续可用端口。
func (m *Manager) suggestFreePorts(start uint16, count int) []uint16 {
	out := make([]uint16, 0, count)
	port := int(start)
	for len(out) < count && port <= 65535 {
		if isPortFree(uint16(port)) {
			out = append(out, uint16(port))
		}
		port++
	}
	return out
}

// PortSuggest 建议一个可用端口（41000~43000 段内 LCG 取 2000 次尝试）。
// 该段避开默认三端口（40000/40080/50080）与常见冲突区。
func (m *Manager) PortSuggest() (uint16, error) {
	seed := randSeed()
	for i := 0; i < 2000; i++ {
		candidate := uint16(41000 + lcgNext(&seed)%2000)
		if isPortFree(candidate) {
			return candidate, nil
		}
	}
	return 0, errors.New("未找到可用端口")
}

// PortCheckResult 端口检查结果（前端契约）。
type PortCheckResult struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason"`
}

// PortCheck 检查端口是否可用。
func (m *Manager) PortCheck(port uint16) PortCheckResult {
	if port < 1024 {
		return PortCheckResult{Available: false, Reason: "端口需 >= 1024"}
	}
	if isPortFree(port) {
		return PortCheckResult{Available: true, Reason: "端口可用"}
	}
	return PortCheckResult{Available: false, Reason: "端口已被本机程序监听"}
}

// ForceFreePort 强制释放端口：netstat 找到 PID → taskkill。
func (m *Manager) ForceFreePort(runner Runner, port uint16) []int {
	if runner == nil {
		runner = &realRunner{}
	}
	var freed []int
	for _, pid := range pidsOnPort(port) {
		if runner.Kill(pid) == nil {
			freed = append(freed, pid)
		}
	}
	return freed
}
