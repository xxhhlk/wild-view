// client_test.go kilo 渠道的离线回归：全部用 httptest 假上游，
// **不打真实上游**（AGENTS.md §0.1：验证上游行为只做单发 A/B，能用离线就不打上游）。
package kilo

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newFake 起一个假上游并返回客户端（Base 指向它）。
func newFake(t *testing.T, h http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := NewWithBase(srv.URL)
	return c, srv
}

// ---------------------------------------------------------------------------
// 模型清单：只认上游 isFree 声明
// ---------------------------------------------------------------------------

func TestFetchModelsKeepsOnlyFreeRows(t *testing.T) {
	listing := `{"data":[
		{"id":"kilo-auto/free","name":"Kilo Auto","isFree":true,"context_length":200000,"max_output":32000,
		 "supported_parameters":["tools","reasoning"]},
		{"id":"some-paid-model","name":"Paid","isFree":false,"context_length":100000},
		{"id":"another/free","name":"Another Free","isFree":true,"context_length":64000}
	]}`
	c, _ := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("路径 = %q，want /models", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("清单请求不应带 Authorization，实际 %q", got)
		}
		_, _ = io.WriteString(w, listing)
	})

	models, err := c.FetchModels(nil)
	if err != nil {
		t.Fatalf("FetchModels 出错：%v", err)
	}
	ids := make([]string, 0, len(models))
	for _, m := range models {
		ids = append(ids, m.ID)
	}
	if len(ids) != 2 || ids[0] != "kilo-auto/free" || ids[1] != "another/free" {
		t.Fatalf("免费集合 = %v，want [kilo-auto/free another/free]", ids)
	}
	// 静态目录命中时用静态元数据；未命中时按上游行构造，能力只认显式声明。
	if !models[0].SupportsTools || !models[0].SupportsReasoning {
		t.Errorf("kilo-auto/free 应声明 tools 与 reasoning，实际 %+v", models[0])
	}
	if models[1].ContextWindow != 64000 {
		t.Errorf("another/free 上下文 = %d，want 64000", models[1].ContextWindow)
	}
}

func TestFetchModelsFallsBackToStaticOnError(t *testing.T) {
	c, _ := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	models, err := c.FetchModels(nil)
	if err != nil {
		t.Fatalf("清单失败时应回静态兜底且不报错，实际 err=%v", err)
	}
	if len(models) == 0 || models[0].ID != "kilo-auto/free" {
		t.Fatalf("静态兜底 = %+v，want 至少含 kilo-auto/free", models)
	}
}

// 免密渠道的空能力：这些方法必须**不报错**（面板据此显示「不适用」而不是失败）。
func TestNoAccountCapabilities(t *testing.T) {
	c := New()
	if err := c.RefreshToken(nil); err != nil {
		t.Errorf("RefreshToken 应空实现不报错：%v", err)
	}
	if n, err := c.UserResource(nil); err != nil || n != 0 {
		t.Errorf("UserResource = (%d, %v)，want (0, nil)", n, err)
	}
	if n, items, err := c.UserResourceDetail(nil); err != nil || n != 0 || items != nil {
		t.Errorf("UserResourceDetail = (%d, %v, %v)，want (0, nil, nil)", n, items, err)
	}
	a := AnonymousAuth()
	if a.ExpiresAt != noExpiry {
		t.Errorf("虚拟账号 ExpiresAt = %d，want %d（远期值，否则 NeedsRefresh 恒真）", a.ExpiresAt, noExpiry)
	}
	if a.UID != AnonymousUID {
		t.Errorf("虚拟账号 UID = %q，want %q", a.UID, AnonymousUID)
	}
}
