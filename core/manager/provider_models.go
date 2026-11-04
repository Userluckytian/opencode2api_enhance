// 供应商服务的模型目录拉取工具。
package manager

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// fetchProviderModels 从供应商服务（50080）的 /v1/models 拉取模型 ID 列表。
func fetchProviderModels(port uint16, key string) ([]string, error) {
	url := fmt.Sprintf("http://127.0.0.1:%d/v1/models", port)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET /v1/models HTTP %d", resp.StatusCode)
	}
	var v struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(v.Data))
	for _, m := range v.Data {
		if m.ID != "" {
			out = append(out, m.ID)
		}
	}
	return out, nil
}
