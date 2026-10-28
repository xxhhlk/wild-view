package qwenwork

import "strings"

// truncate 字符串截断（rune 安全），用于错误信息与日志摘要。
func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return string(rs[:n])
}
