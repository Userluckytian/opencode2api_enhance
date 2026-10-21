package main

import (
	"net/http"
	"sort"
	"time"
)

type DayStat struct {
	Date     string `json:"date"`
	Requests int    `json:"requests"`
	Tokens   int    `json:"tokens"`
	Errors   int    `json:"errors"`
}

type IPAgg struct {
	IP               string  `json:"ip"`
	Requests         int     `json:"requests"`
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	TotalTokens      int     `json:"total_tokens"`
	Keys             int     `json:"keys"`
	Errors           int     `json:"errors"`
	ErrorRate        float64 `json:"error_rate"`
	Blocked          int     `json:"blocked"`
	LastSeen         string  `json:"last_seen"`
}

// aggregateByIP 把明细日志按 IP 汇总；无 IP 的旧日志归入「未知」。
func aggregateByIP(logs []LogEntry) []IPAgg {
	type acc struct {
		agg  IPAgg
		keys map[string]struct{}
		last string
	}
	m := map[string]*acc{}
	for _, e := range logs {
		ip := e.IP
		if ip == "" {
			ip = "未知"
		}
		a := m[ip]
		if a == nil {
			a = &acc{agg: IPAgg{IP: ip}, keys: map[string]struct{}{}}
			m[ip] = a
		}
		a.agg.Requests++
		a.agg.PromptTokens += e.PromptTokens
		a.agg.CompletionTokens += e.CompletionTokens
		a.agg.TotalTokens += e.Total
		if e.Error != "" {
			a.agg.Blocked++
		}
		if e.Status >= 400 {
			a.agg.Errors++
		}
		if e.KeyID != "" {
			a.keys[e.KeyID] = struct{}{}
		}
		if e.Time > a.last {
			a.last = e.Time
		}
	}
	out := make([]IPAgg, 0, len(m))
	for _, a := range m {
		a.agg.Keys = len(a.keys)
		if a.agg.Requests > 0 {
			a.agg.ErrorRate = float64(a.agg.Errors) / float64(a.agg.Requests)
		}
		a.agg.LastSeen = a.last
		out = append(out, a.agg)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TotalTokens != out[j].TotalTokens {
			return out[i].TotalTokens > out[j].TotalTokens
		}
		return out[i].Requests > out[j].Requests
	})
	return out
}

func summarizeLogs(logs []LogEntry) (requests, tokens, errors, blocked int, ips map[string]struct{}) {
	ips = map[string]struct{}{}
	for _, e := range logs {
		requests++
		tokens += e.Total
		if e.Status >= 400 {
			errors++
		}
		if e.Error != "" {
			blocked++
		}
		if e.IP != "" {
			ips[e.IP] = struct{}{}
		}
	}
	return
}

// dailyTrend 返回最近 days 天（含今天）的请求/token/错误趋势。
func (a *App) dailyTrend(days int, now time.Time) []DayStat {
	out := make([]DayStat, 0, days)
	for i := days - 1; i >= 0; i-- {
		date := now.AddDate(0, 0, -i).Format("2006-01-02")
		logs, err := a.Logger.ReadLogs(date, "")
		if err != nil {
			logs = nil
		}
		req, tok, errs, _, _ := summarizeLogs(logs)
		out = append(out, DayStat{Date: date, Requests: req, Tokens: tok, Errors: errs})
	}
	return out
}

func (a *App) handleLogAggregate(w http.ResponseWriter, r *http.Request) {
	date := r.URL.Query().Get("date")
	if date == "" {
		date = time.Now().Format("2006-01-02")
	}
	// 路径穿越修复（finding 543）：该入口同样把 date 拼进日志文件名，
	// 统一走 sanitizeDate 白名单校验（拒绝 / \ .. 与特殊字符）。
	if _, err := sanitizeDate(date); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid date")
		return
	}
	logs, err := a.Logger.ReadLogs(date, "")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "read logs failed")
		return
	}
	req, tok, errs, blocked, ips := summarizeLogs(logs)
	writeJSON(w, http.StatusOK, map[string]any{
		"date": date,
		"ips":  aggregateByIP(logs),
		"totals": map[string]any{
			"requests":  req,
			"tokens":    tok,
			"errors":    errs,
			"blocked":   blocked,
			"unique_ip": len(ips),
		},
	})
}
