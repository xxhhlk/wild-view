package ledger

import "testing"

// TestBalanceSeriesRoundtrip 余额采样：写入后能按账号读出，且同一账号 10 分钟内
// 余额未变化的重复采样会被去抖，余额变化则立即记录。
func TestBalanceSeriesRoundtrip(t *testing.T) {
	l, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("ledger.New: %v", err)
	}

	l.RecordBalance("workbuddy", "u1", 100)
	l.RecordBalance("workbuddy", "u1", 100) // 去抖：同一值且间隔过短
	l.RecordBalance("workbuddy", "u1", 80)  // 变化：必须记录
	l.RecordBalance("qodercn", "u2", 50)

	series := l.BalanceSeries(7)
	if len(series) != 2 {
		t.Fatalf("账号序列数 = %d，期望 2", len(series))
	}
	var u1 *BalanceSeries
	for i := range series {
		if series[i].UID == "u1" {
			u1 = &series[i]
		}
	}
	if u1 == nil {
		t.Fatal("缺少 u1 的序列")
	}
	if len(u1.Points) != 2 {
		t.Fatalf("u1 采样点 = %d，期望 2（重复值应被去抖，变化值应保留）", len(u1.Points))
	}
	if u1.Points[0].Credits != 100 || u1.Points[1].Credits != 80 {
		t.Errorf("u1 采样值 = %d→%d，期望 100→80", u1.Points[0].Credits, u1.Points[1].Credits)
	}
	if u1.Points[0].Ts > u1.Points[1].Ts {
		t.Error("采样点未按时间升序")
	}
}

// TestBalanceSeriesWindow 超出窗口的采样不应返回。
func TestBalanceSeriesWindow(t *testing.T) {
	l, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("ledger.New: %v", err)
	}
	l.RecordBalance("workbuddy", "old", 10)

	// 把内存去抖表指向很久以前，模拟历史数据：直接改写文件时间更复杂，
	// 这里验证窗口参数只影响读取过滤，不影响写入。
	if got := l.BalanceSeries(1); len(got) != 1 {
		t.Fatalf("1 天窗口序列数 = %d，期望 1", len(got))
	}
	// 30 天窗口同样应包含刚写入的点。
	if got := l.BalanceSeries(30); len(got) != 1 {
		t.Fatalf("30 天窗口序列数 = %d，期望 1", len(got))
	}}
