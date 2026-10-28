// traework/traecode 模型元数据解析回归（PR #108）：上游 get_detail_param 下发的
// 上下文/输出上限/多模态必须进 /v1/models；且元数据形状突变不得拖垮整张目录。
package traework

import (
	"wild-view/internal/provider"
)

// byID 模型表按 ID 建索引，便于逐条断言。
func byID(list []provider.ModelInfo) map[string]provider.ModelInfo {
	m := map[string]provider.ModelInfo{}
	for _, x := range list {
		m[x.ID] = x
	}
	return m
}
