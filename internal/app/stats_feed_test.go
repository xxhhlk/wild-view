package app

import (
	"strings"
	"testing"
	"time"

	"wild-work/internal/ledger"
	"wild-work/internal/provider"
)

// TestExpiryDayAmount 临期喂入口径：可用条目按日归组，取最早到期日 + 当日合计。
// TestExpiryItems 守门：喂给引擎的必须是**逐条目 + 原始时刻**。
//
// 历史（2026-10-19 重构）：旧函数 expiryDayAmount 只返回「最早到期日 + 该日合计」，
// 导致 ① 面板只能按日期分桶（今晚 00:00 失效被算成「明日到期」）；
// ② 「7 日内到期」只统计最早那一天，漏掉同账号后续 20+ 个每日包。
// 新函数 expiryItems 保留每条原始 ExpireAt，分桶交给 provider.ExpireBucketOf。
func TestExpiryItems(t *testing.T) {
	items := []provider.ResourceItem{
		{Remain: 100, ExpireAt: "2026-10-08T00:00:00+08:00", Usable: true},
		{Remain: 50, ExpireAt: "2026-10-08T00:00:00+08:00", Usable: true},
		{Remain: 30, ExpireAt: "2026-10-05T00:00:00+08:00", Usable: true},
		{Remain: 999, ExpireAt: "2026-10-09T00:00:00+08:00", Usable: false},               // 不可用：不计
		{Remain: 0, ExpireAt: "2026-10-01T00:00:00+08:00", Usable: true},                  // 零额度：不计
		{Remain: 88, ExpireAt: "", Usable: true},                                          // 无到期日：不计
		{Remain: 77, ExpireAt: "2026-10-10T00:00:00+08:00", Usable: true, InfoOnly: true}, // 仅展示：不计
	}
	got := expiryItems(items)
	if len(got) != 3 {
		t.Fatalf("应保留 3 条（可消耗+有剩余+带到期），实际 %d: %+v", len(got), got)
	}
	// 时刻必须原样保留（不得截成日期）：这是「按真实时分秒分桶」的前提
	for _, e := range got {
		if len(e.At) < 20 || !strings.Contains(e.At, "T") {
			t.Fatalf("ExpireAt 被截断了：%q", e.At)
		}
	}
	// 同日多条各自保留（不合并）——合并会让「逐条目累加」退化回「每账号最早日」
	sameDay := 0
	for _, e := range got {
		if strings.HasPrefix(e.At, "2026-10-08") {
			sameDay++
		}
	}
	if sameDay != 2 {
		t.Fatalf("同日 2 条应各自保留，实际 %d", sameDay)
	}
	if got := expiryItems(nil); len(got) != 0 {
		t.Fatalf("空条目应返回空切片，实际 %+v", got)
	}
}

// TestStatsAccountsMapping pool 状态 → 引擎账号视图：分组/余额/临期/非匿名标记。
func TestStatsAccountsMapping(t *testing.T) {
	a := newPanelApp(t, "127.0.0.1", "")
	rt := a.runtime(provider.WorkBuddy)
	rt.Pool.SetCreditDetail("wb-1", 2050, 100, 0, time.Time{})
	accts := a.statsAccounts()
	if len(accts) != 1 {
		t.Fatalf("应映射 1 个账号，实际 %d", len(accts))
	}
	got := accts[0]
	if got.UID != "wb-1" || got.Group != "workbuddy" {
		t.Fatalf("UID/Group 错误：%s %s", got.UID, got.Group)
	}
	if got.Credits != 2050 || got.Expiring != 100 {
		t.Fatalf("Credits/Expiring 错误：%d %d", got.Credits, got.Expiring)
	}
	if got.CreditsNA {
		t.Fatal("workbuddy 非匿名渠道，不应标 CreditsNA")
	}
}

// TestFeedLedgerExpire 今日作废喂入：ledger 当日 expire 条目按渠道求和进引擎。
func TestFeedLedgerExpire(t *testing.T) {
	a := newPanelApp(t, "127.0.0.1", "")
	a.ledger.AppendCredit(ledger.CreditEntry{Ch: "workbuddy", UID: "wb-1", Kind: "expire", Amount: -100})
	a.ledger.AppendCredit(ledger.CreditEntry{Ch: "workbuddy", UID: "wb-1", Kind: "spend", Amount: -30})
	now := time.Now()
	a.feedLedgerExpire(now)
	snap := a.stats.Snapshot(now)
	if snap.CreditExpired != 100 {
		t.Fatalf("今日作废应 100（spend 不计），实际 %d", snap.CreditExpired)
	}
}

// TestStatsEndpoints 端点挂载与守卫：/api/stats 与 /api/stats/logs 必须过会话守卫（R4），
// 登录后回快照 JSON / rows 形状；limit 缺省与非法值兜底。
func TestLogWriterObserveEvents(t *testing.T) {
	a := newPanelApp(t, "127.0.0.1", "")
	lw := &logWriter{app: a}
	line := time.Now().Format("2006/01/02") + " 12:00:00.000 checkin platform=workbuddy uid=wb-1 ok=true msg=签到成功 remain=100 has_remain=true"
	if _, err := lw.Write([]byte(line)); err != nil {
		t.Fatalf("logWriter.Write: %v", err)
	}
	snap := a.stats.Snapshot(time.Now())
	if snap.CreditIn != 100 {
		t.Fatalf("事件口径收入应 100，实际 %d", snap.CreditIn)
	}
}
