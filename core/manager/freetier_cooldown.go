package manager

// 免费层引擎（40080）的 WARP 出口冷却账本。
//
// 背景：引擎把被上游拒绝的公网出口写进一份 24h 黑名单，落盘在
// <runtime>/free-tier/config.json.warp-state.json（引擎侧路径规则见
// upstream runtime.go:322-327 = configPath + ".warp-state.json"）。
// 引擎启动时读这份文件，把仍在冷却期内的 /64 一并继承为「已烧掉」。
//
// 为什么要能重置：新注册的出口地址落在已冷却的 /64 上时会被判
// 「already active or cooling down」，于是 WARP 池永远起不来 ——
// 实测本机 52 个 /64 全在冷却，引擎每次启动都退回无 WARP 模式。
// 清掉这份本地账本，引擎就能重新注册出口。
//
// 边界（重要）：**这只清本地出口账本，不影响上游账号额度。**
// 免费额度是服务端按账号/session 记账的，重启引擎、换出口、换 key 都不能
// 重置它（见 docs/issue-log/2026-11-06.md「关于『点重启就好用了』——结论：不成立」）。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// warpCoolingState 引擎冷却账本的文件形态。
//
// 字段名与引擎写出的结构一致（internal/warp/pool.go:643-646）：
// cooling 是「出口 /64 前缀 → 解冻时刻」的映射。
//
// 用 map[string]string 而不是 map[string]time.Time：引擎那边用 time.Time
// 解析失败时整份文件按空处理（loadPenalties 出错即返回空表），我们这边
// 保持同一口径 —— 坏文件就是「没有冷却」，与引擎的实际行为一致。
type warpCoolingState struct {
	Cooling map[string]string `json:"cooling"`
}

// WarpCooldownState 出口冷却状态（面板展示用）。
type WarpCooldownState struct {
	// Path 冷却账本文件路径（引擎运行目录内）。
	Path string `json:"path"`
	// Cooling 仍在冷却期内的出口前缀数。
	Cooling int `json:"cooling"`
	// Exists 账本文件是否存在（false = 没有待解冻的出口）。
	Exists bool `json:"exists"`
}

// WarpCooldownStatePath 返回冷却账本文件路径。
//
// 路径完全由 FreeTierEngineDir 拼出，不接受外部输入 —— 面板传路径进来
// 会变成一个任意文件删除入口。
func (m *Manager) WarpCooldownStatePath() string {
	return filepath.Join(m.paths.FreeTierEngineDir(), "config.json.warp-state.json")
}

// WarpCooldown 读出口冷却状态。
//
// 文件不存在 = 没有冷却中的出口（正常状态，不是错误）。
// 文件损坏 = 按 0 条处理并让面板知道它坏了 —— 引擎侧遇到损坏文件也是
// 当空状态继续启动（loadPenalties 出错即返回空表）。
func (m *Manager) WarpCooldown() WarpCooldownState {
	path := m.WarpCooldownStatePath()
	st := WarpCooldownState{Path: path}
	raw, err := os.ReadFile(path)
	if err != nil {
		return st
	}
	st.Exists = true
	var state warpCoolingState
	if json.Unmarshal(raw, &state) != nil {
		return st
	}
	st.Cooling = len(state.Cooling)
	return st
}

// ResetWarpCooldown 清空出口冷却账本（删掉状态文件）。
//
// 返回重置前的状态，Cooling 即被清掉的出口数。
//
// 语义边界：
//   - 只删这一个文件，同目录的 config.json / 模型目录 / 日志都不动。
//   - **不重启引擎**：引擎只在启动时读这份文件，重启由用户在面板点
//     「重启引擎」，避免在这里同步等最长 95s（首次出口注册）。
//   - 不影响上游账号额度 —— 额度在服务端，本操作只清本地出口账本。
func (m *Manager) ResetWarpCooldown() (WarpCooldownState, error) {
	before := m.WarpCooldown()
	if !before.Exists {
		return before, nil
	}
	if err := os.Remove(before.Path); err != nil {
		if os.IsNotExist(err) {
			// 竞态：刚被别人删了。不算失败。
			return before, nil
		}
		return before, fmt.Errorf("删除出口冷却账本失败: %w", err)
	}
	return before, nil
}
