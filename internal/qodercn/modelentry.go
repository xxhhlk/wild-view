package qodercn

import "encoding/json"

// ModelEntry 上游模型目录条目（只读查询用）。
type ModelEntry struct {
	Key            string  `json:"key"`
	DisplayName    string  `json:"display_name"`
	Enable         bool    `json:"enable"`
	IsReasoning    bool    `json:"is_reasoning"`
	IsVL           bool    `json:"is_vl"`
	MaxInputTokens int64   `json:"max_input_tokens"`
	PriceFactor    float64 `json:"price_factor"`
	ContextWindow  int64   `json:"-"` // context_config 默认档（is_default）的 token_count
	// AvailableWindows context_config 全部档位（升序）；空表示无档位表。
	AvailableWindows []int64 `json:"-"`
	// ThinkingConfig 上游声明的思考能力（档位 ladder / 是否可关闭）。
	// 缺失/非法一律当「未知」：不暴露档位、不降级。
	ThinkingConfig json.RawMessage `json:"thinking_config"`
}
