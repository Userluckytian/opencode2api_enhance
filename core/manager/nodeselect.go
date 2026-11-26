package manager

// 自动选源（2.0.18）：用 mihomo 的 /proxies/{name}/delay 对**所有选中节点**
// 做一次真实请求，按延迟挑可达的填槽位。
//
// 解决的是「前 8 个节点恰好全挂，槽位被占死」：现在槽位不再是列表前 8 个，
// 而是 100 个候选里实测能通的 8 个；全挂才报错。

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// nodeSelectProbeURL 与注册/握手同一条目标：能从这里出去才算可用。
const nodeSelectProbeURL = "https://www.cloudflare.com/cdn-cgi/trace"

// mihomoProxyDelay 让 mihomo 通过指定节点访问一次探测目标，返回毫秒延迟。
func mihomoProxyDelay(ctlPort int, secret, name string, timeout time.Duration) (time.Duration, error) {
	q := url.Values{}
	q.Set("url", nodeSelectProbeURL)
	q.Set("timeout", strconv.Itoa(int(timeout/time.Millisecond)))
	endpoint := fmt.Sprintf("http://127.0.0.1:%d/proxies/%s/delay?%s",
		ctlPort, url.PathEscape(name), q.Encode())
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, err
	}
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	client := &http.Client{Timeout: timeout + 3*time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		Delay   int    `json:"delay"`
		Message string `json:"message"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out)
	if resp.StatusCode/100 != 2 || out.Delay <= 0 {
		if out.Message == "" {
			out.Message = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return 0, fmt.Errorf("%s", out.Message)
	}
	return time.Duration(out.Delay) * time.Millisecond, nil
}

// probeCandidates 并发测候选节点（mihomo 内部执行真实请求，16 并发）。
func probeCandidates(ctlPort int, secret string, nodes []PoolNode, timeout time.Duration) map[string]time.Duration {
	out := make(map[string]time.Duration, len(nodes))
	var mu sync.Mutex
	sem := make(chan struct{}, 16)
	var wg sync.WaitGroup
	for _, n := range nodes {
		wg.Add(1)
		sem <- struct{}{}
		go func(n PoolNode) {
			defer wg.Done()
			defer func() { <-sem }()
			if d, err := mihomoProxyDelay(ctlPort, secret, n.Name, timeout); err == nil {
				mu.Lock()
				out[n.Fingerprint] = d
				mu.Unlock()
			}
		}(n)
	}
	wg.Wait()
	return out
}

// chooseSlotNodes 从可达节点里选 slots 个（延迟升序），0 号槽位优先放 UDP 节点
// （warp.socks_udp 固定指向它）。可达数少于槽位数时循环复用，保证监听数量恒为 slots。
func chooseSlotNodes(candidates []PoolNode, delays map[string]time.Duration, slots int) []PoolNode {
	if slots <= 0 {
		return nil
	}
	reachable := make([]PoolNode, 0, len(candidates))
	for _, n := range candidates {
		if _, ok := delays[n.Fingerprint]; ok {
			reachable = append(reachable, n)
		}
	}
	if len(reachable) == 0 {
		return nil
	}
	sort.SliceStable(reachable, func(i, j int) bool {
		return delays[reachable[i].Fingerprint] < delays[reachable[j].Fingerprint]
	})
	for i, n := range reachable {
		if n.UDP {
			if i != 0 {
				reachable[0], reachable[i] = reachable[i], reachable[0]
			}
			break
		}
	}
	out := make([]PoolNode, slots)
	for i := range out {
		out[i] = reachable[i%len(reachable)]
	}
	return out
}

// preferredSlotNodes 只用「上次自动选源的结果」铺槽位（节点还在才用），
// 不再把其余候选补进来——那些很可能是不可达节点，补进来就前功尽弃。
// 偏好全部失效时才退回 slotNodes。
func preferredSlotNodes(candidates []PoolNode, preferred []string, slots int) []PoolNode {
	if len(candidates) == 0 || slots <= 0 {
		return nil
	}
	byFP := make(map[string]PoolNode, len(candidates))
	for _, n := range candidates {
		byFP[n.Fingerprint] = n
	}
	ordered := make([]PoolNode, 0, len(preferred))
	used := map[string]bool{}
	for _, fp := range preferred {
		fp = strings.TrimSpace(fp)
		if n, ok := byFP[fp]; ok && !used[fp] {
			ordered = append(ordered, n)
			used[fp] = true
		}
	}
	if len(ordered) == 0 {
		return slotNodes(candidates, slots)
	}
	res := make([]PoolNode, slots)
	for i := range res {
		res[i] = ordered[i%len(ordered)]
	}
	return res
}
