package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// AI 问答的只读数据工具（2026-10-28）。
//
// 设计意图：不再由服务端预先汇总好少数几个数字塞给模型（那样模型只能回答
// 「错误集中在哪个 IP」，无法下钻到「哪个状态码 / 哪个模型 / 具体错误消息」）。
// 改为把系统的数据源以**只读工具**的形式暴露给模型，让它自己决定查什么、
// 查多细，并基于原始记录做分析。
//
// 安全边界：
//   - 全部工具只读，不接受任意路径、不写盘、不执行命令；
//   - 日期与过滤参数都做白名单/长度校验，避免被当成任意文件读取；
//   - 返回内容有行数上限，避免一次把整月日志灌进上下文。

// askToolSpec 描述一个可被模型调用的只读工具。
type askToolSpec struct {
	Name        string
	Description string
	// Params 是 JSON Schema 形式的参数说明（透传给上游的 tools 字段）。
	Params map[string]any
}

// askTools 返回暴露给模型的工具清单。
func askTools() []askToolSpec {
	dateProp := map[string]any{
		"type":        "string",
		"description": "日期，格式 YYYY-MM-DD。省略表示今天。",
	}
	return []askToolSpec{
		{
			Name: "list_data_sources",
			Description: "列出本系统所有可查询的数据源（文件位置、内容说明、字段含义）。" +
				"不确定该查什么时先调用它。",
			Params: map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			Name: "query_call_logs",
			Description: "查询调用日志明细（每次 /v1 请求一条）。可按日期、IP、Key、状态码过滤。" +
				"用于分析错误来源、状态码分布、某 IP/Key 的具体调用行为。",
			Params: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"date":       dateProp,
					"ip":         map[string]any{"type": "string", "description": "按来源 IP 过滤（精确匹配）"},
					"key_id":     map[string]any{"type": "string", "description": "按 Key ID 过滤（精确匹配）"},
					"status":     map[string]any{"type": "integer", "description": "按 HTTP 状态码过滤（精确匹配）"},
					"errors_only": map[string]any{"type": "boolean", "description": "只返回状态码 >= 400 的记录"},
					"limit":      map[string]any{"type": "integer", "description": "最多返回多少条（默认 50，上限 500）"},
				},
			},
		},
		{
			Name: "aggregate_call_logs",
			Description: "按指定维度聚合调用日志（服务端算好分组计数，比拉明细更省上下文）。" +
				"常用维度：status（状态码分布）、ip、key_id、model、error（错误消息）、hour（按小时）。",
			Params: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"date": dateProp,
					"group_by": map[string]any{
						"type":        "string",
						"enum":        []string{"status", "ip", "key_id", "model", "error", "hour"},
						"description": "聚合维度",
					},
					"errors_only": map[string]any{"type": "boolean", "description": "只统计状态码 >= 400 的记录"},
				},
				"required": []string{"group_by"},
			},
		},
		{
			Name: "query_audit_logs",
			Description: "查询管理员操作审计日志（上游/模型/参数/领取规则的变更记录，含操作人与结果）。",
			Params: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"date":   dateProp,
					"action": map[string]any{"type": "string", "description": "按操作名过滤（如 update_settings）"},
					"limit":  map[string]any{"type": "integer", "description": "最多返回多少条（默认 50，上限 500）"},
				},
			},
		},
		{
			Name: "query_ask_logs",
			Description: "查询 AI 问答自身的调用记录（问题、模型、token、耗时、是否成功、错误）。",
			Params: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"date":  dateProp,
					"limit": map[string]any{"type": "integer", "description": "最多返回多少条（默认 30，上限 200）"},
				},
			},
		},
		{
			Name:        "list_keys",
			Description: "列出所有 API Key 的当前状态（额度用量、绑定模型、来源 IP、是否禁用/过期、额度日）。",
			Params:      map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			Name:        "list_ip_usage",
			Description: "列出各 IP 的窗口用量（token、活跃时长、请求数、被拦截数、最近活跃时间）。",
			Params:      map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			Name:        "get_ip_policies",
			Description: "读取 IP 限流策略（默认策略 + 每个 IP 的覆盖项）。",
			Params:      map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			Name:        "get_config_summary",
			Description: "读取当前生效的配置摘要（额度、有效期、限速、上游列表、白名单模型等；密钥已脱敏）。",
			Params:      map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			Name:        "get_claim_policy",
			Description: "读取领取规则与今日领取记录（允许网段、一 IP 一 Key 开关、各 IP 领取时间）。",
			Params:      map[string]any{"type": "object", "properties": map[string]any{}},
		},
	}
}

// askToolPayload 把工具清单转成 OpenAI 兼容的 tools 字段。
func askToolPayload() []map[string]any {
	specs := askTools()
	out := make([]map[string]any, 0, len(specs))
	for _, s := range specs {
		out = append(out, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        s.Name,
				"description": s.Description,
				"parameters":  s.Params,
			},
		})
	}
	return out
}

// askDateArg 解析并校验日期参数，默认今天。
func askDateArg(args map[string]any, now time.Time) (string, error) {
	raw, _ := args["date"].(string)
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return now.Format("2006-01-02"), nil
	}
	if len(raw) != 10 {
		return "", fmt.Errorf("日期格式应为 YYYY-MM-DD")
	}
	if _, err := time.Parse("2006-01-02", raw); err != nil {
		return "", fmt.Errorf("日期无效: %s", raw)
	}
	return raw, nil
}

// askLimitArg 解析 limit 参数（带默认值与上限）。
func askLimitArg(args map[string]any, def, max int) int {
	v, ok := args["limit"].(float64)
	if !ok || v <= 0 {
		return def
	}
	n := int(v)
	if n > max {
		n = max
	}
	return n
}

// runAskTool 执行一个工具调用，返回给模型的文本结果。
func (a *App) runAskTool(name string, args map[string]any, now time.Time) (string, error) {
	switch name {
	case "list_data_sources":
		return askDataSourcesDoc(a.ConfigPath), nil
	case "query_call_logs":
		return a.toolQueryCallLogs(args, now)
	case "aggregate_call_logs":
		return a.toolAggregateCallLogs(args, now)
	case "query_audit_logs":
		return a.toolQueryAuditLogs(args, now)
	case "query_ask_logs":
		return a.toolQueryAskLogs(args, now)
	case "list_keys":
		return a.toolListKeys(now)
	case "list_ip_usage":
		return a.toolListIPUsage()
	case "get_ip_policies":
		return a.toolGetIPPolicies()
	case "get_config_summary":
		return a.toolGetConfigSummary()
	case "get_claim_policy":
		return a.toolGetClaimPolicy(now)
	default:
		return "", fmt.Errorf("未知工具: %s", name)
	}
}

// askDataSourcesDoc 是「数据源说明书」：告诉模型每个文件在哪、存什么、字段含义。
// 这是让模型能自主分析的前提——它得先知道有什么、在哪、怎么读。
func askDataSourcesDoc(configDir string) string {
	var b strings.Builder
	b.WriteString("本服务的数据源（全部为本地文件，只读；用下面的工具查询，不要尝试直接读文件路径）：\n\n")

	b.WriteString("1) 调用日志 logs/log-YYYY-MM-DD.jsonl\n")
	b.WriteString("   内容：每次 /v1 请求一条 JSON。字段：\n")
	b.WriteString("     time      请求时间（RFC3339，本地时区）\n")
	b.WriteString("     ip        来源 IP\n")
	b.WriteString("     key_id    Key 内部 ID（不是密钥本身）\n")
	b.WriteString("     model     客户端请求的原始模型名（含上游前缀，如 zz/xxx）\n")
	b.WriteString("     prompt_tokens / completion_tokens / total   token 用量\n")
	b.WriteString("     status    HTTP 状态码（>=400 视为错误）\n")
	b.WriteString("     ms        上游耗时（毫秒）\n")
	b.WriteString("     error     被策略拦截或失败时的原因（普通成功记录没有该字段）\n")
	b.WriteString("   工具：query_call_logs（明细）、aggregate_call_logs（分组统计）\n\n")

	b.WriteString("2) 管理员操作审计 logs/audit-YYYY-MM-DD.jsonl\n")
	b.WriteString("   内容：上游/模型/参数/领取规则的变更记录。字段：time、action、detail、ip、result。\n")
	b.WriteString("   工具：query_audit_logs\n\n")

	b.WriteString("3) AI 问答日志 logs/ask-YYYY-MM-DD.jsonl\n")
	b.WriteString("   内容：AI 问答自身的调用记录。字段：time、question、answer、model、\n")
	b.WriteString("   prompt_tokens、completion_tokens、total、ms、ok、error。\n")
	b.WriteString("   工具：query_ask_logs\n\n")

	b.WriteString("4) Key 数据 keys.json\n")
	b.WriteString("   内容：所有 API Key。字段：id、key、model（空=可用全部白名单）、created_at、\n")
	b.WriteString("   expires_at、token_used、quota、disabled、ip（领取来源）、quota_date（额度日）。\n")
	b.WriteString("   工具：list_keys\n\n")

	b.WriteString("5) IP 用量 ip_usage.json\n")
	b.WriteString("   内容：按 IP 的窗口用量。字段：start/end（窗口边界，每日 08:00）、tokens、\n")
	b.WriteString("   active_ms、requests、blocked、last_seen。\n")
	b.WriteString("   工具：list_ip_usage\n\n")

	b.WriteString("6) IP 策略 ip_policies.json\n")
	b.WriteString("   内容：默认策略 + 单 IP 覆盖（窗口 token 上限、活跃时长上限、输出限速）。\n")
	b.WriteString("   工具：get_ip_policies\n\n")

	b.WriteString("7) 主配置 config.json\n")
	b.WriteString("   内容：额度、有效期、限速、上游列表、白名单模型等（密钥已脱敏）。\n")
	b.WriteString("   工具：get_config_summary\n\n")

	b.WriteString("8) 领取规则 claim_policy.json\n")
	b.WriteString("   内容：允许领取的网段、一 IP 一 Key 开关、各 IP 的领取记录。\n")
	b.WriteString("   工具：get_claim_policy\n\n")

	b.WriteString("9) AI 问答用量 ask_quota.json\n")
	b.WriteString("   内容：当日问答 token 用量与请求数（每日 08:00 重置）。\n")
	b.WriteString("   工具：query_ask_logs 的汇总部分会带上。\n\n")

	b.WriteString("提示：先想清楚要回答什么，再选最省的工具。\n")
	b.WriteString("例如「错误主要来自哪里」→ aggregate_call_logs{group_by:\"status\", errors_only:true}\n")
	b.WriteString("配合 group_by:\"ip\"/\"model\"/\"error\" 逐层下钻，而不是一次性拉全部明细。")
	return b.String()
}

// toolQueryCallLogs 返回调用日志明细（支持过滤）。
func (a *App) toolQueryCallLogs(args map[string]any, now time.Time) (string, error) {
	date, err := askDateArg(args, now)
	if err != nil {
		return "", err
	}
	ip, _ := args["ip"].(string)
	keyID, _ := args["key_id"].(string)
	limit := askLimitArg(args, 50, 500)

	logs, rerr := a.Logger.ReadLogs(date, "")
	if rerr != nil {
		return "", fmt.Errorf("读取 %s 的调用日志失败: %v", date, rerr)
	}

	statusFilter := 0
	if v, ok := args["status"].(float64); ok && v > 0 {
		statusFilter = int(v)
	}
	errorsOnly, _ := args["errors_only"].(bool)

	matched := make([]LogEntry, 0, len(logs))
	for _, e := range logs {
		if ip != "" && e.IP != ip {
			continue
		}
		if keyID != "" && e.KeyID != keyID {
			continue
		}
		if statusFilter > 0 && e.Status != statusFilter {
			continue
		}
		if errorsOnly && e.Status < 400 {
			continue
		}
		matched = append(matched, e)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s 调用日志：共 %d 条记录，匹配 %d 条", date, len(logs), len(matched))
	if len(matched) > limit {
		fmt.Fprintf(&b, "（只返回最近 %d 条）", limit)
		matched = matched[len(matched)-limit:]
	}
	b.WriteString("。\n")
	for _, e := range matched {
		fmt.Fprintf(&b, "%s ip=%s key=%s model=%q status=%d ms=%d tok=%d",
			e.Time, orDash(e.IP), shortKeyID(e.KeyID), e.Model, e.Status, e.Ms, e.Total)
		if e.Error != "" {
			fmt.Fprintf(&b, " error=%q", e.Error)
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}

// toolAggregateCallLogs 按维度聚合（比拉明细省上下文）。
func (a *App) toolAggregateCallLogs(args map[string]any, now time.Time) (string, error) {
	date, err := askDateArg(args, now)
	if err != nil {
		return "", err
	}
	groupBy, _ := args["group_by"].(string)
	switch groupBy {
	case "status", "ip", "key_id", "model", "error", "hour":
	default:
		return "", fmt.Errorf("group_by 只支持 status/ip/key_id/model/error/hour")
	}
	errorsOnly, _ := args["errors_only"].(bool)

	logs, rerr := a.Logger.ReadLogs(date, "")
	if rerr != nil {
		return "", fmt.Errorf("读取 %s 的调用日志失败: %v", date, rerr)
	}

	counts := map[string]int{}
	tokens := map[string]int{}
	total := 0
	for _, e := range logs {
		if errorsOnly && e.Status < 400 {
			continue
		}
		total++
		var k string
		switch groupBy {
		case "status":
			k = fmt.Sprintf("%d", e.Status)
		case "ip":
			k = orDash(e.IP)
		case "key_id":
			k = shortKeyID(e.KeyID)
		case "model":
			if e.Model == "" {
				k = "（空：旧日志或未记录）"
			} else {
				k = e.Model
			}
		case "error":
			if e.Error == "" {
				k = "（无 error 字段）"
			} else {
				k = e.Error
			}
		case "hour":
			if t, perr := time.Parse(time.RFC3339, e.Time); perr == nil {
				k = t.Format("15:00")
			} else {
				k = "（时间解析失败）"
			}
		}
		counts[k]++
		tokens[k] += e.Total
	}

	type row struct {
		key    string
		n      int
		tokens int
	}
	rows := make([]row, 0, len(counts))
	for k, n := range counts {
		rows = append(rows, row{k, n, tokens[k]})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].n != rows[j].n {
			return rows[i].n > rows[j].n
		}
		return rows[i].key < rows[j].key
	})

	var b strings.Builder
	fmt.Fprintf(&b, "%s 按 %s 聚合", date, groupBy)
	if errorsOnly {
		b.WriteString("（仅 status>=400）")
	}
	fmt.Fprintf(&b, "：共 %d 条记录，%d 个分组。\n", total, len(rows))
	for _, r := range rows {
		pct := 0.0
		if total > 0 {
			pct = float64(r.n) * 100 / float64(total)
		}
		fmt.Fprintf(&b, "- %s：%d 条（%.1f%%），token %d\n", r.key, r.n, pct, r.tokens)
	}
	return b.String(), nil
}

// toolQueryAuditLogs 返回管理员操作审计。
func (a *App) toolQueryAuditLogs(args map[string]any, now time.Time) (string, error) {
	date, err := askDateArg(args, now)
	if err != nil {
		return "", err
	}
	action, _ := args["action"].(string)
	limit := askLimitArg(args, 50, 500)
	if a.Audit == nil {
		return "审计日志未启用。", nil
	}
	entries := a.Audit.Read(date)
	matched := make([]AuditEntry, 0, len(entries))
	for _, e := range entries {
		if action != "" && e.Action != action {
			continue
		}
		matched = append(matched, e)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s 审计日志：共 %d 条，匹配 %d 条。\n", date, len(entries), len(matched))
	if len(matched) > limit {
		fmt.Fprintf(&b, "（只返回最近 %d 条）\n", limit)
		matched = matched[len(matched)-limit:]
	}
	for _, e := range matched {
		fmt.Fprintf(&b, "%s action=%s result=%s ip=%s detail=%s\n",
			e.Time, e.Action, orDash(e.Result), orDash(e.IP), e.Detail)
	}
	return b.String(), nil
}

// toolQueryAskLogs 返回 AI 问答自身记录。
func (a *App) toolQueryAskLogs(args map[string]any, now time.Time) (string, error) {
	date, err := askDateArg(args, now)
	if err != nil {
		return "", err
	}
	limit := askLimitArg(args, 30, 200)
	if a.Ask == nil {
		return "AI 问答计量未启用。", nil
	}
	entries, rerr := a.Ask.ReadAskLogs(date)
	if rerr != nil {
		return "", fmt.Errorf("读取 %s 的问答日志失败: %v", date, rerr)
	}
	var b strings.Builder
	if st := a.Ask.Snapshot(now); st.Date != "" {
		fmt.Fprintf(&b, "当日问答用量：请求 %d 次，token %d（每日 08:00 重置）。\n",
			st.Requests, st.UsedTokens)
	}
	fmt.Fprintf(&b, "%s 问答日志：共 %d 条。\n", date, len(entries))
	if len(entries) > limit {
		fmt.Fprintf(&b, "（只返回最近 %d 条）\n", limit)
		entries = entries[len(entries)-limit:]
	}
	for _, e := range entries {
		fmt.Fprintf(&b, "%s model=%q ok=%v ms=%d tok=%d q=%q",
			e.Time, e.Model, e.OK, e.Ms, e.Total, truncateRunes(e.Question, 60))
		if e.Error != "" {
			fmt.Fprintf(&b, " error=%q", e.Error)
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}

// toolListKeys 列出 Key 状态。
func (a *App) toolListKeys(now time.Time) (string, error) {
	keys := a.Store.List()
	var b strings.Builder
	fmt.Fprintf(&b, "共 %d 个 Key：\n", len(keys))
	for _, k := range keys {
		model := k.Model
		if model == "" {
			model = "（全部白名单）"
		}
		fmt.Fprintf(&b, "- %s ip=%s model=%s used=%d/%d disabled=%v expired=%v quota_date=%s\n",
			shortKeyID(k.ID), orDash(k.IP), model, k.TokenUsed, k.Quota,
			k.Disabled, isExpired(k, now), orDash(k.QuotaDate))
	}
	return b.String(), nil
}

// toolListIPUsage 列出 IP 窗口用量。
func (a *App) toolListIPUsage() (string, error) {
	if a.IPs == nil {
		return "IP 用量统计未启用。", nil
	}
	rows := a.IPs.Snapshot(time.Now())
	var b strings.Builder
	fmt.Fprintf(&b, "共 %d 个 IP 有窗口用量：\n", len(rows))
	for _, r := range rows {
		fmt.Fprintf(&b, "- %s tokens=%d active_min=%.1f requests=%d blocked=%d reset_at=%s last_seen=%s overridden=%v\n",
			r.IP, r.Tokens, float64(r.ActiveMs)/60000, r.Requests, r.Blocked,
			orDash(r.ResetAt), orDash(r.LastSeen), r.Overridden)
	}
	return b.String(), nil
}

// toolGetIPPolicies 读取 IP 限流策略。
func (a *App) toolGetIPPolicies() (string, error) {
	if a.IPs == nil {
		return "IP 策略未启用。", nil
	}
	d := a.IPs.Policies().Defaults()
	overrides := a.IPs.Policies().ListOverrides()
	var b strings.Builder
	fmt.Fprintf(&b, "默认策略：窗口 %d 分钟，token 上限 %d（0=不限），活跃时长上限 %d 分钟，输出限速 %d token/秒。\n",
		d.WindowMinutes, d.WindowTokens, d.MaxActiveMinutes, d.OutputTPS)
	fmt.Fprintf(&b, "单 IP 覆盖 %d 条：\n", len(overrides))
	for _, o := range overrides {
		fmt.Fprintf(&b, "- %s：%+v\n", o.IP, o)
	}
	return b.String(), nil
}

// toolGetConfigSummary 读取配置摘要（密钥脱敏）。
func (a *App) toolGetConfigSummary() (string, error) {
	a.Mu.RLock()
	cfg := a.Cfg
	ttl, quota, rate := cfg.TTLDays, cfg.TokenQuota, cfg.RateLimitPerMin
	publicBase := cfg.PublicBaseURL
	allowed := append([]string(nil), cfg.AllowedModels...)
	ups := append([]Upstream(nil), cfg.EffectiveUpstreams()...)
	defaultID := cfg.DefaultUpstreamID
	a.Mu.RUnlock()

	var b strings.Builder
	fmt.Fprintf(&b, "Key 有效期 %d 天（0=永不过期）；默认额度 %d token/日；单 Key 每分钟上限 %d 次。\n",
		ttl, quota, rate)
	fmt.Fprintf(&b, "对外访问地址：%s\n", orDash(publicBase))
	fmt.Fprintf(&b, "白名单模型 %d 个：%s\n", len(allowed), strings.Join(allowed, ", "))
	fmt.Fprintf(&b, "上游 %d 个（默认 %s）：\n", len(ups), orDash(defaultID))
	for _, u := range ups {
		fmt.Fprintf(&b, "- id=%s name=%q base_url=%s key=%s enabled=%v\n",
			u.ID, u.Name, u.BaseURL, maskSecret(u.APIKey), u.Enabled)
	}
	return b.String(), nil
}

// toolGetClaimPolicy 读取领取规则与领取记录。
func (a *App) toolGetClaimPolicy(now time.Time) (string, error) {
	if a.Claims == nil {
		return "领取策略未启用。", nil
	}
	rule := a.Claims.Rule()
	var b strings.Builder
	fmt.Fprintf(&b, "允许领取网段：%s\n", strings.Join(rule.AllowCIDRs, ", "))
	fmt.Fprintf(&b, "一 IP 一 Key：%v\n", rule.OneKeyPerIP)
	issued := a.Claims.ClaimedList(now)
	fmt.Fprintf(&b, "今日已领取 %d 个 IP：\n", len(issued))
	for _, iss := range issued {
		fmt.Fprintf(&b, "- %s key=%s time=%s count=%d\n",
			iss.IP, shortKeyID(iss.KeyID), iss.Time, iss.Count)
	}
	return b.String(), nil
}

// ---- 小工具 ----

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// shortKeyID 缩短 Key ID 便于阅读（保留前 10 位）。
func shortKeyID(id string) string {
	if len(id) <= 10 {
		return id
	}
	return id[:10] + "…"
}

// truncateRunes 按字符截断（避免切坏 UTF-8）。
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// jsonArgString 便于调试：把参数对象转成紧凑 JSON。
func jsonArgString(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}
