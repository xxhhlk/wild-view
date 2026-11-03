package login_loomy

// account_code_test.go 锁死「全零 code 算成功」与「msgid 字段名变体/顶层兜底」。
//
// 这两个判据都踩过：上游对**已真实下发**的短信回 code="000000"（desc="成功"），
// 旧实现只认 "0"/空串，于是整次下发被判失败 —— msgid 不落库，用户点登录时
// 后端查不到下发记录，提示「本机没有查到这次验证码的下发记录」。

import (
	"encoding/json"
	"testing"
)

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
