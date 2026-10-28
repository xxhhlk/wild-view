package cline

import (
	"testing"
	"time"

	"wild-view/internal/auth"
	"wild-view/internal/provider"
)

// ---------------------------------------------------------------------------
// 真实抓包形态（2026-10-10，经官方 sk_ Key 直测）
// ---------------------------------------------------------------------------

// dailyMsg / tempMsg 是抓包到的上游原文（勿改措辞，锁的就是上游实际写法）。
const dailyMsg = "Failed to create stream: inference request failed: failed to generate stream " +
	"from OpenRouter: failed to invoke model 'thinkingmachines/inkling-small:free' with streaming: " +
	`request failed with status 429: {"error":{"message":"Rate limit exceeded: ` +
	`limit_rpd/thinkingmachines/inkling-small-20260730/e27a9a6d. Daily limit reached for ` +
	`thinkingmachines/inkling-small:free via Thinking Machines. Credits don't affect this cap.",` +
	`"code":429}}`

const tempMsg = "Failed to create stream: inference request failed: failed to generate stream " +
	"from OpenRouter: failed to invoke model 'google/gemma-4-26b-a4b-it:free' with streaming: " +
	`request failed with status 429: {"error":{"message":"Provider returned error","code":429,` +
	`"metadata":{"raw":"google/gemma-4-26b-a4b-it:free is temporarily rate-limited upstream. ` +
	`Please retry shortly."}}}`

// nsBody429 是**非流式**真实形态：外层被压平成 HTTP 500，内层写着 status 429。
const nsBody429 = `{"error":"inference request failed: failed to invoke model ` +
	`'google/gemma-4-31b-it:free' from Openrouter: request failed with status 429: ` +
	`{\"error\":{\"message\":\"Provider returned error\",\"code\":429,` +
	`\"metadata\":{\"raw\":\"google/gemma-4-31b-it:free is temporarily rate-limited upstream. ` +
	`Please retry shortly, or add your own key to accumulate your rate limits\",` +
	`\"provider_name\":\"Google AI Studio\"}}}"}`

// ---------------------------------------------------------------------------
// Classify：顺序敏感 + 必须从压平的 500 里挖内层状态码
// ---------------------------------------------------------------------------

// TestRateLimitNotBucketedAsServerError 锁死不变量 15：429 必须优先于 5xx/硬冷却。
// 去掉 Classify 里「内层码优先」这条即失败。
func TestRateLimitNotBucketedAsServerError(t *testing.T) {
	if k := Classify(500, nsBody429); k == provider.ErrServer {
		t.Fatal("压平成 500 的 429 被归成 ErrServer —— 共享限流会被误判成上游故障并冷却账号")
	}
	if k := Classify(500, `failed with status 429: too many requests`); k != provider.ErrSoftRate {
		t.Fatalf("内层 429 应归 ErrSoftRate，实得 %s", k)
	}
}

func TestEffectiveStatusUnflattens(t *testing.T) {
	if got := effectiveStatus(500, nsBody429); got != 429 {
		t.Errorf("应把压平的 500 还原成 429，实得 %d", got)
	}
	if got := effectiveStatus(500, `{"error":"bad gateway"}`); got != 500 {
		t.Errorf("无内层码时不得改动状态码，实得 %d", got)
	}
	if got := effectiveStatus(429, `nothing`); got != 429 {
		t.Errorf("4xx 不应被改写，实得 %d", got)
	}
}

// TestRouteOf 锁死 gen-/gen_ 路由判据（实测 9 个 ID 零反例）。
func TestRouteOf(t *testing.T) {
	cases := map[string]string{
		"gen-1791646899-lbPmS4LwoNu8lE9uTyGG": "openrouter",    // 别名与 :free 的真实响应
		"gen_01M4K7MJKQG5BEPQ86N7ACGPQ3":      "cline_gateway", // 厂商真实名的真实响应
		"chatcmpl-abc":                        "unknown",
		"":                                    "unknown",
	}
	for id, want := range cases {
		if got := RouteOf(id); got != want {
			t.Errorf("RouteOf(%q)=%s 期望 %s", id, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// 流内 error 帧：不得被伪装成「模型回了个空答案」（R40）
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// 请求头与 max_tokens
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// 目录：free[] 原样透传（无静态表、无映射、无过滤）+ 实测元数据补全
// ---------------------------------------------------------------------------

func mustModels(t *testing.T, c *Client, a *auth.Auth) []provider.ModelInfo {
	t.Helper()
	m, err := c.FetchModels(a)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// ---------------------------------------------------------------------------
// 接口完整性与默认值
// ---------------------------------------------------------------------------

// TestUpstreamContract 编译期锁死：Client 必须满足 provider.Upstream。
func TestUpstreamContract(t *testing.T) {
	var u provider.Upstream = New()
	if u == nil {
		t.Fatal("nil")
	}
}

// TestNoAccountEndpointsMustNotError：上游确无额度端点 ⇒ 必须返回 0 而不是 error。
func TestNoAccountEndpointsMustNotError(t *testing.T) {
	c := New()
	a := &auth.Auth{Kind: "cline", AccessToken: "sk_x"}
	if n, err := c.UserResource(a); n != 0 || err != nil {
		t.Errorf("UserResource 应为 (0,nil)，实得 (%d,%v)", n, err)
	}
	if n, items, err := c.UserResourceDetail(a); n != 0 || items != nil || err != nil {
		t.Errorf("UserResourceDetail 应为 (0,nil,nil)，实得 (%d,%v,%v)", n, items, err)
	}
	if p, err := c.FetchModelPricing(a); p != nil || err != nil {
		t.Errorf("FetchModelPricing 应为 (nil,nil)，实得 (%v,%v)", p, err)
	}
	if err := c.RefreshToken(a); err != nil {
		t.Errorf("Key 不轮换 ⇒ RefreshToken 应为空实现：%v", err)
	}
}

// TestExpiresAtFarFuture：0 会让 NeedsRefresh 恒真 → 每次请求刷 token（不变量 21 同款陷阱）。
func TestExpiresAtFarFuture(t *testing.T) {
	a := &auth.Auth{Kind: "cline", AccessToken: "sk_x", ExpiresAt: FarFutureExpiry()}
	if a.NeedsRefresh(10 * time.Minute) {
		t.Fatal("远期 ExpiresAt 下不应需要刷新")
	}
	zero := auth.Auth{AccessToken: "sk_x"}
	if !zero.NeedsRefresh(10 * time.Minute) {
		t.Fatal("对照：ExpiresAt=0 应当恒为需要刷新（这正是必须避免 0 值的原因）")
	}
}

// 显式保留对通用聚合的依赖，便于将来行为对照
