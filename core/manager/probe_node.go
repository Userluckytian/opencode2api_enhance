// 单节点探测序列（Rust 语义）+ 探针进程拉起。
package manager

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// probeNode 单节点完整探测。worker 为并发 worker 索引（0 起），用于停止时登记活跃探针。
func (c *ScanController) probeNode(worker int, opts ScanOptions, node ClashNode, pair portPair, workerDir string) ProbeResult {
	base := ProbeResult{Node: node.Name, NodeType: node.NodeType, Server: node.Server, Port: node.Port}
	// S1: 停止扫描响应——探测开始前若已收到停止请求，直接放弃该节点（不 spawn 探针进程）。
	if c.isStopping() {
		base.Category = "stopped"
		base.Message = "已中止"
		return base
	}
	budget := time.Duration(opts.TimeoutSec) * time.Second
	deadline := time.Now().Add(budget)
	password := c.m.effectiveDefaultPassword()

	sbCfg, err := buildSingboxConfig(node, pair.socks)
	if err != nil {
		base.Category = "config"
		base.Message = err.Error()
		return base
	}
	if err := os.MkdirAll(filepath.Join(workerDir, "logs"), 0o755); err != nil {
		base.Category = "config"
		base.Message = err.Error()
		return base
	}
	sbPID, err := c.m.spawnProbeSingbox(c.runner, workerDir, sbCfg, pair.socks)
	if err != nil {
		base.Category = "config"
		base.Message = err.Error()
		return base
	}
	waitTimeout := 8 * time.Second
	if rem := time.Until(deadline); rem < waitTimeout {
		waitTimeout = rem
	}
	// 停止响应：waitForPort 轮询中加入中止检查，停止后不再干等至超时。
	if err := waitForPortAbort(pair.socks, waitTimeout, c.isStopping); err != nil {
		_ = c.runner.Kill(sbPID)
		base.Category = singboxFailCategory(filepath.Join(workerDir, "logs", "singbox.err.log"))
		base.Message = err.Error()
		return base
	}
	time.Sleep(400 * time.Millisecond)

	ocCfg, err := c.m.buildOpenCodeCfg(pair.socks, true)
	if err != nil {
		_ = c.runner.Kill(sbPID)
		base.Category = "config"
		base.Message = err.Error()
		return base
	}
	ocPID, err := c.m.spawnProbeOpen(c.runner, workerDir, ocCfg, pair.api, password)
	if err != nil {
		_ = c.runner.Kill(sbPID)
		base.Category = "config"
		base.Message = err.Error()
		return base
	}
	// 登记活跃探针（V1 停止中断）：两进程 spawn 成功后注册，退出时注销——覆盖所有返回路径，
	// 避免停止时 kill 不到正在跑的探针、或扫描结束登记残留。
	c.registerProbe(worker, sbPID, ocPID)
	defer c.unregisterProbe(worker)
	// 探针进程清理（防泄漏）：spawn 成功后，无论后续探测成败、任何返回路径，
	// 退出前必杀 sing-box + opencode2api 探针进程——否则每次扫描每个节点
	// 都会残留一对进程（任务管理器可见数十个 opencode2api/sing-box 堆积）。
	defer func() {
		_ = c.runner.Kill(ocPID)
		_ = c.runner.Kill(sbPID)
	}()
	apiWait := time.Until(deadline)
	if apiWait > 20*time.Second {
		apiWait = 20 * time.Second
	}
	if apiWait < 2*time.Second {
		apiWait = 2 * time.Second
	}
	if err := waitForPortAbort(pair.api, apiWait, c.isStopping); err != nil {
		_ = c.runner.Kill(ocPID)
		_ = c.runner.Kill(sbPID)
		base.Category = "upstream"
		base.Message = err.Error()
		return base
	}

	remaining := time.Until(deadline)
	if remaining < 2*time.Second {
		remaining = 2 * time.Second
	}
	if remaining > 12*time.Second {
		remaining = 12 * time.Second
	}
	// S1: 发送 HTTP 探测前响应停止——defer 已注册，探针进程会自动清理。
	if c.isStopping() {
		base.Category = "stopped"
		base.Message = "已中止"
		return base
	}
	status, body, modelCount, freeTested, httpErr := freeCompletion(pair.api, password, remaining)
	base.StatusCode = status
	if modelCount >= 0 {
		base.ModelCount = &modelCount
	}
	// 设计原则：/v1/models 返回 2xx 即视为节点可用（能连通上游）。
	// 免费模型 chat 测试只是额外验证——成功则更确信，失败/未测也不影响可用判定。
	if status >= 200 && status < 300 {
		base.OK = true
		base.Category = "ok"
		if freeTested && probeCompletionSuccess(status, body) {
			if modelCount >= 0 {
				base.Message = "可用，models=" + itoa(uint16(modelCount))
			} else {
				base.Message = "可用（免费模型最小请求成功）"
			}
		} else if modelCount >= 0 {
			base.Message = "可用，models=" + itoa(uint16(modelCount)) + "（无免费模型可测试）"
		} else {
			base.Message = "可用（models 接口连通）"
		}
		return base
	}
	if httpErr != nil {
		msg := httpErr.Error()
		if strings.Contains(msg, "timed out") || strings.Contains(msg, "超时") {
			base.Category = "timeout"
		} else {
			base.Category = "other"
		}
		base.Message = "请求失败: " + msg
		return base
	}
	switch {
	case status >= 200 && status < 300:
		base.Category = "invalid_response"
	case status == 502 || status == 503 || status == 504:
		base.Category = "upstream"
	default:
		base.Category = "other"
	}
	base.Message = fmt.Sprintf("HTTP %d，%s", status, truncateProbe(string(body), 160))
	return base
}

// spawnProbeSingbox 起 sing-box 探针进程（写配置 + spawn）。
func (m *Manager) spawnProbeSingbox(runner Runner, dir string, cfg []byte, socks uint16) (int, error) {
	if err := os.WriteFile(filepath.Join(dir, "singbox.json"), cfg, 0o644); err != nil {
		return 0, err
	}
	return runner.Start(ExecSpec{
		Bin:      m.binPath("sing-box"),
		Args:     []string{"run", "-c", filepath.Join(dir, "singbox.json")},
		Dir:      dir,
		LogOut:   filepath.Join(dir, "logs", "singbox.out.log"),
		LogErr:   filepath.Join(dir, "logs", "singbox.err.log"),
		NoWindow: true,
	})
}

// spawnProbeOpen 起 opencode2api 探针进程（cwd=workerDir，stats 落盘于内）。
func (m *Manager) spawnProbeOpen(runner Runner, dir string, ocCfg []byte, apiPort uint16, password string) (int, error) {
	if err := os.WriteFile(filepath.Join(dir, "opencode2api.json"), ocCfg, 0o644); err != nil {
		return 0, err
	}
	return runner.Start(ExecSpec{
		Bin:      m.binPath("opencode2api"),
		Args:     []string{"-port", itoa(apiPort), "-config", filepath.Join(dir, "opencode2api.json"), "-password", password},
		Env:      append([]string{"OPCODE2API_ROLE=probe"}, traceEnvKV()...), // 阶段 2：自报角色；阶段 3：透传 OPENCODE2API_TRACE
		Dir:      dir,
		LogOut:   filepath.Join(dir, "logs", "opencode2api.out.log"),
		LogErr:   filepath.Join(dir, "logs", "opencode2api.err.log"),
		NoWindow: true,
	})
}

// singboxFailCategory 分类 sing-box 启动失败（err.log tail 含 tls/cert/handshake → tls）。
func singboxFailCategory(errLog string) string {
	data, err := readFileTail(errLog, 2000)
	if err != nil {
		return "socks"
	}
	low := strings.ToLower(string(data))
	if strings.Contains(low, "tls") || strings.Contains(low, "certificate") || strings.Contains(low, "handshake") {
		return "tls"
	}
	return "socks"
}

// truncateProbe 截断正文（错误展示）。
func truncateProbe(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
