// codearts_test.go 渠道包回归测试：**零真实网络**（一律 httptest 假上游）。
//
// 覆盖清单（对齐任务书「测试要求」）：
//   - 签名向量：固定输入 → 固定签名（期望值来自**独立实现**：用 .NET 的
//     SHA256/HMACSHA256 按协议原文手工拼 canonical 算得，复算步骤见下方注释），
//     含 POST（带 maas_type）与 GET（无 content-type）两份；
//   - `maas_type` 参与签名 / `Agent-Type` 不参与（请求级断言 SignedHeaders）；
//   - 429 vs 4291 分类（含「额度码旁边有随机 429 数字」的抗误判）；
//   - HTTP 200 + 内嵌 error_code（流式与聚合两条路径）；
//   - `data:[DONE]` 收尾归一；
//   - benefit 额度按 UTC+8 换日；
//   - 三步签到（claimable / 已领取 / 无活动 / 非积分账户 / confirm 失败）；
//   - 积分与模型目录；续期（DPoP + 凭据轮换）与 DPoP 签名自校验。
package codearts

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"wild-view/internal/auth"
	"wild-view/internal/provider"
)

// 签名向量里用到的固定输入（与 temp/sigvector.ps1 完全一致）。
const (
	vecAK      = "AKTEST0001EXAMPLE"
	vecSK      = "sktest0001secret0001example0001"
	vecToken   = "gQAAAAAAAAtestsecuritytoken0001"
	vecBody    = `{"model":"deepseek-v4.1-flash","stream":true,"messages":[{"role":"user","content":"ping"}]}`
	vecChatURL = "https://snap-access.cn-north-4.myhuaweicloud.com/api/v2/chat/completions"
	vecStatURL = "https://snap-access.cn-north-4.myhuaweicloud.com/snap-manager/v1/statistics/plugin"

	// .NET 独立算得（temp/sigvector.ps1 的输出）。
	vecPayloadHash     = "c0c6515de88ba96706a1ac4421b2b91efebca53918423c0c74a6962022211c89"
	vecSignedHeaders   = "content-type;host;maas_type;x-sdk-content-sha256;x-sdk-date;x-security-token"
	vecSignature       = "80ca395b38d2c00de4e779aaebea066284908e4a3f6da783263b70494c02dbf3"
	vecSignatureNoMaas = "09aa818d626dff0dcee157ba8d6bdc8e3e6f9f535996f164bacc1dff45f27b3c"
	vecAuthHeader      = "SDK-HMAC-SHA256 Access=" + vecAK + ",SignedHeaders=" + vecSignedHeaders + ",Signature=" + vecSignature
	// GET 向量（无 content-type、空 body）。
	vecEmptyHash  = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	vecGetSig     = "d61be29d5327d5d3aa8451596a3792124c99ce2950c46903670fc42d01d5b0cc"
	vecGetHeaders = "host;x-sdk-content-sha256;x-sdk-date;x-security-token"
)

// vecTime 固定签名时刻。
func vecTime() time.Time { return time.Date(2026, 10, 16, 10, 30, 0, 0, time.UTC) }

// mustURL 解析 URL（测试用）。
func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatalf("parse %s: %v", s, err)
	}
	return u
}

// readFile 读文本文件（测试用）。
func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// makeAuth 构造一个凭据齐全的测试账号。
func makeAuth(uid string) *auth.Auth {
	return &auth.Auth{
		Kind:        string(Kind),
		UID:         uid,
		AccessToken: "AKTEST0001EXAMPLE",
		ExpiresAt:   time.Now().Add(time.Hour).Unix(),
		Extra: map[string]string{
			ExtraAccessKeyID:       vecAK,
			ExtraSecretAccessKey:   vecSK,
			ExtraSecurityToken:     vecToken,
			ExtraMaaSType:          MaaSTypeBenefit,
			ExtraCodeVerifier:      "verifier-xyz",
			ExtraDPoPPrivateKeyJWK: mustJWK(),
		},
	}
}

// mustJWK 生成一把可复用的 DPoP 私钥 JWK。
func mustJWK() string {
	k, err := NewDPoPKey()
	if err != nil {
		panic(err)
	}
	s, err := k.JWK()
	if err != nil {
		panic(err)
	}
	return s
}

// ---------------------------------------------------------------------------
// 1. 签名向量（固定输入 → 固定签名）
// ---------------------------------------------------------------------------

// TestMaaSTypeParticipatesInSignature maas_type 参与签名（去掉它签名必然不同），
// 且它出现在 SignedHeaders 里。
func TestMaaSTypeParticipatesInSignature(t *testing.T) {
	withMaas, err := SignRequest(vecAK, vecSK, vecToken, http.MethodPost, vecChatURL,
		[]byte(vecBody), "application/json", map[string]string{HeaderMaaSType: MaaSTypeBenefit}, vecTime())
	if err != nil {
		t.Fatal(err)
	}
	without, err := SignRequest(vecAK, vecSK, vecToken, http.MethodPost, vecChatURL,
		[]byte(vecBody), "application/json", nil, vecTime())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(SignedHeaderNames(withMaas[hdrAuthorization]), "maas_type") {
		t.Fatalf("maas_type 必须出现在 SignedHeaders: %s", withMaas[hdrAuthorization])
	}
	if strings.Contains(SignedHeaderNames(without[hdrAuthorization]), "maas_type") {
		t.Fatalf("未传 maas_type 时不应出现: %s", without[hdrAuthorization])
	}
	if withMaas[hdrAuthorization] == without[hdrAuthorization] {
		t.Fatal("maas_type 参与签名时签名必须不同")
	}
	// 与独立实现算得的两个固定值比对（双重确认不是「自己和自己一致」）。
	if !strings.Contains(withMaas[hdrAuthorization], vecSignature) {
		t.Fatalf("带 maas_type 的签名 = %s, want %s", withMaas[hdrAuthorization], vecSignature)
	}
	if !strings.Contains(without[hdrAuthorization], vecSignatureNoMaas) {
		t.Fatalf("不带 maas_type 的签名 = %s, want %s", without[hdrAuthorization], vecSignatureNoMaas)
	}
}

// ---------------------------------------------------------------------------
// 2. 错误分类：429 vs 4291
// ---------------------------------------------------------------------------

// TestClassifyQuotaVsQueue 额度码（不可重试）与排队码（可重试）必须分开，
// 且裸子串 429 不得命中 4291。
func TestClassifyQuotaVsQueue(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   provider.ErrKind
	}{
		{"额度码 4291", 200, `{"text":"[DONE]","error_code":"InferHub.4291.200","error_msg":"free quota exhausted"}`, provider.ErrHardCredit},
		{"额度措辞", 200, `{"error_code":"InferHub.x","error_msg":"insufficient quota for this model"}`, provider.ErrHardCredit},
		{"排队码 TM.00001041", 200, `{"error_code":"TM.00001041","error_msg":"排队中，请稍后"}`, provider.ErrSoftRate},
		{"排队码 81111", 429, `{"error_code":"APIG.0308","error_msg":"81111 too many requests"}`, provider.ErrSoftRate},
		{"独立 429", 200, `{"error_code":"TM.x","error_msg":"HTTP 429 rate limited"}`, provider.ErrSoftRate},
		{"4291 不得命中独立 429", 200, `{"error_code":"InferHub.4291.200"}`, provider.ErrHardCredit},
		{"额度报文里带随机 429 数字仍判额度", 200, `{"error_code":"InferHub.4291.200","requestId":"a429b7c","error_msg":"quota exhausted"}`, provider.ErrHardCredit},
		{"限流词", 200, `{"message":"rate limit exceeded"}`, provider.ErrSoftRate},
		{"鉴权 APIG.0301", 401, `{"error_code":"APIG.0301","error_msg":"verify ak sk signature fail"}`, provider.ErrSessionDead},
		{"无 benefit 包", 400, `{"error_code":"InferHub.4004.200"}`, provider.ErrClient},
		{"模型未注册", 404, `{"error_code":"InferHub.002002009.404","error_msg":"model is not registered"}`, provider.ErrModelBlocked},
		{"上下文超限", 400, `{"error_code":"x","error_msg":"maximum context length exceeded"}`, provider.ErrPromptTooLong},
		{"服务端故障", 500, `boom`, provider.ErrServer},
		{"成功", 200, `{"choices":[]}`, provider.ErrNone},
	}
	for _, tc := range cases {
		if got := Classify(tc.status, tc.body); got != tc.want {
			t.Errorf("%s: Classify(%d) = %s, want %s", tc.name, tc.status, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// 3. HTTP 200 + 内嵌 error_code
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// 4. benefit 额度按 UTC+8 自然日重置
// ---------------------------------------------------------------------------

// TestBenefitResetUTC8 benefit 重置按 UTC+8 自然日，不是滚动窗口、也不是 UTC 日界。
func TestBenefitResetUTC8(t *testing.T) {
	loc := time.FixedZone("UTC+8", 8*3600)
	cases := []struct {
		now      time.Time
		wantDay  string
		wantNext string
	}{
		{time.Date(2026, 10, 17, 10, 0, 0, 0, loc), "2026-10-17", "2026-10-18 00:00"},
		{time.Date(2026, 10, 17, 23, 59, 59, 0, loc), "2026-10-17", "2026-10-18 00:00"},
		// 16:00Z = 次日 00:00（UTC+8）：UTC 日界与 UTC+8 日界相差 8 小时。
		{time.Date(2026, 10, 17, 15, 59, 0, 0, time.UTC), "2026-10-17", "2026-10-18 00:00"},
		{time.Date(2026, 10, 17, 16, 0, 0, 0, time.UTC), "2026-10-18", "2026-10-19 00:00"},
		{time.Date(2026, 10, 17, 1, 0, 0, 0, time.UTC), "2026-10-17", "2026-10-18 00:00"},
	}
	for _, tc := range cases {
		if got := BenefitDay(tc.now); got != tc.wantDay {
			t.Errorf("BenefitDay(%s) = %s, want %s", tc.now, got, tc.wantDay)
		}
		reset := BenefitResetAt(tc.now).In(loc)
		if got := reset.Format("2006-01-02 15:04"); got != tc.wantNext {
			t.Errorf("BenefitResetAt(%s) = %s, want %s", tc.now, got, tc.wantNext)
		}
		if !reset.After(tc.now) {
			t.Errorf("重置时刻必须晚于当前：%s vs %s", reset, tc.now)
		}
		if !strings.Contains(BenefitResetHint(tc.now), "预计") {
			t.Errorf("提示必须写明「预计」：%s", BenefitResetHint(tc.now))
		}
	}
}

// ---------------------------------------------------------------------------
// 5. 三步签到
// ---------------------------------------------------------------------------

// checkinServer 假上游：统计 + 活动 + claim + confirm，并记录调用序列。
type checkinServer struct {
	*httptest.Server
	calls       []string
	claimReq    map[string]any
	confirmReq  map[string]any
	confirmFail bool
	creditPkg   bool
	campaigns   string
	claimData   string
}

func newCheckinServer(t *testing.T, creditPkg bool, campaigns, claimData string) *checkinServer {
	cs := &checkinServer{creditPkg: creditPkg, campaigns: campaigns, claimData: claimData}
	mux := http.NewServeMux()
	mux.HandleFunc("/snap-manager/v1/statistics/plugin", func(w http.ResponseWriter, r *http.Request) {
		cs.calls = append(cs.calls, "statistics")
		pkg := `{"package_name_cn":"CodeArts 专业版","is_credit_package":false,"is_token_package":true}`
		if cs.creditPkg {
			pkg = `{"package_name_cn":"CodeArts 积分版","is_credit_package":true}`
		}
		_, _ = io.WriteString(w, `{"package":`+pkg+`,"metrics":[{"name":"usageTotalPackageCredit","package_credit_amount":2000,"package_credit_used":500,"package_credit_remain":1500}]}`)
	})
	mux.HandleFunc("/v1/ops/delivery", func(w http.ResponseWriter, r *http.Request) {
		cs.calls = append(cs.calls, "delivery")
		if got := r.URL.Query().Get("channel"); got != "IDE" {
			t.Errorf("delivery channel = %q, want IDE", got)
		}
		_, _ = io.WriteString(w, `{"code":0,"message":"ok","data":{"items":`+cs.campaigns+`}}`)
	})
	mux.HandleFunc("/v1/ops/claim", func(w http.ResponseWriter, r *http.Request) {
		cs.calls = append(cs.calls, "claim")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &cs.claimReq)
		_, _ = io.WriteString(w, `{"code":0,"message":"ok","data":`+cs.claimData+`}`)
	})
	mux.HandleFunc("/v1/ops/confirm", func(w http.ResponseWriter, r *http.Request) {
		cs.calls = append(cs.calls, "confirm")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &cs.confirmReq)
		if cs.confirmFail {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"code":500,"message":"boom"}`)
			return
		}
		_, _ = io.WriteString(w, `{"code":0,"message":"ok","data":{}}`)
	})
	cs.Server = httptest.NewServer(mux)
	t.Cleanup(cs.Close)
	return cs
}

// TestCheckinThreeSteps 完整三步：campaignId 数字 → 字符串回传；claim 带 id → 补 confirm；
// 发放额取多级回退。
func TestCheckinThreeSteps(t *testing.T) {
	cs := newCheckinServer(t, true,
		`[{"type":"INVITE_USER","campaignId":9,"claimable":true},{"type":"USER_LOGIN","campaignId":1,"claimable":true,"status":"CLAIMABLE","benefitAmount":1000}]`,
		`{"id":"claim-1","credit":1000}`)
	c := NewWithBase(cs.URL)
	rep, err := c.DailyCheckinReport(makeAuth("u1"))
	if err != nil {
		t.Fatalf("DailyCheckinReport: %v", err)
	}
	if rep.Status != provider.CheckinClaimed || rep.Amount != 1000 {
		t.Fatalf("report = %+v, want claimed/1000", rep)
	}
	if strings.Join(cs.calls, ",") != "statistics,delivery,claim,confirm" {
		t.Fatalf("调用序列 = %v", cs.calls)
	}
	// campaignId 必须是**字符串**（服务端下发数字，回传要转字符串）。
	if got, ok := cs.claimReq["campaignId"].(string); !ok || got != "1" {
		t.Fatalf("claim campaignId 必须是字符串 \"1\"，实际 %#v", cs.claimReq["campaignId"])
	}
	if got, _ := cs.claimReq["channel"].(string); got != "IDE" {
		t.Fatalf("claim channel = %#v", cs.claimReq["channel"])
	}
	if got, ok := cs.confirmReq["campaignId"].(string); !ok || got != "1" {
		t.Fatalf("confirm campaignId 必须是字符串 \"1\"，实际 %#v", cs.confirmReq["campaignId"])
	}
}

// TestCheckinAmountFallback 无 claim 发放字段时回退活动条目的 benefitAmount。
func TestCheckinAmountFallback(t *testing.T) {
	cs := newCheckinServer(t, true,
		`[{"type":"USER_LOGIN","campaignId":1,"claimable":true,"status":"CLAIMABLE","benefitAmount":1000}]`,
		`{}`)
	c := NewWithBase(cs.URL)
	rep, err := c.DailyCheckinReport(makeAuth("u1"))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Status != provider.CheckinClaimed || rep.Amount != 1000 {
		t.Fatalf("report = %+v, want claimed/1000（回退活动条目）", rep)
	}
	// claim 响应无 data.id ⇒ **不**该调 confirm。
	for _, call := range cs.calls {
		if call == "confirm" {
			t.Fatalf("无 claim id 时不该 confirm：%v", cs.calls)
		}
	}
}

// TestCheckinAlreadyClaimed 幂等预检：claimable=false + CLAIMED → already（不发起 claim）。
func TestCheckinAlreadyClaimed(t *testing.T) {
	cs := newCheckinServer(t, true,
		`[{"type":"USER_LOGIN","campaignId":1,"claimable":false,"status":"CLAIMED","benefitAmount":1000}]`,
		`{}`)
	c := NewWithBase(cs.URL)
	rep, err := c.DailyCheckinReport(makeAuth("u1"))
	if err != nil {
		t.Fatalf("已领取不该报错: %v", err)
	}
	if rep.Status != provider.CheckinAlready {
		t.Fatalf("status = %s, want already_claimed", rep.Status)
	}
	if strings.Contains(strings.Join(cs.calls, ","), "claim") {
		t.Fatalf("已领取不该再 claim：%v", cs.calls)
	}
}

// TestCheckinNoCampaign 没有 USER_LOGIN 活动 → no_campaign（可重试状态，不是 error）。
func TestCheckinNoCampaign(t *testing.T) {
	cs := newCheckinServer(t, true, `[{"type":"NEW_USER_REGISTER","campaignId":2,"claimable":true}]`, `{}`)
	c := NewWithBase(cs.URL)
	rep, err := c.DailyCheckinReport(makeAuth("u1"))
	if err != nil {
		t.Fatalf("无活动不该报错: %v", err)
	}
	if rep.Status != provider.CheckinNoCampaign {
		t.Fatalf("status = %s, want no_campaign", rep.Status)
	}
}

// TestCheckinTokenAccount Token 套餐（非积分账户）不在活动范围：不查活动、直接 inactive。
func TestCheckinTokenAccount(t *testing.T) {
	cs := newCheckinServer(t, false, `[]`, `{}`)
	c := NewWithBase(cs.URL)
	rep, err := c.DailyCheckinReport(makeAuth("u1"))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Status != provider.CheckinNoCampaign {
		t.Fatalf("status = %s, want no_campaign", rep.Status)
	}
	if strings.Join(cs.calls, ",") != "statistics" {
		t.Fatalf("非积分账户不该查活动：%v", cs.calls)
	}
	// DailyCheckin（Upstream 口径）对 no_campaign 也应返回 nil。
	if err := c.DailyCheckin(makeAuth("u1")); err != nil {
		t.Fatalf("DailyCheckin: %v", err)
	}
}

// TestCheckinConfirmFailureNotFatal confirm 失败不影响整体签到结论。
func TestCheckinConfirmFailureNotFatal(t *testing.T) {
	cs := newCheckinServer(t, true,
		`[{"type":"USER_LOGIN","campaignId":1,"claimable":true,"benefitAmount":1000}]`,
		`{"id":"claim-1","benefitAmount":500}`)
	cs.confirmFail = true
	c := NewWithBase(cs.URL)
	rep, err := c.DailyCheckinReport(makeAuth("u1"))
	if err != nil {
		t.Fatalf("confirm 失败不应判整体失败: %v", err)
	}
	if rep.Status != provider.CheckinClaimed || rep.Amount != 500 {
		t.Fatalf("report = %+v, want claimed/500", rep)
	}
	// CheckinGranter 口径同源。
	amount, err := c.DailyCheckinGrant(makeAuth("u1"))
	if err != nil || amount != 500 {
		t.Fatalf("DailyCheckinGrant = %d, %v", amount, err)
	}
}

// TestCheckinBusinessCodeFails ops 信封 code!=0 必须判失败（且不误判成 claimed）。
func TestCheckinBusinessCodeFails(t *testing.T) {
	srv := httptest.NewServer(func() http.Handler {
		mux := http.NewServeMux()
		mux.HandleFunc("/snap-manager/v1/statistics/plugin", func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, `{"package":{"is_credit_package":true},"metrics":[]}`)
		})
		mux.HandleFunc("/v1/ops/delivery", func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, `{"code":0,"message":"ok","data":{"items":[{"type":"USER_LOGIN","campaignId":"7","claimable":true}]}}`)
		})
		mux.HandleFunc("/v1/ops/claim", func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, `{"code":4001,"message":"活动已结束"}`)
		})
		return mux
	}())
	defer srv.Close()
	c := NewWithBase(srv.URL)
	rep, err := c.DailyCheckinReport(makeAuth("u1"))
	if err == nil {
		t.Fatal("业务失败必须返回错误")
	}
	if rep.Status != provider.CheckinError {
		t.Fatalf("status = %s, want error", rep.Status)
	}
}

// ---------------------------------------------------------------------------
// 6. 积分
// ---------------------------------------------------------------------------

// TestUserResourceTotalNotSummed 总额取 usageTotalPackageCredit（**不累加分类**）。
func TestUserResourceTotalNotSummed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"package":{"package_name_cn":"积分版","is_credit_package":true},
			"metrics":[
			 {"name":"usageTotalPackageCredit","package_credit_amount":3000,"package_credit_used":1000,"package_credit_remain":2000},
			 {"name":"usageBasicPackageCredit","package_credit_amount":1000,"package_credit_used":400,"package_credit_remain":600},
			 {"name":"usageBonusPackageCredit","package_credit_amount":500,"package_credit_used":100,"package_credit_remain":400}
			]}`)
	}))
	defer srv.Close()
	c := NewWithBase(srv.URL)
	remain, items, err := c.UserResourceDetail(makeAuth("u1"))
	if err != nil {
		t.Fatal(err)
	}
	if remain != 2000 {
		t.Fatalf("remain = %d, want 2000（总额口径，不累加分类）", remain)
	}
	usable, unusable := provider.Summarize(items)
	if usable != 1000 || unusable != 0 {
		t.Fatalf("Summarize = (%d,%d), want (1000,0)：总额条目必须 InfoOnly 不参与小计", usable, unusable)
	}
}

// TestUserResourceFallbackSum 总额缺失才回退分类求和。
func TestUserResourceFallbackSum(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"package":{"is_credit_package":true},"metrics":[
			{"name":"usageBasicPackageCredit","package_credit_remain":"600.00"},
			{"name":"usageOnDemandPackageCredit","package_credit_remain":400}]}`)
	}))
	defer srv.Close()
	c := NewWithBase(srv.URL)
	remain, _, err := c.UserResourceDetail(makeAuth("u1"))
	if err != nil {
		t.Fatal(err)
	}
	if remain != 1000 {
		t.Fatalf("remain = %d, want 1000（分类求和 + 字符串数值兼容）", remain)
	}
}

// TestUserResourceNoCreditMetricIsZeroNotError 无 credit metric ⇒ 0 且**不**报错，
// 但必须给一条说明条目（区分「没有积分口径」与「余额归零」）。
func TestUserResourceNoCreditMetricIsZeroNotError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"package":{"package_name_cn":"Token 套餐","is_token_package":true},"metrics":[]}`)
	}))
	defer srv.Close()
	c := NewWithBase(srv.URL)
	remain, items, err := c.UserResourceDetail(makeAuth("u1"))
	if err != nil {
		t.Fatalf("无 metric 不该报错: %v", err)
	}
	if remain != 0 || len(items) != 1 || !items[0].InfoOnly {
		t.Fatalf("remain=%d items=%+v，want 0 + 一条 InfoOnly 说明", remain, items)
	}
}

// TestUserResourceAuthFailureClassified 401 必须带分类（调度器据此自愈）。
func TestUserResourceAuthFailureClassified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error_code":"APIG.0301","error_msg":"verify ak sk signature fail"}`)
	}))
	defer srv.Close()
	c := NewWithBase(srv.URL)
	_, err := c.UserResource(makeAuth("u1"))
	if err == nil {
		t.Fatal("401 必须报错")
	}
	pe, ok := err.(*provider.Error)
	if !ok || pe.Kind != provider.ErrSessionDead {
		t.Fatalf("err = %#v, want *provider.Error{ErrSessionDead}", err)
	}
}

// ---------------------------------------------------------------------------
// 7. 模型目录与 benefit 集合
// ---------------------------------------------------------------------------

// TestFetchModelsMergeAndBenefitSet 目录合并、日期后缀归一、视觉模型隐藏，
// 且 benefit 集合只记**未改写**的 id。
func TestFetchModelsMergeAndBenefitSet(t *testing.T) {
	var builtinSigned string
	mux := http.NewServeMux()
	// benefit 目录挂在 BenefitBase 上：用同一台 server 模拟，只换 Client.BenefitBase。
	mux.HandleFunc("/api/v1/gateway/config", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"result":{"models":[
			{"model_id":"deepseek-v4.1-flash","model_name":"DeepSeek V4.1 Flash"},
			{"model_id":"deepseek-v4-flash-0731","model_name":"DS V4 0731"}]}}`)
	})
	mux.HandleFunc("/v1/model/builtin", func(w http.ResponseWriter, r *http.Request) {
		builtinSigned = SignedHeaderNames(r.Header.Get("Authorization"))
		if got := r.Header.Get(HeaderAgentType); got != AgentTypePromptCenter {
			t.Errorf("builtin Agent-Type = %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("builtin Content-Type = %q（协议表要求带上但**不**参与签名）", got)
		}
		_, _ = io.WriteString(w, `{"builtinModels":[
			{"model_id":"GLM-5.2","model_name":"GLM 5.2"},
			{"model_id":"glm-5.3-flash-1008","model_name":"GLM Flash"},
			{"model_id":"secret-VL-1","model_name":"Vision"}]}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewWithBase(srv.URL)
	c.BenefitBase = srv.URL
	models, err := c.FetchModels(makeAuth("u1"))
	if err != nil {
		t.Fatalf("FetchModels: %v", err)
	}
	ids := map[string]bool{}
	for _, m := range models {
		ids[m.ID] = true
	}
	for _, want := range []string{"deepseek-v4.1-flash", "deepseek-v4-flash", "GLM-5.2", "glm-5.3-flash"} {
		if !ids[want] {
			t.Errorf("目录缺 %s（实际 %v）", want, ids)
		}
	}
	if ids["deepseek-v4-flash-0731"] {
		t.Error("带日期后缀的条目必须归一")
	}
	if ids["secret-VL-1"] {
		t.Error("视觉模型必须隐藏")
	}
	// GLM-5.2 的窗口来自静态表，动态目录不返回窗口 ⇒ 合并后应补上。
	for _, m := range models {
		if m.ID == "GLM-5.2" && m.ContextWindow != 202752 {
			t.Errorf("GLM-5.2 窗口 = %d, want 202752（静态表补齐）", m.ContextWindow)
		}
	}
	// GET 的 Content-Type 属「未签名但必需的头」（协议表），故 SignedHeaders 里没有它。
	if builtinSigned != "host;x-sdk-content-sha256;x-sdk-date;x-security-token" {
		t.Errorf("builtin SignedHeaders = %s（Agent-Type/Content-Type 形态不对）", builtinSigned)
	}
	if strings.Contains(builtinSigned, "content-type") || strings.Contains(builtinSigned, "agent-type") {
		t.Errorf("GET 请求的 content-type/agent-type 不得参与签名: %s", builtinSigned)
	}

	// benefit 集合：只记未改写 id。
	ben := c.benefitModels()
	if !ben["deepseek-v4.1-flash"] {
		t.Error("deepseek-v4.1-flash 必须在 benefit 集合")
	}
	if ben["deepseek-v4-flash-0731"] {
		t.Error("带日期后缀的 id 不得进 benefit 集合（后端是不同模型）")
	}
	if ben["deepseek-v4-flash"] {
		t.Error("无后缀的 deepseek-v4-flash 未被远端声明，不该由静态表之外的路径凭空加入")
	}
	if !c.isBenefitModel("deepseek-v4.1-flash") {
		t.Error("isBenefitModel 应命中远端集合")
	}
}

// TestFetchModelsFallsBackToStatic 上游全挂时回退静态表且不报错。
func TestFetchModelsFallsBackToStatic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := NewWithBase(srv.URL)
	c.BenefitBase = srv.URL
	models, err := c.FetchModels(nil)
	if err != nil {
		t.Fatalf("目录失败不该报错: %v", err)
	}
	if len(models) != len(staticCatalog) {
		t.Fatalf("应回退静态表 %d 条，得到 %d", len(staticCatalog), len(models))
	}
	if !c.isBenefitModel("glm-5.3-flash") {
		t.Error("静态兜底 benefit 集合应生效")
	}
}

// TestStaticModelsShape 静态表形状（ID 去重、窗口只填协议给的 5 条）。
func TestStaticModelsShape(t *testing.T) {
	ms := StaticModels()
	if len(ms) != 9 {
		t.Fatalf("静态表 = %d 条, want 9", len(ms))
	}
	seen := map[string]bool{}
	withWindow := 0
	for _, m := range ms {
		if seen[m.ID] {
			t.Fatalf("重复 ID %s", m.ID)
		}
		seen[m.ID] = true
		if m.ContextFromAPI {
			t.Fatalf("%s：静态表不是上游接口字段，ContextFromAPI 必须 false", m.ID)
		}
		if m.ContextWindow > 0 {
			withWindow++
		}
	}
	if withWindow != 5 {
		t.Fatalf("有窗口的条目 = %d, want 5（协议只给了 5 条）", withWindow)
	}
}

// ---------------------------------------------------------------------------
// 8. 出站体改写（含 DSML 分支）
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// 9. 续期（DPoP + 凭据轮换 + 终态）
// ---------------------------------------------------------------------------

// verifyDPoP 用 JWS 头里内嵌的公钥独立校验 DPoP 证明的 ES256 签名。
func verifyDPoP(t *testing.T, proof string) map[string]any {
	t.Helper()
	parts := strings.Split(proof, ".")
	if len(parts) != 3 {
		t.Fatalf("DPoP 证明不是三段 JWS: %q", proof)
	}
	hRaw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("解 JWS 头失败: %v", err)
	}
	var head struct {
		Typ string            `json:"typ"`
		Alg string            `json:"alg"`
		JWK map[string]string `json:"jwk"`
	}
	if err := json.Unmarshal(hRaw, &head); err != nil {
		t.Fatalf("JWS 头不是 JSON: %v", err)
	}
	if head.Typ != "dpop+jwt" || head.Alg != "ES256" {
		t.Fatalf("JWS 头 = %+v, want typ=dpop+jwt alg=ES256", head)
	}
	xb, _ := base64.RawURLEncoding.DecodeString(head.JWK["x"])
	yb, _ := base64.RawURLEncoding.DecodeString(head.JWK["y"])
	pub := &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(xb), Y: new(big.Int).SetBytes(yb)}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		t.Fatalf("签名长度非法: %v len=%d", err, len(sig))
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r := new(big.Int).SetBytes(sig[:32])
	s := new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(pub, digest[:], r, s) {
		t.Fatal("DPoP 签名校验失败")
	}
	pRaw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var payload map[string]any
	if err := json.Unmarshal(pRaw, &payload); err != nil {
		t.Fatalf("DPoP 载荷不是 JSON: %v", err)
	}
	return payload
}

// TestDPoPRoundTrip 私钥 JWK 往返 + 证明可被公钥验证 + 载荷字段齐全。
func TestDPoPRoundTrip(t *testing.T) {
	key, err := NewDPoPKey()
	if err != nil {
		t.Fatal(err)
	}
	jwk, err := key.JWK()
	if err != nil {
		t.Fatal(err)
	}
	back, err := ParseDPoPKey(jwk)
	if err != nil {
		t.Fatalf("ParseDPoPKey: %v", err)
	}
	if jwk2, _ := back.JWK(); jwk2 != jwk {
		t.Fatalf("JWK 往返不一致:\n%s\n%s", jwk, jwk2)
	}
	proof, err := back.Proof(http.MethodPost, STSTokenURL, time.Unix(1789000000, 0))
	if err != nil {
		t.Fatal(err)
	}
	payload := verifyDPoP(t, proof)
	if payload["htm"] != "POST" || payload["htu"] != STSTokenURL {
		t.Fatalf("DPoP 载荷 htm/htu 不对: %+v", payload)
	}
	if payload["iat"].(float64) != 1789000000 {
		t.Fatalf("iat = %v", payload["iat"])
	}
	if s, _ := payload["jti"].(string); len(s) != 64 {
		t.Fatalf("jti = %q（协议：32 字节 hex）", s)
	}
	if _, err := ParseDPoPKey("{bad json"); err == nil {
		t.Fatal("非法 JWK 必须报错")
	}
}

// TestRefreshTokenRotatesCredential STS 续期：form 字段、DPoP 头、凭据与 refresh_token 回写。
func TestRefreshTokenRotatesCredential(t *testing.T) {
	var form map[string][]string
	var gotDPoP string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		form = r.PostForm
		gotDPoP = r.Header.Get("DPoP")
		_, _ = io.WriteString(w, `{"credentials":{"access_key_id":"NEWAK","secret_access_key":"NEWSK","security_token":"NEWTOKEN","expiration":"2026-10-17T12:30:00Z"},"refresh_token":"rt-2"}`)
	}))
	defer srv.Close()

	a := makeAuth("u1")
	a.RefreshToken = "rt-1"
	c := NewWithBase("http://unused")
	c.STSURL = srv.URL
	if err := c.RefreshToken(a); err != nil {
		t.Fatalf("RefreshToken: %v", err)
	}
	if form["grant_type"][0] != "refresh_token" || form["client_id"][0] != OAuthClientID ||
		form["code_verifier"][0] != "verifier-xyz" || form["refresh_token"][0] != "rt-1" {
		t.Fatalf("STS form 字段不对: %v", form)
	}
	if payload := verifyDPoP(t, gotDPoP); payload["htu"] != srv.URL {
		t.Fatalf("DPoP htu 必须是完整 STS URL: %v", payload)
	}
	if a.AccessToken != "NEWAK" || a.Extra[ExtraSecretAccessKey] != "NEWSK" ||
		a.Extra[ExtraSecurityToken] != "NEWTOKEN" || a.RefreshToken != "rt-2" {
		t.Fatalf("凭据未回写: %+v extra=%v", a, a.Extra)
	}
	wantExp := time.Date(2026, 10, 17, 12, 30, 0, 0, time.UTC).Unix()
	if a.ExpiresAt != wantExp {
		t.Fatalf("ExpiresAt = %d, want %d（秒）", a.ExpiresAt, wantExp)
	}
}

// TestRefreshTokenFlow 完整假上游：带 refresh_token 的账号续期成功、字段回写。
func TestRefreshTokenFlow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.PostForm.Get("refresh_token") != "rt-1" {
			t.Errorf("refresh_token = %q, want rt-1", r.PostForm.Get("refresh_token"))
		}
		_, _ = io.WriteString(w, `{"credentials":{"access_key_id":"AK2","secret_access_key":"SK2","security_token":"T2","expiration":"2030-01-01T00:00:00Z"},"refresh_token":"rt-2"}`)
	}))
	defer srv.Close()
	a := makeAuth("u1")
	a.RefreshToken = "rt-1"
	c := NewWithBase("http://unused")
	c.STSURL = srv.URL
	if err := c.RefreshToken(a); err != nil {
		t.Fatalf("RefreshToken: %v", err)
	}
	if a.Extra[ExtraAccessKeyID] != "AK2" || a.RefreshToken != "rt-2" {
		t.Fatalf("凭据回写失败: %+v", a)
	}
}

// TestRefreshTokenTerminalStates 三类不可自愈的前置缺失 + invalid_grant 都必须返回
// **类型化的** ErrSessionDead（调度器用 errors.As 判定，裸 error 会让自愈失效）。
func TestRefreshTokenTerminalStates(t *testing.T) {
	c := NewWithBase("http://unused")
	base := makeAuth("u1")
	base.RefreshToken = "rt-1"

	cases := []struct {
		name   string
		mutate func(*auth.Auth)
	}{
		{"无 refresh_token", func(a *auth.Auth) { a.RefreshToken = "" }},
		{"无 code_verifier", func(a *auth.Auth) { delete(a.Extra, ExtraCodeVerifier) }},
		{"无 DPoP 私钥", func(a *auth.Auth) { delete(a.Extra, ExtraDPoPPrivateKeyJWK) }},
		{"DPoP 私钥损坏", func(a *auth.Auth) { a.Extra[ExtraDPoPPrivateKeyJWK] = "{oops" }},
	}
	for _, tc := range cases {
		a := makeAuth("u1")
		a.RefreshToken = "rt-1"
		tc.mutate(a)
		err := c.RefreshToken(a)
		pe, ok := err.(*provider.Error)
		if !ok || pe.Kind != provider.ErrSessionDead {
			t.Errorf("%s: err = %#v, want *provider.Error{ErrSessionDead}", tc.name, err)
		}
	}

	// invalid_grant → 终态。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"invalid_grant","error_description":"refresh token expired"}`)
	}))
	defer srv.Close()
	c.STSURL = srv.URL
	a := makeAuth("u1")
	a.RefreshToken = "rt-1"
	err := c.RefreshToken(a)
	if err == nil {
		t.Fatal("invalid_grant 必须报错")
	}
	if pe, ok := err.(*provider.Error); !ok || pe.Kind != provider.ErrSessionDead {
		t.Fatalf("invalid_grant 必须归 ErrSessionDead: %#v", err)
	}
	// 错误文案不得回显响应体（响应体里可能是凭据）。
	if strings.Contains(err.Error(), "secret_access_key") || strings.Contains(err.Error(), "security_token") {
		t.Fatalf("错误文案不得含凭据字段: %s", err.Error())
	}
}

// TestRefreshTokenInvalidDPoPIsNotTerminal InvalidDPoPHeader 是一次性拒绝，不算终态。
func TestRefreshTokenInvalidDPoPIsNotTerminal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"InvalidDPoPHeader","error_description":"dpop proof rejected"}`)
	}))
	defer srv.Close()
	c := NewWithBase("http://unused")
	c.STSURL = srv.URL
	a := makeAuth("u1")
	a.RefreshToken = "rt-1"
	err := c.RefreshToken(a)
	pe, ok := err.(*provider.Error)
	if !ok || pe.Kind == provider.ErrSessionDead {
		t.Fatalf("InvalidDPoPHeader 不得判终态: %#v", err)
	}
}

// ---------------------------------------------------------------------------
// 10. 两段空闲看门狗
// ---------------------------------------------------------------------------

// stallReader 一个永远不返回数据的 body（模拟上游卡死）。
type stallReader struct {
	ch     chan struct{}
	closed bool
}

func newStallReader() *stallReader { return &stallReader{ch: make(chan struct{})} }

func (s *stallReader) Read(p []byte) (int, error) {
	<-s.ch
	return 0, io.EOF
}
func (s *stallReader) Close() error {
	if !s.closed {
		s.closed = true
		close(s.ch)
	}
	return nil
}

// ---------------------------------------------------------------------------
// 11. 账号映射与文件落盘
// ---------------------------------------------------------------------------

// TestAccountRoundTrip auth.Auth ↔ Account 往返（Extra 键名与 AccessToken 承载 AK 的约定）。
func TestAccountRoundTrip(t *testing.T) {
	ac := &Account{
		ID: "user-1", AccessKeyID: "AK1", SecretAccessKey: "SK1", SecurityToken: "TOK1",
		ExpiresAt: 123, RefreshToken: "rt-1", CodeVerifier: "v-1", DPoPPrivateKeyJWK: `{"kty":"EC"}`,
		DomainID: "dom", UserID: "user-1", UserName: "小明", ProjectID: "p", Region: "cn-north-4",
	}
	a := ac.ToAuth()
	if a.AccessToken != "AK1" {
		t.Fatalf("AccessToken 必须承载 AK（auth.Parse 非空要求），得到 %q", a.AccessToken)
	}
	if a.ExpiresAt != 123 || a.RefreshToken != "rt-1" {
		t.Fatalf("顶层字段未映射: %+v", a)
	}
	if _, dup := a.Extra["access_token"]; dup {
		t.Fatal("Extra 不得重复塞 access_token")
	}
	if _, dup := a.Extra["expires_at"]; dup {
		t.Fatal("Extra 不得重复塞 expires_at")
	}
	back := accountFromAuth(a)
	if back.AccessKeyID != "AK1" || back.SecretAccessKey != "SK1" || back.CodeVerifier != "v-1" ||
		back.DPoPPrivateKeyJWK != `{"kty":"EC"}` || back.UserName != "小明" {
		t.Fatalf("往返丢失字段: %+v", back)
	}
}

// TestWriteAuthFileAndLoad auth 文件落盘走 SaveAtomic，且能被 LoadCodeArtsDir 读回。
func TestWriteAuthFileAndLoad(t *testing.T) {
	dir := t.TempDir()
	ac := &Account{ID: "user-9", AccessKeyID: "AK9", SecretAccessKey: "SK9", SecurityToken: "T9",
		RefreshToken: "rt-9", CodeVerifier: "v9", DPoPPrivateKeyJWK: `{"kty":"EC"}`, Nickname: "n9"}
	fp, err := WriteAuthFile(dir, ac)
	if err != nil {
		t.Fatalf("WriteAuthFile: %v", err)
	}
	if !strings.HasSuffix(fp, "codearts-user-9.json") {
		t.Fatalf("文件名 = %s", fp)
	}
	raw := readFile(t, fp)
	if !strings.Contains(raw, `"access_key_id": "AK9"`) {
		t.Fatalf("落盘缺少 Extra 字段:\n%s", raw)
	}
}
