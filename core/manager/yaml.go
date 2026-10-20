// 极简 YAML 解析器（仅覆盖 clash 订阅 profiles 所需子集）：
// 顶层 map 的数组字段 proxies（每项为 map），支持嵌套 map、标量、行注释、引号。
// 不足以解析任意 YAML —— 只服务 clash 节点列表场景（保持零依赖）。
package manager

import (
	"errors"
	"strconv"
	"strings"
)

const (
	kindPending = -1
	kindScalar  = 0
	kindMap     = 1
	kindSlice   = 2
)

// yamlNode 解析后的节点。
type yamlNode struct {
	kind   int
	scalar string
	vs     map[string]*yamlNode
	list   []*yamlNode
}

// frame 栈帧：节点 + 行缩进。
type frame struct {
	node   *yamlNode
	indent int
}

// yamlParse 解析 clash 场景的 YAML → 顶层 map。
// 顶层 for 用下标以支持 flow-style 多行折叠（消费续行后跳过 li）。
func yamlParse(text string) (*yamlNode, error) {
	root := &yamlNode{kind: kindMap, vs: map[string]*yamlNode{}}
	stack := []frame{{node: root, indent: -1}}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for li := 0; li < len(lines); li++ {
		raw := lines[li]
		trimmed := strings.TrimRight(stripYAMLComment(raw), " \t")
		content := strings.TrimSpace(trimmed)
		if content == "" {
			continue
		}
		// flow-style 多行折叠（mihomo/clash 配置常见写法：dns 段
		// `nameserver-policy: {` + 逐行条目 + `}`、`nameserver: [` + 逐行 IP + `]`）。
		// 不折叠则续行 `114.114.114.114,`/`}` 会被当 key: 行解析而报「无法解析行」。
		if k, v, ok := splitYAML(content); ok && (strings.HasPrefix(v, "[") || strings.HasPrefix(v, "{")) && !flowBalanced(v) {
			for li+1 < len(lines) && !flowBalanced(v) {
				li++
				cont := strings.TrimSpace(strings.TrimRight(stripYAMLComment(lines[li]), " \t"))
				if cont == "" {
					continue
				}
				v = flowJoin(v, cont)
			}
			content = k + ": " + v
		} else if content[0] == '[' || content[0] == '{' {
			// 独立成行的 flow（上一行是 `key:` 空值 → pending 帧）
			if !flowBalanced(content) {
				for li+1 < len(lines) && !flowBalanced(content) {
					li++
					cont := strings.TrimSpace(strings.TrimRight(stripYAMLComment(lines[li]), " \t"))
					if cont == "" {
						continue
					}
					content = flowJoin(content, cont)
				}
			}
			if flowBalanced(content) {
				pv := stack[len(stack)-1]
				if pv.node.kind == kindPending {
					var n *yamlNode
					if content[0] == '{' {
						if fm, ok := parseFlowMap(content); ok {
							n = fm
						}
					} else {
						n = parseScalarOrList(content)
					}
					if n != nil {
						pv.node.kind, pv.node.vs, pv.node.list = n.kind, n.vs, n.list
					}
					continue
				}
			}
		}
		ind := indentOf(trimmed)
		// 回退：弹出缩进 ≥ 当前行的帧。
		// 例外：same-indent 列表项（Clash 常见的 `proxies:\n- name: ...`）必须保留
		// 父容器的 pending/slice 帧，否则列表项会被误判为“父级是 map”。
		isListItem := strings.HasPrefix(content, "- ") || content == "-"
		for len(stack) > 1 {
			top := stack[len(stack)-1]
			if top.indent < ind {
				break
			}
			if isListItem && top.indent == ind &&
				(top.node.kind == kindPending || top.node.kind == kindSlice) {
				break
			}
			stack = stack[:len(stack)-1]
		}
		// 解析 pending 容器（前一行 `key:` 空值）
		if stack[len(stack)-1].node.kind == kindPending {
			pv := stack[len(stack)-1]
			if isListItem {
				pv.node.kind = kindSlice
			} else {
				pv.node.kind = kindMap
				pv.node.vs = map[string]*yamlNode{}
			}
		}
		cur := stack[len(stack)-1].node

		if strings.HasPrefix(content, "- ") || content == "-" {
			rest := strings.TrimSpace(strings.TrimPrefix(content, "-"))
			// 纯标量列表项（proxy-groups 内 `- 节点名`、dns 数组、rules 行）：存 scalar、
			// 不压帧——压帧会让同级下一项错误嵌进本项，且原实现丢标量值。
			// 排除锚点/别名/flow（& * { [）：这些保留原 map-item 路径（其 vs 非 nil，
			// 子键行仍可安全写入，行为与旧版逐字节一致）。
			if rest != "" && !strings.HasPrefix(rest, "{") && !strings.HasPrefix(rest, "[") &&
				!strings.HasPrefix(rest, "&") && !strings.HasPrefix(rest, "*") {
				if _, _, ok := splitYAML(rest); !ok {
					scalar := &yamlNode{kind: kindScalar, scalar: trimYAMLKey(rest)}
					if cur := stack[len(stack)-1].node; cur.kind == kindSlice {
						cur.list = append(cur.list, scalar)
						continue
					}
					// 父非 slice 时落到通用分支报错（行为与旧版一致）
				}
			}
			item := &yamlNode{kind: kindMap, vs: map[string]*yamlNode{}}
			if cur.kind == kindSlice {
				cur.list = append(cur.list, item)
			} else if cur.kind == kindPending || cur.kind == kindScalar {
				// 不该出现
				return nil, errors.New("yaml: 列表项父级无效 (行 " + strconv.Itoa(li+1) + ": " + content + ")")
			} else {
				return nil, errors.New("yaml: 列表项父级是 map (行 " + strconv.Itoa(li+1) + ": " + content + ")")
			}
			stack = append(stack, frame{node: item, indent: ind})
			if rest != "" {
				if strings.HasPrefix(rest, "{") {
					// flow-style map 列表项：- {k: v, ...} → 填进 item.vs
					if fm, ok := parseFlowMap(rest); ok {
						item.vs = fm.vs
					} else {
						item.kind = kindSlice
					}
				} else if k, v, ok := splitYAML(rest); ok {
					yamlSetKey(&stack, ind, k, strings.TrimSpace(v))
				} else {
					item.kind = kindSlice
				}
			}
			continue
		}

		key, val, ok := splitYAML(content)
		if !ok {
			return nil, errors.New("yaml: 无法解析行(第 " + strconv.Itoa(li+1) + " 行): " + content)
		}
		yamlSetKey(&stack, ind, key, strings.TrimSpace(val))
	}
	return root, nil
}

// yamlSetKey 把 key:val 写入当前 map（空 val → 压入 pending/mm 子容器）。
// 注意以 *[]frame 传递（追加可能触发 realloc，必须写回调用方）。
func yamlSetKey(stack *[]frame, ind int, key, val string) {
	st := *stack
	cur := st[len(st)-1].node
	if cur.kind != kindMap && cur.kind != kindSlice {
		return
	}
	if val == "" && !strings.HasPrefix(val, "[") {
		child := &yamlNode{kind: kindPending}
		cur.vs[key] = child
		if cur.kind == kindMap {
			st = append(st, frame{node: child, indent: ind})
			*stack = st
		}
		return
	}
	cur.vs[key] = parseScalarOrList(val)
}

// parseScalarOrList 标量、内联列表或内联 map（flow-style）。
func parseScalarOrList(v string) *yamlNode {
	if strings.HasPrefix(v, "[") {
		node := &yamlNode{kind: kindSlice}
		v = strings.TrimSuffix(strings.TrimSpace(v), "]")
		v = strings.TrimPrefix(v, "[")
		for _, part := range splitFlowItems(v) {
			if s := strings.TrimSpace(part); s != "" {
				node.list = append(node.list, parseScalarOrList(s))
			}
		}
		return node
	}
	if strings.HasPrefix(v, "{") {
		if n, ok := parseFlowMap(v); ok {
			return n
		}
	}
	return parseScalar(v)
}

// parseFlowMap 解析 flow-style map：{k: v, k2: v2, ...} → kindMap 节点。
// 值支持标量/内联列表/嵌套 map；逗号切分遵循引号与括号深度。
func parseFlowMap(s string) (*yamlNode, bool) {
	inner := strings.TrimSpace(s)
	if !strings.HasPrefix(inner, "{") || !strings.HasSuffix(inner, "}") {
		return nil, false
	}
	inner = strings.TrimSpace(inner[1 : len(inner)-1])
	node := &yamlNode{kind: kindMap, vs: map[string]*yamlNode{}}
	for _, part := range splitFlowItems(inner) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, v, ok := splitFlowKV(part)
		if !ok {
			continue
		}
		node.vs[trimYAMLKey(strings.TrimSpace(k))] = parseScalarOrList(strings.TrimSpace(v))
	}
	return node, true
}

// splitFlowKV flow 项内部按「引号外首个冒号」切 key:value（JSON 风格 "k":"v" 冒号后无空格）。
// 与 splitYAML 的区别：block 语境要求冒号后跟空格，flow 语境的键值常紧贴（Clash JSON 化节点）。
func splitFlowKV(line string) (string, string, bool) {
	inSQ, inDQ := false, false
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '\'':
			if !inDQ {
				inSQ = !inSQ
			}
		case '"':
			if !inSQ {
				inDQ = !inDQ
			}
		case ':':
			if !inSQ && !inDQ {
				return strings.TrimSpace(line[:i]), strings.TrimSpace(line[i+1:]), true
			}
		}
	}
	return "", "", false
}

// trimYAMLKey 去 flow 键两侧引号（"name" → name）。
func trimYAMLKey(k string) string {
	if len(k) >= 2 && ((k[0] == '"' && k[len(k)-1] == '"') || (k[0] == '\'' && k[len(k)-1] == '\'')) {
		return k[1 : len(k)-1]
	}
	return k
}

// splitFlowItems 按“引号外 + 括号深度 0”的逗号切分（flow-style 内部）。
func splitFlowItems(s string) []string {
	var out []string
	inSQ, inDQ := false, false
	depth := 0
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\'':
			if !inDQ {
				inSQ = !inSQ
			}
		case '"':
			if !inSQ {
				inDQ = !inDQ
			}
		case '{', '[':
			if !inSQ && !inDQ {
				depth++
			}
		case '}', ']':
			if !inSQ && !inDQ && depth > 0 {
				depth--
			}
		case ',':
			if !inSQ && !inDQ && depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	out = append(out, s[start:])
	return out
}

func parseScalar(v string) *yamlNode {
	v = strings.TrimSpace(v)
	if len(v) >= 2 {
		if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
			v = v[1 : len(v)-1]
		}
	}
	return &yamlNode{kind: kindScalar, scalar: v}
}

// stripYAMLComment 去掉行内注释（忽略引号内 #）。
func stripYAMLComment(line string) string {
	inSQ, inDQ := false, false
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '\'':
			if !inDQ {
				inSQ = !inSQ
			}
		case '"':
			if !inSQ {
				inDQ = !inDQ
			}
		case '#':
			if !inSQ && !inDQ {
				return line[:i]
			}
		}
	}
	return line
}

// flowBalanced 判断 flow-style 文本的 []{} 是否闭合（引号内不计）。
func flowBalanced(s string) bool {
	inSQ, inDQ := false, false
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\'':
			if !inDQ {
				inSQ = !inSQ
			}
		case '"':
			if !inSQ {
				inDQ = !inDQ
			}
		case '[', '{':
			if !inSQ && !inDQ {
				depth++
			}
		case ']', '}':
			if !inSQ && !inDQ {
				depth--
				if depth < 0 {
					return false
				}
			}
		}
	}
	return depth == 0 && !inSQ && !inDQ
}

// flowJoin 把续行并入进行中的 flow 文本（补空格分隔，幂等）。
func flowJoin(acc, cont string) string {
	acc = strings.TrimSpace(acc)
	if strings.HasSuffix(acc, ",") {
		return acc + " " + cont
	}
	return acc + ", " + cont
}

// splitYAML 按“引号外 : 后随空格/行尾”切分。
func splitYAML(line string) (string, string, bool) {
	inSQ, inDQ := false, false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch c {
		case '\'':
			if !inDQ {
				inSQ = !inSQ
			}
		case '"':
			if !inSQ {
				inDQ = !inDQ
			}
		case ':':
			if !inSQ && !inDQ && (i+1 == len(line) || line[i+1] == ' ' || line[i+1] == '\t') {
				return strings.TrimSpace(line[:i]), strings.TrimSpace(line[i+1:]), true
			}
		}
	}
	return "", "", false
}

func indentOf(line string) int {
	n := 0
	for n < len(line) && line[n] == ' ' {
		n++
	}
	return n
}

// ---- 读取辅助 ----

func (n *yamlNode) string(key string) string {
	if n == nil || n.kind != kindMap {
		return ""
	}
	if v, ok := n.vs[key]; ok && v.kind == kindScalar {
		return v.scalar
	}
	return ""
}

func (n *yamlNode) boolPtr(key string) *bool {
	s := n.string(key)
	if s == "" {
		return nil
	}
	b, err := strconv.ParseBool(s)
	if err != nil {
		return nil
	}
	return &b
}

func (n *yamlNode) intVal(key string) int {
	if s := n.string(key); s != "" {
		v, _ := strconv.Atoi(s)
		return v
	}
	return 0
}

func (n *yamlNode) mapOf(key string) *yamlNode {
	if n == nil || n.kind != kindMap {
		return nil
	}
	if v, ok := n.vs[key]; ok && v.kind == kindMap {
		return v
	}
	return nil
}

func (n *yamlNode) sliceOf(key string) []*yamlNode {
	if n == nil || n.kind != kindMap {
		return nil
	}
	if v, ok := n.vs[key]; ok && v.kind == kindSlice {
		return v.list
	}
	return nil
}
