// 文件系统原子写辅助：runtime 配置统一写入口（2026-08-28 审查缺口 2）。
package manager

import (
	"os"
	"path/filepath"
	"time"
)

// rename 瞬时失败重试参数：Windows 下目标文件正被并发读取/替换（Go 打开文件的
// 共享模式不含 FILE_SHARE_DELETE，config watcher 周期读盘即此类读者）时，
// Rename 替换目标会瞬时报 Access denied，短暂重试即可越过（与根包
// writeFileAtomicRetry 同款经验）。
const (
	atomicRenameRetryAttempts = 5
	atomicRenameRetryDelay    = 10 * time.Millisecond
)

// WriteFileAtomic 原子写文件：同目录唯一临时文件（os.CreateTemp）+ rename 替换。
// 读者要么看到旧文件、要么看到完整新文件，崩溃/断电不留半截 JSON；多写者并发
// 也不再有固定 tmp 名碰撞。供 runtime 配置各类写者（实例/网关整写、propagate/
// auto 补丁）统一替换裸 os.WriteFile，消除撕裂读写窗口。
//
// 目标目录不存在时返回错误（调用方各自持有既有 MkdirAll 语义，本函数不代建）。
// rename 覆盖语义：os.Rename 在 Windows 走 MoveFileEx(REPLACE_EXISTING)，可覆盖
// 已存在目标（go1.20+ 恒如此；fsutil_test.go 有平台验证）。
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // 失败路径清理；成功 Rename 后目标已不存在，无副作用
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), perm); err != nil {
		return err
	}
	for i := 0; ; i++ {
		err = os.Rename(tmp.Name(), path)
		if err == nil || i+1 >= atomicRenameRetryAttempts {
			return err
		}
		time.Sleep(atomicRenameRetryDelay) // 瞬时占用（并发读/并发替换）→ 重试越过
	}
}
