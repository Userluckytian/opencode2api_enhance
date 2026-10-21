package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// AskEntry 是管理员 AI 问数的业务日志条目。
// 独立于 /v1 的调用日志（那个记的是使用者流量），这里记的是管理员自用的问答。
type AskEntry struct {
	Time             string `json:"time"`
	Question         string `json:"question"`
	Answer           string `json:"answer"`
	Model            string `json:"model"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	Total            int    `json:"total"`
	Ms               int64  `json:"ms"`
	OK               bool   `json:"ok"`
	Error            string `json:"error,omitempty"`
}

// AskQuotaState 是 AI 问数的每日用量统计（按 8:00 边界重置）。
// 早期版本带每日上限与「额度用尽」拦截，现已按需求移除——管理员自用的问答
// 不设额度闸门，只做计量与业务日志。
type AskQuotaState struct {
	Date         string `json:"date"`
	UsedTokens   int    `json:"used_tokens"`
	Requests     int    `json:"requests"`
	LastQuestion string `json:"last_question,omitempty"`
	LastAskedAt  string `json:"last_asked_at,omitempty"`
	ResetsAt     string `json:"resets_at"`
	ResetHour    int    `json:"reset_hour"`
	TotalAllTime int    `json:"total_all_time"`
}

// AskMeter 负责 AI 问数的计量、配额判定与业务日志。
//
// 刻意不复用 IPTracker：IP 策略治理的是「使用者流量」，而 AI 问数是管理员
// 自用行为，两者额度、维度、封禁语义都不同，混在一起会让规则难以解释。
type AskMeter struct {
	mu      sync.Mutex
	dir     string
	state   AskQuotaState
	allTime int
}

func NewAskMeter(dir string) *AskMeter {
	m := &AskMeter{dir: dir}
	m.load()
	return m
}

func (m *AskMeter) quotaPath() string { return filepath.Join(m.dir, "ask_quota.json") }
func (m *AskMeter) logDir() string    { return filepath.Join(m.dir, "logs") }

type askQuotaFile struct {
	Date         string `json:"date"`
	UsedTokens   int    `json:"used_tokens"`
	Requests     int    `json:"requests"`
	AllTimeToken int    `json:"all_time_tokens"`
}

func (m *AskMeter) load() {
	m.state = AskQuotaState{Date: dailyResetDate(time.Now())}
	data, err := os.ReadFile(m.quotaPath())
	if err != nil {
		return
	}
	var f askQuotaFile
	if json.Unmarshal(data, &f) != nil {
		return
	}
	m.allTime = f.AllTimeToken
	// 跨额度日（8:00 边界）则清零当日用量（累计总量保留）
	if f.Date == m.state.Date {
		m.state.UsedTokens = f.UsedTokens
		m.state.Requests = f.Requests
	} else {
		m.state.UsedTokens = 0
		m.state.Requests = 0
	}
	m.state.TotalAllTime = m.allTime
}

func (m *AskMeter) saveLocked() {
	f := askQuotaFile{
		Date:         m.state.Date,
		UsedTokens:   m.state.UsedTokens,
		Requests:     m.state.Requests,
		AllTimeToken: m.allTime,
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return
	}
	_ = os.MkdirAll(m.dir, 0o755)
	_ = os.WriteFile(m.quotaPath(), data, 0o644)
}

// Record 记账一次问答。
func (m *AskMeter) Record(tokens int, question string, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rollLocked(now)
	m.state.UsedTokens += tokens
	m.state.Requests++
	m.allTime += tokens
	m.state.TotalAllTime = m.allTime
	m.state.LastQuestion = question
	m.state.LastAskedAt = now.Format(time.RFC3339)
	m.saveLocked()
}

// rollLocked 跨额度日（8:00 边界）重置当日用量。
func (m *AskMeter) rollLocked(now time.Time) {
	today := dailyResetDate(now)
	if m.state.Date != today {
		m.state.Date = today
		m.state.UsedTokens = 0
		m.state.Requests = 0
		m.saveLocked()
	}
}

func (m *AskMeter) snapshotLocked(now time.Time) AskQuotaState {
	st := m.state
	st.TotalAllTime = m.allTime
	st.ResetHour = dailyResetHour
	st.ResetsAt = nextDailyReset(now).Format(time.RFC3339)
	return st
}

// Snapshot 返回当前用量快照（供后台页面展示）。
func (m *AskMeter) Snapshot(now time.Time) AskQuotaState {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rollLocked(now)
	return m.snapshotLocked(now)
}

// WriteAskLog 把问答写入业务日志（独立文件，不与 /v1 调用日志混在一起）。
func (m *AskMeter) WriteAskLog(e AskEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	dir := m.logDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	t, err := time.Parse(time.RFC3339, e.Time)
	if err != nil {
		t = time.Now()
	}
	path := filepath.Join(dir, "ask-"+t.Format("2006-01-02")+".jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
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

// ReadAskLogs 读取某天的 AI 问数日志。
//
// 安全修复（finding 543）：date 来自管理端 query 参数，旧实现直接拼接
// filepath.Join(m.logDir(), "ask-"+date+".jsonl")，可用 "../" 越出 logs 目录。
// 现在先 sanitizeDate 白名单校验，再用 joinWithin 强制路径落在 logs 目录内。
func (m *AskMeter) ReadAskLogs(date string) ([]AskEntry, error) {
	date, err := sanitizeDate(date)
	if err != nil {
		return nil, err
	}
	path, err := joinWithin(m.logDir(), "ask-"+date+".jsonl")
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return []AskEntry{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []AskEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e AskEntry
		if json.Unmarshal([]byte(line), &e) != nil {
			continue
		}
		out = append(out, e)
	}
	return out, sc.Err()
}

// maskSecret 脱敏显示密钥：只保留末 4 位。
func maskSecret(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if len(s) <= 4 {
		return "****"
	}
	return "****" + s[len(s)-4:]
}

// AuditEntry 是管理端敏感操作的审计记录。
type AuditEntry struct {
	Time   string `json:"time"`
	Action string `json:"action"`
	Detail string `json:"detail,omitempty"`
	IP     string `json:"ip,omitempty"`
	Result string `json:"result,omitempty"`
}

// AuditLog 记录管理员对上游配置等敏感设置的修改。
type AuditLog struct {
	mu  sync.Mutex
	dir string
}

func NewAuditLog(dir string) *AuditLog { return &AuditLog{dir: filepath.Join(dir, "logs")} }

func (a *AuditLog) Write(e AuditEntry) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := os.MkdirAll(a.dir, 0o755); err != nil {
		return
	}
	t, err := time.Parse(time.RFC3339, e.Time)
	if err != nil {
		t = time.Now()
	}
	path := filepath.Join(a.dir, "audit-"+t.Format("2006-01-02")+".jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	line, err := json.Marshal(e)
	if err != nil {
		return
	}
	_, _ = f.Write(append(line, '\n'))
}

// Read 读取某天的审计日志。
//
// 安全修复（finding 543）：date 来自 /api/admin/audit?date=，旧实现直接拼接文件名，
// 可用 "../" 越出 logs 目录。现在做白名单校验 + 目录归属校验；非法 date 返回空列表
// （HTTP 层在调用前已用 sanitizeDate 返回 400，这里只是纵深防御）。
func (a *AuditLog) Read(date string) []AuditEntry {
	date, err := sanitizeDate(date)
	if err != nil {
		return []AuditEntry{}
	}
	path, err := joinWithin(a.dir, "audit-"+date+".jsonl")
	if err != nil {
		return []AuditEntry{}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return []AuditEntry{}
	}
	if err != nil {
		return []AuditEntry{}
	}
	defer f.Close()
	var out []AuditEntry
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e AuditEntry
		if json.Unmarshal([]byte(line), &e) == nil {
			out = append(out, e)
		}
	}
	return out
}

// AskUsageRow 是后台展示用的一行问答记录。
type AskUsageRow struct {
	Time     string `json:"time"`
	Question string `json:"question"`
	Total    int    `json:"total"`
	Model    string `json:"model"`
	OK       bool   `json:"ok"`
	Error    string `json:"error,omitempty"`
}

// AskUsageList 返回当天问答摘要（按时间倒序，最多 n 条）。
func (m *AskMeter) AskUsageList(date string, n int) []AskUsageRow {
	entries, err := m.ReadAskLogs(date)
	if err != nil {
		return nil
	}
	rows := make([]AskUsageRow, 0, len(entries))
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		q := e.Question
		if len([]rune(q)) > 60 {
			q = string([]rune(q)[:60]) + "…"
		}
		rows = append(rows, AskUsageRow{
			Time: e.Time, Question: q, Total: e.Total, Model: e.Model, OK: e.OK, Error: e.Error,
		})
		if len(rows) >= n {
			break
		}
	}
	return rows
}

var _ = fmt.Sprintf
