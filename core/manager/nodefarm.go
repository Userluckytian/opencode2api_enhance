package manager

// 节点代理农场：用托管 mihomo 把每个选中节点映射成一个本地 mixed 端口。
//
// 端口只用于 WARP 注册（写进 engine_warp_registration_proxies），不对外提供
// 业务出口；业务出口仍由「设置 → 出口代理」决定。

import (
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// 25001 起：24001-24016 已被 siftq 等插件的 egress 池占用，
	// 23001/22001/21001 也各有主，25001 段当前空闲。
	nodeFarmDefaultPortBase = 25001
	nodeFarmMaxNodes        = 32
)

type nodeFarm struct {
	mu         sync.Mutex
	cmd        *exec.Cmd
	pid        int
	ports      []int
	nodes      []PoolNode // 与 ports 一一对应（生成配置时的那份，避免重新读取时错位）
	configPath string
	logPath    string
	lastErr    string
	running    bool
	// gen 每次 Start/Stop 递增；监护协程据此判断自己是否已被取代，
	// 避免旧进程退出后把新农场再重启一次。
	gen        int
	done       chan struct{} // 当前进程退出时关闭
	crashCount int
}

// NodeFarmInfo 农场运行状态。
type NodeFarmInfo struct {
	Running   bool   `json:"running"`
	PID       int    `json:"pid"`
	Ports     []int  `json:"ports"`
	LastError string `json:"last_error,omitempty"`
}

func (m *Manager) farm() *nodeFarm {
	m.nodeFarmMu.Lock()
	defer m.nodeFarmMu.Unlock()
	if m.nodeFarm == nil {
		m.nodeFarm = &nodeFarm{}
	}
	return m.nodeFarm
}

// NodeFarmInfo 读当前农场状态。
func (m *Manager) NodeFarmInfo() NodeFarmInfo {
	f := m.farm()
	f.mu.Lock()
	defer f.mu.Unlock()
	ports := append([]int(nil), f.ports...)
	return NodeFarmInfo{Running: f.running, PID: f.pid, Ports: ports, LastError: f.lastErr}
}

// mihomoBin 解析 mihomo 路径：显式配置 → bin 目录 → third_party 目录。
func (m *Manager) mihomoBin() (string, error) {
	cfg := m.loadConfig()
	name := "mihomo"
	if isWindows() {
		name += ".exe"
	}
	candidates := []string{}
	if p := strings.TrimSpace(cfg.NodePoolMihomoPath); p != "" {
		candidates = append(candidates, p)
	}
	candidates = append(candidates,
		filepath.Join(m.paths.BinDir, name),
		filepath.Join(m.paths.DataDir, "node-pool", name),
	)
	for _, p := range candidates {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	return "", fmt.Errorf("未找到 mihomo（可配置 node_pool_mihomo_path，或把 %s 放到 bin 目录）", name)
}

// pickPort 从 base 起找一个空闲端口。
func pickPort(base int, used map[int]bool) (int, error) {
	for p := base; p < base+200; p++ {
		if used[p] {
			continue
		}
		ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(p))
		if err != nil {
			continue
		}
		_ = ln.Close()
		return p, nil
	}
	return 0, fmt.Errorf("从 %d 起找不到空闲端口", base)
}

// buildFarmConfig 生成 mihomo 配置：proxies 原样拼入，listeners 每节点一个端口。
func buildFarmConfig(cache nodePoolCache, nodes []PoolNode, ports []int) string {
	var b strings.Builder
	b.WriteString("# 由 opencode2api 节点池自动生成，仅用于 WARP 注册\n")
	b.WriteString("allow-lan: false\nmode: rule\nlog-level: warning\nipv6: false\n")
	b.WriteString("dns:\n  enable: false\n")
	b.WriteString("proxies:\n")
	for _, n := range nodes {
		if block := strings.TrimSpace(cache.Blocks[n.Fingerprint]); block != "" {
			b.WriteString(block)
			if !strings.HasSuffix(block, "\n") {
				b.WriteByte('\n')
			}
		}
	}
	b.WriteString("listeners:\n")
	for i, n := range nodes {
		// udp: true 是 WARP 握手中继的前提：引擎的 warp.socks_udp 通过
		// SOCKS5 UDP ASSOCIATE 把 WireGuard 数据包交给这条监听；
		// mixed 监听默认不开 UDP，漏了就只对 HTTPS 注册生效。
		fmt.Fprintf(&b, "  - name: np-%02d\n    type: mixed\n    port: %d\n    listen: 127.0.0.1\n    udp: true\n    proxy: %s\n",
			i+1, ports[i], yamlQuoteName(n.Name))
	}
	b.WriteString("rules:\n  - MATCH,DIRECT\n")
	return b.String()
}

func yamlQuoteName(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}

// StartNodeFarm 用选中节点启动农场（幂等：先停后起）。
func (m *Manager) StartNodeFarm() error {
	cfg := m.loadConfig()
	cache := m.LoadNodePoolCache()
	nodes := m.SelectedNodes()
	if len(nodes) == 0 {
		return fmt.Errorf("没有选中任何节点")
	}
	if len(nodes) > nodeFarmMaxNodes {
		nodes = nodes[:nodeFarmMaxNodes]
	}
	bin, err := m.mihomoBin()
	if err != nil {
		return err
	}
	base := cfg.NodePoolPortBase
	if base <= 0 {
		base = nodeFarmDefaultPortBase
	}
	// 先停旧农场再选端口：旧进程不退出的话它还占着 25001+，
	// pickPort 会一路跳到新端口，导致每次应用端口基址都漂移。
	m.StopNodeFarm()
	used := map[int]bool{}
	ports := make([]int, 0, len(nodes))
	for range nodes {
		p, perr := pickPort(base, used)
		if perr != nil {
			return perr
		}
		used[p] = true
		ports = append(ports, p)
	}

	dir := m.nodePoolDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	configPath := filepath.Join(dir, "farm.yaml")
	if err := os.WriteFile(configPath, []byte(buildFarmConfig(cache, nodes, ports)), 0o600); err != nil {
		return err
	}

	f := m.farm()
	cmd := exec.Command(bin, "-d", dir, "-f", configPath)
	cmd.Dir = dir
	logPath := filepath.Join(dir, "farm.log")
	lf, lerr := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if lerr == nil {
		cmd.Stdout = lf
		cmd.Stderr = lf
	}
	if err := cmd.Start(); err != nil {
		if lf != nil {
			_ = lf.Close()
		}
		return fmt.Errorf("启动 mihomo 失败: %w", err)
	}
	if lf != nil {
		_ = lf.Close()
	}

	done := make(chan struct{})
	f.mu.Lock()
	f.gen++
	gen := f.gen
	f.cmd = cmd
	f.pid = cmd.Process.Pid
	f.ports = ports
	f.nodes = append([]PoolNode(nil), nodes...)
	f.configPath = configPath
	f.logPath = logPath
	f.done = done
	f.running = true
	f.lastErr = ""
	f.mu.Unlock()

	// 等端口就绪（任一端口能连即认为起来了）
	ready := false
	for i := 0; i < 50; i++ {
		if portReady := probePort(uint16(ports[0]), 200*time.Millisecond); portReady {
			ready = true
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !ready {
		f.mu.Lock()
		f.lastErr = "mihomo 端口未就绪（详见 node-pool/farm.log）"
		f.mu.Unlock()
	}

	// 进程退出监护：非主动停止则按退避重起。
	go func(gen int, done chan struct{}) {
		err := cmd.Wait()
		f.mu.Lock()
		if f.gen != gen {
			// 已被新的 Start/Stop 取代：只清理自己，不动新农场的状态。
			f.mu.Unlock()
			close(done)
			return
		}
		f.running = false
		f.pid = 0
		if err != nil {
			f.lastErr = err.Error()
		}
		f.crashCount++
		n := f.crashCount
		f.mu.Unlock()
		close(done)

		// 只有仍启用时才自动重起
		if !m.nodePoolOn(m.loadConfig()) {
			return
		}
		delay := 5 * time.Second
		if n >= 3 {
			delay = 60 * time.Second
		} else if n == 2 {
			delay = 15 * time.Second
		}
		time.Sleep(delay)
		// 退避期间可能已被停用或手动重启
		f.mu.Lock()
		stale := f.gen != gen
		f.mu.Unlock()
		if stale || !m.nodePoolOn(m.loadConfig()) {
			return
		}
		if rerr := m.StartNodeFarm(); rerr != nil {
			log.Printf("node farm restart failed: %v", rerr)
		}
	}(gen, done)
	return nil
}

// StopNodeFarm 停止农场（幂等）。返回前最多等 3s 让旧进程退出，
// 以便调用方立刻复用同一批端口。
func (m *Manager) StopNodeFarm() {
	f := m.farm()
	f.mu.Lock()
	cmd, done := f.cmd, f.done
	f.gen++ // 让旧进程的监护协程失效
	f.cmd = nil
	f.done = nil
	f.pid = 0
	f.running = false
	f.ports = nil
	f.nodes = nil
	f.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
	}
}

// farmNodes 返回当前农场启动时使用的节点快照（与 NodeFarmInfo().Ports 对齐）。
func (m *Manager) farmNodes() []PoolNode {
	f := m.farm()
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]PoolNode(nil), f.nodes...)
}

// writeEngineRegistration 依据选中节点与分配端口，写引擎侧的两个旋钮：
//
//   - registration_proxies：注册请求轮换走这些节点（换取不同 /64）；
//   - socks_udp：把 WireGuard 外层 UDP 交给第一个 UDP 节点的 SOCKS5
//     UDP ASSOCIATE。本机没有可用的公网 IPv6 时，握手包发不出去
//     （wsasendto: address not valid in its context），只能靠中继。
//
// 引擎要求 warp.socks_udp 与 warp.source_ipv6 互斥：配了源 IPv6 前缀时
// 跳过中继（否则引擎启动即 LOAD_ERROR）。
func (m *Manager) writeEngineRegistration(ports []int) (string, error) {
	list := make([]string, 0, len(ports))
	for _, p := range ports {
		list = append(list, "http://127.0.0.1:"+strconv.Itoa(p))
	}
	if err := m.ConfigSet("engine_warp_registration_proxies", strings.Join(list, "\n")); err != nil {
		return "", err
	}

	socksUDP := ""
	if strings.TrimSpace(m.loadConfig().EngineWarpSourceIPv6) == "" {
		// 用启动农场时的节点快照，和 ports 一一对应；直接重读 SelectedNodes
		// 可能因订阅刷新改了顺序而把中继指到别的节点上。
		nodes := m.farmNodes()
		if len(nodes) != len(ports) {
			nodes = m.SelectedNodes()
		}
		for i, n := range nodes {
			if i >= len(ports) {
				break
			}
			if n.UDP {
				socksUDP = "socks5://127.0.0.1:" + strconv.Itoa(ports[i])
				break
			}
		}
	}
	if err := m.ConfigSet("engine_warp_socks_udp", socksUDP); err != nil {
		return "", err
	}
	return socksUDP, nil
}

// syncEngineRegistration 写注册代理 + SOCKS-UDP 中继，并重启引擎（异步）。
func (m *Manager) syncEngineRegistration(ports []int) error {
	if socks, err := m.writeEngineRegistration(ports); err != nil {
		return err
	} else if socks != "" {
		log.Printf("node pool: WARP handshake relay %s", socks)
	}
	go func() {
		if err := m.RestartFreeTierEngine(NewRealRunner()); err != nil {
			log.Printf("restart engine after node pool sync failed: %v", err)
		}
	}()
	return nil
}

// ApplyNodePool 保存选中项、启动农场、同步到引擎。
func (m *Manager) ApplyNodePool(selected []string) error {
	clean := make([]string, 0, len(selected))
	seen := map[string]bool{}
	for _, s := range selected {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		clean = append(clean, s)
	}
	if err := m.ConfigSet("node_pool_selected", strings.Join(clean, "\n")); err != nil {
		return err
	}
	if b := "false"; len(clean) > 0 {
		_ = m.ConfigSet("node_pool_enabled", "true")
	} else {
		_ = m.ConfigSet("node_pool_enabled", b)
	}
	if len(clean) == 0 {
		return m.DisableNodePool()
	}
	if err := m.StartNodeFarm(); err != nil {
		return err
	}
	info := m.NodeFarmInfo()
	if len(info.Ports) == 0 {
		return fmt.Errorf("农场未就续: %s", info.LastError)
	}
	return m.syncEngineRegistration(info.Ports)
}

// AutoStartNodePool 启动时按配置恢复农场（仅当启用且已选节点）。
// 只写配置、不重启引擎：引擎启动时自己会读。
func (m *Manager) AutoStartNodePool() {
	m.nodePoolRefreshOnce.Do(func() { go m.nodePoolRefreshLoop() })
	if !m.nodePoolOn(m.loadConfig()) || len(m.SelectedNodes()) == 0 {
		return
	}
	if err := m.StartNodeFarm(); err != nil {
		log.Printf("node pool autostart failed: %v", err)
		return
	}
	info := m.NodeFarmInfo()
	if len(info.Ports) == 0 {
		return
	}
	if _, err := m.writeEngineRegistration(info.Ports); err != nil {
		log.Printf("node pool sync failed: %v", err)
	}
}

// nodePoolRefreshLoop 按 node_pool_refresh_min 定时重拉订阅（只更新节点缓存，不改选中项）。
func (m *Manager) nodePoolRefreshLoop() {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for range t.C {
		m.refreshNodePoolIfDue()
	}
}

func (m *Manager) refreshNodePoolIfDue() {
	cfg := m.loadConfig()
	if !m.nodePoolOn(cfg) || cfg.NodePoolRefreshMin <= 0 || strings.TrimSpace(cfg.NodePoolSubscriptionURL) == "" {
		return
	}
	cache := m.LoadNodePoolCache()
	if last, err := time.Parse(time.RFC3339, cache.FetchedAt); err == nil {
		if time.Since(last) < time.Duration(cfg.NodePoolRefreshMin)*time.Minute {
			return
		}
	}
	if _, _, err := m.ImportNodePool(cfg.NodePoolSubscriptionURL, ""); err != nil {
		log.Printf("node pool refresh failed: %v", err)
	}
}

// DisableNodePool 停止农场并清空引擎的注册代理与中继（回退直连）。
func (m *Manager) DisableNodePool() error {
	_ = m.ConfigSet("node_pool_enabled", "false")
	m.StopNodeFarm()
	if err := m.ConfigSet("engine_warp_registration_proxies", ""); err != nil {
		return err
	}
	if err := m.ConfigSet("engine_warp_socks_udp", ""); err != nil {
		return err
	}
	go func() {
		if err := m.RestartFreeTierEngine(NewRealRunner()); err != nil {
			log.Printf("restart engine after node pool disable failed: %v", err)
		}
	}()
	return nil
}

// NodePoolStatusOf 汇总面板状态。
func (m *Manager) NodePoolStatusOf() NodePoolStatus {
	cfg := m.loadConfig()
	cache := m.LoadNodePoolCache()
	info := m.NodeFarmInfo()
	sel := cfg.NodePoolSelected
	if sel == nil {
		sel = []string{}
	}
	st := NodePoolStatus{
		Enabled:     m.nodePoolOn(cfg),
		Running:     info.Running,
		PID:         info.PID,
		PortBase:    cfg.NodePoolPortBase,
		Ports:       info.Ports,
		Nodes:       cache.Nodes,
		Selected:    sel,
		NodeCount:   len(cache.Nodes),
		SelectedLen: len(sel),
		SocksUDP:    cfg.EngineWarpSocksUDP,
		LastError:   info.LastError,
		UpdatedAt:   cache.FetchedAt,
	}
	if st.PortBase <= 0 {
		st.PortBase = nodeFarmDefaultPortBase
	}
	if bin, err := m.mihomoBin(); err == nil {
		st.MihomoPath = bin
	}
	if len(info.Ports) > 0 {
		st.SyncedTo = "engine_warp_registration_proxies"
	}
	// 中继不可用的两种原因直接告诉用户，避免「导了节点还是起不来」的困惑。
	if st.Enabled {
		selected := m.SelectedNodes()
		hasUDP := false
		for _, n := range selected {
			if n.UDP {
				hasUDP = true
				break
			}
		}
		switch {
		case strings.TrimSpace(cfg.EngineWarpSourceIPv6) != "":
			st.Warning = "已配置源 IPv6 前缀：引擎要求它与 SOCKS-UDP 互斥，已跳过握手中继"
		case len(selected) > 0 && !hasUDP:
			st.Warning = "所选节点都不支持 UDP：握手中继不可用，WARP 握手仍会直连并可能失败"
		case st.SocksUDP == "" && hasUDP:
			st.Warning = "尚未写入握手中继，点一次「应用并重启引擎」生效"
		}
	}
	return st
}

// nodePoolOn 节点池开关（未配置默认关闭）。
func (m *Manager) nodePoolOn(cfg Config) bool {
	return cfg.NodePoolEnabled != nil && *cfg.NodePoolEnabled
}
