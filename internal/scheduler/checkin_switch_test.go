package scheduler

// checkin_switch_test.go 锁死「无签到渠道不被面板时间污染」与「总开关运行中生效」。
//
// 这两条都踩过：2026-11-03 日志里 qwenwork / workbuddyai 在 21:04 被自动签到打了，
// 报「无签到活动」「签到依赖对话」，还被标成 retryable 反复重试——
// 原因是面板改签到时间时 SetCheckinMinutes 把全局时刻灌进了所有调度器。

import "testing"

// TestNoScheduledCheckinIgnoresGlobalTimes 无签到渠道必须无视面板推来的全局时间。
func TestNoScheduledCheckinIgnoresGlobalTimes(t *testing.T) {
	s := New(Config{CheckinMinutes: []int{}, NoScheduledCheckin: true})
	s.SetCheckinMinutes([]int{21*60 + 4}) // 面板：08:12/12:33/18:48/21:04
	if got := s.CheckinMinutes(); len(got) != 0 {
		t.Fatalf("无签到渠道被灌入了签到时刻 %v —— 会在该时刻空跑必然失败的签到", got)
	}
	// 即便调用方误传了时刻，New 也要清空。
	s2 := New(Config{CheckinMinutes: []int{600}, NoScheduledCheckin: true})
	if got := s2.CheckinMinutes(); len(got) != 0 {
		t.Fatalf("NoScheduledCheckin 应强制清空，得到 %v", got)
	}
}

// TestSetCheckinEnabledRuntime 总开关必须落到运行中的调度器上（不能只改配置文件）。
func TestSetCheckinEnabledRuntime(t *testing.T) {
	s := New(Config{CheckinMinutes: []int{10 * 60}, CheckinRetryUntil: 12 * 60})

	s.SetCheckinEnabled(false)
	if got := s.CheckinMinutes(); len(got) != 0 {
		t.Fatalf("关闭后仍有签到时刻 %v —— 进程会继续打上游", got)
	}

	s.SetCheckinEnabled(true)
	got := s.CheckinMinutes()
	if len(got) != 1 || got[0] != 10*60 {
		t.Fatalf("重新打开应恢复本渠道自己的时刻 [600]，得到 %v", got)
	}
}

// TestSetCheckinMinutesWhileDisabled 关闭期间改时间：只更新快照，重新打开才生效。
func TestSetCheckinMinutesWhileDisabled(t *testing.T) {
	s := New(Config{CheckinMinutes: []int{9 * 60}})
	s.SetCheckinEnabled(false)
	s.SetCheckinMinutes([]int{20 * 60})
	if got := s.CheckinMinutes(); len(got) != 0 {
		t.Fatalf("关闭期间不得激活新时刻，得到 %v", got)
	}
	s.SetCheckinEnabled(true)
	got := s.CheckinMinutes()
	if len(got) != 1 || got[0] != 20*60 {
		t.Fatalf("重新打开应使用关闭期间保存的新时刻 [1200]，得到 %v", got)
	}
}
