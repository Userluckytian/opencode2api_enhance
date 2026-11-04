// Package manager 承载管理域（配置/供应商服务/免费层引擎/统计/调用日志）。
//
// 目录约定：
//
//	dataDir    = OPCODE2API_DATA_DIR（非空）| <UserConfigDir>/opencode2api-manager
//	runtime    = dataDir/runtime/…（free-tier 引擎目录 + _provider-service）
//	binDir     = <当前可执行文件所在目录>（内嵌/外置二进制同目录）
package manager

import (
	"os"
	"path/filepath"
	"runtime"
)

// DefaultDataDir 计算管理数据目录。
func DefaultDataDir() string {
	if dir := os.Getenv("OPCODE2API_DATA_DIR"); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
		return dir
	}
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		base = "."
	}
	dir := filepath.Join(base, "opencode2api-manager")
	_ = os.MkdirAll(dir, 0o755)
	return dir
}

// Paths 是管理器常用的路径集合。
type Paths struct {
	DataDir    string
	Config     string // dataDir/config.json
	RuntimeDir string // dataDir/runtime
	BinDir     string // <exe> 所在目录
}

// ResolvePaths 计算全部路径。dataDir 为空时用 DefaultDataDir。
func ResolvePaths(dataDir string) Paths {
	if dataDir == "" {
		dataDir = DefaultDataDir()
	}
	exeDir := "."
	if p, e := os.Executable(); e == nil {
		exeDir = filepath.Dir(p)
	}
	return Paths{
		DataDir:    dataDir,
		Config:     filepath.Join(dataDir, "config.json"),
		RuntimeDir: filepath.Join(dataDir, "runtime"),
		BinDir:     exeDir,
	}
}

// FreeTierEngineDir 免费层引擎（40080）的工作目录。
// 引擎会在 config.json 同目录写 models.catalog / rotation / availability 等状态文件，
// 因此必须给它一个独立可写目录（不能与 dataDir 混用，也不能放 Program Files）。
func (p Paths) FreeTierEngineDir() string {
	return filepath.Join(p.RuntimeDir, "free-tier")
}

// prepareRuntimes 确保 runtime 目录存在。
func (p Paths) prepareRuntimes() error {
	return os.MkdirAll(p.RuntimeDir, 0o755)
}

// isWindows 报告当前是否为 Windows（用于二进制后缀判断）。
func isWindows() bool { return runtime.GOOS == "windows" }
