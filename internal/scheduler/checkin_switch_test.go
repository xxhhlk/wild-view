package scheduler

// checkin_switch_test.go 锁死「无签到渠道不被面板时间污染」与「总开关运行中生效」。
//
// 这两条都踩过：2026-11-03 日志里 qwenwork / workbuddyai 在 21:04 被自动签到打了，
// 报「无签到活动」「签到依赖对话」，还被标成 retryable 反复重试——
// 原因是面板改签到时间时 SetCheckinMinutes 把全局时刻灌进了所有调度器。

import "testing"

// TestFollowsGlobalCheckinFlag 白名单标记要如实反映到访问器上
// （App.SetCheckinTimes 只向它为 true 的调度器推送新时刻）。
func TestFollowsGlobalCheckinFlag(t *testing.T) {
	on := New(Config{CheckinMinutes: []int{9 * 60}, FollowGlobalCheckin: true})
	if !on.FollowsGlobalCheckin() {
		t.Error("FollowGlobalCheckin=true 的调度器应报告跟随全局")
	}
	off := New(Config{CheckinMinutes: []int{}, FollowGlobalCheckin: false})
	if off.FollowsGlobalCheckin() {
		t.Error("未声明跟随全局的调度器不得被推送（Qoder 固定窗口 / 无签到渠道）")
	}
}

// TestNoCheckinTimesSkipsRecording 没有签到时刻的渠道不落盘、不上报
// ——即使被外部直接调 RunCheckinNow（否则面板会被必然失败的桩染红）。
func TestNoCheckinTimesSkipsRecording(t *testing.T) {
	var got []CheckinResult
	s := New(Config{CheckinMinutes: []int{}})
	s.SetCheckinObserver(func(r CheckinResult) { got = append(got, r) })
	// 直接走收尾函数：模拟「本渠道没有签到任务，却有人触发了签到」。
	s.finishCheckin("uid-1", CheckinResult{UID: "uid-1", Msg: "无签到活动"})
	if len(got) != 0 {
		t.Fatalf("无签到时刻的渠道不应上报观察者，得到 %+v", got)
	}
}

// TestNoCheckinTimesSkipOnManualRun 没有签到时刻的渠道即使被直接触发也不落盘、不上报。
// （白名单在 App.SetCheckinTimes 一侧，见 internal/app 的广播用例。）

// TestSetCheckinEnabledRuntime 总开关必须落到运行中的调度器上（不能只改配置文件）。
func TestSetCheckinEnabledRuntime(t *testing.T) {
	s := New(Config{CheckinMinutes: []int{10 * 60}, CheckinRetryUntil: 12 * 60, FollowGlobalCheckin: true})

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
	s := New(Config{CheckinMinutes: []int{9 * 60}, FollowGlobalCheckin: true})
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
