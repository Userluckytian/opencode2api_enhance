package main

import (
	"fmt"
	"net"
	"net/netip"
	"os"
	"strings"
)

// EnvListen 是部署方注入监听地址的环境变量名。
// 解析优先级：命令行 -listen > 环境变量 TOKEN_BUFFET_LISTEN > config.json 的 listen > 默认回环。
const EnvListen = "TOKEN_BUFFET_LISTEN"

// DefaultListenAddress 是默认监听地址：仅回环。
//
// 安全默认值（收敛 finding 547 / fact 589 / 592）：
// 旧实现把监听地址硬编码成 "0.0.0.0:<port>"，于是 web-server（网页自助领 Key 服务）
// 一启动就对整个网段暴露——包括管理后台（/admin.html + /api/admin/*）与领取口。
// 同机的 nginx 反代只需要访问 127.0.0.1，因此默认监听回环即可满足反代场景；
// 确实需要对外直连时，必须显式声明（-listen 0.0.0.0 或 config.json 的 listen 字段），
// 让「暴露管理面」成为部署者主动做出的决定，而不是默认副作用。
const DefaultListenAddress = "127.0.0.1"

// listenSource 描述生效监听地址来自哪一层，便于运维复盘。
const (
	listenSourceFlag    = "命令行 -listen"
	listenSourceEnv     = "环境变量 " + EnvListen
	listenSourceConfig  = "config.json 的 listen 字段"
	listenSourceDefault = "内置默认值（仅回环）"
)

// resolveListenAddr 解析生效的监听地址。
//
// 返回值 addr 只含主机部分（不含端口），端口由 config.json 的 port 决定；
// source 说明该值来自哪一层，写进启动日志，便于事后审计「谁把服务开到外网」。
func resolveListenAddr(flagValue string, cfg *Config) (addr string, source string) {
	// 1) 命令行
	if v := strings.TrimSpace(flagValue); v != "" {
		return normalizeListenHost(v), listenSourceFlag
	}
	// 2) 环境变量
	if v := strings.TrimSpace(os.Getenv(EnvListen)); v != "" {
		return normalizeListenHost(v), listenSourceEnv
	}
	// 3) config.json
	if cfg != nil {
		if v := strings.TrimSpace(cfg.Listen); v != "" {
			return normalizeListenHost(v), listenSourceConfig
		}
	}
	// 4) 默认回环
	return DefaultListenAddress, listenSourceDefault
}

// normalizeListenHost 规整监听主机：允许只写主机或 host:port（端口部分被忽略，
// 端口统一由 config.json 的 port 决定），空值等价于回环。
func normalizeListenHost(v string) string {
	v = strings.TrimSpace(v)
	v = strings.Trim(v, "[]")
	if v == "" {
		return DefaultListenAddress
	}
	// 形如 "0.0.0.0:8080" → 取主机部分
	if h, _, err := net.SplitHostPort(v); err == nil {
		v = h
	}
	v = strings.Trim(v, "[]")
	if v == "" {
		return DefaultListenAddress
	}
	// localhost 视为回环：它是本机别名，不该被当成对外监听。
	if strings.EqualFold(v, "localhost") {
		return "127.0.0.1"
	}
	return v
}

// isLoopbackListen 判断监听地址是否只对本机可见。
//
// 通配地址（0.0.0.0 / :: / 空）视为「对外」，因为它会绑定所有网卡。
func isLoopbackListen(addr string) bool {
	a := normalizeListenHost(addr)
	if a == "" {
		return false
	}
	ap, err := netip.ParseAddr(a)
	if err != nil {
		// 解析失败（例如配置了主机名）按「对外」处理：宁可告警也不漏判。
		return false
	}
	ap = ap.Unmap()
	if !ap.IsValid() {
		return false
	}
	return ap.IsLoopback()
}

// describeListenExposure 给出监听面的可读描述（写进启动日志与管理说明）。
func describeListenExposure(addr string) string {
	if isLoopbackListen(addr) {
		return fmt.Sprintf("%s（仅本机可访问，管理面不会直接暴露到网络）", addr)
	}
	return fmt.Sprintf("%s（**对外监听**：同网段可直接访问管理后台与领取口，必须由反代/防火墙收口）", addr)
}

// checkListenPolicy 是启动自检：核对「生效监听地址」与部署者声明的策略是否一致。
//
// requireLoopback=true（开发/测试实例用）时，非回环监听直接返回错误阻止启动，
// 防止 dev 实例被误当成正式实例上线，把管理面暴露到整个网段。
// requireLoopback=false（正式实例）时，非回环监听不阻断，但必须打高危告警——
// 因为此时唯一的边界就只剩 nginx/防火墙。
func checkListenPolicy(addr string, requireLoopback bool) error {
	if isLoopbackListen(addr) {
		return nil
	}
	if requireLoopback {
		return fmt.Errorf("拒绝启动：要求仅回环监听，但生效地址为 %s；"+
			"如确需对外，请去掉 -require-loopback 并自行承担暴露面", addr)
	}
	return nil
}
