// sse.go 额度重置时刻的 UTC+8 口径（额度查询与面板展示共用）。
//
// 为什么在本地推算：服务端报文**不给重置时刻**（`InferHub.4291.200` 的 details 只有
// requestId/timestamps/modelId/traceId），参考实现同样按「UTC+8 次日 00:00」推算并
// 明确写「预计」——本文件与 BenefitResetHint 的措辞保持同一口径，不当成上游声明。
package codearts

import "time"

// benefitLoc benefit 免费额度的墙钟口径：**UTC+8 自然日**（协议 §2.6(a)）。
var benefitLoc = time.FixedZone("UTC+8", 8*60*60)

// BenefitResetAt 返回 now 之后的下一个 UTC+8 自然日零点。
func BenefitResetAt(now time.Time) time.Time {
	t := now.In(benefitLoc)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, benefitLoc).AddDate(0, 0, 1)
}

// BenefitResetHint 生成人类可读的额度重置说明（含「预计」二字，见上）。
func BenefitResetHint(now time.Time) string {
	return "免费额度按 UTC+8 自然日重置，预计 " + BenefitResetAt(now).Format("2006-01-02 15:04") + "（UTC+8）恢复"
}

// BenefitDay 返回 UTC+8 自然日标识（YYYY-MM-DD）——额度是按日重置，不是滚动窗口。
func BenefitDay(now time.Time) string { return now.In(benefitLoc).Format("2006-01-02") }
