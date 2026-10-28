// wb_utils.go WorkBuddy 国际版渠道的通用纯函数：请求体改写、SSE/聚合、文案与倍率。
// Stream/Aggregate 委托 internal/upstream（共用已修复的实现，避免重复代码）。
package workbuddyai

import (
	"fmt"

	"strings"
)

// ---------------------------------------------------------------------------
// 请求体改写
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// SSE 透传 / 聚合（委托 internal/upstream）
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// 文案与倍率
// ---------------------------------------------------------------------------

// truncate 截断错误文案（保留可读头部，补 n<=0 守卫防 panic）。
func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// parseCredits 解析目录 credits 字段为倍率数值。
func parseCredits(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	s = strings.TrimPrefix(s, "x")
	s = strings.TrimSuffix(s, "x")
	s = strings.TrimSuffix(s, " credits")
	s = strings.TrimSpace(s)
	var v float64
	fmt.Sscanf(s, "%f", &v)
	return v
}
