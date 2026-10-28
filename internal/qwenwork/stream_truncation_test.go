package qwenwork

import (
	"fmt"
	"io"
)

type qwReadErr struct {
	payload string
	sent    bool
}

func (r *qwReadErr) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		return copy(p, r.payload), nil
	}
	return 0, fmt.Errorf("simulated: connection reset")
}

type qwCut struct {
	payload   string
	keep, off int
}

func (r *qwCut) Read(p []byte) (int, error) {
	if r.off >= r.keep {
		return 0, io.EOF
	}
	n := copy(p, r.payload[r.off:r.keep])
	r.off += n
	return n, nil
}
