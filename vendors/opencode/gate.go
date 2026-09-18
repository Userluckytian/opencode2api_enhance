package opencode

// 上游免费通道（Authorization: Bearer public）2026-09-18 起新增「请求体级门禁」：
//
//	① body 必须 stream:true——stream:false 时即使身份头与工具都正确也一律 403；
//	② tools 必须包含 bash / glob / grep / read 四个名字（大小写敏感）；
//	   schema 与描述不限（空 schema 亦放行），但名字缺失或大小写不符即 403。
//
// 穷举结论（缺任一 → 403；缺 edit/write → 仍 200；5 个非核心官方名 → 403）与
// 官方 1.18.14 抓包见 docs/issue-log/2026-09-18.md。
var gateToolNames = []string{"bash", "glob", "grep", "read"}

// gateToolPlaceholderDesc 是占位工具的描述：名字是硬要求，描述显式劝阻调用，
// 降低「客户端没提供工具、模型却调用注入工具」的概率（chat 路径另有 tool_choice:none 兜底）。
const gateToolPlaceholderDesc = "Placeholder required by the upstream client check. Do not call this tool."

// isGateTool 判断工具名是否属于门禁要求集合。
func isGateTool(name string) bool {
	for _, n := range gateToolNames {
		if n == name {
			return true
		}
	}
	return false
}

// gateToolDef 返回 chat/completions 形态（function 包裹）的门禁占位工具定义。
func gateToolDef(name string) map[string]any {
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        name,
			"description": gateToolPlaceholderDesc,
			"parameters":  map[string]any{"type": "object", "properties": map[string]any{}},
		},
	}
}

// ensureAgentGate 就地补齐免费通道的 body 级门禁（chat 与 responses 共用同一 bodyMap）：
//   - stream 强制 true（上游拒绝非流式）；
//   - tools 补齐 bash/glob/grep/read（客户端已有同名工具则保留其定义，不覆盖）；
//   - allowToolChoiceNone 为真且客户端原本没有任何工具时置 tool_choice:"none"，
//     禁止模型调用注入的占位工具（responses 端点不支持 none，故其调用方传 false）。
//
// 返回是否注入了占位工具（供诊断/测试）。
func ensureAgentGate(bodyMap map[string]any, allowToolChoiceNone bool) bool {
	bodyMap["stream"] = true

	tools, _ := bodyMap["tools"].([]any)
	clientHadTools := len(tools) > 0
	has := make(map[string]bool, len(tools))
	for _, t := range tools {
		tm, ok := t.(map[string]any)
		if !ok {
			continue
		}
		// chat 形态：{"type":"function","function":{"name":...}}；responses 形态：name 在顶层
		if fn, ok := tm["function"].(map[string]any); ok {
			if n, ok := fn["name"].(string); ok {
				has[n] = true
			}
			continue
		}
		if n, ok := tm["name"].(string); ok {
			has[n] = true
		}
	}

	injected := false
	for _, n := range gateToolNames {
		if !has[n] {
			tools = append(tools, gateToolDef(n))
			injected = true
		}
	}
	bodyMap["tools"] = tools

	if allowToolChoiceNone && !clientHadTools {
		bodyMap["tool_choice"] = "none"
	}
	return injected
}

// filterGateTools 从 Responses 形态工具列表中只保留门禁占位工具。
// 用于「客户端显式要求 tool_choice:none」时：既要尊重不调工具，又要留住门禁要求的名字。
func filterGateTools(tools []any) []any {
	var out []any
	for _, t := range tools {
		tm, ok := t.(map[string]any)
		if !ok {
			continue
		}
		if n, _ := tm["name"].(string); isGateTool(n) {
			out = append(out, tm)
		}
	}
	return out
}
