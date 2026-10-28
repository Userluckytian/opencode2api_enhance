package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// AI 配置建议（方案 A：AI 只提议，管理员确认后才写入）。
//
// 为什么不给 AI 写权限：模型可能理解偏差或幻觉，而配置写坏是服务级故障——
// 把 allow_cidrs 写成空列表会让所有人都领不到 Key，改错上游地址会让整个网关不可用。
// 因此 AI 只能调用 propose_config_change「记录建议」，真正写入必须由管理员
// 在前端点「应用」，再走 applyAskSuggestion 的完整校验。

// askSuggestion 是一条待确认的配置修改建议。
type askSuggestion struct {
	// Target 是配置项标识（见 askSuggestionTargets）。
	Target string `json:"target"`
	// Label 是给人看的中文名。
	Label string `json:"label"`
	// Current 是建议生成时的当前值（字符串化，便于展示 diff）。
	Current string `json:"current"`
	// Proposed 是建议的新值。
	Proposed string `json:"proposed"`
	// Value 是结构化新值（应用到配置时使用）。
	Value any `json:"value"`
	// Reason 是 AI 给出的理由。
	Reason string `json:"reason"`
}

// askSuggestionTarget 描述一个可建议修改的配置项。
type askSuggestionTarget struct {
	Label string
	// Get 读取当前值（字符串形式，用于展示）。
	Get func(a *App) string
	// Apply 校验并写入；返回新的当前值。
	Apply func(a *App, v any) (string, error)
}

// askSuggestionTargets 是允许 AI 建议修改的白名单配置项。
// 不在表里的目标一律拒绝——这是安全边界，不是可配置项。
var askSuggestionTargets = map[string]askSuggestionTarget{
	"token_quota": {
		Label: "每 Key 每日额度（token）",
		Get: func(a *App) string {
			a.Mu.RLock()
			defer a.Mu.RUnlock()
			return fmt.Sprintf("%d", a.Cfg.TokenQuota)
		},
		Apply: func(a *App, v any) (string, error) {
			n, err := askIntValue(v)
			if err != nil {
				return "", err
			}
			if n <= 0 || n > MaxTokenQuota {
				return "", fmt.Errorf("token_quota 必须在 1..%d 之间", MaxTokenQuota)
			}
			a.Mu.Lock()
			a.Cfg.TokenQuota = n
			serr := a.Cfg.Save(a.ConfigPath)
			a.Mu.Unlock()
			if serr != nil {
				return "", fmt.Errorf("保存失败: %v", serr)
			}
			// 与 handlePutSettings 一致：同步 store 默认额度（次日 08:00 对存量生效）。
			a.Store.SetDefaultQuota(n)
			return fmt.Sprintf("%d", n), nil
		},
	},
	"ttl_days": {
		Label: "Key 有效期（天，0=永不过期）",
		Get: func(a *App) string {
			a.Mu.RLock()
			defer a.Mu.RUnlock()
			return fmt.Sprintf("%d", a.Cfg.TTLDays)
		},
		Apply: func(a *App, v any) (string, error) {
			n, err := askIntValue(v)
			if err != nil {
				return "", err
			}
			if n < 0 || n > MaxTTLDays {
				return "", fmt.Errorf("ttl_days 必须在 0..%d 之间", MaxTTLDays)
			}
			a.Mu.Lock()
			a.Cfg.TTLDays = n
			serr := a.Cfg.Save(a.ConfigPath)
			a.Mu.Unlock()
			if serr != nil {
				return "", fmt.Errorf("保存失败: %v", serr)
			}
			a.Store.ApplyTTL(n, time.Now())
			return fmt.Sprintf("%d", n), nil
		},
	},
	"rate_limit_per_min": {
		Label: "单 Key 每分钟请求上限",
		Get: func(a *App) string {
			a.Mu.RLock()
			defer a.Mu.RUnlock()
			return fmt.Sprintf("%d", a.Cfg.RateLimitPerMin)
		},
		Apply: func(a *App, v any) (string, error) {
			n, err := askIntValue(v)
			if err != nil {
				return "", err
			}
			if n < 0 || n > MaxRateLimitPerMin {
				return "", fmt.Errorf("rate_limit_per_min 必须在 0..%d 之间", MaxRateLimitPerMin)
			}
			a.Mu.Lock()
			a.Cfg.RateLimitPerMin = n
			serr := a.Cfg.Save(a.ConfigPath)
			a.Mu.Unlock()
			if serr != nil {
				return "", fmt.Errorf("保存失败: %v", serr)
			}
			a.SetRateLimiter(NewRateLimiter(n))
			return fmt.Sprintf("%d", n), nil
		},
	},
	"ip_window_tokens": {
		Label: "IP 窗口 token 上限（0=不限）",
		Get: func(a *App) string {
			if a.IPs == nil {
				return "未启用"
			}
			return fmt.Sprintf("%d", a.IPs.Policies().Defaults().WindowTokens)
		},
		Apply: func(a *App, v any) (string, error) {
			return applyIPDefault(a, v, func(d *IPDefaults, n int) { d.WindowTokens = n })
		},
	},
	"ip_window_minutes": {
		Label: "IP 窗口长度（分钟）",
		Get: func(a *App) string {
			if a.IPs == nil {
				return "未启用"
			}
			return fmt.Sprintf("%d", a.IPs.Policies().Defaults().WindowMinutes)
		},
		Apply: func(a *App, v any) (string, error) {
			return applyIPDefault(a, v, func(d *IPDefaults, n int) { d.WindowMinutes = n })
		},
	},
	"ip_max_active_minutes": {
		Label: "IP 活跃时长上限（分钟）",
		Get: func(a *App) string {
			if a.IPs == nil {
				return "未启用"
			}
			return fmt.Sprintf("%d", a.IPs.Policies().Defaults().MaxActiveMinutes)
		},
		Apply: func(a *App, v any) (string, error) {
			return applyIPDefault(a, v, func(d *IPDefaults, n int) { d.MaxActiveMinutes = n })
		},
	},
	"ip_output_tps": {
		Label: "IP 输出限速（token/秒）",
		Get: func(a *App) string {
			if a.IPs == nil {
				return "未启用"
			}
			return fmt.Sprintf("%d", a.IPs.Policies().Defaults().OutputTPS)
		},
		Apply: func(a *App, v any) (string, error) {
			return applyIPDefault(a, v, func(d *IPDefaults, n int) { d.OutputTPS = n })
		},
	},
	"allowed_models": {
		Label: "模型白名单",
		Get: func(a *App) string {
			a.Mu.RLock()
			defer a.Mu.RUnlock()
			return strings.Join(a.Cfg.AllowedModels, ", ")
		},
		Apply: func(a *App, v any) (string, error) {
			list, err := askStringList(v)
			if err != nil {
				return "", err
			}
			// 与 validateSuggestionValue 同一判定：空白名单会让领取页无模型可选。
			if len(list) == 0 {
				return "", fmt.Errorf("模型白名单不能为空（否则领取页无模型可选）")
			}
			a.Mu.Lock()
			a.Cfg.AllowedModels = list
			serr := a.Cfg.Save(a.ConfigPath)
			a.Mu.Unlock()
			if serr != nil {
				return "", fmt.Errorf("保存失败: %v", serr)
			}
			return strings.Join(list, ", "), nil
		},
	},
	"one_key_per_ip": {
		Label: "一 IP 一 Key",
		Get: func(a *App) string {
			if a.Claims == nil {
				return "未启用"
			}
			return fmt.Sprintf("%v", a.Claims.Rule().OneKeyPerIP)
		},
		Apply: func(a *App, v any) (string, error) {
			b, ok := v.(bool)
			if !ok {
				return "", fmt.Errorf("one_key_per_ip 需要布尔值")
			}
			if a.Claims == nil {
				return "", fmt.Errorf("领取策略未启用")
			}
			rule := a.Claims.Rule()
			rule.OneKeyPerIP = b
			a.Claims.SetRule(rule)
			return fmt.Sprintf("%v", b), nil
		},
	},
}

// applyIPDefault 是 IP 默认策略四个字段的公共写入路径。
func applyIPDefault(a *App, v any, set func(*IPDefaults, int)) (string, error) {
	n, err := askIntValue(v)
	if err != nil {
		return "", err
	}
	if a.IPs == nil {
		return "", fmt.Errorf("IP 策略未启用")
	}
	d := a.IPs.Policies().Defaults()
	set(&d, n)
	if verr := d.validate(); verr != nil {
		return "", verr
	}
	if serr := a.IPs.Policies().SetDefaults(d); serr != nil {
		return "", serr
	}
	return fmt.Sprintf("%d", n), nil
}

// askIntValue 把 JSON 数值/字符串统一转成 int。
func askIntValue(v any) (int, error) {
	switch x := v.(type) {
	case float64:
		return int(x), nil
	case int:
		return x, nil
	case string:
		var n int
		if _, err := fmt.Sscanf(strings.TrimSpace(x), "%d", &n); err != nil {
			return 0, fmt.Errorf("无法解析为数字: %q", x)
		}
		return n, nil
	}
	return 0, fmt.Errorf("需要数字，收到 %T", v)
}

// askStringList 把 JSON 数组转成字符串切片。
func askStringList(v any) ([]string, error) {
	arr, ok := v.([]any)
	if !ok {
		if ss, ok2 := v.([]string); ok2 {
			return ss, nil
		}
		return nil, fmt.Errorf("需要字符串数组")
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		s, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("数组元素必须是字符串")
		}
		if t := strings.TrimSpace(s); t != "" {
			out = append(out, t)
		}
	}
	return out, nil
}

// proposeConfigChangeTool 是 propose_config_change 的工具描述。
func proposeConfigChangeTool() askToolSpec {
	targets := make([]string, 0, len(askSuggestionTargets))
	for k := range askSuggestionTargets {
		targets = append(targets, k)
	}
	return askToolSpec{
		Name: "propose_config_change",
		Description: "提议修改一项配置。此工具**只记录建议、不会立即生效**——管理员会在界面上看到" +
			"「应用/忽略」按钮，点「应用」后才真正写入。只在用户明确询问配置调整、" +
			"且你已用只读工具确认了当前值与数据依据时调用。一次回答可提议多项。" +
			"可用 target：" + strings.Join(targets, " / "),
		Params: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"target": map[string]any{
					"type":        "string",
					"enum":        targets,
					"description": "要修改的配置项",
				},
				"value": map[string]any{
					"description": "新值。数值项传数字；allowed_models 传字符串数组；one_key_per_ip 传布尔。",
				},
				"reason": map[string]any{
					"type":        "string",
					"description": "为什么建议改成这个值（引用你查到的数据依据）",
				},
			},
			"required": []string{"target", "value", "reason"},
		},
	}
}

// buildAskSuggestion 校验 AI 传入的参数并构造建议（不写入任何配置）。
func buildAskSuggestion(a *App, args map[string]any) (askSuggestion, error) {
	target, _ := args["target"].(string)
	target = strings.TrimSpace(target)
	spec, ok := askSuggestionTargets[target]
	if !ok {
		return askSuggestion{}, fmt.Errorf("不支持修改 %q", target)
	}
	if _, has := args["value"]; !has {
		return askSuggestion{}, fmt.Errorf("缺少 value")
	}
	reason, _ := args["reason"].(string)
	if strings.TrimSpace(reason) == "" {
		return askSuggestion{}, fmt.Errorf("缺少 reason（请说明数据依据）")
	}

	// 先校验建议值本身合法（避免给出「应用必然失败」的建议），但不写入任何配置。
	if err := validateSuggestionValue(a, target, args["value"]); err != nil {
		return askSuggestion{}, fmt.Errorf("建议值不合法: %v", err)
	}
	return askSuggestion{
		Target:   target,
		Label:    spec.Label,
		Current:  spec.Get(a),
		Proposed: describeProposedValue(args["value"]),
		Value:    args["value"],
		Reason:   strings.TrimSpace(reason),
	}, nil
}

// describeProposedValue 把待写入值转成展示字符串。
func describeProposedValue(v any) string {
	switch x := v.(type) {
	case float64:
		// JSON 数字统一是 float64；整数就不显示小数。
		if x == float64(int64(x)) {
			return fmt.Sprintf("%d", int64(x))
		}
		return fmt.Sprintf("%v", x)
	case []any:
		parts := make([]string, 0, len(x))
		for _, item := range x {
			parts = append(parts, fmt.Sprintf("%v", item))
		}
		return strings.Join(parts, ", ")
	case nil:
		return "（空）"
	}
	return fmt.Sprintf("%v", v)
}

// validateSuggestionValue 只做范围校验，不落盘。
//
// 不能直接调 spec.Apply：那会真的写配置。这里按 target 复用同一套边界常量
// 重新校验一遍，保证「建议值」与「应用时」的判定一致。
func validateSuggestionValue(a *App, target string, v any) error {
	switch target {
	case "token_quota":
		n, err := askIntValue(v)
		if err != nil {
			return err
		}
		if n <= 0 || n > MaxTokenQuota {
			return fmt.Errorf("token_quota 必须在 1..%d 之间", MaxTokenQuota)
		}
		return nil
	case "ttl_days":
		n, err := askIntValue(v)
		if err != nil {
			return err
		}
		if n < 0 || n > MaxTTLDays {
			return fmt.Errorf("ttl_days 必须在 0..%d 之间", MaxTTLDays)
		}
		return nil
	case "rate_limit_per_min":
		n, err := askIntValue(v)
		if err != nil {
			return err
		}
		if n < 0 || n > MaxRateLimitPerMin {
			return fmt.Errorf("rate_limit_per_min 必须在 0..%d 之间", MaxRateLimitPerMin)
		}
		return nil
	case "ip_window_tokens", "ip_window_minutes", "ip_max_active_minutes", "ip_output_tps":
		n, err := askIntValue(v)
		if err != nil {
			return err
		}
		if a.IPs == nil {
			return fmt.Errorf("IP 策略未启用")
		}
		d := a.IPs.Policies().Defaults()
		switch target {
		case "ip_window_tokens":
			d.WindowTokens = n
		case "ip_window_minutes":
			d.WindowMinutes = n
		case "ip_max_active_minutes":
			d.MaxActiveMinutes = n
		case "ip_output_tps":
			d.OutputTPS = n
		}
		return d.validate()
	case "allowed_models":
		list, err := askStringList(v)
		if err != nil {
			return err
		}
		if len(list) == 0 {
			return fmt.Errorf("模型白名单不能为空（否则领取页无模型可选）")
		}
		return nil
	case "one_key_per_ip":
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("one_key_per_ip 需要布尔值")
		}
		return nil
	}
	return fmt.Errorf("不支持修改 %q", target)
}

// handleApplyAskSuggestion 应用一条 AI 建议（管理员确认后调用）。
func (a *App) handleApplyAskSuggestion(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Target string `json:"target"`
		Value  any    `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	spec, ok := askSuggestionTargets[req.Target]
	if !ok {
		writeErr(w, http.StatusBadRequest, "不支持修改该项: "+req.Target)
		return
	}

	before := spec.Get(a)
	after, err := spec.Apply(a, req.Value)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	// 配置变更落审计：谁在什么时候通过 AI 建议改了什么。
	if a.Audit != nil {
		a.Audit.Write(AuditEntry{
			Time:   time.Now().Format(time.RFC3339),
			Action: "apply_ask_suggestion",
			Detail: fmt.Sprintf("%s: %s → %s", req.Target, before, after),
			IP:     clientIP(r),
			Result: "ok",
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"target":  req.Target,
		"label":   spec.Label,
		"before":  before,
		"after":   after,
	})
}
