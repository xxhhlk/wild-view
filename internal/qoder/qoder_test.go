package qoder

import (
	"encoding/json"

	"testing"
)

func TestNormalizeModelName(t *testing.T) {
	cases := map[string]string{
		"Qwen3.8-Max":      "qwen3.8-max",
		"DeepSeek-V4-Pro":  "deepseek-v4-pro",
		"GLM-5.3":          "glm-5.3",
		"Kimi-K2.7-Code":   "kimi-k2.7-code",
		"MiniMax-M2.7":     "minimax-m2.7",
		"Auto":             "auto",
		"Qwen3.8 Max Test": "qwen3.8-max-test",
	}
	for in, want := range cases {
		if got := NormalizeModelName(in); got != want {
			t.Errorf("NormalizeModelName(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestParseContextOptions 上游 context_config → 升序去重的档位列表。
func TestParseContextOptions(t *testing.T) {
	raw := json.RawMessage(`{"1M":{"token_count":1000000},"200K":{"token_count":200000,"is_default":true},"400K":{"token_count":400000}}`)
	got := parseContextOptions(raw)
	want := []int64{200000, 400000, 1000000}
	if len(got) != len(want) {
		t.Fatalf("档位 = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("档位[%d] = %d, want %d（应升序）", i, got[i], want[i])
		}
	}
	if opts := parseContextOptions(nil); opts != nil {
		t.Errorf("无 context_config 应为空，得到 %v", opts)
	}
	if opts := parseContextOptions(json.RawMessage(`{}`)); len(opts) != 0 {
		t.Errorf("空对象应为空，得到 %v", opts)
	}
}

// errAfterReader 先吐完 data，再返回指定错误（模拟「读到一半流断了」）。
type errAfterReader struct {
	data string
	err  error
	off  int
}
