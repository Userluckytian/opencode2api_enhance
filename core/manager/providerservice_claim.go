// 供应商服务（50080）的「按端口认领」能力。
//
// 2026-11-16 事故：进程被外部原因重启（或管理器自身重启）后，ProviderService
// 的 pid 字段归零，而 50080 上仍有一个本项目的供应商服务在监听。此时点「启动」
// 会撞上 isPortFree 预检，报「端口 50080 已被其他进程占用」；点「停止」也因为
// pid=0 直接返回，什么都杀不掉 —— 用户视角就是「停不掉也起不来」。
//
// 认领判据（三重，宁可放弃也不误杀）：
//  1. /health 返回 200 且正文为 OK（本项目进程通用探针）；
//  2. /api/admin/plugins 用当前 gateway key 可读到 plugins 数组（只有供应商服务
//     才注册这个聚合入口）；
//  3. 端口只有唯一监听者（netstat 反查，解析见 netstat_windows.go）。
package manager

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// providerServiceIdentityProbe 判定 port 上是否为「本项目的供应商服务」。
func providerServiceIdentityProbe(port uint16, key string) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/health", port))
	if err != nil {
		return false
	}
	body := make([]byte, 16)
	n, _ := resp.Body.Read(body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(string(body[:n]), "OK") {
		return false
	}
	// 供应商服务独有的聚合入口：能读到 plugins 才认。
	code, body, err := httpRequest(http.MethodGet, "/api/admin/plugins", port, 2*time.Second, key, nil, 2*time.Second)
	if err != nil || code != http.StatusOK || !bytes.Contains(body, []byte("\"plugins\"")) {
		return false
	}
	return true
}

// claimByPort 返回端口上唯一的、且被确认为本项目供应商服务的 PID。
func (p *ProviderService) claimByPort(port uint16, key string) (int, bool) {
	if port == 0 || !providerServiceIdentityProbe(port, key) {
		return 0, false
	}
	pids := listPortPids(port)
	if len(pids) != 1 {
		return 0, false
	}
	return pids[0], true
}
