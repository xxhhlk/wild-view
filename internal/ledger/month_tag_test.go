package ledger

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 月末的月份运算必须走 monthTagShift。
//
// 直接 t.AddDate(0,-N,0) 在 29/30/31 日会溢出：10 月 31 日减 1 个月得到「9 月 31 日」，
// Go 规整成 10 月 1 日 —— 上月标签等于本月标签，同一份文件被读两遍。

func TestMonthTagShiftAtMonthEnd(t *testing.T) {
	cases := []struct {
		date string
		back int
		want string
	}{
		{"2026-10-31", 1, "202609"}, // 溢出高发日：曲线把 balance-202610 读两遍
		{"2026-03-31", 1, "202602"},
		{"2026-03-29", 1, "202602"},
		{"2026-05-31", 1, "202604"},
		{"2026-12-31", 1, "202611"},
		{"2026-01-31", 1, "202512"},
		{"2026-10-31", 6, "202604"}, // 保留期 cutoff：直接 AddDate 会得到 202605（多删一个月）
		{"2026-04-15", 1, "202603"},
	}
	for _, c := range cases {
		ts, err := time.ParseInLocation("2006-01-02", c.date, time.Local)
		if err != nil {
			t.Fatalf("用例日期非法 %s: %v", c.date, err)
		}
		if got := monthTagShift(ts, c.back); got != c.want {
			t.Errorf("monthTagShift(%s, %d) = %s，期望 %s", c.date, c.back, got, c.want)
		}
	}
}

func TestBalanceSeriesReadsMonthFileOnce(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	now := time.Now()
	// 直接落文件（绕开 RecordBalance 的 10 分钟去抖）：一条采样只允许读出一个点。
	line, err := json.Marshal(BalancePoint{Ts: now.Add(-time.Minute).Unix(), Channel: "workbuddy", UID: "u9", Credits: 42})
	if err != nil {
		t.Fatal(err)
	}
	fp := filepath.Join(dir, fmt.Sprintf("balance-%s.jsonl", now.Format("200601")))
	if err := os.WriteFile(fp, append(line, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	series := l.BalanceSeries(7)
	if len(series) != 1 {
		t.Fatalf("账号序列数 = %d，期望 1", len(series))
	}
	if len(series[0].Points) != 1 {
		t.Fatalf("采样点 = %d，期望 1（月文件被重复读取 = 月末标签溢出）", len(series[0].Points))
	}
}
