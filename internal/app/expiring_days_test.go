package app

import (
	"testing"

	"wild-view/internal/provider"
)

func TestSetExpiringDaysAcceptsCustom(t *testing.T) {
	a, _ := newTestApp(t, provider.WorkBuddy, &fakeUpstream{remain: 1}, "uid-1")

	for _, d := range []int{1, 2, 3, 7, 30, 365} {
		if err := a.SetExpiringDays(d); err != nil {
			t.Fatalf("SetExpiringDays(%d) 应接受：%v", d, err)
		}
		if got := a.cfg.ExpiringThresholdDays(); got != d {
			t.Fatalf("存盘后 ExpiringThresholdDays = %d，期望 %d", got, d)
		}
	}

	for _, d := range []int{0, -1, 366, 1000} {
		if err := a.SetExpiringDays(d); err == nil {
			t.Fatalf("SetExpiringDays(%d) 应被拒绝", d)
		}
	}
}
