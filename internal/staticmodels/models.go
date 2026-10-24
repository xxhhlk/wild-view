// Package staticmodels 渠道静态模型兜底表。
//
// 上游目录接口不可用时，查看器仍需展示已知模型；这里只保留纯数据，
// 不依赖任何转发实现。
package staticmodels

import "wild-work/internal/provider"

// WorkBuddy 静态兜底表无上游能力数据，故不声明能力。
var workbuddy = []provider.ModelInfo{
	{ID: "glm-5.2", ContextWindow: 131072}, {ID: "glm-5.1", ContextWindow: 131072}, {ID: "glm-5v-turbo", ContextWindow: 131072},
	{ID: "kimi-k2.7", ContextWindow: 131072}, {ID: "minimax-m3", ContextWindow: 131072}, {ID: "hy3", ContextWindow: 131072},
	{ID: "hy3-preview", ContextWindow: 131072}, {ID: "hy3-preview-agent", ContextWindow: 131072},
	{ID: "deepseek-v4-pro", ContextWindow: 131072}, {ID: "deepseek-v4-flash", ContextWindow: 131072},
}

// TraeWork 静态兜底表同样无上游能力数据，不声明能力。
var traework = []provider.ModelInfo{
	{ID: "glm-5.2"}, {ID: "glm-5-turbo"}, {ID: "glm-5"}, {ID: "DeepSeek-V4-Pro"}, {ID: "DeepSeek-V4-Flash"},
	{ID: "kimi-k2.6"}, {ID: "kimi-k2.7-code"}, {ID: "minimax-m3"}, {ID: "qwen3-coder"}, {ID: "Doubao-Seed-2.1-Pro"},
}

// TraeCode 静态兜底表：TraeCode（solo_agent）与 TraeWork（solo_work_lite）
// 下发的模型集不同，此表按 solo_agent 分组的实际下发清单整理（2026-09 实测）。
var traecode = []provider.ModelInfo{
	{ID: "Doubao-Seed-2.1-Pro"}, {ID: "Doubao-Seed-Evolving"}, {ID: "Doubao-Seed-2.1-Turbo"},
	{ID: "Doubao-Seed-Code"}, {ID: "DeepSeek-V4-Flash-Official"}, {ID: "DeepSeek-V4-Pro-Official"},
	{ID: "deepseek-v4.1-flash"}, {ID: "glm-5.3-flash"}, {ID: "glm-5.3"}, {ID: "glm-5.2"},
	{ID: "kimi-k3"}, {ID: "kimi-k2.8-preview"}, {ID: "minimax-m3"}, {ID: "qwen3.8-flash"},
	{ID: "qwen3.8-max"}, {ID: "qwen-3.7-plus"},
}

// WorkBuddy 返回 WorkBuddy(CodeBuddy) 国内版的静态兜底模型表（副本）。
func WorkBuddy() []provider.ModelInfo {
	return append([]provider.ModelInfo{}, workbuddy...)
}

// TraeWork 返回 TraeWork 的静态兜底模型表（副本）。
func TraeWork() []provider.ModelInfo {
	return append([]provider.ModelInfo{}, traework...)
}

// TraeCode 返回 TraeCode 的静态兜底模型表（副本）。
func TraeCode() []provider.ModelInfo {
	return append([]provider.ModelInfo{}, traecode...)
}
