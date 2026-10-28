package officeace

import (
	"testing"
)

// 上游把同一自然日拆多条时必须合并（否则按日消耗被重复计入 ledger）。
func TestMergeUsageBuckets(t *testing.T) {
	got := mergeUsageBuckets([]UsageBucket{
		{Day: "2026-10-21", Points: 100, Tokens: 10},
		{Day: "2026-10-20", Points: 1, Tokens: 1},
		{Day: "2026-10-21", Points: 2.5, Tokens: 5},
	})
	want := []UsageBucket{
		{Day: "2026-10-20", Points: 1, Tokens: 1},
		{Day: "2026-10-21", Points: 102.5, Tokens: 15},
	}
	if len(got) != len(want) {
		t.Fatalf("合并后桶数 = %d, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
	if n := len(mergeUsageBuckets(nil)); n != 0 {
		t.Errorf("nil 输入应回空：%d", n)
	}
}

func TestPickUsageDay(t *testing.T) {
	bs := []UsageBucket{
		{Day: "2026-10-20", Points: 926.53, Tokens: 36210499},
		{Day: "2026-10-21", Points: 1220.57, Tokens: 52013762},
	}
	if got := pickUsageDay(bs, "2026-10-21"); got.Points != 1220.57 || got.Tokens != 52013762 {
		t.Errorf("pickUsageDay 命中错桶：%+v", got)
	}
	got := pickUsageDay(bs, "2026-10-22")
	if got.Day != "2026-10-22" || got.Points != 0 || got.Tokens != 0 {
		t.Errorf("缺失日应回零值桶（Day 仍填该日）：%+v", got)
	}
	if got := pickUsageDay(nil, "2026-10-21"); got.Points != 0 || got.Day != "2026-10-21" {
		t.Errorf("空切片应回零值桶：%+v", got)
	}
}
