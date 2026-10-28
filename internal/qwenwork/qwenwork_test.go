// qwenwork_test.go 渠道单测：签名、body 改写、SSE 剥壳、费率/明细解析、错误分类。
// 不打真实上游；SSE 用 httptest 或内嵌字符串模拟。
package qwenwork

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"wild-view/internal/auth"
	"wild-view/internal/provider"
)

// ---------------------------------------------------------------------------
// COSY 签名
// ---------------------------------------------------------------------------

// TestNewCosySession 验证签名材料结构：tempKey 16 字符、cosyKey 可 base64 解出 128 字节。
func TestNewCosySession(t *testing.T) {
	s, err := NewCosySession("uid-1", "nick", "a@b.c", "tok-1")
	if err != nil {
		t.Fatalf("NewCosySession: %v", err)
	}
	if len(s.TempKey) != 16 {
		t.Fatalf("tempKey len = %d, want 16", len(s.TempKey))
	}
	for _, c := range string(s.TempKey) {
		if !strings.ContainsRune("0123456789abcdef", c) {
			t.Fatalf("tempKey 含非 hex 字符 %q", c)
		}
	}
	// RSA-1024 密文固定 128 字节 → base64 172 字符
	decoded, err := base64Raw(s.CosyKey)
	if err != nil {
		t.Fatalf("cosyKey not base64: %v", err)
	}
	if len(decoded) != 128 {
		t.Fatalf("cosyKey decoded len = %d, want 128 (RSA-1024)", len(decoded))
	}
	// info 含 uid 与 token（AES 密文，无法直接断言内容，但长度必须非零）
	if s.Info == "" {
		t.Fatal("info 为空")
	}
}

// TestAuthHeaderFormat 验证 Authorization 三段结构与 path 归一化。
func TestAuthHeaderFormat(t *testing.T) {
	s, _ := NewCosySession("uid", "n", "", "tok")
	auth, date, err := s.AuthHeader(`{"x":1}`, "https://gateway.qwenwork.cn/algo/api/v2/service/pro/sse/agent_chat_generation?FetchKeys=x")
	if err != nil {
		t.Fatal(err)
	}
	if date == "" {
		t.Fatal("AuthHeader 应返回签名所用时间戳（供 Cosy-Date 头复用）")
	}
	parts := strings.SplitN(auth, ".", 3)
	if len(parts) != 3 || parts[0] != "Bearer COSY" {
		t.Fatalf("auth 格式非法: %q", auth[:40])
	}
	// 三段签名串确定性：同 ts 不同 body → 不同签名（此处只验不 panic 且格式合法）
	if !strings.Contains(auth, "Bearer COSY.") {
		t.Fatal("缺 COSY 前缀")
	}
}

// ---------------------------------------------------------------------------
// body 改写
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// SSE 剥壳
// ---------------------------------------------------------------------------

// ssePayload 构造一行嵌套 SSE data。
func ssePayload(t *testing.T, inner any, scv int) string {
	bodyB, _ := json.Marshal(inner)
	bodyStr := string(bodyB)
	env := map[string]any{
		"headers":         map[string]any{"Content-Type": []string{"application/json"}},
		"body":            bodyStr,
		"statusCodeValue": scv,
		"statusCode":      "OK",
	}
	b, _ := json.Marshal(env)
	return "data:" + string(b)
}

// ---------------------------------------------------------------------------
// 余额解析
// ---------------------------------------------------------------------------

// TestParseWalletTotals 验证 /user/wallets 三类汇总解析（回归：内联 struct 标签丢失）。
func TestParseWalletTotals(t *testing.T) {
	raw := []byte(`{"daily_credits":{"total_balance":100},"longterm_credits":{"total_balance":2000},"monthly_credits":{"total_balance":0}}`)
	var w struct {
		DailyCredits    walletTotals `json:"daily_credits"`
		LongtermCredits walletTotals `json:"longterm_credits"`
		MonthlyCredits  walletTotals `json:"monthly_credits"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		t.Fatal(err)
	}
	if w.DailyCredits.TotalBalance != 100 || w.LongtermCredits.TotalBalance != 2000 {
		t.Fatalf("daily=%v longterm=%v（标签丢失回归）", w.DailyCredits, w.LongtermCredits)
	}
}

// TestTruncCredits 验证浮点积分 → int64 直转（与其他渠道 int64(Remaining) 口径一致，不 ×100）。
// 回归：早期实现误 ×100 导致面板显示虚高 100 倍。
func TestTruncCredits(t *testing.T) {
	cases := []struct {
		in  float64
		out int64
	}{{2090.38, 2090}, {2094.8294, 2094}, {0.5, 0}, {0, 0}, {-1, 0}}
	for _, c := range cases {
		if got := truncCredits(c.in); got != c.out {
			t.Errorf("truncCredits(%v) = %d, want %d", c.in, got, c.out)
		}
	}
}

// TestExpireDate 验证 UTC+8 墙钟解析（含带偏移与不带偏移两种形态），
// 并锁住「**保留精确时刻**」——这是「N 小时内到期」分桶的前提。
//
// 历史：旧版断言返回值是 "2026-09-19"（日期粒度）。改为 RFC3339 后，
// 面板才能分辨「今晚 00:00 失效」与「明天 23:59:59 失效」
// （2026-10-19 实测：按日期粒度会把前者误报成「明日到期」）。
func TestExpireDate(t *testing.T) {
	// 带时区偏移：原样保留时刻
	if got := expireDate("2026-09-19T00:00:00+08:00"); got != "2026-09-19T00:00:00+08:00" {
		t.Fatalf("RFC3339 = %q", got)
	}
	// 无时区后缀：按 UTC+8 墙钟解析，补上 +08:00
	if got := expireDate("2026-09-19T00:00:00"); got != "2026-09-19T00:00:00+08:00" {
		t.Fatalf("无时区 = %q", got)
	}
	// 上游真实形态（每日奖励）：次日零点失效，时刻必须保留
	if got := expireDate("2026-10-20T00:00:00+08:00"); got != "2026-10-20T00:00:00+08:00" {
		t.Fatalf("每日奖励 valid_to = %q", got)
	}
	if got := expireDate(""); got != "" {
		t.Fatalf("空串应返回空, got %q", got)
	}
	if got := expireDate("not-a-time"); got != "" {
		t.Fatalf("不可解析应返回空, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// 错误分类
// ---------------------------------------------------------------------------

// TestClassify 分类矩阵（429 优先于 hardMarker，见 AGENTS.md §6.15）。
func TestClassify(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   provider.ErrKind
	}{
		{429, `{"error":{"message":"quota exceeded"}}`, provider.ErrSoftRate},
		{402, "insufficient credit", provider.ErrHardCredit},
		{401, `{"errorCode":"INVALID_TOKEN"}`, provider.ErrSessionDead},
		{403, `{"code":"101","message":"Signature invalid"}`, provider.ErrSessionDead},
		{404, "not found", provider.ErrNotFound},
		{500, "internal", provider.ErrServer},
		{200, `{"code":"400","message":"request_id is required"}`, provider.ErrNone},
		{400, `{"code":"400","message":"prompt is too long"}`, provider.ErrPromptTooLong},
	}
	for _, c := range cases {
		if got := Classify(c.status, c.body); got != c.want {
			t.Errorf("Classify(%d,%q) = %v, want %v", c.status, c.body, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Stream 透传
// ---------------------------------------------------------------------------

// base64Raw 标准 base64 解码辅助。
func base64Raw(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(s)
}

// TestRefreshExpiresInSeconds 验证 refresh 响应的 expires_in 按【秒】解释。
// 回归：早期误按毫秒处理（*time.Millisecond），把 7 天压成 604.8 秒，
// 落盘 expiresAt 比 access token 真实寿命少 ~7 天 → NeedsRefresh 恒为真 →
// 每次请求都刷 token，与千问办公 App 高频互踩直至 refresh token 作废。
// 证据：上游 expires_in=604800 恰好等于 access token JWT 的 iat→exp（7 天）。
func TestRefreshExpiresInSeconds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// 上游真实形态：expires_in 以【秒】计
		_, _ = w.Write([]byte(`{"device_token":"dt-new","refresh_token":"rt-new","expires_in":604800}`))
	}))
	defer srv.Close()

	a := &auth.Auth{UID: "u1", AccessToken: "old", RefreshToken: "rt-old"}
	c := New()
	c.Gateway = srv.URL
	before := time.Now().Unix()
	if err := c.RefreshToken(a); err != nil {
		t.Fatalf("RefreshToken: %v", err)
	}
	got := a.ExpiresAt - before
	want := int64(604800)
	if got < want-60 || got > want+60 {
		t.Fatalf("expiresAt 距现在 %ds，want ≈%ds（7 天）；按毫秒解释会得到 ≈604s", got, want)
	}
	if a.AccessToken != "dt-new" || a.RefreshToken != "rt-new" {
		t.Fatalf("token 未更新: %q / %q", a.AccessToken, a.RefreshToken)
	}
}

// errAfterReader 先吐完 data，再返回指定错误（模拟「读到一半流断了」）。
type errAfterReader struct {
	data string
	err  error
	off  int
}

func (r *errAfterReader) Read(p []byte) (int, error) {
	if r.off >= len(r.data) {
		return 0, r.err
	}
	n := copy(p, r.data[r.off:])
	r.off += n
	return n, nil
}
