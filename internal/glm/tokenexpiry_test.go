package glm

// 本文件锁定一个真实 bug（2026-09-26 从运行日志发现）：
//
//	上游 refresh **不返回 expires_in** → 早期实现把 expiresAt 落成 0
//	→ NeedsRefresh 恒为真 → **每次请求都刷 token**
//	（日志实测：同一账号 9 秒内刷两次；危害是与 App 端高频互踩、可能触发风控）
//
// 修复：expires_in 缺失时用 access_token 的 JWT exp 兜底。
