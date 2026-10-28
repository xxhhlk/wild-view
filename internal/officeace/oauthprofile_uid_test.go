package officeace

import "testing"

// 本文件锁死 uid → 桌面端档案文件名的编码契约（2026-10-17 独立复核补充）。
//
// 真实 uid 是 `<hex>:<hex>`，但编码函数必须在**已 PATH 转义的串**上做 ':' 替换：
// 若退回原串替换，会丢掉 PathEscape 对 '/'、'%'、空格等字符的转义，形状一旦变化
// 就会算出与桌面端不同的文件名 ⇒ 档案永远 miss（与 R-2 同一族，只是触发条件更隐蔽）。

// TestOAuthProfileNameCandidatesUsesEscapedForm 候选名第 1 位必须是 %3A 形态（编码优先）。
func TestOAuthProfileNameCandidatesUsesEscapedForm(t *testing.T) {
	uid := "01a1:01a2"
	got := oauthProfileNameCandidates(uid)
	if len(got) < 2 {
		t.Fatalf("候选名应同时含编码与解码两种形态，实际 %v", got)
	}
	if got[0] != "oauth-01a1%3A01a2.json" {
		t.Errorf("候选第 1 位应为编码形态，实际 %q", got[0])
	}
	if got[1] != "oauth-01a1:01a2.json" {
		t.Errorf("候选第 2 位应为解码形态，实际 %q", got[1])
	}
}
