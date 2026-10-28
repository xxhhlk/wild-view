package officeace

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"wild-view/internal/auth"

	"wild-view/internal/provider"
)

// 推理预算常量（对齐 officeace2api/src/config.mjs）：
// 上游把 max_tokens 当成「推理 + 正文」的总预算，推理模型的思维链会先把预算吃光 ⇒ 正文空。
const (
	tinyMaxTokens = 16     // 小于此值原样透传
	defaultMinMax = 1024   // 推理预算追加量
	maxTokensCeil = 393216 // 全局上限
)

// glm53NoThinking：glm-5.3 系不吃 thinking 参数，需剥离后重试。
var glm53NoThinking = map[string]bool{
	"glm-5.3": true, "glm-5.3-test": true, "glm-5.3-zx": true, "glm-5-test": true,
}

// stripWhenUnsupported 上游不认这些参数时剥离（glm-5.3 系）。
// 注意：**不含 stream_options** —— 它是本工具记账所需的 include_usage 载体，
// 剥掉会让该模型 token 流水恒 0（见下方 rewriteBody 的注入顺序）。
var stripWhenUnsupported = []string{"thinking", "enable_thinking", "reasoning_effort"}

// fetchModels 拉取上游 /models（OpenAI 形状），映射为 provider.ModelInfo。
// 失败时返回错误（调用方回退本地静态表）。
func (c *Client) fetchModels(a *auth.Auth) ([]provider.ModelInfo, error) {
	ac := accountFromAuth(a)
	if ac.BaseURL == "" {
		return nil, fmt.Errorf("officeace: missing base_url")
	}
	req, err := http.NewRequest(http.MethodGet, ac.modelsURL(), nil)
	if err != nil {
		return nil, err
	}
	if ac.Authorization != "" {
		req.Header.Set("Authorization", ac.Authorization)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		rb, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("officeace: models HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(rb)))
	}
	var data struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	out := make([]provider.ModelInfo, 0, len(data.Data))
	for _, d := range data.Data {
		id := strings.TrimSpace(d.ID)
		if id == "" {
			continue
		}
		// 上游 /models 只回 id，无任何能力声明 ⇒ 能力字段一律保持零值（未知），
		// 不得编造 SupportsTools/SupportsReasoning（R49 / ContextFromAPI 哲学：
		// 上游声明了才透出，未声明不编造）。
		out = append(out, provider.ModelInfo{
			ID:   id,
			Name: id,
		})
	}
	return out, nil
}
