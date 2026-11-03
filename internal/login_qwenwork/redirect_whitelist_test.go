package login_qwenwork

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestBuildAuthURLUsesRegisteredClient 守门 OAuth 客户端与回调地址的一致性。
//
// 2026-11 起官方客户端 qwenwork-desktop-app 的 redirect_uri 白名单只剩
// https://gateway.qwenwork.cn/oauth/callback，自建回环一律 invalid_request；
// 该官方回调页又会校验桌面端会话，粘回来的 code 已被判 INVALID_OAUTH_CALLBACK。
// 因此登录改为「动态注册 public client + 本机回环回调」：
// 授权 URL 里的 client_id 必须是注册返回值，redirect_uri 必须与注册值逐字一致，
// 否则授权页会再次报 invalid_request。
func TestBuildAuthURLUsesRegisteredClient(t *testing.T) {
	const cid = "61f06437-a70b-4048-a5f5-76942e44f964"
	const redirect = "http://127.0.0.1:17863/callback"

	raw := buildAuthURL(cid, redirect, "challenge-value", "state-value")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("auth url 解析失败: %v", err)
	}
	q := u.Query()
	if got := q.Get("client_id"); got != cid {
		t.Fatalf("client_id = %q，期望注册返回的 %q", got, cid)
	}
	if got := q.Get("redirect_uri"); got != redirect {
		t.Fatalf("redirect_uri = %q，期望 %q", got, redirect)
	}
	if q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" {
		t.Errorf("授权参数不完整: %v", q)
	}
	if len(q.Get("state")) < 8 {
		t.Errorf("state 太短（服务端要求 ≥8 字符）: %q", q.Get("state"))
	}
	if q.Get("scope") == "" {
		t.Error("缺少 scope")
	}
}

// TestRegisterClientParsesClientID 动态注册：从响应里取 client_id。
func TestRegisterClientParsesClientID(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		buf, _ := io.ReadAll(r.Body)
		gotBody = string(buf)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"client_id":"cid-123","redirect_uris":["http://127.0.0.1:17863/callback"]}`))
	}))
	defer srv.Close()

	cid, err := registerClient(srv.Client(), srv.URL, "http://127.0.0.1:17863/callback")
	if err != nil {
		t.Fatalf("registerClient: %v", err)
	}
	if cid != "cid-123" {
		t.Fatalf("client_id = %q", cid)
	}
	if !strings.Contains(gotBody, "authorization_code") || !strings.Contains(gotBody, "17863") {
		t.Errorf("注册请求体不符合预期: %s", gotBody)
	}
}

// TestRegisterClientRejectsError 注册失败要带出上游原因，不能静默返回空 client_id。
func TestRegisterClientRejectsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_client_metadata"}`))
	}))
	defer srv.Close()

	if _, err := registerClient(srv.Client(), srv.URL, "http://127.0.0.1:17863/callback"); err == nil {
		t.Fatal("应返回错误")
	} else if !strings.Contains(err.Error(), "400") {
		t.Errorf("错误信息未带状态码: %v", err)
	}
}

// TestSubmitCallbackURLParsesCode 粘贴兜底：能从回调地址里取出 code。
func TestSubmitCallbackURLParsesCode(t *testing.T) {
	pending.reset()
	raw := "http://127.0.0.1:17863/callback?code=abc123&state=xyz"
	if err := SubmitCallbackURL(raw); err != nil {
		t.Fatalf("SubmitCallbackURL: %v", err)
	}
	code, errMsg, ok := pending.snapshot()
	if !ok || code != "abc123" {
		t.Fatalf("code=%q ok=%v err=%q", code, ok, errMsg)
	}
}

// TestSubmitCallbackURLReportsOAuthError 授权页带回 error 时要如实透传，不能当成成功。
func TestSubmitCallbackURLReportsOAuthError(t *testing.T) {
	pending.reset()
	err := SubmitCallbackURL("http://127.0.0.1:17863/callback?error=access_denied&error_description=user+canceled")
	if err == nil {
		t.Fatal("应返回错误")
	}
	if !strings.Contains(err.Error(), "user canceled") {
		t.Errorf("错误信息未透传: %v", err)
	}
}
