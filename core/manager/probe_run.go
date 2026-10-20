// 扫描执行（并发 worker + 端口分配）。
package manager

import (
	"fmt"
	"path/filepath"
	"sync"
	"time"
)

type portPair struct{ api, socks uint16 }

// run 执行扫描（后台 goroutine）。
func (c *ScanController) run(opts ScanOptions, nodes []ClashNode) {
	defer func() {
		c.mu.Lock()
		c.progress.FinishedMS = time.Now().UnixMilli()
		// L8：收尾只对"进行中/停止中"的扫描置 Done—— allocatePorts 等失败路径
		// 已置 ScanError 时原样保留，defer 不得把错误态覆盖成 done。
		switch c.progress.Status {
		case ScanError:
			// 错误态保留（body 已写 Error 文案）
		case ScanStopping:
			c.progress.Status = ScanDone
			c.progress.Error = "扫描已中止（已完成部分结果保留）"
		default:
			c.progress.Status = ScanDone
		}
		c.mu.Unlock()
	}()

	n := len(nodes)
	workers := opts.Concurrency
	if workers > n {
		workers = n
	}
	if workers < 1 {
		workers = 1
	}
	ports, err := c.allocatePorts(opts, workers)
	if err != nil {
		c.mu.Lock()
		c.progress.Status = ScanError
		c.progress.Error = err.Error()
		c.mu.Unlock()
		return
	}
	c.mu.Lock()
	c.progress.Total = n
	c.mu.Unlock()

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			// worker 错开启动（300ms/worker）：避免多进程同时拉起的资源尖峰
			// （对齐 main 修复——并发 8 个 sing-box/opencode2api 同时 spawn 易超时）。
			time.Sleep(time.Duration(w) * 300 * time.Millisecond)
			pair := ports[w]
			workerDir := filepath.Join(c.m.paths.RuntimeDir, fmt.Sprintf("_probe/worker-%02d", w+1))
			for i := w; i < n; i += workers {
				if c.isStopping() {
					return
				}
				start := time.Now()
				res := c.probeNode(w, opts, nodes[i], pair, workerDir)
				res.LatencyMS = time.Since(start).Milliseconds()
				c.mu.Lock()
				c.progress.Results = append(c.progress.Results, res)
				if !c.stoppingLocked() {
					c.progress.CurrentNode = nodes[i].Name
				}
				c.progress.Current++
				c.mu.Unlock()
			}
		}(w)
	}
	wg.Wait()
}

func (c *ScanController) isStopping() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.progress.Status == ScanStopping
}

func (c *ScanController) stoppingLocked() bool {
	return c.progress.Status == ScanStopping
}

// allocatePorts 按序分配端口对（api+socks 均 ≥1024、互不重复、当前空闲）。
// 关键：**显式避开当前环境的实例段与 sing-box 段**——探针 worker 端口不能与
// 实例/实例 sing-box 端口重叠（否则扫描后添加的实例会因端口被探针占用而
// 启动失败/测试 401；实测 worker 跳号曾占 44201/46201）。
func (c *ScanController) allocatePorts(opts ScanOptions, need int) ([]portPair, error) {
	instBase := uint16(0)
	if c.m != nil {
		instBase = c.m.instanceBasePort()
	}
	instLo, instHi := instBase+100, instBase+2100 // 实例 API 段 [instLo, instHi)
	sbLo, sbHi := instBase+2100, instBase+4100    // 实例 sing-box 段 [sbLo, sbHi)（singbox = api+2000）
	var pairs []portPair
	used := map[uint16]bool{}
	for offset := 0; offset < 4096 && len(pairs) < need; offset++ {
		api := opts.APIPort + uint16(offset)
		socks := opts.SocksPort + uint16(offset)
		if api < 1024 || socks < 1024 || used[api] || used[socks] || api == socks {
			continue
		}
		// 探针端口不得落入实例段 / sing-box 段（跨环境槽同理，instBase=0 时跳过）。
		if (instBase > 0) && ((api >= instLo && api < instHi) || (socks >= sbLo && socks < sbHi)) {
			continue
		}
		if !isPortFree(api) || !isPortFree(socks) {
			continue
		}
		used[api], used[socks] = true, true
		pairs = append(pairs, portPair{api: api, socks: socks})
	}
	if len(pairs) < need {
		return nil, fmt.Errorf("无法为并发节点扫描找到 %d 组空闲 API/SOCKS 端口", need)
	}
	return pairs[:need], nil
}
