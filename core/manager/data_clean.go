// 数据清理（Rust commands.rs data_clean 移植）。
// 级别：1=清理运行数据（runtime/）；2=+清空实例记录；3=+删除配置（先备份 .bak）。
package manager

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// DataClean 执行三级数据清理。
// 先停后清：停网关 → 停全部实例 → 等 300ms → 清理数据目录。
func (m *Manager) DataClean(runner Runner, gw *Gateway, level int) error {
	if level < 1 || level > 3 {
		return fmt.Errorf("无效的清理级别: %d", level)
	}
	if runner == nil {
		runner = &realRunner{}
	}
	if gw != nil {
		gw.stop(runner)
	}
	m.StopAllInstances(runner)
	time.Sleep(300 * time.Millisecond)
	// 供应商服务（50080）持有 runtime/_provider-service 下的日志/统计文件句柄，
	// 不先停掉会导致 Windows 上 RemoveAll 失败。它由主进程看护，清理后会自动重启。
	m.stopProviderServiceForClean()

	if err := m.cleanDataAt(level); err != nil {
		return err
	}

	// 清空内存态实例（前端刷新即见空）
	if level >= 2 {
		m.mu.Lock()
		defer m.mu.Unlock()
		if err := writeFileAtomic(m.Paths().Instances, []byte("[]")); err != nil {
			return err
		}
	}
	return nil
}

// cleanDataAt 纯文件系统清理（按级别）。
func (m *Manager) cleanDataAt(level int) error {
	p := m.Paths()

	// 1) 删除 runtime 目录（运行数据）
	if err := os.RemoveAll(p.RuntimeDir); err != nil {
		return fmt.Errorf("删除运行数据失败: %w", err)
	}

	// 2) 清空实例记录
	if level >= 2 {
		if err := writeFileAtomic(p.Instances, []byte("[]")); err != nil {
			return fmt.Errorf("清空实例记录失败: %w", err)
		}
	}

	// 3) 删除配置（备份 .bak）——但保留 providers/routing：
	// 自定义模型源定义存在 config.json 的 providers 里（桌面态核心与 manager 共用此文件），
	// 整份删除会连带清空用户的自定义模型；"清除记录"的语义不应包含它们
	//（想清空自定义模型请用自定义模型页的「清空全部」）。
	if level == 3 {
		if _, err := os.Stat(p.Config); err == nil {
			_ = copyFile(p.Config, p.Config+".bak")
			if err := os.Remove(p.Config); err != nil {
				return fmt.Errorf("删除配置失败: %w", err)
			}
			if err := writePreservedVendorConfig(p.Config); err != nil {
				return fmt.Errorf("保留自定义模型源失败: %w", err)
			}
		}
	}
	return nil
}

// writePreservedVendorConfig 从 .bak 中提取 providers/routing 写回最小配置，
// 使三级清理后自定义模型源与路由映射幸存（其余配置项回到默认）。
func writePreservedVendorConfig(configPath string) error {
	data, err := os.ReadFile(configPath + ".bak")
	if err != nil {
		return nil // 无备份可提取：保持删除态
	}
	var full map[string]any
	if json.Unmarshal(data, &full) != nil {
		return nil // 备份损坏：保持删除态
	}
	minimal := map[string]any{}
	if v, ok := full["providers"]; ok && v != nil {
		minimal["providers"] = v
	}
	if v, ok := full["routing"]; ok && v != nil {
		minimal["routing"] = v
	}
	if len(minimal) == 0 {
		return nil
	}
	out, err := json.MarshalIndent(minimal, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath, out, 0o644)
}

func writeFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o644)
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if dir := filepath.Dir(dst); dir != "." {
		_ = os.MkdirAll(dir, 0o755)
	}
	return os.WriteFile(dst, data, 0o644)
}

// stopProviderServiceForClean 请求供应商服务优雅退出，释放 runtime/_provider-service
// 下的文件句柄。服务由主进程看护，清理完成后会自动重启；未运行则直接跳过。
func (m *Manager) stopProviderServiceForClean() {
	port := m.managerProviderPort()
	if !probePort(port, statsResetProbeTimeout) {
		return
	}
	_, _, _ = httpRequest("POST", "/api/admin/shutdown", port, 2*time.Second, effectiveGatewayKey(m.loadConfig()), nil, 2*time.Second)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && probePort(port, 200*time.Millisecond) {
		time.Sleep(100 * time.Millisecond)
	}
}
