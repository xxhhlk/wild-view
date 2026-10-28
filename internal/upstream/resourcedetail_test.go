package upstream

import (
	"testing"
)

// TestExpireAtRejectsMalformed 不可解析的时间串不得被当成有效到期日。
//
// 返回值是 **RFC3339 精确时刻**（旧版返回 "YYYY-MM-DD"）：面板按「N 小时内到期」
// 分桶需要时分秒，否则今晚失效的包会被误报成「明日到期」（2026-10-19 实测）。
// 同时验证：纯日期串 "2026-09-30" 不在可解析列表里（上游不会下发纯日期，
// 混进来说明形状变了，宁可当无到期也不猜）。
func TestExpireAtRejectsMalformed(t *testing.T) {
	cases := map[string]string{
		"":                    "",
		"   ":                 "",
		"2026-09-30":          "",
		"not-a-time":          "",
		"2026-09-30 23:59:59": "2026-09-30T23:59:59+08:00",
		"2026-01-01 00:00:00": "2026-01-01T00:00:00+08:00",
	}
	for in, want := range cases {
		r := resourceAccount{CycleEndTime: in}
		if got := r.expireAt(); got != want {
			t.Errorf("expireAt(%q)=%q want %q", in, got, want)
		}
	}
}

// TestExpireDayIsDateOnlyForLedgerKey 守门：伪键必须只含日期，不含时刻。
//
// 为什么这条必须存在：ExpireAt 从日期改成 RFC3339 后，如果伪键跟着变，
// 升级瞬间每个包会产出一对 expire+earn 假流水（workbuddy 单账号 28 个包
// ⇒ 56 条，把「今日作废」刷成虚高）。expireDay() 就是为这个而存在的。
func TestExpireDayIsDateOnlyForLedgerKey(t *testing.T) {
	r := resourceAccount{CycleEndTime: "2026-09-30 23:59:59"}
	if got := r.expireDay(); got != "2026-09-30" {
		t.Fatalf("expireDay()=%q want 2026-09-30（伪键不得含时刻）", got)
	}
	if got := r.expireAt(); got == r.expireDay() {
		t.Fatalf("expireAt()=%q 与 expireDay() 相同，时刻信息丢了", got)
	}
	// 空值：两者都空，不得造出 "|" 之类的畸形键片段
	r2 := resourceAccount{CycleEndTime: ""}
	if r2.expireDay() != "" || r2.expireAt() != "" {
		t.Fatalf("空 CycleEndTime: expireDay=%q expireAt=%q", r2.expireDay(), r2.expireAt())
	}
}
