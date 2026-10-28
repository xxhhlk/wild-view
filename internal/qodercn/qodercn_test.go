package qodercn

import (
	"encoding/json"

	"net/http"

	"testing"

	"wild-view/internal/provider"
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

// TestParseDynamicModelsContextWindow 验证解析端真的把 context_config 读进 ModelEntry。
// 回归：此前 ContextWindow 是 json:"-" 且解析只 Unmarshal 到 ModelEntry，
// context_config 被整段丢弃，导致永远回退到 max_input_tokens（恒为 180000 兜底）。
func TestParseDynamicModelsContextWindow(t *testing.T) {
	raw := map[string]json.RawMessage{
		"assistant": json.RawMessage(`[
			{"key":"m1","display_name":"M1","enable":true,"is_default":true,
			 "is_reasoning":true,"is_vl":true,"max_input_tokens":180000,"price_factor":0.5,
			 "context_config":{"default":{"is_default":true,"token_count":1000000},
			                   "compact":{"is_default":false,"token_count":180000}}},
			{"key":"m2","display_name":"M2","enable":true,"max_input_tokens":96000,"price_factor":0.1},
			{"key":"m3","display_name":"M3","enable":true,"max_input_tokens":180000,
			 "context_config":{"c":{"token_count":500000}}},
			{"key":"m4","display_name":"M4","enable":true,"max_input_tokens":180000,
			 "context_config":[{"token_count":7}]},
			{"key":"m5","display_name":"M5","enable":true,"max_input_tokens":180000,
			 "context_config":{"a":{"is_default":true,"token_count":400000},
			                   "b":{"is_default":true,"token_count":272000}}},
			{"key":"off","display_name":"OFF","enable":false,
			 "context_config":{"d":{"is_default":true,"token_count":9}}}
		]`),
	}
	ms, err := parseDynamicModels(raw)
	if err != nil {
		t.Fatalf("parseDynamicModels: %v", err)
	}
	if len(ms) != 5 {
		t.Fatalf("enabled len = %d, want 5 (off 应被过滤)", len(ms))
	}
	// ① is_default 那项胜出，而不是 max_input_tokens 的 180000
	if ms[0].ContextWindow != 1000000 {
		t.Errorf("is_default token_count not parsed: %+v", ms[0])
	}
	// ② 无 context_config → 0，留给 toModelInfos 回退 max_input_tokens
	if ms[1].ContextWindow != 0 {
		t.Errorf("absent context_config should stay 0: %+v", ms[1])
	}
	// ③ **无 is_default 标记时不猜**：返回 0，让上层回退 max_input_tokens。
	// 回归：曾按 label 字典序取最小项，而字典序首位是 "1M"，会取到**最大档**（不安全方向）。
	if ms[2].ContextWindow != 0 {
		t.Errorf("unmarked context_config must not be guessed: %+v", ms[2])
	}
	// ④ 形状不符（数组）只损失本字段，不应让整批模型解析失败
	if ms[3].Key != "m4" || ms[3].ContextWindow != 0 {
		t.Errorf("malformed context_config should degrade to 0: %+v", ms[3])
	}
	// ⑤ 多个档同时标默认 → 取最小值（确定性 + 保守，不依赖 map 迭代序）
	if ms[4].ContextWindow != 272000 {
		t.Errorf("multi-default should take min: %+v", ms[4])
	}
	// ⑥ 一路落到对外展示的 ContextWindow：宁高勿低 → max(档位表)
	infos := toModelInfos(ms)
	if infos[0].ContextWindow != 1000000 || !infos[0].ContextFromAPI {
		t.Errorf("toModelInfos[0]: %+v", infos[0])
	}
	if infos[1].ContextWindow != 96000 || !infos[1].ContextFromAPI {
		t.Errorf("toModelInfos[1] max_input fallback: %+v", infos[1])
	}
	// 无 is_default 但有档位表 → 广告最大档（而非回退 max_input_tokens=180000）
	if infos[2].ContextWindow != 500000 {
		t.Errorf("toModelInfos[2] should advertise max tier: %+v", infos[2])
	}
}

// TestClassifyOrder 429 优先于 hardMarkers（不变式 15）。
func TestClassifyOrder(t *testing.T) {
	if got := Classify(http.StatusTooManyRequests, `{"isQuotaExceeded":true}`); got != provider.ErrSoftRate {
		t.Errorf("429 + quota text should be soft rate, got %v", got)
	}
	if got := Classify(402, "x"); got != provider.ErrHardCredit {
		t.Errorf("402 should be hard credit, got %v", got)
	}
	if got := Classify(401, `{"code":"TOKEN_EXPIRE"}`); got != provider.ErrSessionDead {
		t.Errorf("TOKEN_EXPIRE should be session dead, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// 思考档位：能力解析 → 投影 → body 字段（realm 为 RealmQoderCN）
// ---------------------------------------------------------------------------

// errAfterReader 先吐完 data，再返回指定错误（模拟「读到一半流断了」）。
type errAfterReader struct {
	data string
	err  error
	off  int
}

func (r *errAfterReader) Read(p []byte) (int, error) {
	if r.off >= len(r.data) {
		return 0, r.err
	}
	n := copy(p, r.data[r.off:])
	r.off += n
	return n, nil
}
