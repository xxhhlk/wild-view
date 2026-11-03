package login_loomy

// account_code_test.go 锁死「全零 code 算成功」与「msgid 字段名变体/顶层兜底」。
//
// 这两个判据都踩过：上游对**已真实下发**的短信回 code="000000"（desc="成功"），
// 旧实现只认 "0"/空串，于是整次下发被判失败 —— msgid 不落库，用户点登录时
// 后端查不到下发记录，提示「本机没有查到这次验证码的下发记录」。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

type localAccountTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (tr localAccountTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	local := req.Clone(req.Context())
	local.URL.Scheme = tr.target.Scheme
	local.URL.Host = tr.target.Host
	return tr.base.RoundTrip(local)
}

func accountTestClient(t *testing.T, path string, status int, response string) *http.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != path {
			t.Errorf("意外请求：%s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected request", http.StatusNotFound)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(server.Close)
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Transport: localAccountTransport{target: target, base: server.Client().Transport}}
}

func TestSendCodeResponseHandling(t *testing.T) {
	const secret = "0123456789abcdef0123456789abcdef"
	cases := []struct {
		name, response, wantID, wantError string
		status                            int
	}{
		{"非零代码带 msgid", `{"code":"020002","desc":"异常","data":{"msgid":"message-id"}}`, "message-id", "", http.StatusOK},
		{"非零代码无 msgid", `{"code":"020002","desc":"请求失败 ` + secret + `"}`, "", "请求失败 [已隐去]", http.StatusOK},
		{"成功代码无 msgid", `{"code":"000000","desc":"成功"}`, "", "短信验证码响应缺少 msgid", http.StatusOK},
		{"HTTP 错误带 msgid", `{"code":"0","data":{"msgid":"message-id"}}`, "", "HTTP 500", http.StatusInternalServerError},
		{"HTTP 错误带非零代码和 msgid", `{"code":"020002","desc":"服务错误 ` + secret + `","data":{"msgid":"message-id"}}`, "", "服务错误 [已隐去]", http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := accountTestClient(t, epSendCode, tc.status, tc.response)
			s := &session{ph: PhasePending, sendErr: "旧错误"}
			id, err := s.sendCode(client, "13800138000")
			if id != tc.wantID {
				t.Fatalf("msgid = %q，期望 %q", id, tc.wantID)
			}
			if tc.wantError == "" {
				if err != nil || !s.sendDone || s.storedMsgID() != tc.wantID || s.status().SendErr != "" {
					t.Fatalf("成功后会话状态不符：err=%v sendDone=%t msgid=%q 状态=%+v", err, s.sendDone, s.storedMsgID(), s.status())
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantError) || strings.Contains(err.Error(), secret) {
				t.Fatalf("错误信息不符或未脱敏：%v", err)
			}
			if s.sendDone || s.storedMsgID() != "" || s.status().Phase != PhasePending || !strings.Contains(s.status().SendErr, tc.wantError) || strings.Contains(s.status().SendErr, secret) {
				t.Fatalf("失败后会话状态不符：sendDone=%t msgid=%q 状态=%+v", s.sendDone, s.storedMsgID(), s.status())
			}
		})
	}
}

func TestLoginRejectsNonzeroCodeEvenWithSession(t *testing.T) {
	client := accountTestClient(t, epCheckCode, http.StatusOK, `{"code":"020002","desc":"验证码错误","data":{"session":"0123456789abcdef0123456789abcdef"}}`)
	s := &session{ph: PhasePending}
	result, err := s.login(client, "13800138000", "123456", "message-id")
	if err == nil || !strings.Contains(err.Error(), "验证码错误") {
		t.Fatalf("登录应返回上游错误，得到 %v", err)
	}
	if result.AccessToken != "" || s.status().Phase != PhasePending || s.status().HasSession {
		t.Fatalf("登录失败不得保存 session：result=%+v 状态=%+v", result, s.status())
	}
}

func TestAccountEnvelopeAcceptsAllZeroCode(t *testing.T) {
	for _, c := range []string{`"0"`, `"000000"`, `0`, `""`, `null`} {
		var e accountEnvelope
		if err := json.Unmarshal([]byte(`{"code":`+c+`,"desc":"成功"}`), &e); err != nil {
			t.Fatalf("解析 %s 失败：%v", c, err)
		}
		if !e.ok() {
			t.Errorf("code=%s 应判为成功（全零形态都是成功）", c)
		}
	}
	for _, c := range []string{`"020002"`, `"40001"`, `"1"`} {
		var e accountEnvelope
		if err := json.Unmarshal([]byte(`{"code":`+c+`}`), &e); err != nil {
			t.Fatal(err)
		}
		if e.ok() {
			t.Errorf("code=%s 不应判为成功", c)
		}
	}
}

func TestPickMsgIDVariants(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"data.msgid", `{"msgid":"m1"}`, "m1"},
		{"data.msgId", `{"msgId":"m2"}`, "m2"},
		{"data.msg_id", `{"msg_id":"m3"}`, "m3"},
		{"data.MsgID", `{"MsgID":"m4"}`, "m4"},
		{"顶层兜底", `{"msgid":"m5"}`, "m5"},
	}
	for _, c := range cases {
		if got := pickMsgID(nil, []byte(c.raw)); got != c.want {
			t.Errorf("%s：pickMsgID = %q，期望 %q", c.name, got, c.want)
		}
	}
	// 空值与非法 JSON 都返回空串（调用方据此报「响应缺少 msgid」）。
	if got := pickMsgID(nil, []byte(`{"msgid":""}`)); got != "" {
		t.Errorf("空 msgid 应返回空串，得到 %q", got)
	}
	if got := pickMsgID([]byte("not-json"), []byte("not-json")); got != "" {
		t.Errorf("非法 JSON 应返回空串，得到 %q", got)
	}
}
