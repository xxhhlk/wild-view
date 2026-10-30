package app

import (
	"errors"
	"strings"
	"testing"
	"time"

	"wild-view/internal/provider"
)

// 授权页由谁打开，决定「点登录却看不到页面」是否会发生（2026-10-30 千问办公实测）。

// openedState 读取本次登录的打开方式与「后端是否已打开」。
func (a *App) openedState() (mode string, backendOpened bool) {
	a.muLogin.Lock()
	defer a.muLogin.Unlock()
	return a.loginOpenMode, a.loginBackendOpened
}

func TestOpenAuthURLForUsesSystemBrowserByDefault(t *testing.T) {
	a := catalogTestApp(t)
	var got string
	a.openURL = func(url string) error { got = url; return nil }

	mode := a.openAuthURLFor(provider.QwenWork, &loginFlow{}, "https://example/oauth2/auth")
	if mode != "system" {
		t.Fatalf("open_mode = %q，期望 system（交给前端 window.open 会被弹窗拦截）", mode)
	}
	if got != "https://example/oauth2/auth" {
		t.Fatalf("未把授权地址交给系统浏览器：%q", got)
	}
	if _, opened := a.openedState(); !opened {
		t.Fatal("backend_opened 应为 true，否则前端会再开一次")
	}
}

func TestOpenAuthURLForKeepsChannelOwnedBrowser(t *testing.T) {
	a := catalogTestApp(t)
	called := false
	a.openURL = func(string) error { called = true; return nil }

	mode := a.openAuthURLFor(provider.Raccoon, &loginFlow{opensBrowser: true}, "https://example/x")
	if mode != "self" || called {
		t.Fatalf("自带浏览器的渠道不该再由后端开一次：mode=%q called=%v", mode, called)
	}
}

func TestOpenAuthURLForFallsBackToFrontendWhenOpenFails(t *testing.T) {
	a := catalogTestApp(t)
	a.openURL = func(string) error { return errors.New("no display") }

	mode := a.openAuthURLFor(provider.QwenWork, &loginFlow{}, "https://example/x")
	if mode != "" {
		t.Fatalf("打开失败时 open_mode 应为空串（前端显示可点链接兜底），得 %q", mode)
	}
	if _, opened := a.openedState(); opened {
		t.Fatal("打开失败却标记 backend_opened，前端会既不开页面也不给链接")
	}
}

func TestStartLoginBusyNamesRunningChannel(t *testing.T) {
	a := catalogTestApp(t)
	a.muLogin.Lock()
	a.loginBusy = true
	a.loginKind = provider.QwenWork
	a.loginStartedAt = time.Now().Add(-90 * time.Second)
	a.muLogin.Unlock()

	_, err := a.StartLoginFor(string(provider.WorkBuddy))
	if err == nil {
		t.Fatal("忙碌时应返回错误")
	}
	msg := err.Error()
	for _, want := range []string{"qwenwork", "登录流程已在进行中", "取消"} {
		if !strings.Contains(msg, want) {
			t.Errorf("提示缺少 %q：%s", want, msg)
		}
	}
}
