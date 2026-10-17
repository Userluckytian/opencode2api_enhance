// 批量代理链接导入（手动粘贴 vless/vmess/trojan/ss/hy2/http/socks5 等链接 → 节点池）。
//
// 与订阅 URL 的区别：没有远端可拉，用户直接把链接（或整段 Clash YAML / base64 订阅文本）
// 粘贴进来。解析复用 parseSubscription（自动识别四种格式），落库复用订阅缓存 + 分组机制，
// 因此导入后的节点与订阅节点完全同源：节点池可见、可扫描、可入池，删除分组即清节点。
package manager

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// manualGroupPrefix 手动导入的默认分组名前缀（后接序号，避开已有分组）。
const manualGroupPrefix = "手动导入"

// manualSourceURLPrefix 手动导入在订阅列表中的占位 URL 前缀。
// 不是真 URL，仅用于让「订阅列表」能展示/删除这条手动分组（删除即清该组节点）。
const manualSourceURLPrefix = "manual://"

// ImportLinks 批量导入粘贴的代理链接。
// text 支持：多行明文链接 / base64 订阅文本 / 整段 Clash YAML / sing-box JSON。
// group 为空时自动取「手动导入N」（N 从 1 起，避开缓存中已有分组名）。
// 返回：导入节点数、实际分组名、被跳过的坏行数、逐行错误说明。
func (m *Manager) ImportLinks(text, group string) (imported int, groupName string, skipped int, errs []string, err error) {
	text = strings.TrimSpace(strings.TrimPrefix(text, "\uFEFF"))
	if text == "" {
		return 0, "", 0, nil, fmt.Errorf("粘贴内容不能为空")
	}
	groupName = strings.TrimSpace(group)
	if groupName == "" {
		groupName = m.nextManualGroupName()
	}

	var nodes []SubscribeNode
	if looksLikePlainLinks(text) {
		// 明文链接：逐行解析，坏行能精确到行号（用户粘贴几十条时最需要的反馈）。
		nodes, errs = parseLinesLoose(text)
		skipped = len(errs)
		if len(nodes) == 0 {
			if len(errs) == 0 {
				return 0, "", 0, nil, fmt.Errorf("粘贴内容中未解析到任何可用节点")
			}
			return 0, "", skipped, errs, fmt.Errorf("未解析到任何可用节点（%s）", strings.Join(errs, "；"))
		}
	} else {
		// Clash YAML / sing-box JSON / base64 订阅文本：整体解析。
		parsed, parseErr := parseSubscription(text)
		if parseErr != nil {
			return 0, "", 0, nil, parseErr
		}
		nodes = parsed
	}

	sourceURL := manualSourceURLPrefix + groupName
	if owner := m.groupOwner(groupName, "", sourceURL); owner != "" {
		return 0, "", skipped, errs, fmt.Errorf("分组 %q 已被%s使用，请换一个分组名", groupName, owner)
	}
	for i := range nodes {
		nodes[i].Group = groupName
	}
	_, existed := m.subscriptionSourceByURL(sourceURL)
	if !existed {
		if err := m.AddSubscription(sourceURL, 0, TargetPoolOnly, groupName); err != nil {
			return 0, "", skipped, errs, err
		}
	}
	if err := m.saveSubscriptionCacheGrouped(nodes); err != nil {
		if !existed {
			_, _ = m.RemoveSubscription(sourceURL)
		}
		return 0, "", skipped, errs, err
	}
	return len(nodes), groupName, skipped, errs, nil
}

// looksLikePlainLinks 判断粘贴内容是「明文链接列表」还是「订阅文档（YAML/JSON/base64）」。
// 明文链接走逐行解析以获得行号级错误；文档格式整体解析，避免逐行误判。
func looksLikePlainLinks(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.Contains(line, "://") {
			return true
		}
		// 首个非空非注释行不是链接 → 按文档格式处理（YAML 的 "proxies:" / JSON 的 "{"）
		return false
	}
	return false
}

// parseLinesLoose 逐行解析明文链接：跳过空行/注释行，坏行收集为「第 N 行：原因」。
func parseLinesLoose(text string) ([]SubscribeNode, []string) {
	var nodes []SubscribeNode
	var errs []string
	seen := map[string]bool{}
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		node, ok, err := parseURILink(line)
		if err != nil {
			errs = append(errs, fmt.Sprintf("第 %d 行：%v", i+1, err))
			continue
		}
		if !ok {
			proto := line
			if idx := strings.Index(proto, "://"); idx > 0 {
				proto = proto[:idx+3]
			}
			errs = append(errs, fmt.Sprintf("第 %d 行：不支持的协议 %s", i+1, proto))
			continue
		}
		if isPseudoNode(node.Name) || seen[node.Name] {
			continue
		}
		seen[node.Name] = true
		nodes = append(nodes, node)
	}
	return nodes, errs
}

// nextManualGroupName 生成不与现有缓存分组重名的手动分组名（手动导入1、手动导入2…）。
func (m *Manager) nextManualGroupName() string {
	groups := map[string]bool{}
	for _, n := range m.loadSubscriptionCache() {
		if n.Group != "" {
			groups[n.Group] = true
		}
	}
	for i := 1; ; i++ {
		name := manualGroupPrefix + itoa16(i)
		if !groups[name] {
			return name
		}
	}
}

// SubscriptionsImportLinksHandler POST {text, group} → 批量导入粘贴的代理链接。
func (m *Manager) SubscriptionsImportLinksHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodOK(w, r, http.MethodPost) {
			return
		}
		var req struct {
			Text  string `json:"text"`
			Group string `json:"group"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			writeErr(w, http.StatusBadRequest, "请求体解析失败")
			return
		}
		imported, group, skipped, errs, err := m.ImportLinks(req.Text, req.Group)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		resp := map[string]any{
			"status":   "ok",
			"imported": imported,
			"group":    group,
			"skipped":  skipped,
		}
		if len(errs) > 0 {
			resp["errors"] = errs
		}
		writeJSON(w, resp)
	}
}
