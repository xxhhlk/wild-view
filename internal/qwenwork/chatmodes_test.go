package qwenwork

// 全量模型获取：/api/chat-modes 优先，且要能吃下「非信封」的原始 JSON。
// 网关 /api/v2/model/list 只有三个档位，拿不到 DeepSeek / Kimi / GLM / MiniMax。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"wild-view/internal/auth"
)

func TestFetchModelsPrefersChatModesRawJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != EpChatModes {
			t.Errorf("应请求 %s，实际 %s", EpChatModes, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		// 上游直接返回原始 JSON，不是 {code,data} 信封。
		_ = json.NewEncoder(w).Encode(map[string][]map[string]any{
			"qwork": {
				{"key": "qwen4-max", "enable": true, "is_vl": true, "is_reasoning": true,
					"max_input_tokens": 1_000_000,
					"i18n":             map[string]any{"display_name": map[string]string{"zh": "Qwen4-Max"}}},
				{"key": "deepseek-flash", "enable": true, "max_input_tokens": 128_000},
				{"key": "disabled-one", "enable": false},
			},
		})
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client(), Web: srv.URL}
	got, err := c.FetchModels(&auth.Auth{AccessToken: "tok"})
	if err != nil {
		t.Fatalf("FetchModels: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("应返回 2 个启用模型，实际 %d：%+v", len(got), got)
	}
	if got[0].ID != "qwen4-max" || got[0].Name != "Qwen4-Max" {
		t.Fatalf("首条不符：%+v", got[0])
	}
	// 1M 声明按网关实测 180K 封顶。
	if got[0].ContextWindow != 180_000 {
		t.Fatalf("上下文应封顶 180000，实际 %d", got[0].ContextWindow)
	}
	if !got[0].SupportsImages || !got[0].SupportsReasoning {
		t.Fatalf("能力声明未透出：%+v", got[0])
	}
	if got[1].ContextWindow != 128_000 || !got[1].ContextFromAPI {
		t.Fatalf("上游给的上下文应原样保留：%+v", got[1])
	}
}

func TestDoBearerAcceptsRawJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"qwork":[{"key":"pro","enable":true}]}`))
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client(), Web: srv.URL}
	raw, err := c.doBearer(srv.URL+"/x", &auth.Auth{AccessToken: "tok"})
	if err != nil {
		t.Fatalf("原始 JSON 不该报解析错：%v", err)
	}
	if len(raw) == 0 {
		t.Fatal("应返回原始 JSON")
	}
}

func TestStaticModelKeysAliases(t *testing.T) {
	cases := map[string]string{
		"deepseek-v4.1-flash": "deepseek-flash",
		"kimi":                "kimi-k3.1-pro",
		"minimax":             "minimax-m3.1-flash",
		"step-5":              "step-5-preview",
		"unknown-model":       "unknown-model",
	}
	for in, want := range cases {
		if got := ModelKey(in); got != want {
			t.Errorf("ModelKey(%q)=%q，期望 %q", in, got, want)
		}
	}
}
