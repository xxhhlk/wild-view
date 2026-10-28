// lobsterai_test.go LobsterAI 渠道的回归测试。
//
// 纪律：**零真实网络请求**——每个用例都用 httptest 起假上游，并把 Client.Base 与
// Client.Version 同时指到它（ClientVersion 会去打版本号接口，漏掉就会真的出网）。
package lobsterai

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"wild-view/internal/auth"
)

// ---------------------------------------------------------------------------
// 测试脚手架
// ---------------------------------------------------------------------------

// upstreamRec 记录假上游收到的每次请求（顺序、方法、路径、query、头、体）。
type upstreamRec struct {
	mu      sync.Mutex
	methods []string
	paths   []string
	queries []url.Values
	headers []http.Header
	bodies  [][]byte
}

func (u *upstreamRec) add(r *http.Request, body []byte) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.methods = append(u.methods, r.Method)
	u.paths = append(u.paths, r.URL.Path)
	u.queries = append(u.queries, r.URL.Query())
	u.headers = append(u.headers, r.Header.Clone())
	u.bodies = append(u.bodies, body)
}

// apiPaths 返回除版本号接口（路径 "/"）之外的请求路径，便于断言调用顺序。
func (u *upstreamRec) apiPaths() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	out := make([]string, 0, len(u.paths))
	for _, p := range u.paths {
		if p == "/" {
			continue
		}
		out = append(out, p)
	}
	return out
}

// last 返回最近一次请求的快照（方法、路径、query、头、体）。
func (u *upstreamRec) last() (string, string, url.Values, http.Header, []byte) {
	u.mu.Lock()
	defer u.mu.Unlock()
	n := len(u.paths)
	if n == 0 {
		return "", "", nil, nil, nil
	}
	n--
	return u.methods[n], u.paths[n], u.queries[n], u.headers[n], u.bodies[n]
}

// byPath 返回指定路径上最近一次请求的 (query, 头, 体)。
func (u *upstreamRec) byPath(path string) (url.Values, http.Header, []byte) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for i := len(u.paths) - 1; i >= 0; i-- {
		if u.paths[i] == path {
			return u.queries[i], u.headers[i], u.bodies[i]
		}
	}
	return nil, nil, nil
}

// count 返回除版本号接口之外的请求次数。
func (u *upstreamRec) count() int { return len(u.apiPaths()) }

// newUpstream 起一个假上游：路径 "/" 当版本号接口，其余交给 api 处理函数。
func newUpstream(t *testing.T, api http.HandlerFunc) (*Client, *upstreamRec) {
	t.Helper()
	rec := &upstreamRec{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rec.add(r, body)
		// 还原 body，让 api 处理函数仍能读（记录与处理共享一次读取）。
		r.Body = io.NopCloser(bytes.NewReader(body))
		if r.URL.Path == "/" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":{"value":{"version":"2026.9.4"}}}`)
			return
		}
		api(w, r)
	}))
	t.Cleanup(srv.Close)
	clearVersionCache()
	c := NewWithBase(srv.URL)
	c.Version = srv.URL // 版本号也走假上游：测试零真实请求
	return c, rec
}

// clearVersionCache 清空进程内版本号缓存（用例之间互不污染）。
func clearVersionCache() {
	versionCache.mu.Lock()
	versionCache.value = ""
	versionCache.expiry = time.Time{}
	versionCache.mu.Unlock()
}

// testAuth 造一个带完整 keyfrom 四元组的假账号（全部是假值，非真实凭据）。
func testAuth() *auth.Auth {
	return &auth.Auth{
		Kind:         string(Kind),
		AccessToken:  "fake-access-token",
		RefreshToken: "fake-refresh-token",
		UID:          "uid-1",
		Nickname:     "测试账号",
		Extra: map[string]string{
			ExtraUUID:          "11111111-2222-3333-4444-555555555555",
			ExtraFirstKeyfrom:  "1700000000000",
			ExtraLatestKeyfrom: "1700000000001",
			ExtraUserID:        "yid-1",
		},
	}
}

func writeJSON(w http.ResponseWriter, code int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = io.WriteString(w, body)
}

// ---------------------------------------------------------------------------
// ChatStream：请求头、body 改写、返回约定
// ---------------------------------------------------------------------------
