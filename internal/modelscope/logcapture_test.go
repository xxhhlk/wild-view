package modelscope

import (
	"bytes"
	"log"
	"testing"
)

// captureLog 抓取本包日志输出（默认串行执行，无并发污染）。
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	return &buf
}
