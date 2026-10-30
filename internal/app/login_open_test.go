package app

import (
	"strings"
	"testing"
	"time"

	"wild-view/internal/provider"
)

// 登录授权页的「由谁打开」与「忙锁提示」。
//
// 2026-10-30 千问办公报「点登录没页面」：日志证实授权页照常打开、只是 5 分钟内没完成
// 授权（21:33 / 21:50 两次超时；23:37 同一份代码 54 秒登录成功）。
// 真正缺的是忙锁提示不清楚、取消入口不可达 —— 最初「弹窗被拦截」的判断不成立，已回退。

// openedState 读取本次登录的打开方式与「后端是否已打开」。
func (a *App) openedState() (mode string, backendOpened bool) {
	a.muLogin.Lock()
	defer a.muLogin.Unlock()
	return a.loginOpenMode, a.loginBackendOpened
}

func TestOpenAuthURLForLeavesNormalChannelsToFrontend(t *testing.T) {
	a := catalogTestApp(t)

	mode := a.openAuthURLFor(provider.QwenWork, &loginFlow{}, "https://example/oauth2/auth")
	if mode != "" {
		t.Fatalf("open_mode = %q，期望空串（普通渠道仍由前端 window.open）", mode)
	}
	if _, opened := a.openedState(); opened {
		t.Fatal("后端没打开却标记 backend_opened，前端就不会再开授权页了")
	}
}

func TestOpenAuthURLForKeepsChannelOwnedBrowser(t *testing.T) {
	a := catalogTestApp(t)

	mode := a.openAuthURLFor(provider.Raccoon, &loginFlow{opensBrowser: true}, "https://example/x")
	if mode != "self" {
		t.Fatalf("自带浏览器的渠道应回报 self，得 %q", mode)
	}
	if _, opened := a.openedState(); !opened {
		t.Fatal("self 必须标记 backend_opened，否则前端会再开一个没有调试端口的标签页")
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
	for _, want := range []string{"qwenwork", "登录流程已在进行中", "1m30s", "取消"} {
		if !strings.Contains(msg, want) {
			t.Errorf("提示缺少 %q：%s", want, msg)
		}
	}
}
