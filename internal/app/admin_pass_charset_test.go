package app

import (
	"strings"
	"testing"
)

// 面板密码只接受半角可打印 ASCII。全角自动折半角（码位由输入法状态决定，不该让人重打），
// 中文等其余非 ASCII 直接拒绝。

func TestAdminPasswordRejectsNonASCII(t *testing.T) {
	a := catalogTestApp(t)
	for _, pass := range []string{"中文密码", "口令123", "pass·word", "pässword"} {
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

func TestAdminPasswordFoldsFullWidth(t *testing.T) {
	a := catalogTestApp(t)
	const full = "Ｐ＠ｓｓｗ０ｒｄ！"
	const want = "P@ssw0rd!"

	if err := a.SetAdminPassword(full); err != nil {
		t.Fatalf("全角密码应折成半角后被接受：%v", err)
	}
	if got := a.adminPass(); got != want {
		t.Fatalf("存储值 = %q，期望折半角后的 %q", got, want)
	}
	// 全角、半角两种写法都要能登录（保存侧折半角，输入侧同口径）。
	for _, input := range []string{full, want} {
		if _, _, ok, msg := a.panelLogin(input, "127.0.0.1"); !ok {
			t.Errorf("用 %q 登录失败：%s", input, msg)
		}
	}
	// 全角空格折成半角后同样被去首尾。
	if err := a.SetAdminPassword("　padded　"); err != nil {
		t.Fatalf("全角空格应被折成半角再去首尾：%v", err)
	}
	if got := a.adminPass(); got != "padded" {
		t.Fatalf("全角空格未正确处理：%q", got)
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
	if err := a.SetAdminPassword("with space 123"); err != nil {
		t.Fatalf("内部空格应允许：%v", err)
	}
}
