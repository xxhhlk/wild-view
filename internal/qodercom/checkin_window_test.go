// checkin_window_test.go QoderCOM 签到「活动有效窗口」回归测试。
//
// 为什么要单独测窗口：本仓曾把 qodercom 判成 no_campaign（「以为上游没发活动」）。2026-10-18
// 的单发只读探测（3 个本机 qodercom 账号各 1 次 GET /sash/api/v1/me/campaigns，无重试、
// 无领取）证明本工具**能**拿到 CLAIM_BENEFIT 活动，并顺带暴露了每条活动的有效窗口字段。
// 证据（原样摘自 account 0 的回包，仅去掉无关的 placements 文案）：
//
//	"campaignKey":"act-20261013-119","actionType":"CLAIM_BENEFIT",
//	"startAt":1792202400,"endAt":1792288740,"claimStatus":"CLAIMED"
//
// endAt=1792288740 = 2026-10-18 09:59 +08（= 01:59 UTC），startAt=1792202400 = 2026-10-17 10:00 +08，
// 探测发生在 2026-10-18 01:10 +08 —— 该活动**当时确实仍在有效窗口内**（离 endAt 还有约 8.8 小时），
// 所以「窗口」这条在 10-18 的探测里只是**结构证据**（字段确实存在、取值语义清楚），
// 而桌面端 0.4.8 日志给出了同一条活动的**状态随窗口迁移**：
//
//	07:47:46 claimable=true  → 列表里那条 CLAIM_BENEFIT 的 claimStatus 是 CLAIMABLE；
//	08:47:45 claimable=true  → 同上；
//	15:52:42 claimable=true  → 仍是 CLAIMABLE；14 秒后 forceRefresh 才变 CLAIMED。
//
// 结论（分层标注）：startAt/endAt 是**实测**字段（探测回包里每条活动都带）；
// 「过期 CLAIM_BENEFIT 条目会留在列表里」这一形态本身**尚未直接观察到**，按**推断**对待。
// 正因是推断，本文件测的是**不变量**而不是某次回包：
//
//  1. 窗口已结束的 CLAIMED 活动**绝不能**把当天判成「今日已领取」——否则窗口内不再重试，
//     等于当天漏领（与 AGENTS.md 不变式 23「假成功」同类）。
//  2. 窗口未开始的 CLAIMABLE 活动**绝不能**发 claim（否则把「还没到点」记成一条假失败）。
//  3. 上游不下发 startAt/endAt（0 值）时行为与改动前一致（向后兼容）。
package qodercom

import (
	"time"
)

// windowsAroundNow 返回与「现在」相关的三个时刻（Unix 秒），供夹具构造。
// 用真实的 time.Now 而不是固定时间戳：tryCampaigns 内部取 time.Now，
// 固定值会让测试在几年后自己过期。
func windowsAroundNow() (ended, active, pending int64) {
	now := time.Now().Unix()
	return now - 24*3600, now - 60, now + 24*3600
}

// and 让夹具里的时间戳保持十进制，不引入 fmt（本文件只做字符串拼接）。
func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
