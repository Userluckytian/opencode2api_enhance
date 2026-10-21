package main

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// errInvalidDate 表示客户端传入的 date（或类似的文件名片段）不合法。
// 处理入口统一返回 400，而不是继续拼接文件路径。
var errInvalidDate = errors.New("invalid date")

// datePattern 是日志文件名中允许出现的字符白名单：
// 数字、字母、下划线、点、短横线，最多 32 字符（足够 YYYY-MM-DD，也兼容日期区间等扩展写法）。
var datePattern = regexp.MustCompile(`^[0-9A-Za-z_.-]{0,32}$`)

// sanitizeDate 校验并归一化 date 参数。
//
// 历史问题（finding 543）：/api/admin/logs、/api/admin/ask/usage、/api/admin/audit
// 把 date 直接拼进 "log-"+date+".jsonl"，攻击者用 "../" 即可越出 logs 目录。
// 这里采用「白名单字符集 + 显式拒绝路径分隔符/上跳段」双重校验：
//   - 任何 / \ 字符直接拒绝（含 Windows 下的 \ 与 URL 解码后的形式）；
//   - 任何 ".." 直接拒绝（"." "..." 等只含点的值同样拒绝）；
//   - filepath.Base(s) != s 说明仍可被解释成路径；
//   - 只允许白名单字符集，杜绝 ":""、"%"、"*" 等 Windows 特殊字符与 NUL。
func sanitizeDate(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	if !datePattern.MatchString(s) {
		return "", errInvalidDate
	}
	if strings.Contains(s, "..") || strings.ContainsAny(s, `/\`) {
		return "", errInvalidDate
	}
	if filepath.Base(s) != s || filepath.IsAbs(s) || filepath.VolumeName(s) != "" {
		return "", errInvalidDate
	}
	return s, nil
}

// joinWithin 把 name 安全地拼进 dir，并保证最终路径（含符号链接解析后）仍落在 dir 内。
// name 必须是单一文件名片段（不能包含路径分隔符或上跳段）。
func joinWithin(dir, name string) (string, error) {
	if name == "" || filepath.Base(name) != name || strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		return "", errInvalidDate
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	final := filepath.Join(root, name)
	if !pathWithin(root, final) {
		return "", errInvalidDate
	}
	// 解析符号链接：即使某天 logs 目录被替换成指向外部的软链接也不放行。
	resolvedRoot := root
	if r, err := filepath.EvalSymlinks(root); err == nil {
		resolvedRoot = r
	}
	resolved := final
	if r, err := filepath.EvalSymlinks(final); err == nil {
		resolved = r
	} else {
		// 文件可能尚未创建：退化为校验父目录（父目录不存在时再退一层）。
		if rp, perr := filepath.EvalSymlinks(filepath.Dir(final)); perr == nil {
			resolved = filepath.Join(rp, filepath.Base(final))
		}
	}
	if !pathWithin(resolvedRoot, resolved) {
		return "", errInvalidDate
	}
	return final, nil
}

// pathWithin 判断 p 是否位于 root 之下（或就是 root）。
func pathWithin(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}
