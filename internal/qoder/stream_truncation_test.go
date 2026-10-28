package qoder

import (
	"errors"
)

// errStreamTruncatedSentinel 仅为让上面断言写法统一（实现返回的是 errors.New 字面量）。
func errStreamTruncatedSentinel() error { return errors.New("upstream stream truncated") }

// readErrAfter 先吐完 payload，再返回一个读错误（模拟上游连接被掐断 / 读超时）。
type readErrAfter struct {
	payload string
	sent    bool
}

// cutReader 只吐 payload 的前 keep 字节后干净 EOF（模拟断在半个帧上）。
type cutReader struct {
	payload string
	keep    int
	off     int
}
