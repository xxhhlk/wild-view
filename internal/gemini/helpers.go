package gemini

import "strings"

// 本地出站体错误码（进 OpenAI 错误体，由 Classify 识别成语义分类）。
const (
	errCodeBadParams     = "bad_params"
	errCodePromptTooLong = "prompt_too_long"
)

// trimSpace 取空白裁剪后的字符串（信封字段值的归一，薄封装便于一处修改）。
func trimSpace(s string) string { return strings.TrimSpace(s) }
