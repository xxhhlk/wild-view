package workbuddyai

import (
	"strings"
	"testing"
)

// TestClassify 错误分类（实测码）。
func TestClassify(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   string
	}{
		{402, "", "hard_credit"},
		{200, "积分不足", "hard_credit"},
		{200, "Offline user session not found", "session_dead"},
		{200, "code=12153", "session_dead"},
		{429, "too many requests", "soft_rate"},
		// issue #53：按**模型**限流（6004 / "switch to the other models"）仍归 soft_rate，
		// 由 handler 按 provider.IsModelScopedSoftRate 分流成 (账号, 模型) 粒度冷却，
		// 账号上其它模型照常路由（见 R47）。
		{429, "usage exceeds frequency limit ... switch to the other models to continue", "soft_rate"},
		{429, "our gateway is closed currently", "soft_rate"},
		// 14018 积分耗尽（2026-09-23 生产实录原文 body）：账号级余额问题归 hard_credit
		// ——误判软限流只冷 60s，降级链永不触发，客户端死循环吃 429。
		{429, `{"error":{"data":{"code":14018,"msg":"Credits exhausted. Please visit the link below to purchase add-on packs and get more credits: https://www.codebuddy.ai/profile/usage ","requestId":"9044201e"}}`, "hard_credit"},
		// 匹配防前缀误伤（S2）：code 140180 含子串 "code":14018，须保持软限流；
		// 尾形（code 后紧跟 }，无 msg）是真 14018，须命中。
		{429, `{"error":{"data":{"code":140180,"msg":"future code"}}}`, "soft_rate"},
		{429, `{"error":{"data":{"code":14018}}}`, "hard_credit"},
		// 14003 过频（构造 body，未实证）：保持软限流，不回归。
		{429, `{"code":14003,"msg":"too many requests"}`, "soft_rate"},
		// 429 + quota exceeded（非 14018）：429 优先于 hardRule 的既有语义保持——
		// 先判 hard 会把 6004 类模型级限流误归 12h 账号冷却。
		{429, `{"error":{"msg":"quota exceeded"}}`, "soft_rate"},
		{404, "", "not_found"},
		// 会话头族缺失（11128 conversation id missing）= 上游按账号灰度的渠道校验，
		// 归账号级（软冷却 + 换号，见 R116）；同码的 unapproved channel 是请求级内容
		// 指纹问题，不罚号。
		{400, `{"code":11128,"msg":"conversation id missing","requestId":"x"}`, "account_fault"},
		{400, `{"code":11128,"msg":"Illegal API invocation from an unapproved channel"}`, "content_blocked"},
		{500, "", "server"},
		{400, "code=11102", "client"},
		{200, "ok", "none"},
	}
	for _, c := range cases {
		if got := Classify(c.status, c.body).String(); got != c.want {
			t.Errorf("Classify(%d,%q) = %s, want %s", c.status, c.body, got, c.want)
		}
	}
}

// TestParseCredits 倍率解析：目录 credits 字段的多种写法。
func TestParseCredits(t *testing.T) {
	cases := map[string]float64{
		"x0.79 credits": 0.79,
		"x3.47":         3.47,
		"x0.00":         0,
		"0x":            0,
		"":              0,
	}
	for in, want := range cases {
		if got := parseCredits(in); got != want {
			t.Errorf("parseCredits(%q) = %v, want %v", in, got, want)
		}
	}
}

// TestFetchModelsExcludesBrokenAndAddsExtra 目录结果应剔除不可用模型并补入目录外可用模型。
func TestFetchModelsExcludesBrokenAndAddsExtra(t *testing.T) {
	seen := map[string]bool{}
	// 模拟 FetchModels 的合并逻辑（不发起网络请求）
	raws := []catalogModel{
		{ID: "hy3", Name: "Hy3"},
		{ID: "deepseek-v4-pro", Name: "DS Pro"},     // broken，应剔除
		{ID: "deepseek-v4-flash", Name: "DS Flash"}, // broken，应剔除
		{ID: "glm-5.2", Name: "GLM"},
	}
	out := make([]string, 0)
	for _, m := range raws {
		if m.ID == "" || m.Disabled || brokenModels[m.ID] || seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		out = append(out, m.ID)
	}
	for _, m := range extraModels {
		if seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		out = append(out, m.ID)
	}
	joined := strings.Join(out, ",")
	if strings.Contains(joined, "deepseek-v4-pro") || strings.Contains(joined, "deepseek-v4-flash") {
		t.Fatalf("broken models must be excluded: %s", joined)
	}
	if !strings.Contains(joined, "deepseek-v4.1-flash") {
		t.Fatalf("deepseek-v4.1-flash must be added: %s", joined)
	}
	if !strings.Contains(joined, "hy4-preview-f") {
		t.Fatalf("hy4-preview-f must be added: %s", joined)
	}
}

// TestStaticModelsSane 静态表不含不可用模型且包含免费模型。
func TestStaticModelsSane(t *testing.T) {
	ids := map[string]bool{}
	for _, m := range StaticModels() {
		ids[m.ID] = true
		if brokenModels[m.ID] {
			t.Errorf("static table contains broken model %s", m.ID)
		}
	}
	for _, want := range []string{"hy3", "hy4-preview", "deepseek-v4.1-flash", "hy4-preview-f"} {
		if !ids[want] {
			t.Errorf("static table missing %s", want)
		}
	}
}
