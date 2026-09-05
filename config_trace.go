package main

// 阶段 6：配置溯源（谁改的 / 何时 / 历史）。
//
// 每次配置落盘后追加一条脱敏快照（writer + 时间戳 + sha256 + 结构），限最近 N 条；
// 提供 /api/config/history（历史列表）与 /api/config/effective（当前生效 + 与最近
// 快照的一致性 + 键级 diff），回答「运行中的配置是不是我以为的那个」。
// 快照只存脱敏后的结构/开关，绝不落密钥明文。纯标准库实现。

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	configHistoryName = "config_history.jsonl"
	configHistoryMax  = 100
)

var configTraceMu sync.Mutex

// ConfigSnapshot 一次配置写入的溯源快照（脱敏）。
type ConfigSnapshot struct {
	Writer string         `json:"writer"`
	TS     string         `json:"ts"`
	SHA    string         `json:"sha"`
	Size   int            `json:"size"`
	Config map[string]any `json:"config,omitempty"`
}

// configHistoryPath 历史文件与 config.json 同目录。
func configHistoryPath() string {
	return filepath.Join(filepath.Dir(configPath), configHistoryName)
}

// appendConfigHistory 落盘成功后追加快照；失败仅告警，不影响主流程。
func appendConfigHistory(writer string, data []byte) {
	if err := writeConfigHistory(writer, data); err != nil {
		slog.Warn("config history append failed", "error", err, "writer", writer)
	}
}

func writeConfigHistory(writer string, data []byte) error {
	sum := sha256.Sum256(data)
	snap := ConfigSnapshot{
		Writer: writer,
		TS:     time.Now().Format(time.RFC3339Nano),
		SHA:    hex.EncodeToString(sum[:]),
		Size:   len(data),
	}
	var m map[string]any
	if json.Unmarshal(data, &m) == nil {
		redactConfigMap(m) // 复用阶段5递归脱敏
		snap.Config = m
	}
	line, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	configTraceMu.Lock()
	defer configTraceMu.Unlock()
	hp := configHistoryPath()
	if err := os.MkdirAll(filepath.Dir(hp), 0o755); err != nil {
		return err
	}
	var lines [][]byte
	if existing, err := os.ReadFile(hp); err == nil && len(existing) > 0 {
		lines = splitNonEmptyLines(existing)
	}
	lines = append(lines, line)
	if len(lines) > configHistoryMax {
		lines = lines[len(lines)-configHistoryMax:]
	}
	var buf bytes.Buffer
	for _, l := range lines {
		buf.Write(l)
		buf.WriteByte('\n')
	}
	return writeFileAtomic(hp, buf.Bytes(), 0o644)
}

// readConfigHistory 返回全部快照（按时间序）；文件不存在返回空。
func readConfigHistory() ([]ConfigSnapshot, error) {
	configTraceMu.Lock()
	defer configTraceMu.Unlock()
	raw, err := os.ReadFile(configHistoryPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var snaps []ConfigSnapshot
	for _, ln := range splitNonEmptyLines(raw) {
		var s ConfigSnapshot
		if json.Unmarshal(ln, &s) == nil {
			snaps = append(snaps, s)
		}
	}
	return snaps, nil
}

// splitNonEmptyLines 按换行切分并去空行。
func splitNonEmptyLines(raw []byte) [][]byte {
	var out [][]byte
	for _, ln := range bytes.Split(raw, []byte("\n")) {
		if len(bytes.TrimSpace(ln)) > 0 {
			out = append(out, ln)
		}
	}
	return out
}

func jsonEqual(a, b any) bool {
	ab, err1 := json.Marshal(a)
	bb, err2 := json.Marshal(b)
	if err1 != nil || err2 != nil {
		return false
	}
	return bytes.Equal(ab, bb)
}

// diffConfigSnapshots 返回两个配置 map 的顶层键级差异（added/removed/changed）。
func diffConfigSnapshots(old, cur map[string]any) map[string]any {
	added, removed, changed := []string{}, []string{}, []string{}
	for k := range cur {
		if _, ok := old[k]; !ok {
			added = append(added, k)
		} else if !jsonEqual(old[k], cur[k]) {
			changed = append(changed, k)
		}
	}
	for k := range old {
		if _, ok := cur[k]; !ok {
			removed = append(removed, k)
		}
	}
	return map[string]any{"added": added, "removed": removed, "changed": changed}
}

// configHistoryHandler GET /api/config/history → 溯源快照列表。
func configHistoryHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	snaps, err := readConfigHistory()
	if err != nil {
		http.Error(w, "read history failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if snaps == nil {
		snaps = []ConfigSnapshot{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"history": snaps, "count": len(snaps)})
}

// configEffectiveHandler GET /api/config/effective → 当前生效配置（脱敏）+ sha +
// 与最近快照的一致性 + 键级 diff。
func configEffectiveHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		http.Error(w, "read config failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	var cur map[string]any
	if json.Unmarshal(raw, &cur) != nil {
		http.Error(w, "config parse error", http.StatusInternalServerError)
		return
	}
	redactConfigMap(cur)
	sum := sha256.Sum256(raw)
	curSHA := hex.EncodeToString(sum[:])
	resp := map[string]any{
		"effective": cur,
		"sha":       curSHA,
		"size":      len(raw),
	}
	if snaps, _ := readConfigHistory(); len(snaps) > 0 {
		last := snaps[len(snaps)-1]
		resp["matches_last_snapshot"] = last.SHA == curSHA
		resp["last_writer"] = last.Writer
		resp["last_ts"] = last.TS
		resp["diff_vs_last"] = diffConfigSnapshots(last.Config, cur)
	} else {
		resp["matches_last_snapshot"] = false
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
