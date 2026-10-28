package upstream

import (
	"errors"
	"io"
)

const sseFixture = "data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1753600000,\"model\":\"glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"你好\"}}]}\n\n" +
	"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1753600000,\"model\":\"glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"，世界\"}}]}\n\n" +
	"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1753600000,\"model\":\"glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":2,\"total_tokens\":7}}\n\n" +
	"data: [DONE]\n\n"

// liveUpstreamSSEFixture 线上实抓的 WorkBuddy 上游形态（2026-09-18，deepseek-v4.1-flash）：
//   - 首帧 role-only，且 finish_reason 是**空字符串**（非 OpenAI 规范的 null）；
//   - 中间帧同样带 finish_reason:""，并夹带空 content/reasoning_content/refusal、
//     空 tool_calls 列表、null function_call、上游私有 extra_fields；
//   - 末帧才给 finish_reason:"stop" + usage；
//   - 显式 [DONE] 收尾。
//
// 该形态是本仓库 normalizeFrame 存在的直接原因（回归锚点，勿删）。
const liveUpstreamSSEFixture = "" +
	`data: {"id":"5fbd114d1bb140658cbd5a90e9b41e05","model":"deepseek-v4.1-flash","object":"chat.completion.chunk","created":1789679930,"choices":[{"index":0,"delta":{"role":"assistant","content":"","reasoning_content":"","function_call":null,"refusal":"","tool_calls":[],"extra_fields":null},"logprobs":null,"finish_reason":""}],"usage":null}` + "\n\n" +
	`data: {"id":"5fbd114d1bb140658cbd5a90e9b41e05","model":"deepseek-v4.1-flash","object":"chat.completion.chunk","created":1789679930,"choices":[{"index":0,"delta":{"content":"Hi","reasoning_content":"","function_call":null,"refusal":"","tool_calls":[],"extra_fields":null},"logprobs":null,"finish_reason":""}],"usage":null}` + "\n\n" +
	`data: {"id":"5fbd114d1bb140658cbd5a90e9b41e05","model":"deepseek-v4.1-flash","object":"chat.completion.chunk","created":1789679930,"choices":[{"index":0,"delta":{"role":"assistant","content":"","reasoning_content":"","function_call":{"name":"","arguments":""},"refusal":"","tool_calls":[],"extra_fields":null},"logprobs":null,"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":10,"total_tokens":15}}` + "\n\n" +
	"data: [DONE]\n\n"

type readErrAfter struct {
	payload string
	err     error
	sent    bool
}

func (r *readErrAfter) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		return copy(p, r.payload), nil
	}
	if r.err != nil {
		return 0, r.err
	}
	return 0, errors.New("simulated: broken pipe")
}

type cutStream struct {
	payload   string
	keep, off int
}

func (r *cutStream) Read(p []byte) (int, error) {
	if r.off >= r.keep {
		return 0, io.EOF
	}
	n := copy(p, r.payload[r.off:r.keep])
	r.off += n
	return n, nil
}
