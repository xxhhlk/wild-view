package workbuddyai

// body14003 上游瞬时拥塞限流的真实响应体（2026-10-12 生产日志实测，节选判定字段）。
// 关键在结构化字段 `retry_policy.retry=true`：这是上游的协议级承诺「这个错误可以重试」。
const body14003 = `{"code":14003,"msg":"too many requests","requestId":"r1",` +
	`"retry_policy":{"retry":true},` +
	`"displayMsg":{"en":"Too many requests. Please retry later."},` +
	`"displayTips":{"en":"Wait about a minute before retry, or switch to a less busy model."}}`

// body14003Actions 同一 14003 的上游改版形态（2026-10-17 生产日志实测）：
// retry_policy 字段消失，可重试语义改由 actions 数组表达（含 "RETRY"）。
const body14003Actions = `{"code":14003,"msg":"too many requests","requestId":"r1",` +
	`"displayMsg":{"en":"Model busy. Please switch models or retry later"},` +
	`"displayTips":{"en":"This model is currently saturated. Please switch to another model or wait a moment and retry."},` +
	`"actions":["SWITCH_MODEL","SUBMIT_FEEDBACK","RETRY"]}`
