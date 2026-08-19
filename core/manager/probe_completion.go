// 免费模型探测与判定（Rust is_free_model / probe_free_completion_response 语义）。
package manager

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"time"
)

// isFreeModelID 免费模型判定。
func isFreeModelID(id string) bool {
	low := strings.ToLower(strings.TrimSpace(id))
	if strings.Contains(low, "-free") || low == "big-pickle" {
		return true
	}
	switch low {
	case "deepseek-v4-flash", "mimo-v2.5", "ling-3.0-flash",
		"nemotron-3-ultra", "north-mini-code", "laguna-s-2.1",
		"hy3", "nemotron-3.5-lightning":
		return true
	}
	return false
}

// pickFreeModel 从 /v1/models data[] 挑选免费模型（-free/big-pickle 立即命中）。
// 兜底：本系统实例/探测进程的 /v1/models 已按 isFreeModel 只返回免费模型
// （空目录返回 503，见 listModelsHandler），因此 data 非空时任意非 auto 模型
// 都值得实测——避免上游新增免费模型（如 hy3、nemotron-3.5-lightning）后，
// 硬编码名单过期导致误报「无免费模型可测试」。
func pickFreeModel(data []map[string]any) string {
	var firstFree string
	for _, mp := range data {
		id, _ := mp["id"].(string)
		if id == "" {
			continue
		}
		low := strings.ToLower(id)
		if strings.Contains(low, "-free") || low == "big-pickle" {
			return id
		}
		if firstFree == "" && isFreeModelID(id) {
			firstFree = id
		}
	}
	if firstFree == "" {
		// 兜底：跳过 auto 虚拟模型，取第一个可测模型
		for _, mp := range data {
			id, _ := mp["id"].(string)
			if id != "" && !strings.EqualFold(strings.TrimSpace(id), "auto") {
				return id
			}
		}
	}
	return firstFree
}

// freeCompletion 免费模型测试：GET /v1/models → 挑免费模型 → POST chat。
// 返回 (status, body, modelCount, freeTested, err)：
//   - status: 最终 HTTP 状态码（models 2xx + 有免费模型时为 chat 结果；无免费模型时为 200）
//   - modelCount: /v1/models 条目数（未知 -1）
//   - freeTested: 是否实际执行了免费模型 chat 测试
//
// 设计原则：/v1/models 返回 2xx 即视为节点可用（能连通上游），
// 免费模型 chat 测试只是额外验证，不影响节点可用性判定。
func freeCompletion(port uint16, password string, budget time.Duration) (int, []byte, int, bool, error) {
	deadline := time.Now().Add(budget)
	modelStatus, body, err := httpGetJSON(port, "/v1/models", time.Until(deadline), password)
	if err != nil || modelStatus < 200 || modelStatus >= 300 {
		if modelStatus != 0 {
			return modelStatus, body, -1, false, nil
		}
		return 0, body, -1, false, err
	}
	modelCount := -1
	var payload struct {
		Data []map[string]any `json:"data"`
	}
	if json.Unmarshal(body, &payload) == nil && payload.Data != nil {
		modelCount = len(payload.Data)
	} else {
		var raw []map[string]any
		if json.Unmarshal(body, &raw) == nil {
			modelCount = len(raw)
		}
	}
	modelID := pickFreeModel(payload.Data)
	if modelID == "" {
		// 无免费模型：/v1/models 已返回 2xx，节点可用，只是无法做 chat 验证
		return 200, body, modelCount, false, nil
	}
	chatBody, _ := json.Marshal(map[string]any{
		"model":      modelID,
		"messages":   []any{map[string]any{"role": "user", "content": "Reply with OK"}},
		"max_tokens": 1,
		"stream":     false,
	})
	status, chatResp, err := httpPostJSON(port, "/v1/chat/completions", time.Until(deadline), password, chatBody)
	return status, chatResp, modelCount, true, err
}

// probeCompletionSuccess 判定 chat 通过：2xx 且 choices 非空。
func probeCompletionSuccess(status int, body []byte) bool {
	if status < 200 || status >= 300 {
		return false
	}
	var obj map[string]any
	if json.Unmarshal(body, &obj) != nil {
		return false
	}
	chs, ok := obj["choices"].([]any)
	return ok && len(chs) > 0
}

// modelsCount 从 /v1/models 响应统计数量。
func modelsCount(body []byte) (int, bool) {
	var obj struct {
		Data []any `json:"data"`
	}
	if json.Unmarshal(body, &obj) == nil && obj.Data != nil {
		return len(obj.Data), true
	}
	var arr []any
	if json.Unmarshal(body, &arr) == nil {
		return len(arr), true
	}
	return 0, false
}

// readFileTail 读文件尾部 max 字节。
func readFileTail(path string, max int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if fi.Size() <= 0 {
		return nil, os.ErrNotExist
	}
	if fi.Size() > int64(max) {
		if _, err := f.Seek(-int64(max), io.SeekEnd); err != nil {
			return nil, err
		}
	}
	return io.ReadAll(f)
}
