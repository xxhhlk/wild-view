package modelscope

// truncateText 截断过长文本（错误消息用）。按字节截断足够：调用方只用于日志与
// 错误文案，无需 rune 边界安全。
func truncateText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
