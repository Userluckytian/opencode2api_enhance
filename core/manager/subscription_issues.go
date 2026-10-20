// 订阅解析问题收集（阶段 X T3 / B2，借鉴 uniproxy parser.go:50-60 + model/types.go:16-25）。
// 原则：坏条目不杀好节点——逐行/逐 outbounds 的失败被收集为 issue（带定位），
// 好节点照常存活；详情封顶 128 条但 total 记全量（truncated 口径给 UI「至少 M 项」真话）。
package manager

import (
	"errors"
	"fmt"
)

// maxParseIssueDetails 详情上限（uniproxy 同值），超出仅计数。
const maxParseIssueDetails = 128

// ParseIssue 单条解析问题：Where 定位（"第 7 行" / "outbound #3"），Reason 人话原因。
type ParseIssue struct {
	Where  string `json:"where"`
	Reason string `json:"reason"`
}

// ParseReport 解析过程问题汇总。
type ParseReport struct {
	Issues    []ParseIssue `json:"issues,omitempty"`
	Total     int          `json:"issues_total"`
	Skipped   int          `json:"skipped,omitempty"` // 类型不认识/字段不全而被跳过的条目数
	Truncated bool         `json:"issues_truncated,omitempty"`
}

func (r *ParseReport) add(where, format string, a ...any) {
	if r == nil {
		return
	}
	r.Total++
	if len(r.Issues) < maxParseIssueDetails {
		r.Issues = append(r.Issues, ParseIssue{Where: where, Reason: fmt.Sprintf(format, a...)})
	} else {
		r.Truncated = true
	}
}

// addSkip 计入「静默跳过」类问题（未知类型/字段不全）。
func (r *ParseReport) addSkip(where, format string, a ...any) {
	if r == nil {
		return
	}
	r.Skipped++
	r.add(where, format, a...)
}

// outboundWhere 为 sing-box JSON 第 i 个 outbound 生成定位串。
func outboundWhere(i int) string {
	return "outbounds 第 " + fmt.Sprint(i+1) + " 项"
}

// RefreshRejectedError 删除保护闸拒绝（阶段 X B3）。
// 结构化类型而非字符串哨兵（审查 g 项）：调用方 errors.As 判定，
// 文案怎么改都不会隐性破坏「rejected」语义链路（前端 force 弹窗/日志级别/自动拉取路径）。
type RefreshRejectedError struct {
	Msg string
}

func (e *RefreshRejectedError) Error() string { return e.Msg }

// asRejected 统一判定（错误链任意层级包装都认）。
func asRejected(err error) bool {
	var re *RefreshRejectedError
	return errors.As(err, &re)
}
