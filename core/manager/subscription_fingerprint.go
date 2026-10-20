// 阶段 X（订阅协议模块化）：节点指纹身份。
// 借鉴 uniproxy（internal/importer/parser.go:386-416）：指纹 = SHA-256(协议|服务器|端口|凭据|传输参数)，
// **排除显示名**——机场改节点备注不再打断实例匹配、订阅刷新 diff 与去重。
package manager

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// fingerprintOf 计算 SubscribeNode 的身份指纹（16 hex 短指纹）。
// 覆盖：canonical 协议、server、port、全部凭据（password/uuid/cipher/username）、
// 传输形态（network/wsPath/grpcService/flow/reality/obfs 系/ssr 系）、TLS 开关。
// 排除：Name、Group、Raw（显示属性/来源差异不参与身份）、UDP（能力位，非身份）。
func fingerprintOf(n SubscribeNode) string {
	canon := canonicalProtocol(n.NodeType)
	if canon == "" {
		canon = strings.ToLower(strings.TrimSpace(n.NodeType))
	}
	// headers 排序进指纹（map 遍历序不稳定）。
	var hdrKeys []string
	for k := range n.WsHeaders {
		hdrKeys = append(hdrKeys, k)
	}
	// 简单稳定化：排序后拼接
	for i := 0; i < len(hdrKeys); i++ {
		for j := i + 1; j < len(hdrKeys); j++ {
			if hdrKeys[j] < hdrKeys[i] {
				hdrKeys[i], hdrKeys[j] = hdrKeys[j], hdrKeys[i]
			}
		}
	}
	var sb strings.Builder
	sb.WriteString(canon + "\x00")
	fmt.Fprintf(&sb, "%s:%d\x00", strings.ToLower(n.Server), n.Port)
	sb.WriteString(n.Password + "\x00" + n.UUID + "\x00" + n.Cipher + "\x00" + n.Username + "\x00")
	sb.WriteString(n.SNI + "\x00") // 审查 e 项：同 server 不同 SNI（CDN 分流）是不同节点
	sb.WriteString(strings.ToLower(n.Network) + "\x00" + n.WsPath + "\x00" + n.GrpcService + "\x00")
	for _, k := range hdrKeys {
		sb.WriteString(k + "=" + n.WsHeaders[k] + "\x00")
	}
	sb.WriteString(n.Flow + "\x00" + n.RealityPbk + "\x00" + n.RealitySid + "\x00" + n.ClientFingerprint + "\x00")
	sb.WriteString(n.Obfs + "\x00" + n.ObfsPassword + "\x00" + n.SSRProtocol + "\x00" + n.SSRProtoParam + "\x00" + n.SSRObfsParam + "\x00")
	fmt.Fprintf(&sb, "%v", n.TLS)
	sum := sha256.Sum256([]byte(sb.String()))
	return hex.EncodeToString(sum[:8])
}

// fillFingerprints 给解析产物的节点批量补指纹（解析入口统一出口调用）。
// 幂等：已有指纹的节点跳过（往返导入不重算）。
func fillFingerprints(nodes []SubscribeNode) {
	for i := range nodes {
		if nodes[i].Fingerprint == "" {
			nodes[i].Fingerprint = fingerprintOf(nodes[i])
		}
	}
}

// diffByFingerprint 新旧节点按指纹 diff（uniproxy imports.go reconcile 思路）。
// 返回：added（新有旧无）、removed（旧有新无）、renamed（指纹同但显示名变——不算删加）。
// 迁移兼容（审查 c 项修复）：旧缓存节点无 Fingerprint 字段 → 就地按当前算法补算
// （fillFingerprints 语义），与新生成节点走同一轨道；否则「旧 legacy 键 vs 新 fp 键」
// 永不相交，升级后首轮刷新会被 shrink 闸 100% 误拒。
func diffByFingerprint(oldNodes, newNodes []SubscribeNode) (added, removed []SubscribeNode, renamed int) {
	fillFingerprints(oldNodes)
	fillFingerprints(newNodes)
	keyOf := func(n SubscribeNode) string {
		if n.Fingerprint != "" {
			return "fp:" + n.Fingerprint
		}
		return "legacy:" + n.Name + "|" + n.Server + "|" + fmt.Sprint(n.Port)
	}
	oldIdx := map[string]SubscribeNode{}
	for _, n := range oldNodes {
		oldIdx[keyOf(n)] = n
	}
	newSet := map[string]bool{}
	for _, n := range newNodes {
		k := keyOf(n)
		newSet[k] = true
		o, ok := oldIdx[k]
		if !ok {
			added = append(added, n)
		} else if o.Name != n.Name {
			renamed++
		}
	}
	for _, o := range oldNodes {
		if !newSet[keyOf(o)] {
			removed = append(removed, o)
		}
	}
	return
}

// identityKeyFor 供实例匹配路径使用（importSubscription 的 nodeID 迁移到指纹口径）。
func identityKeyFor(n SubscribeNode) string {
	if n.Fingerprint != "" {
		return "fp:" + n.Fingerprint
	}
	return n.Name + "|" + fmt.Sprintf("%s:%d", n.Server, n.Port) // 旧口径回退
}
