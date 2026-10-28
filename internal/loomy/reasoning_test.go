// reasoning_test.go 思考档位透传测试（issue #96）。
//
// Loomy 的 /models 为每个模型声明 reasoning_efforts（上游权威档位，实测
// 8 模型一致：none/low/medium/high/xhigh，default=low）——本项目原样透传，
// 不做本地假设。
package loomy
