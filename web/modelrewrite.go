package main

import (
	"bytes"
	"encoding/json"
	"errors"
)

// errModelFieldNotFound 表示在请求体里没找到可改写的 model 字段。
// 正常情况下 resolveRequestedModel 已保证存在，这里是防御性兜底。
var errModelFieldNotFound = errors.New("model field not found for rewrite")

// rewriteModelField 把请求体顶层 model 字段替换为 newModel，其余字节原样保留。
//
// 为什么不用 map[string]any 反序列化再序列化：
//   - 会丢字段顺序、数字精度（大整数变 float64）、未知字段的原始格式；
//   - 上游可能对 payload 做指纹/规范化校验，往返序列化会改变字节形态。
//
// 定位方式：逐 token 走一遍顶层对象，对每个值先读成 json.RawMessage——
// RawMessage 保存的是**原始字节**，因此值的结束偏移是 dec.InputOffset()，
// 起始偏移 = 结束 - len(rawVal)。只替换这一段。
//
// 调用前 resolveRequestedModel 已保证：合法 JSON、顶层对象、恰好一个
// "model" 键、值为非空字符串，因此这里可以直接信任结构。
func rewriteModelField(raw []byte, newModel string) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()

	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, errModelFieldNotFound
	}

	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, _ := kt.(string)

		var rawVal json.RawMessage
		if err := dec.Decode(&rawVal); err != nil {
			return nil, err
		}
		if key != "model" {
			continue
		}
		valEnd := dec.InputOffset()
		valStart := valEnd - int64(len(rawVal))

		encoded, err := json.Marshal(newModel)
		if err != nil {
			return nil, err
		}
		out := make([]byte, 0, len(raw)-int(valEnd-valStart)+len(encoded))
		out = append(out, raw[:valStart]...)
		out = append(out, encoded...)
		out = append(out, raw[valEnd:]...)
		return out, nil
	}
	return nil, errModelFieldNotFound
}
