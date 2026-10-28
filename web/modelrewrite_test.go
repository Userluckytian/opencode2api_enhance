package main

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestRewriteModelFieldBasic(t *testing.T) {
	in := `{"model":"backup/gpt-4o","messages":[{"role":"user","content":"hi"}],"temperature":0.7}`
	out, err := rewriteModelField([]byte(in), "gpt-4o")
	if err != nil {
		t.Fatal(err)
	}
	want := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"temperature":0.7}`
	if string(out) != want {
		t.Fatalf("got  %s\nwant %s", out, want)
	}
}

// 空白、字段顺序、未知字段都必须逐字节保留（只替换 model 值本身）。
func TestRewriteModelFieldPreservesBytes(t *testing.T) {
	in := "{\n  \"stream\": true,\n  \"x_unknown\": {\"a\": 1},\n  \"model\" :  \"backup/gpt-4o\" ,\n  \"n\": 12345678901234567890\n}"
	out, err := rewriteModelField([]byte(in), "gpt-4o")
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"stream\": true,\n  \"x_unknown\": {\"a\": 1},\n  \"model\" :  \"gpt-4o\" ,\n  \"n\": 12345678901234567890\n}"
	if string(out) != want {
		t.Fatalf("got  %q\nwant %q", out, want)
	}
}

// 大整数精度不能因改写而丢失。
func TestRewriteModelFieldKeepsNumberPrecision(t *testing.T) {
	in := `{"model":"backup/m","n":9007199254740993}`
	out, err := rewriteModelField([]byte(in), "m")
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(out) {
		t.Fatalf("output is not valid json: %s", out)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if string(got["n"]) != "9007199254740993" {
		t.Fatalf("number precision lost: %s", got["n"])
	}
	if string(got["model"]) != `"m"` {
		t.Fatalf("model = %s, want \"m\"", got["model"])
	}
}

// model 必须是第一个键时也能正确改写。
func TestRewriteModelFieldFirstKey(t *testing.T) {
	in := `{"model":"backup/m","a":1}`
	out, err := rewriteModelField([]byte(in), "m")
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"model":"m","a":1}` {
		t.Fatalf("got %s", out)
	}
}

// model 是最后一个键时也能正确改写（末尾无多余字节）。
func TestRewriteModelFieldLastKey(t *testing.T) {
	in := `{"a":1,"model":"backup/m"}`
	out, err := rewriteModelField([]byte(in), "m")
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"a":1,"model":"m"}` {
		t.Fatalf("got %s", out)
	}
}

// 嵌套对象里的 model 键不能被误改（只改顶层）。
func TestRewriteModelFieldIgnoresNested(t *testing.T) {
	in := `{"outer":{"model":"keep-me"},"model":"backup/m"}`
	out, err := rewriteModelField([]byte(in), "m")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if got["model"] != "m" {
		t.Fatalf("top-level model = %v, want m", got["model"])
	}
	outer, _ := got["outer"].(map[string]any)
	if outer["model"] != "keep-me" {
		t.Fatalf("nested model was modified: %v", outer["model"])
	}
}

// 目标模型名含特殊字符（引号、反斜杠、中文）时必须是合法 JSON。
func TestRewriteModelFieldEscapes(t *testing.T) {
	cases := []string{`a"b`, `a\b`, "中文模型", "a\nb"}
	for _, want := range cases {
		in := `{"model":"x/y"}`
		out, err := rewriteModelField([]byte(in), want)
		if err != nil {
			t.Fatalf("%q: %v", want, err)
		}
		var got map[string]string
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("%q: invalid json %s: %v", want, out, err)
		}
		if got["model"] != want {
			t.Fatalf("roundtrip %q -> %q", want, got["model"])
		}
	}
}

// 找不到 model 字段时报错（防御性，正常路径由 resolveRequestedModel 拦截）。
func TestRewriteModelFieldMissing(t *testing.T) {
	if _, err := rewriteModelField([]byte(`{"a":1}`), "m"); err == nil {
		t.Fatal("want error for missing model field")
	}
}

// 改写后其它字段语义完全不变（结构等价性兜底）。
func TestRewriteModelFieldStructuralEquality(t *testing.T) {
	in := `{"model":"backup/m","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function"}],"stream":true}`
	out, err := rewriteModelField([]byte(in), "m")
	if err != nil {
		t.Fatal(err)
	}
	var before, after map[string]any
	if err := json.Unmarshal([]byte(in), &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out, &after); err != nil {
		t.Fatal(err)
	}
	before["model"] = "m"
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("structure changed:\nbefore %+v\nafter  %+v", before, after)
	}
}
