package traework

import (
	"strings"

	"testing"
)

// TestFunctionForModelBuiltInFallback 内置兜底：目录尚未拉取时，已知 solo_agent
// 池专属模型也能按归属池调用——否则新账号首日列表拉取失败前这些模型恒 4001。
func TestFunctionForModelBuiltInFallback(t *testing.T) {
	for model, want := range map[string]string{
		"qwen3.8-flash": FunctionCode,
		"glm-5.3-flash": FunctionCode,
	} {
		if got := functionForModel(model); got != want {
			t.Fatalf("functionForModel(%q) = %q, want %q", model, got, want)
		}
	}
	if got := functionForModel("totally-unknown-model"); got != "" {
		t.Fatalf("未登记的模型应返回空串（沿用渠道默认），实际 %q", got)
	}
}

// TestOtherFunction 双池枚举完备：两个 function 互为另一池，无第三种取值。
func TestOtherFunction(t *testing.T) {
	if otherFunction(Function) != FunctionCode || otherFunction(FunctionCode) != Function {
		t.Fatalf("otherFunction 映射错误：%q→%q / %q→%q", Function, otherFunction(Function), FunctionCode, otherFunction(FunctionCode))
	}
	if strings.Contains(otherFunction("whatever"), "remote") {
		t.Fatal("定价分组名（*_remote）不是池 function")
	}
}
