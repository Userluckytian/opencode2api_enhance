package manager

// 节点代理农场：用托管 mihomo 把每个选中节点映射成一个本地 mixed 端口。
//
// 端口只用于 WARP 注册（写进 engine_warp_registration_proxies），不对外提供
// 业务出口；业务出口仍由「设置 → 出口代理」决定。

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// newFarmSecret 生成 mihomo controller 的随机 secret（仅本机回环可达，
// 仍然加一道门：同机其它进程不该能改节点池的出口映射）。
func newFarmSecret() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "oc-nodefarm"
	}
	return hex.EncodeToString(b[:])
}

const (
	// 25001 起：24001-24016 已被 siftq 等插件的 egress 池占用，
	// 23001/22001/21001 也各有主，25001 段当前空闲。
	nodeFarmDefaultPortBase = 25001
	nodeFarmMaxNodes        = 32
	// nodeFarmSlots 固定注册槽位数（引擎生命周期内端口集合不变）。
	//
	// 引擎只在启动时读 warp.registration_proxies / socks_udp，之后注册器列表
	// 就固定了。若按「选中节点数」分配端口，增删一个节点就要重启引擎。
	// 固定 8 个槽位后，增删节点只改「槽位 → 节点」映射，通过 mihomo 的
	// controller 热重载（PUT /configs）生效，引擎完全不用动。
	nodeFarmSlots = 8
	// nodeFarmCtlOffset controller 端口相对基址的偏移（26001）。
	nodeFarmCtlOffset = 1000
)

type nodeFarm struct {
	mu         sync.Mutex
	cmd        *exec.Cmd
	pid        int
	ports      []int
	nodes      []PoolNode // 与 ports 一一对应（槽位映射后的节点，避免读取时错位）
	configPath string
	logPath    string
	lastErr    string
	running    bool
	// ctlPort / secret 是 mihomo external-controller，用于热重载配置。
	ctlPort int
	secret  string
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
	Slots     int    `json:"slots"`
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
	return NodeFarmInfo{Running: f.running, PID: f.pid, Ports: ports, Slots: len(ports), LastError: f.lastErr}
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

// slotNodes 把选中节点摊到固定槽位上：去重、第一个 UDP 节点优先占 0 号槽，
// 其余按顺序循环填充。返回长度恒为 slots，与 ports 一一对应。
//
// 0 号槽固定作为 warp.socks_udp 的中继端口：引擎只在启动时读这个字段，
// 端口不变就不需要重启；把 UDP 节点钉在 0 号槽，增删其它节点都不会让中继失效。
func slotNodes(nodes []PoolNode, slots int) []PoolNode {
	if slots <= 0 || len(nodes) == 0 {
		return nil
	}
	uniq := make([]PoolNode, 0, len(nodes))
	seen := map[string]bool{}
	udpIdx := -1
	for _, n := range nodes {
		if seen[n.Fingerprint] {
			continue
		}
		seen[n.Fingerprint] = true
		if udpIdx < 0 && n.UDP {
			udpIdx = len(uniq)
		}
		uniq = append(uniq, n)
	}
	if udpIdx > 0 {
		first := uniq[udpIdx]
		copy(uniq[1:udpIdx+1], uniq[:udpIdx])
		uniq[0] = first
	}
	out := make([]PoolNode, slots)
	for i := range out {
		out[i] = uniq[i%len(uniq)]
	}
	return out
}

// buildFarmConfig 生成 mihomo 配置：proxies 去重后原样拼入，每个槽位一个监听。
//
// external-controller + secret 用于热重载（PUT /configs?force=true）：
// 增删节点只改槽位映射，不重启引擎也不需要重启 mihomo 进程。
func buildFarmConfig(cache nodePoolCache, all []PoolNode, slots []PoolNode, ports []int, ctlPort int, secret string) string {
	var b strings.Builder
	b.WriteString("# 由 opencode2api 节点池自动生成，仅用于 WARP 注册\n")
	b.WriteString("allow-lan: false\nmode: rule\nlog-level: warning\nipv6: false\n")
	if ctlPort > 0 {
		fmt.Fprintf(&b, "external-controller: 127.0.0.1:%d\n", ctlPort)
		if secret != "" {
			fmt.Fprintf(&b, "secret: %s\n", yamlQuoteName(secret))
		}
	}
	b.WriteString("dns:\n  enable: false\n")
	b.WriteString("proxies:\n")
	written := map[string]bool{}
	for _, n := range slots {
		if written[n.Fingerprint] {
			continue
		}
		written[n.Fingerprint] = true
		if block := strings.TrimSpace(cache.Blocks[n.Fingerprint]); block != "" {
			b.WriteString(block)
			if !strings.HasSuffix(block, "\n") {
				b.WriteByte('\n')
			}
		}
	}
	b.WriteString("listeners:\n")
	for i, n := range slots {
		if i >= len(ports) {
			break
		}
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
	// 固定槽位：端口集合在引擎生命周期内不变（引擎只在启动时读注册列表）。
	// 槽位映射优先用上次自动选源（实测可达）的结果。
	mapped := preferredSlotNodes(nodes, splitLinesOrCommas(cfg.NodePoolSlotMap), nodeFarmSlots)
	ports := make([]int, 0, len(mapped))
	for range mapped {
		p, perr := pickPort(base, used)
		if perr != nil {
			return perr
		}
		used[p] = true
		ports = append(ports, p)
	}
	// controller 端口：拿不到只是失去「不重启 mihomo 的热重载」，
	// 注册本身不受影响（ReloadNodeFarm 会退化成重启农场进程）。
	ctlPort, _ := pickPort(base+nodeFarmCtlOffset, used)
	used[ctlPort] = true
	secret := newFarmSecret()

	dir := m.nodePoolDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	configPath := filepath.Join(dir, "farm.yaml")
	// 保留「所有候选」在 proxies 段，槽位只决定 listeners。
	all := m.SelectedNodes()
	if len(all) == 0 {
		all = nodes
	}
	if err := os.WriteFile(configPath, []byte(buildFarmConfig(cache, all, mapped, ports, ctlPort, secret)), 0o600); err != nil {
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
	f.nodes = mapped
	f.configPath = configPath
	f.ctlPort = ctlPort
	f.secret = secret
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
		// 配置错误（例如订阅节点缺 uuid/password）会让 mihomo 启动即退出。
		// 这里必须把失败传出去：否则 ApplyNodePool 会写一组指向死端口的
		// 注册配置并重启引擎，用户看到「已应用」但引擎一个出口都注册不上。
		msg := "mihomo 端口未就绪（详见 node-pool/farm.log）"
		f.mu.Lock()
		f.lastErr = msg
		f.running = false
		f.pid = 0
		f.gen++ // 让即将启动的监护协程失效
		f.mu.Unlock()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		close(done)
		return fmt.Errorf("%s", msg)
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

// ReloadNodeFarm 只改「槽位 → 节点」映射：重写 farm.yaml 并让 mihomo 热重载。
//
// 端口集合与 socks_udp 都不变，所以引擎不需要重启——这是「补充/替换节点」
// 不再断服务的核心。controller 不可用或重载失败时退化为重启 mihomo 进程，
// 端口集合仍然不变，引擎依旧不用动。
func (m *Manager) ReloadNodeFarm(nodes []PoolNode) error {
	if len(nodes) == 0 {
		return fmt.Errorf("没有选中任何节点")
	}
	f := m.farm()
	f.mu.Lock()
	running := f.running
	ports := append([]int(nil), f.ports...)
	configPath, ctlPort, secret := f.configPath, f.ctlPort, f.secret
	f.mu.Unlock()
	if !running || len(ports) == 0 {
		return fmt.Errorf("农场未运行")
	}
	if len(ports) != nodeFarmSlots {
		// 2.0.14 及更早的农场按节点数分配端口；升级后第一次需要重建成固定槽位。
		return fmt.Errorf("农场槽位未固定（%d 个端口），需要重建农场", len(ports))
	}
	preferred := splitLinesOrCommas(m.loadConfig().NodePoolSlotMap)
	mapped := preferredSlotNodes(nodes, preferred, len(ports))
	cache := m.LoadNodePoolCache()
	// 保留「所有候选」在 proxies 段，槽位只决定 listeners。
	all := m.SelectedNodes()
	if len(all) == 0 {
		all = nodes
	}
	if err := os.WriteFile(configPath, []byte(buildFarmConfig(cache, all, mapped, ports, ctlPort, secret)), 0o600); err != nil {
		return err
	}
	if ctlPort > 0 {
		if err := mihomoReload(ctlPort, secret, configPath); err == nil {
			f.mu.Lock()
			f.nodes = mapped
			f.mu.Unlock()
			log.Printf("node farm hot-reloaded: %d nodes -> %d slots (engine untouched)", len(nodes), len(ports))
			return nil
		} else {
			log.Printf("node farm hot reload failed (%v); restarting mihomo with the same ports", err)
		}
	}
	// 退化：重启 mihomo 进程（端口集合不变，引擎仍然不用重启）。
	return m.StartNodeFarm()
}

// mihomoReload 调 mihomo 的 external-controller 重载配置。
//
// PUT /configs?force=true 会按 path 重新读取配置文件并重建 listeners；
// force 让它在端口被自己占用时也能重绑（这正是我们要的）。
func mihomoReload(port int, secret, configPath string) error {
	body, _ := json.Marshal(map[string]string{"path": configPath})
	req, err := http.NewRequest(http.MethodPut,
		fmt.Sprintf("http://127.0.0.1:%d/configs?force=true", port), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("mihomo reload: %s %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	return nil
}

// restartEngineAsync 异步重启引擎（节点池配置变化后用）。
// 引擎被显式关闭时不重启：只写配置，下次用户自己启动时生效。
func (m *Manager) restartEngineAsync() {
	if !engineEnabled(m.loadConfig()) {
		log.Printf("node pool: engine disabled by config; skip restart")
		return
	}
	go func() {
		if err := m.RestartFreeTierEngine(NewRealRunner()); err != nil {
			log.Printf("restart engine after node pool sync failed: %v", err)
		}
	}()
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
	// 引擎不支持 registration_proxies 时**必须**写空：正式版引擎用
	// DisallowUnknownFields，残留这个键会让它启动即退出（见 enginecaps.go）。
	if m.engineSupportsRegistrationProxies() {
		if err := m.ConfigSet("engine_warp_registration_proxies", strings.Join(list, "\n")); err != nil {
			return "", err
		}
	} else if err := m.ConfigSet("engine_warp_registration_proxies", ""); err != nil {
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

// syncRegistrationAfterApply 把农场端口写进引擎配置，并且只在「引擎能看到的
// 配置真的变了」时重启引擎：
//
//   - 端口集合没变（固定 8 槽位）→ 只改了 mihomo 的槽位映射，引擎不用动；
//   - 空 → 有（首次启用）、或 socks_udp 空↔有 → 引擎只在启动时读这两个字段，
//     必须重启一次。
func (m *Manager) syncRegistrationAfterApply() (hot bool, err error) {
	info := m.NodeFarmInfo()
	prevList, _ := m.ConfigGet("engine_warp_registration_proxies")
	prevSocks, _ := m.ConfigGet("engine_warp_socks_udp")
	socks, err := m.writeEngineRegistration(info.Ports)
	if err != nil {
		return false, err
	}
	if socks != "" && socks != prevSocks {
		log.Printf("node pool: WARP handshake relay %s", socks)
	}
	newList, _ := m.ConfigGet("engine_warp_registration_proxies")
	if prevList == newList && prevSocks == socks {
		log.Printf("node pool: engine registration unchanged (%d slots); no engine restart", len(info.Ports))
		return true, nil
	}
	m.restartEngineAsync()
	return false, nil
}

// ApplyNodePool 保存选中项、启动/热重载农场、同步到引擎。
//
// hot=true 表示没有重启引擎：增删节点只换槽位映射（下一次出口注册生效）。
func (m *Manager) ApplyNodePool(selected []string) (hot bool, err error) {
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
		return false, err
	}
	if b := "false"; len(clean) > 0 {
		_ = m.ConfigSet("node_pool_enabled", "true")
	} else {
		_ = m.ConfigSet("node_pool_enabled", b)
	}
	if len(clean) == 0 {
		return false, m.DisableNodePool()
	}
	// 农场已按固定槽位在跑：先热重载映射（引擎完全不动）；失败再重建农场。
	info := m.NodeFarmInfo()
	if info.Running && len(info.Ports) == nodeFarmSlots {
		if rerr := m.ReloadNodeFarm(m.SelectedNodes()); rerr != nil {
			log.Printf("node pool: hot reload failed (%v); rebuilding farm", rerr)
			if serr := m.StartNodeFarm(); serr != nil {
				return false, serr
			}
		}
	} else if serr := m.StartNodeFarm(); serr != nil {
		return false, serr
	}
	info = m.NodeFarmInfo()
	if len(info.Ports) == 0 {
		return false, fmt.Errorf("农场未就绪: %s", info.LastError)
	}
	// 自动选源：mihomo 对每个候选节点跑一次真实请求（delay API），按延迟挑
	// 可达的填槽位——不再固定用列表前 8 个。verified=false（无 controller）
	// 时退回槽位探测。
	info, verified, selErr := m.autoSelectNodeSlots(info)
	if selErr != nil {
		_ = m.ConfigSet("node_pool_last_warning", selErr.Error())
		return false, selErr
	}
	if !verified {
		if err := m.verifySlotReachability(info); err != nil {
			return false, err
		}
	}
	return m.syncRegistrationAfterApply()
}

// verifySlotReachability 槽位级兜底探测（没有 controller 时用）。
func (m *Manager) verifySlotReachability(info NodeFarmInfo) error {
	probes := probeNodeFarmSlots(info.Ports)
	slots := m.farmNodes()
	dead := make([]string, 0, len(probes))
	for i, pr := range probes {
		if pr.Err == nil {
			continue
		}
		label := fmt.Sprintf("端口 %d", pr.Port)
		if i < len(slots) {
			label = slots[i].Name
		}
		dead = append(dead, label)
	}
	if len(dead) == len(probes) {
		msg := fmt.Sprintf("选中节点全部不可达（%d/%d）：%s；请检查订阅节点或先停用节点池",
			len(dead), len(probes), strings.Join(dead, "、"))
		_ = m.ConfigSet("node_pool_last_warning", msg)
		return fmt.Errorf("%s", msg)
	}
	if len(dead) > 0 {
		msg := fmt.Sprintf("%d/%d 个节点不可达：%s（这些槽位的注册会失败）",
			len(dead), len(probes), strings.Join(dead, "、"))
		_ = m.ConfigSet("node_pool_last_warning", msg)
		log.Printf("node pool: %s", msg)
	} else {
		_ = m.ConfigSet("node_pool_last_warning", "")
	}
	return nil
}

// autoSelectNodeSlots 用 mihomo 的 /proxies/{name}/delay 测所有选中的节点，
// 按延迟挑可达的填满槽位（0 号槽优先 UDP），并把结果持久化到 node_pool_slot_map。
//
// 返回 verified=false 表示当前农场没有 controller（2.0.14 及更早的农场），
// 调用方应退回槽位探测。
func (m *Manager) autoSelectNodeSlots(info NodeFarmInfo) (NodeFarmInfo, bool, error) {
	candidates := m.SelectedNodes()
	if len(candidates) == 0 {
		return info, false, nil
	}
	const maxCandidates = 120
	if len(candidates) > maxCandidates {
		candidates = candidates[:maxCandidates]
	}
	f := m.farm()
	f.mu.Lock()
	cfgPath, ctlPort, secret := f.configPath, f.ctlPort, f.secret
	ports := append([]int(nil), f.ports...)
	current := append([]PoolNode(nil), f.nodes...)
	f.mu.Unlock()
	if !info.Running || ctlPort <= 0 || len(ports) == 0 || cfgPath == "" {
		return info, false, nil
	}
	cache := m.LoadNodePoolCache()
	// 第一阶段：把所有候选写进 proxies 段（listeners 保持当前映射），
	// 这样 delay API 能测到任意候选节点。
	if err := os.WriteFile(cfgPath, []byte(buildFarmConfig(cache, candidates, current, ports, ctlPort, secret)), 0o600); err != nil {
		return info, true, err
	}
	if err := mihomoReload(ctlPort, secret, cfgPath); err != nil {
		return info, true, err
	}
	delays := probeCandidates(ctlPort, secret, candidates, 4*time.Second)
	chosen := chooseSlotNodes(candidates, delays, len(ports))
	if len(chosen) == 0 {
		return info, true, fmt.Errorf("所有选中节点都不可达（延迟测试全部失败）；请检查订阅节点或先停用节点池")
	}
	if !sameSlotFingerprints(chosen, current) {
		if err := os.WriteFile(cfgPath, []byte(buildFarmConfig(cache, candidates, chosen, ports, ctlPort, secret)), 0o600); err != nil {
			return info, true, err
		}
		if err := mihomoReload(ctlPort, secret, cfgPath); err != nil {
			return info, true, err
		}
		f.mu.Lock()
		f.nodes = chosen
		f.mu.Unlock()
	}
	dead := len(candidates) - len(delays)
	if dead > 0 {
		_ = m.ConfigSet("node_pool_last_warning",
			fmt.Sprintf("%d/%d 个候选节点不可达，已自动跳过（按延迟选源）", dead, len(candidates)))
	} else {
		_ = m.ConfigSet("node_pool_last_warning", "")
	}
	// 持久化槽位映射：订阅刷新/农场重启沿用，不再冲回列表前 8 个。
	uniq := make([]string, 0, len(chosen))
	seen := map[string]bool{}
	for _, n := range chosen {
		if !seen[n.Fingerprint] {
			seen[n.Fingerprint] = true
			uniq = append(uniq, n.Fingerprint)
		}
	}
	_ = m.ConfigSet("node_pool_slot_map", strings.Join(uniq, "\n"))
	return m.NodeFarmInfo(), true, nil
}

// sameSlotFingerprints 比较两组槽位映射是否一致。
func sameSlotFingerprints(a, b []PoolNode) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Fingerprint != b[i].Fingerprint {
			return false
		}
	}
	return true
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
		return
	}
	// 订阅刷新可能改了节点参数（server / 密码 / udp）：农场在跑就热重载一次，
	// 让新参数在下一轮注册生效 —— 不重启引擎。
	if info := m.NodeFarmInfo(); info.Running && len(info.Ports) == nodeFarmSlots {
		if err := m.ReloadNodeFarm(m.SelectedNodes()); err != nil {
			log.Printf("node farm reload after subscription refresh failed: %v", err)
		}
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
	m.restartEngineAsync()
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
		HiddenCount: len(cache.Hidden),
		Slots:       info.Slots,
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
		// 槽位 → 节点指纹：与引擎 warp.registrars 的 index 一一对应。
		slots := m.farmNodes()
		st.SlotsNodes = make([]string, 0, len(slots))
		for _, n := range slots {
			st.SlotsNodes = append(st.SlotsNodes, n.Fingerprint)
		}
	}
	var warns []string
	// 槽位有限：超出部分的节点不会参与注册轮换，必须说清楚，否则用户会以为
	// 选中的 47 个节点都在用。
	if st.Enabled && len(info.Ports) > 0 && len(sel) > len(info.Ports) {
		unique := map[string]bool{}
		for _, n := range m.farmNodes() {
			unique[n.Fingerprint] = true
		}
		warns = append(warns, fmt.Sprintf(
			"已选 %d 个节点，注册轮换固定 %d 个槽位，实际只用到前 %d 个（其余为备用）",
			len(sel), len(info.Ports), len(unique)))
	}
	// 端口漂移：农场崩溃重启后若端口被别的进程占走，会换一组端口，而引擎
	// 只在启动时读注册表 —— 不提示的话注册会静默走死端口。
	if st.Running && len(info.Ports) > 0 && registrationPortDrift(cfg.EngineWarpRegistrationProxies, info.Ports) {
		warns = append(warns, "农场端口与引擎注册表不一致（端口漂移）：重启引擎或重新「应用」一次")
	}
	// 中继不可用的原因直接告诉用户，避免「导了节点还是起不来」的困惑。
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
			warns = append(warns, "已配置源 IPv6 前缀：引擎要求它与 SOCKS-UDP 互斥，已跳过握手中继")
		case !m.engineSupportsRegistrationProxies():
			warns = append(warns, "当前引擎不支持注册代理轮换（warp.registration_proxies）：只启用 SOCKS-UDP 中继，可用出口会比所选节点少")
		case len(selected) > 0 && !hasUDP:
			warns = append(warns, "所选节点都不支持 UDP：握手中继不可用，WARP 握手仍会直连并可能失败")
		case st.SocksUDP == "" && hasUDP:
			warns = append(warns, "尚未写入握手中继，点一次「应用」生效")
		}
	}
	if last := strings.TrimSpace(cfg.NodePoolLastWarning); last != "" {
		warns = append(warns, last)
	}
	st.Warning = strings.Join(warns, "；")
	return st
}

// registrationPortDrift 判断引擎注册表里的端口集合与当前农场端口是否一致。
// 空注册表（未启用/引擎不支持）不算漂移。
func registrationPortDrift(proxies []string, ports []int) bool {
	cur := map[int]bool{}
	for _, line := range proxies {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		u, err := url.Parse(line)
		if err != nil {
			continue
		}
		if p, perr := strconv.Atoi(u.Port()); perr == nil {
			cur[p] = true
		}
	}
	if len(cur) == 0 {
		return false
	}
	if len(cur) != len(ports) {
		return true
	}
	for _, p := range ports {
		if !cur[p] {
			return true
		}
	}
	return false
}

// nodePoolOn 节点池开关（未配置默认关闭）。
func (m *Manager) nodePoolOn(cfg Config) bool {
	return cfg.NodePoolEnabled != nil && *cfg.NodePoolEnabled
}
