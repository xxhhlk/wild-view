package qwenwork

import (
	"context"
	"fmt"
	"net"
)

// resetAfter 先吐完 payload，再返回一个非空闲类读错误。
type resetAfter struct {
	payload string
	sent    bool
}

func (r *resetAfter) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		return copy(p, r.payload), nil
	}
	return 0, &net.OpError{Op: "read", Err: fmt.Errorf("connection reset by peer")}
}

// 编译期断言：context.Canceled 确实被判为空闲超时（链路上游依赖该判据）。
var _ = context.Canceled
