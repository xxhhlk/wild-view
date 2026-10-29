package app

import (
	"path/filepath"
	"testing"
	"time"

	"wild-view/internal/config"
	"wild-view/internal/provider"
)

func catalogTestApp(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.StateFile = filepath.Join(dir, "state.json")
	cfg.AuthDir = dir
	a, err := New(Options{ConfigPath: filepath.Join(dir, "config.json"), Config: cfg, Runtimes: map[provider.Kind]*Runtime{}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(a.Close)
	return a
}

// TestCatalogRefreshQueueKeepsManualRequest 验证手动强制刷新不会撞上正在跑的自动刷新。
func TestCatalogRefreshQueueKeepsManualRequest(t *testing.T) {
	a := catalogTestApp(t)

	started := make(chan bool, 1)
	release := make(chan bool)
	var order []bool
	a.refreshFn = func(force bool) {
		order = append(order, force)
		started <- true
		<-release
	}

	first := a.RequestCatalogRefresh(false)
	<-started
	second := a.RequestCatalogRefresh(true)
	if running, done := a.CatalogRefreshState(); !running || done != 0 {
		t.Fatalf("排队期间状态错误: running=%v done=%d", running, done)
	}
	release <- true
	release <- true

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		running, done := a.CatalogRefreshState()
		if !running && done >= second {
			a.pricingMu.Lock()
			defer a.pricingMu.Unlock()
			if len(order) != 2 || order[0] != false || order[1] != true {
				t.Fatalf("刷新顺序错误，期望先自动后强制: %v", order)
			}
			if first >= second {
				t.Fatalf("任务号未递增: first=%d second=%d", first, second)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	running, done := a.CatalogRefreshState()
	t.Fatalf("等待排队的强制刷新超时: running=%v done=%d order=%v", running, done, order)
}

// TestFeesInfoExposesCatalogRefreshState 前端需区分“仍在后台刷新”和“已经显示最终结果”。
func TestFeesInfoExposesCatalogRefreshState(t *testing.T) {
	a := catalogTestApp(t)
	a.pricingMu.Lock()
	a.pricingBusy, a.pricingDone = true, 7
	a.pricingMu.Unlock()
	result := a.FeesInfo()
	if result["refresh_running"] != true || result["refresh_done"] != uint64(7) {
		t.Fatalf("刷新状态未下发: running=%v done=%v", result["refresh_running"], result["refresh_done"])
	}
}

// TestCatalogRefreshPanicReleasesQueue panic 后的任务必须释放运行状态，避免面板永久显示刷新中。
func TestCatalogRefreshPanicReleasesQueue(t *testing.T) {
	a := catalogTestApp(t)
	a.refreshFn = func(bool) { panic("boom") }
	id := a.RequestCatalogRefresh(true)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		running, done := a.CatalogRefreshState()
		if !running && done == id {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	running, done := a.CatalogRefreshState()
	t.Fatalf("panic 后队列未释放: running=%v done=%d", running, done)
}
