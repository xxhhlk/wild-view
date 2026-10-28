// zcode_test.go ZCode 渠道包的回归测试。
//
// 纪律：**全部走 httptest 假上游，零真实网络**（AGENTS.md §0.1：严禁穷举探测上游）。
// 覆盖点（对应任务要求）：
//   - 头族齐全（含 X-Device-Mid）；
//   - 身份块缺失 → 405/3012 的**本地前置校验**（且一个包都没发出去）；
//   - Anthropic SSE → OpenAI SSE（signature_delta 忽略、message_stop 收尾、error 帧抛错）；
//   - output_config.effort 下发；
//   - 额度解析（balance 的 token 桶 + preview 的可领计划 + 企业版）；
//   - DailyCheckin 返回「不支持」而非成功。
package zcode

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"wild-view/internal/auth"
	"wild-view/internal/provider"
)

// ---------------------------------------------------------------------------
// 测试工具
// ---------------------------------------------------------------------------

func testAuth(extra map[string]string) *auth.Auth {
	if extra == nil {
		extra = map[string]string{"device_mid": "11111111-2222-4333-8444-555555555555"}
	}
	return &auth.Auth{
		Kind:        "zcode",
		AccessToken: "test.jwt.payload",
		UID:         "12345678901234567",
		Extra:       extra,
	}
}

// chatBody 构造一个最小的 OpenAI Chat 请求体（含 system / user / tools）。
func chatBody(t *testing.T, effort string) []byte {
	t.Helper()
	req := map[string]any{
		"model": "zcode/GLM-5.3-Flash",
		"messages": []any{
			map[string]any{"role": "system", "content": "CALLER-SYSTEM-TEXT"},
			map[string]any{"role": "user", "content": "你好"},
		},
		"tools": []any{
			map[string]any{"type": "function", "function": map[string]any{
				"name": "read_file", "description": "read",
				"parameters": map[string]any{"type": "object", "properties": map[string]any{}},
			}},
		},
	}
	if effort != "" {
		req["reasoning_effort"] = effort
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	return raw
}

// ---------------------------------------------------------------------------
// 身份块
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// 头族
// ---------------------------------------------------------------------------

func TestRequiredHeadersComplete(t *testing.T) {
	a := testAuth(nil)
	h := zcodeHeaders(a, "Bearer tok", true)
	for _, name := range requiredHeaderNames() {
		if strings.TrimSpace(h.Get(name)) == "" {
			t.Errorf("缺少必需头 %s", name)
		}
	}
	if got := h.Get(HdrDeviceMid); got != "11111111-2222-4333-8444-555555555555" {
		t.Errorf("X-Device-Mid = %q", got)
	}
	if got := h.Get(HdrUserAgent); got != "ZCode/3.14.3" {
		t.Errorf("User-Agent = %q", got)
	}
	if got := h.Get(HdrPlatform); got != "win32" {
		t.Errorf("X-Platform = %q", got)
	}
	if got := h.Get(HdrOSCategory); got != "windows" {
		t.Errorf("X-Os-Category = %q", got)
	}
	if got := h.Get(HdrAnthropicVersion); got != "2023-06-01" {
		t.Errorf("anthropic-version = %q", got)
	}
	if got := h.Get(HdrAuthorization); got != "Bearer tok" {
		t.Errorf("Authorization = %q", got)
	}
	// 不带 Authorization / Content-Type 时那两个键不出现（preview 端点形态）。
	plain := zcodeHeaders(a, "", false)
	if plain.Get(HdrAuthorization) != "" || plain.Get(HdrContentType) != "" {
		t.Error("preview 形态不应带 Authorization / Content-Type")
	}
	for _, name := range requiredHeaderNames() {
		if plain.Get(name) == "" {
			t.Errorf("preview 形态缺必需头 %s", name)
		}
	}
}

func TestDeviceMidFallbackIsStable(t *testing.T) {
	a := &auth.Auth{Kind: "zcode", AccessToken: "jwt", UID: "u1"}
	first := deviceMidOf(a)
	second := deviceMidOf(a)
	if first != second {
		t.Errorf("缺 device_mid 时的兜底值必须确定性稳定：%q vs %q", first, second)
	}
	if len(strings.Split(first, "-")) != 5 {
		t.Errorf("兜底值应是 UUID 形态：%q", first)
	}
	b := &auth.Auth{Kind: "zcode", AccessToken: "jwt", UID: "u2"}
	if deviceMidOf(b) == first {
		t.Error("不同账号的兜底 mid 不应相同")
	}
}

// ---------------------------------------------------------------------------
// 出站形态（Anthropic Messages + 身份块 + output_config.effort）
// ---------------------------------------------------------------------------

func catalogEntryForTest(id string) (catalogEntry, bool) {
	e, ok := staticIndex[id]
	return e, ok
}

// ---------------------------------------------------------------------------
// 入站 SSE
// ---------------------------------------------------------------------------

// sseStream 构造一段完整的 Anthropic SSE（thinking → text → 收尾）。
func sseStream(text string) string {
	return strings.Join([]string{
		"event: message_start",
		`data: {"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":1000,"cache_read_input_tokens":400,"cache_creation_input_tokens":100}}}`,
		"",
		"event: ping",
		`data: {"type":"ping"}`,
		"",
		"event: content_block_start",
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}`,
		"",
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"想一下"}}`,
		"",
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"DEADBEEFSIGNATURE"}}`,
		"",
		"event: content_block_stop",
		`data: {"type":"content_block_stop","index":0}`,
		"",
		"event: content_block_start",
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"text"}}`,
		"",
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"` + text + `"}}`,
		"",
		"event: content_block_stop",
		`data: {"type":"content_block_stop","index":1}`,
		"",
		"event: message_delta",
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":20,"output_tokens_details":{"thinking_tokens":7}}}`,
		"",
		"event: message_stop",
		`data: {"type":"message_stop"}`,
		"",
	}, "\n")
}

// ---------------------------------------------------------------------------
// 额度
// ---------------------------------------------------------------------------

func TestBillingParsing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case EpBalance:
			if r.Header.Get(HdrAuthorization) == "" {
				t.Error("balance 必须带 Authorization（缺则上游 401）")
			}
			if r.Header.Get(HdrDeviceMid) == "" {
				t.Error("balance 必须带 X-Device-Mid（缺则上游 400 code 3001）")
			}
			_, _ = io.WriteString(w, `{"code":0,"data":{"displayMode":"personal","balances":[
				{"plan_id":"plan-a","show_name":"GLM-5.3-Flash","unit_type":"token","meter":"model_usage",
				 "total_units":100000000,"used_units":5460725,"remaining_units":94539275,"available_units":94539275,
				 "expires_at":1893456000}]}}`)
		case EpPreview:
			if !strings.Contains(r.URL.RawQuery, "platform=win32") {
				t.Errorf("preview 必须带 platform=win32：%q", r.URL.RawQuery)
			}
			_, _ = io.WriteString(w, `{"code":0,"data":{"plans":[
				{"plan_id":"trust-1003","priority":1,"name":"每日赠送",
				 "entitlements":[{"grant_units":100000000,"unit_type":"token"}]}]}}`)
		default:
			t.Errorf("意外请求 %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := NewWithBase(srv.URL)
	a := testAuth(nil)
	// 2026-10-17：UserResource 返回 Σ token —— **这是 token 口径的额度余额，不是积分**
	// （由渠道级 credits_unit="token" 标注：前端用「Token 计数」面板展示，
	// 后端不进积分合计、不进积分流水；条目仍是 InfoOnly，见下）。
	remain, err := c.UserResource(a)
	if err != nil {
		t.Fatalf("UserResource: %v", err)
	}
	if remain != 94539275 {
		t.Errorf("remain = %d，期望 94539275（token 桶剩余之和）", remain)
	}
	_, items, err := c.UserResourceDetail(a)
	if err != nil {
		t.Fatalf("UserResourceDetail: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("明细条目数 = %d，期望 2（1 个余额桶 + 1 个可领计划）", len(items))
	}
	bal := items[0]
	if !strings.Contains(bal.Name, "tokens") {
		t.Errorf("token 桶的名称应标明单位：%q", bal.Name)
	}
	if bal.Total != 100000000 || bal.Used != 5460725 || bal.Remain != 94539275 {
		t.Errorf("桶数值不符：%+v", bal)
	}
	if bal.Key != "plan-a" {
		t.Errorf("桶 Key 应为 plan_id：%q", bal.Key)
	}
	// ⚠️ 2026-10-17 修正：token 桶**必须 InfoOnly**（它不是积分）。
	// 旧断言要求「非 InfoOnly」，正是「上亿 token 被当成积分污染统计」的根源。
	if !bal.Usable || !bal.InfoOnly {
		t.Errorf("token 桶应 可展示(Usable) 且 InfoOnly（token 不是积分）：%+v", bal)
	}
	// 保留精确时刻（2026-10-19）：面板按「N 小时内到期」分桶需要时分秒，
	// 旧版截成日期会让今晚失效的包被算进「明日到期」。
	if bal.ExpireAt != "2030-01-01T08:00:00+08:00" {
		t.Errorf("ExpireAt = %q，期望保留时刻的 RFC3339（UTC+8）", bal.ExpireAt)
	}
	plan := items[1]
	if !plan.InfoOnly || !plan.Usable {
		t.Errorf("可领计划必须是 InfoOnly + Usable：%+v", plan)
	}
	if plan.Total != 100000000 {
		t.Errorf("可领额度应取 entitlements[0].grant_units：%+v", plan)
	}
	// InfoOnly 条目不进任何小计：token 桶与可领计划都不得算进「可消耗积分」，
	// 否则上亿 token 会污染积分口径（2026-10-17 修正）。
	usable, unusable := provider.Summarize(items)
	if usable != 0 || unusable != 0 {
		t.Errorf("Summarize = (%d,%d)，期望 (0,0)（token 不是积分）", usable, unusable)
	}
}

func TestBillingEnterpriseDoesNotShowZero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == EpBalance {
			_, _ = io.WriteString(w, `{"code":0,"data":{"displayMode":"enterprise","balances":[]}}`)
			return
		}
		_, _ = io.WriteString(w, `{"code":0,"data":{"plans":[]}}`)
	}))
	defer srv.Close()

	c := NewWithBase(srv.URL)
	_, items, err := c.UserResourceDetail(testAuth(nil))
	if err != nil {
		t.Fatalf("UserResourceDetail: %v", err)
	}
	if len(items) != 1 || !items[0].InfoOnly {
		t.Fatalf("企业版应给一条只作展示的说明条目，实际 %+v", items)
	}
	if !strings.Contains(items[0].Name, "企业版") {
		t.Errorf("说明条目文案不符：%q", items[0].Name)
	}
}

func TestBillingErrorIsTyped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"code":1002,"msg":"unauthorized"}`)
	}))
	defer srv.Close()
	c := NewWithBase(srv.URL)
	// 用 UserResourceDetail 验证 401 分类：UserResource 自 2026-10-17 起恒 0 且**不发请求**
	// （token 不是积分），401 的自愈判据由仍在请求上游的明细路径承担。
	_, _, err := c.UserResourceDetail(testAuth(nil))
	var pe *provider.Error
	if !errors.As(err, &pe) || pe.Kind != provider.ErrSessionDead {
		t.Fatalf("401 必须是 *provider.Error{ErrSessionDead}（不变式 19 的自愈判据），实际 %v", err)
	}
}

// ---------------------------------------------------------------------------
// 签到：本渠道不支持
// ---------------------------------------------------------------------------

func TestDailyCheckinUnsupported(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewWithBase(srv.URL)
	err := c.DailyCheckin(testAuth(nil))
	if !errors.Is(err, ErrClaimUnsupported) {
		t.Fatalf("DailyCheckin 必须返回 ErrClaimUnsupported，实际 %v", err)
	}
	if !strings.Contains(err.Error(), "不支持") || !strings.Contains(err.Error(), "验证码") {
		t.Errorf("错误文案应说明「不支持 + 原因」：%v", err)
	}
	if hits != 0 {
		t.Errorf("不支持领取时不得请求上游，实际 %d 个请求", hits)
	}
	// 不得实现 CheckinReporter：实现了会让调度器在当日窗口内反复重试一条永不可能成功的路径。
	if _, ok := any(c).(provider.CheckinReporter); ok {
		t.Error("本渠道不得实现 provider.CheckinReporter")
	}
}

// ---------------------------------------------------------------------------
// 续期：空实现
// ---------------------------------------------------------------------------

func TestRefreshTokenIsNoop(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer srv.Close()
	c := NewWithBase(srv.URL)
	a := testAuth(nil)
	if err := c.RefreshToken(a); err != nil {
		t.Errorf("RefreshToken 应是空实现且返回 nil，实际 %v", err)
	}
	if hits != 0 {
		t.Errorf("不可续期渠道不得请求上游，实际 %d", hits)
	}
	if a.AccessToken != "test.jwt.payload" {
		t.Error("RefreshToken 不得改写凭据")
	}
}

// ---------------------------------------------------------------------------
// 错误分类
// ---------------------------------------------------------------------------

func TestClassify(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   provider.ErrKind
	}{
		{"并发限流 3009", 429, `{"code":3009,"msg":"model concurrency limit exceeded"}`, provider.ErrSoftRate},
		{"额度用尽 1005", 429, `{"code":1005,"msg":"quota exceeded"}`, provider.ErrHardCredit},
		{"订阅余额不足 1113", 429, `{"code":1113}`, provider.ErrHardCredit},
		{"余额不足文本（429 但无 3009）", 429, `{"msg":"余额不足"}`, provider.ErrHardCredit},
		{"风控 3012", 405, `{"code":3012,"msg":"request has been blocked due to unusual activity."}`, provider.ErrClient},
		{"风控短语无码", 405, `request has been blocked due to unusual activity.`, provider.ErrClient},
		{"captcha 索要 3007", 400, `{"code":3007}`, provider.ErrSoftRate},
		{"凭据失效 1002", 401, `{"code":1002}`, provider.ErrSessionDead},
		{"字符串业务码 3012", 405, `{"code":"3012"}`, provider.ErrClient},
		{"本地身份块拦截体", 405, `{"error":{"code":"bad_params","message":"…code 3012…"}}`, provider.ErrBadParams},
		{"本地出站体错误", 400, `{"error":{"code":"bad_params"}}`, provider.ErrBadParams},
		{"402 欠费", 402, `{}`, provider.ErrHardCredit},
		{"普通 429", 429, `{}`, provider.ErrSoftRate},
		{"401", 401, `{}`, provider.ErrSessionDead},
		{"404", 404, `{}`, provider.ErrNotFound},
		{"500", 500, `{}`, provider.ErrServer},
		{"其它 400", 400, `{"msg":"bad request"}`, provider.ErrClient},
		{"200", 200, `{}`, provider.ErrNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(tc.status, tc.body); got != tc.want {
				t.Errorf("Classify(%d, %q) = %v，期望 %v", tc.status, tc.body, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 模型目录
// ---------------------------------------------------------------------------

func TestStaticModels(t *testing.T) {
	got := StaticModels()
	if len(got) != 2 {
		t.Fatalf("静态表应只有协议实测的 2 个模型，实际 %d", len(got))
	}
	byID := map[string]provider.ModelInfo{}
	for _, m := range got {
		byID[m.ID] = m
	}
	flash, ok := byID["GLM-5.3-Flash"]
	if !ok {
		t.Fatal("缺 GLM-5.3-Flash")
	}
	if !flash.SupportsImages || flash.ContextWindow != 1_000_000 || flash.MaxTokens != 128_000 {
		t.Errorf("GLM-5.3-Flash 能力字段不符：%+v", flash)
	}
	if len(flash.ReasoningEfforts) != 3 || flash.DefaultReasoningEffort != "max" {
		t.Errorf("档位应透传 low/high/max + 默认 max：%+v", flash)
	}
	if flash.CanDisableThinking == nil || *flash.CanDisableThinking {
		t.Error("本渠道无关闭思考的写法 ⇒ can_disable_thinking 应为 false")
	}
	pro := byID["GLM-5.3"]
	if pro.SupportsImages {
		t.Error("GLM-5.3 上游 capabilities 为空对象 ⇒ 不得声明 vision")
	}
	for _, m := range got {
		if !m.SupportsTools {
			t.Errorf("%s 应声明工具能力（渠道级）", m.ID)
		}
		if !m.ContextFromAPI {
			t.Errorf("%s 的容量来自上游声明 ⇒ ContextFromAPI 应为 true", m.ID)
		}
	}
}

func TestFetchModelsDynamicAndFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != EpClientConfigs {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		// platform 必须是 unknown（协议 §1.2.2）。
		if !strings.Contains(r.URL.RawQuery, "platform=unknown") {
			t.Errorf("client/configs 的 platform 必须是 unknown：%q", r.URL.RawQuery)
		}
		if r.Header.Get(HdrAuthorization) == "" || r.Header.Get(HdrDeviceMid) == "" {
			t.Error("client/configs 需要 Authorization + X-Device-Mid")
		}
		// builtinModels 是**对象**（键为序号字串），不是数组。
		_, _ = io.WriteString(w, `{"code":0,"data":{"builtinModels":{
			"0":{"modelId":"GLM-5.3-Flash","contextWindow":1000000,"maxCompletionTokens":128000,
			     "capabilities":{"vision":true},"modalities":{"input":["text","image"]},
			     "reasoning":{"levels":{"max":{"anthropic":{"set":{"path":["output_config","effort"],"value":"max"}}},
			                            "low":{"anthropic":{"set":{"path":["output_config","effort"],"value":"low"}}}},
			                  "defaultLevel":"max"}},
			"1":{"modelId":"GLM-5.3","contextWindow":1000000,"maxCompletionTokens":128000,
			     "capabilities":{},"modalities":{"input":["text"]},
			     "reasoning":{"levels":{},"defaultLevel":""}}}}}`)
	}))
	defer srv.Close()

	c := NewWithBase(srv.URL)
	models, err := c.FetchModels(testAuth(nil))
	if err != nil {
		t.Fatalf("FetchModels: %v", err)
	}
	if len(models) != 2 || models[0].ID != "GLM-5.3-Flash" || models[1].ID != "GLM-5.3" {
		t.Fatalf("动态目录解析不符：%+v", models)
	}
	// 档位展示序 low → max（未声明的 high 不编造）。
	if got := strings.Join(models[0].ReasoningEfforts, ","); got != "low,max" {
		t.Errorf("档位序 = %q，期望 low,max", got)
	}
	if models[0].DefaultReasoningEffort != "max" {
		t.Errorf("defaultLevel 应为 max，实际 %q", models[0].DefaultReasoningEffort)
	}
	if models[1].SupportsImages {
		t.Error("capabilities 为空对象时不得声明 vision")
	}

	// 远端失败 → 回退静态表且**不返回错误**。
	bad := NewWithBase("http://127.0.0.1:1")
	fallback, err := bad.FetchModels(testAuth(nil))
	if err != nil {
		t.Fatalf("目录失败不得返回错误：%v", err)
	}
	if len(fallback) != 2 {
		t.Errorf("应回退静态兜底表 2 条，实际 %d", len(fallback))
	}
}

func TestFetchModelPricingEmpty(t *testing.T) {
	c := NewWithBase("http://127.0.0.1:1")
	got, err := c.FetchModelPricing(testAuth(nil))
	if err != nil || len(got) != 0 {
		t.Fatalf("本渠道无定价端点 ⇒ 应返回空切片且无错误，实际 %v %v", got, err)
	}
}
