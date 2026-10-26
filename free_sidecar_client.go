// 免费层 Bun sidecar 客户端（主包薄封装）。
//
// 实现与测试见 core/freesidecar：把 TierFree 的上游请求改写为本地 sidecar
// /proxy 请求，节点选择/健康/重试/统计仍由本包负责。
package main

import (
	"net/http"

	"github.com/6Kmfi6HP/opencode2api/core/freesidecar"
)

// newSidecarClient 返回走 sidecar 的 HTTP 客户端。
func newSidecarClient(sidecarURL, proxyAddr string, streaming bool) *http.Client {
	return freesidecar.NewClient(sidecarURL, proxyAddr, streaming)
}

// sidecarProxyURL 把池出口地址转换为 sidecar 可用的 HTTP 代理 URL。
func sidecarProxyURL(addr string) string {
	return freesidecar.ProxyURL(addr)
}
