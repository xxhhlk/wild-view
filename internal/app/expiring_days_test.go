package app

// expiring_days_test.go 锁死临期展示窗口只接受 1/2/7（与上游 R112b 同口径）。

import (
	"testing"

	"wild-view/internal/provider"
)

func TestSetExpiringDaysAccepts127(t *testing.T) {
	a, _ := newTestApp(t, provider.WorkBuddy, &fakeUpstream{remain: 1}, "uid-1")

	for _, d := range []int{1, 2, 7} {
		if err := a.SetExpiringDays(d); err != nil {
			t.Fatalf("SetExpiringDays(%d) 应接受：%v", d, err)
		}
		if got := a.cfg.ExpiringThresholdDays(); got != d {
			t.Fatalf("存盘后 ExpiringThresholdDays = %d，期望 %d", got, d)
		}
	}

	// 3 天是改造前的旧档位，现已下线；0/8/负数同样拒绝。
	for _, d := range []int{0, 3, 8, -1} {
		if err := a.SetExpiringDays(d); err == nil {
			t.Fatalf("SetExpiringDays(%d) 应被拒绝", d)
		}
	}
}
