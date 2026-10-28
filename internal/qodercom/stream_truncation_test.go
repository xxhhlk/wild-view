package qodercom

import (
	"errors"
	"fmt"
	"io"
)

// errStreamTruncatedSentinel 仅为让上面断言写法统一（实现返回的是 errors.New 字面量）。
func errStreamTruncatedSentinel() error { return errors.New("upstream stream truncated") }

// readErrAfter 先吐完 payload，再返回一个读错误（模拟上游连接被掐断 / 读超时）。
type readErrAfter struct {
	payload string
	sent    bool
}

func (r *readErrAfter) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		n := copy(p, r.payload)
		return n, nil
	}
	return 0, fmt.Errorf("simulated: connection reset by peer")
}

// cutReader 只吐 payload 的前 keep 字节后干净 EOF（模拟断在半个帧上）。
type cutReader struct {
	payload string
	keep    int
	off     int
}

func (r *cutReader) Read(p []byte) (int, error) {
	if r.off >= r.keep {
		return 0, io.EOF
	}
	n := copy(p, r.payload[r.off:r.keep])
	r.off += n
	return n, nil
}
