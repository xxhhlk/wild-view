// reasoning_test.go 思考档位透传测试（issue #96）。
//
// 原则与能力字段同款：只透传上游目录真实返回的档位，不自造数据。
// 形状依据：ref/Buddy2api v2.1.11（reasoning.supportedEfforts/defaultEffort/
// canDisableThinking；deepseek 系只有老式 effort 默认档）。
package upstream

import (
	"testing"

	"wild-view/internal/provider"
)

// TestApplyCatalogReasoningEmpty 空对象/缺对象不留痕迹。
func TestApplyCatalogReasoningEmpty(t *testing.T) {
	if eff, def, cd := applyCatalogReasoning(&catalogModel{ID: "x"}); eff != nil || def != "" || cd != nil {
		t.Fatalf("无 reasoning 对象应有 nil 痕迹: %v %q %v", eff, def, cd)
	}
	if eff, def, cd := applyCatalogReasoning(&catalogModel{ID: "x", Reasoning: modelReasoningMeta{}}); eff != nil || def != "" || cd != nil {
		t.Fatalf("空 reasoning 对象应整体视为未声明: %v %q %v", eff, def, cd)
	}
}

// TestCleanEfforts 规范化：去空白/去重/去空串/全无效归 nil。
func TestCleanEfforts(t *testing.T) {
	cases := []struct {
		in   []string
		want []string
	}{
		{[]string{"low", "high", "max", "max", ""}, []string{"low", "high", "max"}},
		{[]string{"  low  ", "low"}, []string{"low"}},
		{[]string{"", "   "}, nil},
		{nil, nil},
	}
	for _, c := range cases {
		got := provider.CleanEfforts(c.in)
		if len(got) != len(c.want) {
			t.Fatalf("CleanEfforts(%q) = %v, want %v", c.in, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("CleanEfforts(%q) = %v, want %v", c.in, got, c.want)
			}
		}
	}
}
