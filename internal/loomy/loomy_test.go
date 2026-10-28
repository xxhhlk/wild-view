package loomy

import (
	"strings"
	"testing"
)

func TestPriceFromName(t *testing.T) {
	cases := map[string]float64{
		"DeepSeek V4 Flash 0731（x3.0）": 3.0,
		"Qwen 3.8 Max (x12.0)":         12.0,
		"Spark X2.5（x0.1）":             0.1,
		"qwen 3.8 flash（x0.8）":         0.8,
		"Kimi k2.6（x6.5）":              6.5,
		"没有倍率":                         0,
	}
	for in, want := range cases {
		if got := priceFromName(in); got != want {
			t.Fatalf("priceFromName(%q) = %v, want %v", in, got, want)
		}
	}
	if got := cleanName("Spark X2.5（x0.1）"); got != "Spark X2.5" {
		t.Fatalf("cleanName = %q", got)
	}
}

// errAfterReader 先吐完 data，再返回指定错误（模拟「读到一半流断了」）。
type errAfterReader struct {
	data string
	err  error
	off  int
}

func (r *errAfterReader) Read(p []byte) (int, error) {
	if r.off >= len(r.data) {
		return 0, r.err
	}
	n := copy(p, r.data[r.off:])
	r.off += n
	return n, nil
}

func TestTraceparentShape(t *testing.T) {
	tp := traceparent()
	parts := strings.Split(tp, "-")
	if len(parts) != 4 || len(parts[0]) != 2 || len(parts[1]) != 32 || len(parts[2]) != 16 || parts[3] != "01" {
		t.Fatalf("traceparent 形状不对: %s", tp)
	}
	if traceparent() == tp {
		t.Fatal("traceparent 应每次不同（缺它会挂死到超时，上游依赖它做链路追踪）")
	}
}
