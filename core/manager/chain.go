// 链式代理（前置节点）解析与自动检测。
//
// 场景：目标节点（如 Falemon trojan）本地 TCP 直连被阻断，但经另一个可达节点
// （如 merged hysteria2）可以到达。sing-box 用 outbound.detour 把目标出站
// 挂到前置出站后面：本地 → 前置 → 目标 → 上游。
package manager

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// chainCacheTTL 自动检测结果缓存时长：避免每次实例启动都重新探测。
	chainCacheTTL = 10 * time.Minute
	// chainCandidateLimit 自动检测最多尝试的前置候选数（避免启动等待过久）。
	chainCandidateLimit = 4
	// chainTestTimeout 单个候选整条链验证超时。
	chainTestTimeout = 10 * time.Second
)

type chainCacheEntry struct {
	front   string
	expires time.Time
}

// ChainCheckResult 是「检测链路」接口的返回。
type ChainCheckResult struct {
	Target    string `json:"target"`
	Front     string `json:"front,omitempty"`
	OK        bool   `json:"ok"`
	LatencyMS int64  `json:"latency_ms,omitempty"`
	Error     string `json:"error,omitempty"`
}

func chainEnabled(cfg Config) bool {
	return cfg.ChainEnabled != nil && *cfg.ChainEnabled
}

func chainAutoDetect(cfg Config) bool {
	return cfg.ChainAutoDetect == nil || *cfg.ChainAutoDetect
}

// chainGroupMatch 目标节点组是否需要链式代理。
func chainGroupMatch(cfg Config, group string) bool {
	if !chainEnabled(cfg) {
		return false
	}
	if len(cfg.ChainGroups) == 0 {
		return true
	}
	for _, g := range cfg.ChainGroups {
		if strings.TrimSpace(g) == group {
			return true
		}
	}
	return false
}

func (m *Manager) cachedFront(target string) (string, bool) {
	m.chainMu.Lock()
	defer m.chainMu.Unlock()
	e, ok := m.chainCache[target]
	if !ok || time.Now().After(e.expires) {
		return "", false
	}
	return e.front, true
}

func (m *Manager) setCachedFront(target, front string) {
	m.chainMu.Lock()
	defer m.chainMu.Unlock()
	if m.chainCache == nil {
		m.chainCache = map[string]chainCacheEntry{}
	}
	m.chainCache[target] = chainCacheEntry{front: front, expires: time.Now().Add(chainCacheTTL)}
}

// invalidateChainCache 配置变更后清空缓存。
func (m *Manager) invalidateChainCache() {
	m.chainMu.Lock()
	defer m.chainMu.Unlock()
	m.chainCache = nil
}

// frontCandidates 返回目标节点的前置候选（手动优先，否则前置组内可用节点）。
func (m *Manager) frontCandidates(cfg Config, target ClashNode) ([]ClashNode, error) {
	sf := m.currentSeams()
	if sf.ListNodes == nil {
		return nil, errors.New("节点列表未装配")
	}
	if name := strings.TrimSpace(cfg.FrontNode); name != "" {
		if sf.ResolveNode == nil {
			return nil, errors.New("节点解析未装配")
		}
		f, ok := sf.ResolveNode(name)
		if !ok {
			return nil, fmt.Errorf("前置节点 '%s' 不存在", name)
		}
		if f.Server == target.Server && f.Port == target.Port {
			return nil, errors.New("前置节点不能是目标节点本身")
		}
		return []ClashNode{f}, nil
	}
	group := strings.TrimSpace(cfg.FrontGroup)
	if group == "" {
		return nil, errors.New("未配置前置组 front_group")
	}
	out := make([]ClashNode, 0, chainCandidateLimit)
	for _, n := range sf.ListNodes() {
		if n.Group != group || n.Name == target.Name {
			continue
		}
		if n.Server == target.Server && n.Port == target.Port {
			continue
		}
		if !nodeHasCredential(n) {
			continue
		}
		if usable, _ := kernelUsable(n.NodeType); !usable {
			continue
		}
		out = append(out, n)
		if len(out) >= chainCandidateLimit {
			break
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("前置组 '%s' 中没有可用节点", group)
	}
	return out, nil
}

// resolveFrontNode 为实例/扫描解析前置节点。
// autoDetect=false 时只用手动/缓存/首个候选（扫描的 12s 预算内不做完整探测）。
// 返回 chained=false 表示目标组未启用链式代理。
func (m *Manager) resolveFrontNode(target ClashNode, autoDetect bool) (ClashNode, bool, error) {
	cfg := m.loadConfig()
	if !chainGroupMatch(cfg, target.Group) {
		return ClashNode{}, false, nil
	}
	candidates, err := m.frontCandidates(cfg, target)
	if err != nil {
		return ClashNode{}, true, err
	}
	if name, ok := m.cachedFront(target.Name); ok {
		for _, c := range candidates {
			if c.Name == name {
				return c, true, nil
			}
		}
	}
	if !autoDetect || !chainAutoDetect(cfg) {
		return candidates[0], true, nil
	}
	var lastErr error
	for _, c := range candidates {
		if err := m.testChain(target, c); err != nil {
			lastErr = err
			continue
		}
		m.setCachedFront(target.Name, c.Name)
		return c, true, nil
	}
	if lastErr == nil {
		lastErr = errors.New("没有可用前置节点")
	}
	return ClashNode{}, true, fmt.Errorf("前置组无可用链路: %w", lastErr)
}

// CheckChain 手动检测某个目标节点的整条链（设置页「检测链路」）。
func (m *Manager) CheckChain(targetName string) ChainCheckResult {
	res := ChainCheckResult{Target: targetName}
	sf := m.currentSeams()
	if sf.ResolveNode == nil {
		res.Error = "节点解析未装配"
		return res
	}
	target, ok := sf.ResolveNode(targetName)
	if !ok {
		res.Error = fmt.Sprintf("节点 '%s' 不存在", targetName)
		return res
	}
	cfg := m.loadConfig()
	if !chainGroupMatch(cfg, target.Group) {
		res.Error = "目标组未启用链式代理（检查 chain_enabled / chain_groups）"
		return res
	}
	candidates, err := m.frontCandidates(cfg, target)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	var lastErr error
	for _, c := range candidates {
		start := time.Now()
		if err := m.testChain(target, c); err != nil {
			lastErr = err
			continue
		}
		m.setCachedFront(target.Name, c.Name)
		res.OK = true
		res.Front = c.Name
		res.LatencyMS = time.Since(start).Milliseconds()
		return res
	}
	if lastErr == nil {
		lastErr = errors.New("没有可用前置节点")
	}
	res.Error = lastErr.Error()
	return res
}

// testChain 启动临时 sing-box（front → target），经其 HTTP inbound 请求
// opencode.ai 的模型目录；200 表示整条链可用。只验证链路，不消耗免费 chat 额度。
func (m *Manager) testChain(target, front ClashNode) error {
	socksPort, httpPort, err := freeChainPorts()
	if err != nil {
		return err
	}
	dir := filepath.Join(m.paths.RuntimeDir, "_chain-test", sanitizeInstanceName(front.Name+"__"+target.Name))
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		return fmt.Errorf("创建链路测试目录失败: %w", err)
	}
	cfg, err := buildSingboxConfigWithFront(target, front, socksPort)
	if err != nil {
		return err
	}
	cfgPath := filepath.Join(dir, "singbox.json")
	if err := os.WriteFile(cfgPath, cfg, 0o644); err != nil {
		return fmt.Errorf("写入链路测试配置失败: %w", err)
	}
	runner := m.Run()
	if runner == nil {
		runner = &realRunner{}
	}
	pid, err := runner.Start(ExecSpec{
		Bin:      m.binPath("sing-box"),
		Args:     []string{"run", "-c", cfgPath},
		Dir:      dir,
		LogOut:   filepath.Join(dir, "logs", "singbox.out.log"),
		LogErr:   filepath.Join(dir, "logs", "singbox.err.log"),
		NoWindow: true,
	})
	if err != nil {
		return fmt.Errorf("启动链路测试 sing-box 失败: %w", err)
	}
	defer func() { _ = runner.Kill(pid) }()
	if err := waitForPort(socksPort, 5*time.Second); err != nil {
		return fmt.Errorf("链路测试 sing-box 未就绪: %w", err)
	}
	proxyURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", httpPort))
	client := &http.Client{
		Timeout: chainTestTimeout,
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		},
	}
	resp, err := client.Get("https://opencode.ai/zen/v1/models")
	if err != nil {
		return fmt.Errorf("链路测试请求失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("链路测试返回 %d", resp.StatusCode)
	}
	return nil
}

// freeChainPorts 找一对空闲的 SOCKS/HTTP 端口（HTTP = SOCKS + 2000）。
func freeChainPorts() (uint16, uint16, error) {
	for base := 46600; base < 47000; base += 2 {
		socks := uint16(base)
		httpPort := SingboxHTTPPort(socks)
		if httpPort == 0 {
			continue
		}
		if isPortFree(socks) && isPortFree(httpPort) {
			return socks, httpPort, nil
		}
	}
	return 0, 0, errors.New("找不到空闲的链路测试端口")
}
