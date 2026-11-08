package scheduler

// expiring_window_test.go 锁死「签到路径的临期窗口跟随面板配置」。
//
// 背景：签到成功后会重算 expiring 并写回 pool（ReenableIfCredits），若这里硬编码
// provider.ExpiringDays=1，用户在面板选了 7 天，10:04 那次签到又会把它改回 1 天，
// 面板数字整天来回跳。

import (
	"testing"
	"time"

	"wild-view/internal/provider"
)

func TestExpiringDaysFollowsConfig(t *testing.T) {
	s := New(Config{})
	if got := s.expiringDays(); got != provider.ExpiringDays {
		t.Fatalf("未配置时应取默认 %d，得到 %d", provider.ExpiringDays, got)
	}

	s.SetExpiringDays(7)
	if got := s.expiringDays(); got != 7 {
		t.Fatalf("SetExpiringDays(7) 后 = %d，期望 7", got)
	}

	// 非法/零值回落到默认，不得把 0 天当成「窗口为 0」用。
	s.SetExpiringDays(0)
	if got := s.expiringDays(); got != provider.ExpiringDays {
		t.Fatalf("SetExpiringDays(0) 应回落默认 %d，得到 %d", provider.ExpiringDays, got)
	}
}

// TestExpiringWindowAffectsCount 同一批 items，窗口越大统计到的临期积分越多。
// 日期按 UTC+8 生成：provider.ExpireWithinDays 是固定 UTC+8 墙钟口径，
// 用本地时区会在 UTC runner 上差一天。
func TestExpiringWindowAffectsCount(t *testing.T) {
	utc8 := time.FixedZone("UTC+8", 8*3600)
	now := time.Now().In(utc8)
	items := []provider.ResourceItem{
		{Name: "tomorrow", Remain: 100, Usable: true, ExpireAt: now.AddDate(0, 0, 1).Format("2006-01-02")},
		{Name: "in6d", Remain: 50, Usable: true, ExpireAt: now.AddDate(0, 0, 6).Format("2006-01-02")},
	}
	if got := provider.ExpiringWithinDays(items, 1, now); got != 0 {
		t.Fatalf("1 天窗口不应包含明天到期的 100，得到 %d", got)
	}
	if got := provider.ExpiringWithinDays(items, 7, now); got != 150 {
		t.Fatalf("7 天窗口应包含两笔共 150，得到 %d", got)
	}
}
