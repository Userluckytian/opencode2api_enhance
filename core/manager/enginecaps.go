package manager

// 引擎能力探测。
//
// 为什么需要：上游 fork 的 tag（1a208ac，v1.5.1-win.1）**并不包含**
// `warp.registration_proxies`——那个字段是本地补丁加上去的，只在开发机
// 的 bin/ 里存在。管理器如果照常把它写进 config.json，正式版引擎会按
// 「未知字段」直接退出（DisallowUnknownFields），表现为 OpenCode 起不来
// （issue-log 2026-11-25 #14）。
//
// 为什么不用版本号：同一 tag 的自编译产物里有没有这个字段完全取决于是否
// 打了补丁，版本号区分不了。Go 会把 struct tag 编进二进制，直接搜字符串
// 是这里最可靠的判据。

import (
	"bytes"
	"log/slog"
	"os"
	"sync"
)

// engineCapabilityKey 缓存的键：路径 + 大小 + 修改时间。
type engineCapabilityKey struct {
	path    string
	size    int64
	modNano int64
	support bool
}

var (
	engineCapMu    sync.Mutex
	engineCapCache engineCapabilityKey
)

// engineSupportsRegistrationProxies 判断当前引擎二进制是否支持
// warp.registration_proxies。读不到文件时按「不支持」处理（保守）。
func (m *Manager) engineSupportsRegistrationProxies() bool {
	path := m.EngineBinPath()
	st, err := os.Stat(path)
	if err != nil {
		return false
	}
	key := engineCapabilityKey{path: path, size: st.Size(), modNano: st.ModTime().UnixNano()}

	engineCapMu.Lock()
	if engineCapCache == key {
		support := engineCapCache.support
		engineCapMu.Unlock()
		return support
	}
	engineCapMu.Unlock()

	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	support := bytes.Contains(raw, []byte("registration_proxies"))

	engineCapMu.Lock()
	engineCapCache = engineCapabilityKey{path: key.path, size: key.size, modNano: key.modNano, support: support}
	engineCapMu.Unlock()

	if !support {
		slog.Info("engine does not support warp.registration_proxies; registration rotation disabled",
			"engine", path)
	}
	return support
}
