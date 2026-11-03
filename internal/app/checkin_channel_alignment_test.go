package app

// checkin_channel_alignment_test.go 锁死「无手动签到渠道」两份名单的同口径。
//
// 名单存在两处，作用不同但必须一致：
//
//	internal/app/app.go        noExplicitCheckin   → 拒绝 /api/account/checkin(…all)，批量签到跳过
//	cmd/wild-view/web/app.js   NO_EXPLICIT_CHECKIN → 面板不显示「签到」按钮
//
// 漂移的两种表现都不会编译报错：
//   - 后端有、前端没有 → 按钮亮着，点了必失败（GLM 曾经如此）
//   - 前端有、后端没有 → 按钮灰着，但 API 仍可打（Qoder 曾经如此）

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestNoExplicitCheckinListsMatch(t *testing.T) {
	goSrc, err := os.ReadFile("app.go")
	if err != nil {
		t.Fatalf("读 app.go 失败（函数改名会让本测试静默失效）：%v", err)
	}
	body := funcBody(string(goSrc), "func noExplicitCheckin(")
	if body == "" {
		t.Fatal("未找到 noExplicitCheckin 函数体")
	}
	backend := map[string]bool{}
	for _, m := range regexp.MustCompile(`provider\.([A-Za-z]+)`).FindAllStringSubmatch(body, -1) {
		backend[strings.ToLower(m[1])] = true
	}

	jsPath := filepath.Join("..", "..", "cmd", "wild-view", "web", "app.js")
	jsSrc, err := os.ReadFile(jsPath)
	if err != nil {
		t.Fatalf("读 %s 失败：%v", jsPath, err)
	}
	m := regexp.MustCompile(`NO_EXPLICIT_CHECKIN\s*=\s*new Set\(\[([^\]]*)\]\)`).FindStringSubmatch(string(jsSrc))
	if m == nil {
		t.Fatal("未找到 NO_EXPLICIT_CHECKIN 定义")
	}
	frontend := map[string]bool{}
	for _, q := range regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(m[1], -1) {
		frontend[q[1]] = true
	}

	if len(backend) == 0 || len(frontend) == 0 {
		t.Fatalf("解析结果为空（后端 %v / 前端 %v）——正则或格式变了", backend, frontend)
	}
	for k := range backend {
		if !frontend[k] {
			t.Errorf("%s 在后端 noExplicitCheckin 里，但面板仍显示签到按钮（点了必失败）", k)
		}
	}
	for k := range frontend {
		if !backend[k] {
			t.Errorf("%s 在面板隐藏了签到按钮，但后端没拦（API 仍可打）", k)
		}
	}
}

// funcBody 取以 prefix 开头的函数的函数体（从签名后的 "{" 到第一个顶格 "}"）。
// 必须跳过签名，否则 `k provider.Kind` 里的 provider.Kind 会被当成一个渠道名。
func funcBody(src, prefix string) string {
	i := strings.Index(src, prefix)
	if i < 0 {
		return ""
	}
	rest := src[i:]
	open := strings.Index(rest, "{")
	if open < 0 {
		return ""
	}
	rest = rest[open+1:]
	if end := strings.Index(rest, "\n}"); end >= 0 {
		return rest[:end]
	}
	return rest
}
