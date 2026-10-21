package main

import (
	"fmt"
	"strings"
	"unicode"
)

// 管理员口令强度策略（修复 finding 544 的配套项 / finding 714 修复要点第⑤条）。
//
// 背景：旧实现（web/admin.go handlePutPassword）只有 `len(req.Password) < 4`
// 一道长度闸门，等价于「允许 4 位口令」。现网管理口令 `0099` 正是 4 位纯数字，
// 而 finding 544 已证登录限流可被并发齐射放大（30 并发中 24 次进入真实口令校验），
// 二者叠加等于把管理后台的爆破成本压到极低。
//
// 策略要点：
//  1. 硬性最小长度 MinAdminPasswordLen（8 位），与根 main.go:758 的
//     「管理口令长度不足 8 位」告警口径保持一致；RecommendedAdminPasswordLen（12 位）
//     是推荐值（写进错误提示与前端说明），不做硬性拒绝，避免把管理员挡在门外。
//  2. 拒绝常见弱口令：纯数字、纯字母、同一字符重复、常见弱口令字典。
//  3. 拒绝与当前口令相同 / 高度相似（防止「改密」形同虚设）。
//  4. 至少包含 3 类字符（大写、小写、数字、符号）中的 3 类。
//
// 校验失败时调用方必须：返回 HTTP 400、写审计记录（沿用 Audit.Write 模式）、
// 且**不落盘**（保持原 AdminPasswordHash 不变）。
const (
	// MinAdminPasswordLen 是管理口令的硬性最小长度。
	MinAdminPasswordLen = 8
	// RecommendedAdminPasswordLen 是推荐长度（仅提示，不做硬性拒绝）。
	RecommendedAdminPasswordLen = 12
	// MaxAdminPasswordLen 是管理口令的最大长度，防止超长输入拖慢 PBKDF2 校验。
	MaxAdminPasswordLen = 256
)

// commonWeakPasswords 是常见弱口令字典（大小写不敏感比较）。
// 只收录最常见的几类，够挡住「口令强度检查」被当成摆设的情况。
var commonWeakPasswords = map[string]bool{
	"password":      true,
	"passw0rd":      true,
	"password1":     true,
	"password123":   true,
	"admin":         true,
	"admin123":      true,
	"admin1234":     true,
	"admin12345":    true,
	"administrator": true,
	"adminadmin":    true,
	"qwerty":        true,
	"qwerty123":     true,
	"qwertyuiop":    true,
	"123456":        true,
	"1234567":       true,
	"12345678":      true,
	"123456789":     true,
	"1234567890":    true,
	"123456789012":  true,
	"111111":        true,
	"11111111":      true,
	"000000":        true,
	"00000000":      true,
	"888888":        true,
	"666666":        true,
	"654321":        true,
	"abc123":        true,
	"abc12345":      true,
	"abc123456":     true,
	"a123456":       true,
	"a12345678":     true,
	"iloveyou":      true,
	"letmein":       true,
	"welcome":       true,
	"welcome1":      true,
	"monkey":        true,
	"dragon":        true,
	"football":      true,
	"baseball":      true,
	"sunshine":      true,
	"princess":      true,
	"trustno1":      true,
	"changeme":      true,
	"default":       true,
	"root":          true,
	"toor":          true,
	"test":          true,
	"test123":       true,
	"test1234":      true,
	"guest":         true,
	"secret":        true,
	"secret123":     true,
	"tokenbuffet":   true,
	"buffet":        true,
	"opencode2api":  true,
}

// PasswordPolicyHint 是给前端/错误提示用的策略说明（不含任何敏感值）。
const PasswordPolicyHint = "密码至少 8 位（建议 12 位以上），需包含大写字母、小写字母、数字、符号中的至少 3 类，" +
	"不能是纯数字、纯字母、同一字符重复或常见弱口令，也不能与当前密码相同或过于相似。"

// CurrentPassword 描述「当前口令」这一比较基准。
//
// 服务端只持久化口令哈希（PBKDF2），因此**相同性**判定走 Verify 回调
// （用现成的哈希直接校验候选口令，不落任何明文）；**相似性**判定需要一个
// 可比较的明文基准，由登录成功时在内存里暂存（见 setCurrentAdminPlain），
// 进程重启后明文基准消失，此时相似性检查自动降级为「仅相同性」——
// 不会为了相似性把明文口令写进 config.json（无盐 sha256 落盘等于把
// 口令空间降级成可秒破的字典，这是绝不能做的）。
type CurrentPassword struct {
	// Plain 是当前口令明文（仅内存，进程重启后为空）。
	Plain string
	// Verify 用当前口令哈希校验候选口令是否与当前口令**完全相同**。
	// 为 nil 时跳过相同性检查。
	Verify func(candidate string) bool
}

// passwordCharClasses 统计口令里出现过的字符类别数量。
func passwordCharClasses(pw string) int {
	var upper, lower, digit, symbol bool
	for _, r := range pw {
		switch {
		case unicode.IsUpper(r):
			upper = true
		case unicode.IsLower(r):
			lower = true
		case unicode.IsDigit(r):
			digit = true
		default:
			symbol = true
		}
	}
	n := 0
	for _, b := range []bool{upper, lower, digit, symbol} {
		if b {
			n++
		}
	}
	return n
}

// isAllSameChar 判断口令是否由单一字符重复构成（如 "aaaaaaaa"）。
func isAllSameChar(pw string) bool {
	if pw == "" {
		return false
	}
	first := []rune(pw)[0]
	for _, r := range pw {
		if r != first {
			return false
		}
	}
	return true
}

// isAllDigits 判断口令是否全为数字。
func isAllDigits(pw string) bool {
	if pw == "" {
		return false
	}
	for _, r := range pw {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// isAllLetters 判断口令是否全为字母（不含数字/符号）。
func isAllLetters(pw string) bool {
	if pw == "" {
		return false
	}
	for _, r := range pw {
		if !unicode.IsLetter(r) {
			return false
		}
	}
	return true
}

// isCommonWeakPassword 判断口令是否属于常见弱口令。
//
// 除了直接查字典，还额外用「去掉符号后的形式」再查一次：
// 这样 "Admin@123"、"p@ssw0rd"、"12345!" 之类的「加了符号的弱口令」
// 也会被识别出来（去掉符号后就是字典里的弱口令）。
func isCommonWeakPassword(pw string) bool {
	low := strings.ToLower(pw)
	if commonWeakPasswords[low] {
		return true
	}
	var stripped strings.Builder
	for _, r := range low {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			stripped.WriteRune(r)
		}
	}
	s := stripped.String()
	if s == "" {
		return false
	}
	if commonWeakPasswords[s] {
		return true
	}
	// 再去掉尾部的数字："changeme1" → "changeme"、"admin123" → "admin"。
	trimmed := strings.TrimRight(s, "0123456789")
	return trimmed != "" && commonWeakPasswords[trimmed]
}

// levenshtein 计算两个字符串的编辑距离（按 rune 计算）。
func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	la, lb := len(ra), len(rb)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}
	prev := make([]int, lb+1)
	cur := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		cur[0] = i
		for j := 1; j <= lb; j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			m := prev[j] + 1
			if cur[j-1]+1 < m {
				m = cur[j-1] + 1
			}
			if prev[j-1]+cost < m {
				m = prev[j-1] + cost
			}
			cur[j] = m
		}
		prev, cur = cur, prev
	}
	return prev[lb]
}

// passwordSimilar 判断 new 与 current 是否「相同或高度相似」。
//
// 相似判定用三条互补的启发式（避免过度复杂、也避免把正常的新口令误杀）：
//  1. 忽略大小写后完全相同；
//  2. 一方是另一方的前缀/后缀（管理员常见的「加个 1」「换个大小写」式弱改动）；
//  3. 编辑距离 ≤ 2（长度 ≥ 8 时）或 ≤ 1（更短时），即只改了一两个字符。
func passwordSimilar(new, current string) bool {
	if current == "" {
		return false
	}
	if strings.EqualFold(new, current) {
		return true
	}
	limit := 1
	if len([]rune(new)) >= 8 && len([]rune(current)) >= 8 {
		limit = 2
	}
	// 前缀/后缀追加：只在「多出来的字符数 ≤ limit」时才算相似，
	// 避免把「在原口令后追加一大段」这种合理的强口令改动误杀。
	ln, lc := len([]rune(new)), len([]rune(current))
	if ln >= 4 && lc >= 4 {
		if diff := ln - lc; diff >= 0 && diff <= limit &&
			strings.HasPrefix(new, current) {
			return true
		}
		if diff := lc - ln; diff >= 0 && diff <= limit &&
			strings.HasPrefix(current, new) {
			return true
		}
		if diff := ln - lc; diff >= 0 && diff <= limit &&
			strings.HasSuffix(new, current) {
			return true
		}
		if diff := lc - ln; diff >= 0 && diff <= limit &&
			strings.HasSuffix(current, new) {
			return true
		}
	}
	return levenshtein(strings.ToLower(new), strings.ToLower(current)) <= limit
}

// ValidateAdminPassword 校验管理口令强度。
//
// newPassword：本次要设置的新口令；cur：当前口令的比较基准（可为空结构体）。
// 返回非 nil error 表示拒绝，error 文案可直接作为给管理员的提示（不含口令内容）。
func ValidateAdminPassword(newPassword string, cur CurrentPassword) error {
	pw := newPassword
	if pw == "" {
		return fmt.Errorf("密码不能为空")
	}
	if n := len([]rune(pw)); n < MinAdminPasswordLen {
		return fmt.Errorf("密码长度不足 %d 位（当前 %d 位），至少需要 %d 位，建议 %d 位以上",
			MinAdminPasswordLen, n, MinAdminPasswordLen, RecommendedAdminPasswordLen)
	}
	if n := len([]rune(pw)); n > MaxAdminPasswordLen {
		return fmt.Errorf("密码过长（最多 %d 位）", MaxAdminPasswordLen)
	}
	if isAllDigits(pw) {
		return fmt.Errorf("密码不能是纯数字")
	}
	if isAllLetters(pw) {
		return fmt.Errorf("密码不能是纯字母")
	}
	if isAllSameChar(pw) {
		return fmt.Errorf("密码不能是同一个字符重复")
	}
	if isCommonWeakPassword(pw) {
		return fmt.Errorf("密码过于常见，请换一个不易猜测的口令")
	}
	if passwordCharClasses(pw) < 3 {
		return fmt.Errorf("密码需至少包含大写字母、小写字母、数字、符号中的 3 类")
	}
	// 与当前口令**完全相同**：用现成的哈希直接校验（不落明文，进程重启后依然有效）。
	// 注意 PBKDF2 哈希对大小写敏感，哈希只能判定「逐字符完全相同」；
	// 「只改了大小写」这类弱改动由下面的明文基准 passwordSimilar 覆盖
	// （明文基准缺失时该判定自动跳过，属有意的降级，不误杀正常改密）。
	if cur.Verify != nil && cur.Verify(pw) {
		return fmt.Errorf("新密码不能与当前密码相同")
	}
	// 与当前口令相似：需要明文基准（登录成功时在内存里暂存），没有基准则跳过。
	if cur.Plain != "" && passwordSimilar(pw, cur.Plain) {
		return fmt.Errorf("新密码不能与当前密码相同或过于相似")
	}
	return nil
}

// currentAdminPlain 返回内存中暂存的当前管理口令明文（可能为空）。
//
// 仅在进程内存中保存，**绝不**写入 config.json / keys.json / 日志 / 审计。
func (a *App) currentAdminPlain() string {
	if a == nil {
		return ""
	}
	a.curPwMu.RLock()
	defer a.curPwMu.RUnlock()
	return a.curAdminPlain
}

// setCurrentAdminPlain 暂存/更新当前管理口令明文（登录成功、改密成功时调用）。
func (a *App) setCurrentAdminPlain(pw string) {
	if a == nil {
		return
	}
	a.curPwMu.Lock()
	a.curAdminPlain = pw
	a.curPwMu.Unlock()
}
