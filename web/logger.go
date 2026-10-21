package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type LogEntry struct {
	Time             string `json:"time"`
	IP               string `json:"ip,omitempty"`
	KeyID            string `json:"key_id"`
	Model            string `json:"model"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	Total            int    `json:"total"`
	Status           int    `json:"status"`
	Ms               int64  `json:"ms"`
	Error            string `json:"error,omitempty"`
}

type Logger struct {
	mu  sync.Mutex
	dir string
}

func NewLogger(dir string) *Logger {
	return &Logger{dir: filepath.Join(dir, "logs")}
}

func (l *Logger) logPath(t time.Time) string {
	return filepath.Join(l.dir, "log-"+t.Format("2006-01-02")+".jsonl")
}

func (l *Logger) Write(e LogEntry) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	t, err := time.Parse(time.RFC3339, e.Time)
	if err != nil {
		t = time.Now()
	}
	if err := os.MkdirAll(l.dir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(l.logPath(t), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = f.Write(append(line, '\n'))
	return err
}

func (l *Logger) ReadLogs(date, keyID string) ([]LogEntry, error) {
	return l.ReadLogsFilter(date, keyID, "")
}

// ReadLogsFilter 读取某天日志，支持按 Key ID 与客户端 IP 过滤。
//
// 安全修复（finding 543）：date 来自管理端 query 参数，旧实现直接拼成
// filepath.Join(l.dir, "log-"+date+".jsonl")，用 "../" 可越出 logs 目录读取
// 数据目录及其父目录下的任意 .jsonl。现在：
//  1. 先用 sanitizeDate 做白名单校验（拒绝 / \ .. 与特殊字符）；
//  2. 再用 joinWithin 保证最终路径（含符号链接解析）必须落在 logs 目录内。
func (l *Logger) ReadLogsFilter(date, keyID, ip string) ([]LogEntry, error) {
	date, err := sanitizeDate(date)
	if err != nil {
		return nil, err
	}
	path, err := joinWithin(l.dir, "log-"+date+".jsonl")
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return []LogEntry{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []LogEntry
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e LogEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		if keyID != "" && e.KeyID != keyID {
			continue
		}
		if ip != "" && e.IP != ip {
			continue
		}
		out = append(out, e)
	}
	return out, sc.Err()
}
