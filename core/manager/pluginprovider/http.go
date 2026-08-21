// 管理 API（设计文档 §七）：GET /api/admin/plugins、POST .../rescan、
// POST .../{id}/config、POST .../{id}/toggle、DELETE .../{id}。
// 统一由 main 经 requireAuth 鉴权挂载（与 /api/admin/custom-providers 同款）。
package pluginprovider

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// errNotFound 插件不存在（handler 映射 404）。
var errNotFound = errors.New("插件不存在")

// writeJSON / writeErr 与 core/manager 同语义（跨包各自持有，沿用项目惯例）。
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": msg})
}

func requireMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method != method {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	return true
}

// ListHandler GET /api/admin/plugins：扫描结果列表。
func (m *Manager) ListHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethod(w, r, http.MethodGet) {
			return
		}
		writeJSON(w, map[string]any{"plugins": m.Views()})
	}
}

// RescanHandler POST /api/admin/plugins/rescan：手动重扫 providers/。
func (m *Manager) RescanHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethod(w, r, http.MethodPost) {
			return
		}
		writeJSON(w, map[string]any{"plugins": m.Rescan()})
	}
}

// ConfigSaveHandler POST /api/admin/plugins/{id}/config：保存编辑后的 provider.json
// 全文（{"provider_json":"..."}），校验后原子写盘并触发必要的重载。
func (m *Manager) ConfigSaveHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethod(w, r, http.MethodPost) {
			return
		}
		id := r.PathValue("id")
		body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		if err != nil {
			writeErr(w, http.StatusBadRequest, "读取请求体失败")
			return
		}
		var req struct {
			ProviderJSON string `json:"provider_json"`
		}
		if err := json.Unmarshal(body, &req); err != nil || req.ProviderJSON == "" {
			writeErr(w, http.StatusBadRequest, "请求体需为 {\"provider_json\":\"<provider.json 全文>\"}")
			return
		}
		if err := m.SaveConfig(id, []byte(req.ProviderJSON)); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, map[string]any{"status": "ok", "plugin": m.View(id)})
	}
}

// ToggleHandler POST /api/admin/plugins/{id}/toggle：启停。body 可带 {"enabled":bool}，
// 缺省 = 翻转当前状态。
func (m *Manager) ToggleHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethod(w, r, http.MethodPost) {
			return
		}
		id := r.PathValue("id")
		var req struct {
			Enabled *bool `json:"enabled"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req) // 空 body 合法（= 翻转）
		enabled := !m.enabledOf(id)
		if req.Enabled != nil {
			enabled = *req.Enabled
		}
		view, err := m.Toggle(id, enabled)
		if err != nil {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, map[string]any{"status": "ok", "plugin": view})
	}
}

// ExposedModelsHandler POST /api/admin/plugins/{id}/exposed-models：保存模型暴露白名单
// （设计文档 §六「获取模型并自定义暴露」）。body: {"expose_all":bool,"exposed_models":[...]}；
// expose_all=true 时忽略 exposed_models（全量透传并清除旧白名单）。
func (m *Manager) ExposedModelsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethod(w, r, http.MethodPost) {
			return
		}
		id := r.PathValue("id")
		var req struct {
			ExposeAll     bool     `json:"expose_all"`
			ExposedModels []string `json:"exposed_models"`
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil || json.Unmarshal(body, &req) != nil {
			writeErr(w, http.StatusBadRequest, "请求体需为 {\"expose_all\":bool,\"exposed_models\":[...]}")
			return
		}
		if err := m.SetExposedModels(id, req.ExposeAll, req.ExposedModels); err != nil {
			if errors.Is(err, errNotFound) {
				writeErr(w, http.StatusNotFound, err.Error())
			} else {
				writeErr(w, http.StatusBadRequest, err.Error())
			}
			return
		}
		writeJSON(w, map[string]any{"status": "ok", "plugin": m.View(id)})
	}
}

// DeleteHandler DELETE /api/admin/plugins/{id}：停进程 + 整目录删除。
func (m *Manager) DeleteHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethod(w, r, http.MethodDelete) {
			return
		}
		id := r.PathValue("id")
		if err := m.Delete(id); err != nil {
			if errors.Is(err, errNotFound) {
				writeErr(w, http.StatusNotFound, err.Error())
			} else {
				writeErr(w, http.StatusInternalServerError, "删除失败: "+err.Error())
			}
			return
		}
		writeJSON(w, map[string]any{"status": "ok", "deleted": id})
	}
}

func (m *Manager) enabledOf(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.plugins[id]; ok {
		return p.enabled
	}
	return false
}

// writeFileAtomic 临时文件+Rename 原子落盘（provider.json 全量写回；崩溃不留半截 JSON）。
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // 失败路径清理；成功 Rename 后目标已不存在
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// killPID 小工具（pid<=0 跳过）。
func killPID(pid int) {
	if pid <= 0 {
		return
	}
	if err := killProcess(pid); err != nil {
		slog.Debug("plugin child kill failed", "pid", pid, "error", err)
	}
}

// waitPIDGone 有界等待进程退出（Windows 上删除目录前必须等 exe 句柄释放）。
func waitPIDGone(pid int, timeout time.Duration) {
	if pid <= 0 {
		return
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !pidAlive(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}
