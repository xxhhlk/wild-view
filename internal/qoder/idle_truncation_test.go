package qoder

// resetAfter 先吐完 payload，再返回一个非空闲类读错误。
type resetAfter struct {
	payload string
	sent    bool
}
