package app

import (
	"strings"
	"testing"
)

// 面板密码只接受可打印 ASCII：中文/全角在不同输入法下码位不同，登录时容易输错。

func TestAdminPasswordRejectsNonASCII(t *testing.T) {
	a := catalogTestApp(t)
	for _, pass := range []string{"中文密码", "ｐａｓｓｗｏｒｄ", "pass·word", "pässword", "口令123"} {
		err := a.SetAdminPassword(pass)
		if err == nil {
			t.Errorf("%q 应被拒绝", pass)
			continue
		}
		if !strings.Contains(err.Error(), "半角符号") {
			t.Errorf("%q 的报错没说明字符集限制：%v", pass, err)
		}
	}
	if got := a.adminPass(); got != "" {
		t.Fatalf("被拒绝的密码不应写入：%q", got)
	}
}

func TestAdminPasswordAcceptsPrintableASCII(t *testing.T) {
	a := catalogTestApp(t)
	const pass = "P@ssw0rd-!#$%^&*()_+[]{};:,.?/"

	if err := a.SetAdminPassword(pass); err != nil {
		t.Fatalf("合法 ASCII 密码被拒：%v", err)
	}
	if got := a.adminPass(); got != pass {
		t.Fatalf("读回 %q，期望 %q", got, pass)
	}
	if _, _, ok, msg := a.panelLogin(pass, "127.0.0.1"); !ok {
		t.Fatalf("ASCII 密码登录失败：%s", msg)
	}
	// 内部空格允许，首尾空格仍会被裁剪。
	if err := a.SetAdminPassword("with space 123"); err != nil {
		t.Fatalf("内部空格应允许：%v", err)
	}
}
