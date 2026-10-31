package oplog_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/basalt-go/basalt/pkg/kernel/app"
	"github.com/basalt-go/basalt/pkg/modules/identity"
	"github.com/basalt-go/basalt/pkg/modules/oplog"
	"github.com/basalt-go/basalt/pkg/stores/gormstore"
)

func openGORM(t *testing.T) (*gormstore.Stores, error) {
	t.Helper()
	return gormstore.Open(":memory:")
}

// newFixture 装配 identity + oplog（采集中间件手动包 router——等价
// serve.go 的 App.UseHandler 注入效果）。
func newFixture(t *testing.T, mode string, store oplog.Store) (http.Handler, *oplog.Module, *identity.Module) {
	t.Helper()
	a := app.New(nil)
	idm := identity.New()
	m := oplog.NewWithStore(mode, store)
	_ = a.Registry().Register(idm)
	_ = a.Registry().Register(m)
	mods, err := a.Registry().Resolve()
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range mods {
		if err := x.Init(context.Background(), a); err != nil {
			t.Fatalf("init %s: %v", x.Name(), err)
		}
		if err := x.Start(context.Background()); err != nil {
			t.Fatalf("start %s: %v", x.Name(), err)
		}
	}
	// 手动包装（UseHandler 在 Run 时才叠加，测试等价直包）。
	return m.Middleware(a.Router().Handler()), m, idm
}

func do(h http.Handler, method, path, token string) *httptest.ResponseRecorder {
	var req *http.Request
	if method == http.MethodPost {
		req = httptest.NewRequest(method, path, strings.NewReader("{}"))
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	req.Header.Set("User-Agent", "test-agent/1.0")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func adminToken(t *testing.T, h http.Handler) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/identity/login",
		strings.NewReader(`{"username":"admin","password":"Basalt@2026"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "test-agent/1.0")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var env struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	if env.Data.Token == "" {
		t.Fatalf("login: %d %s", rec.Code, rec.Body.String())
	}
	return env.Data.Token
}

// flushWait 等异步聚合器消费（批量窗口 100ms + 余量）。
func flushWait() { time.Sleep(350 * time.Millisecond) }

func queryLogs(t *testing.T, h http.Handler, token, qs string) oplog.PageResult {
	t.Helper()
	rec := do(h, http.MethodGet, "/api/v1/oplog/logs"+qs, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("query logs: %d %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Data oplog.PageResult `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	return env.Data
}

func TestMiddlewareCollectFullFields(t *testing.T) {
	h, m, _ := newFixture(t, oplog.ModeAll, nil)
	tok := adminToken(t, h)

	// 业务请求（404 路径也有日志——status 如实记录）。
	do(h, http.MethodGet, "/api/v1/dict/types", tok)
	do(h, http.MethodPost, "/api/v1/identity/users", tok)
	flushWait()

	res := queryLogs(t, h, tok, "?module=identity&page_size=50")
	if res.Total == 0 {
		t.Fatal("no identity entries collected")
	}
	found := false
	for _, e := range res.Rows {
		if e.Path == "/api/v1/identity/users" && e.Method == http.MethodPost {
			found = true
			if e.UserID == "" {
				t.Fatal("user_id not extracted from JWT")
			}
			if e.UserAgent != "test-agent/1.0" {
				t.Fatalf("ua = %q", e.UserAgent)
			}
			if e.Status == 0 || e.Remote == "" {
				t.Fatalf("status/remote missing: %+v", e)
			}
		}
	}
	if !found {
		t.Fatalf("POST entry missing: %+v", res.Rows)
	}

	// 时间倒序。
	for i := 1; i < len(res.Rows); i++ {
		if res.Rows[i-1].Time.Before(res.Rows[i].Time) {
			t.Fatal("rows must be time-desc")
		}
	}
	_ = m
}

func TestSamplingModes(t *testing.T) {
	// write 模式：GET 不采集，POST 采集。
	h, _, _ := newFixture(t, oplog.ModeWrite, nil)
	tok := adminToken(t, h) // login 是 POST（auth 路径，会采集）
	do(h, http.MethodGet, "/api/v1/dict/types", tok)
	do(h, http.MethodPost, "/api/v1/identity/users", tok)
	flushWait()
	res := queryLogs(t, h, tok, "?page_size=100")
	for _, e := range res.Rows {
		if e.Method == http.MethodGet && e.Path == "/api/v1/dict/types" {
			t.Fatal("write mode must not collect GET")
		}
	}
	gets := 0
	posts := 0
	for _, e := range res.Rows {
		switch e.Method {
		case http.MethodGet:
			gets++
		case http.MethodPost:
			posts++
		}
	}
	if posts == 0 {
		t.Fatal("write mode must collect POST")
	}
	if gets > 0 {
		// GET（如查询本身 oplog/logs）不采集：查询后已 flush 的窗口内
		// 可能有本次查询自身？——查询走 GET，不应入库；此断言在 flush
		// 竞态下允许 logs 查询自身未入库（它确实不该在）。
		t.Fatalf("unexpected GET entries: %d", gets)
	}

	// off 模式：不采集（查询仍可用）。
	h2, _, _ := newFixture(t, oplog.ModeOff, nil)
	tok2 := adminToken(t, h2)
	do(h2, http.MethodPost, "/api/v1/identity/users", tok2)
	flushWait()
	res2 := queryLogs(t, h2, tok2, "?page_size=100")
	for _, e := range res2.Rows {
		if e.Path == "/api/v1/identity/users" {
			t.Fatal("off mode must not collect")
		}
	}
}

func TestSkipProbeAndNonAPI(t *testing.T) {
	h, _, _ := newFixture(t, oplog.ModeAll, nil)
	tok := adminToken(t, h)
	do(h, http.MethodGet, "/healthz", "")
	do(h, http.MethodGet, "/readyz", "")
	do(h, http.MethodGet, "/console/", "")
	flushWait()
	res := queryLogs(t, h, tok, "?page_size=100")
	for _, e := range res.Rows {
		if e.Path == "/healthz" || e.Path == "/readyz" || !strings.HasPrefix(e.Path, "/api/") {
			t.Fatalf("probe/non-api collected: %+v", e)
		}
	}
}

func TestQueryFilterMatrix(t *testing.T) {
	store := oplog.NewMemStore(100)
	now := time.Now().UTC()
	seed := []oplog.Entry{
		{ID: "1", Time: now.Add(-4 * time.Minute), Method: "GET", Path: "/api/v1/a/x", Status: 200, Module: "a", UserID: "u1"},
		{ID: "2", Time: now.Add(-3 * time.Minute), Method: "POST", Path: "/api/v1/a/y", Status: 409, Module: "a", UserID: "u2"},
		{ID: "3", Time: now.Add(-2 * time.Minute), Method: "GET", Path: "/api/v1/b/x", Status: 500, Module: "b", UserID: "u1"},
		{ID: "4", Time: now.Add(-1 * time.Minute), Method: "DELETE", Path: "/api/v1/b/y", Status: 200, Module: "b", UserID: "u3"},
	}
	_ = store.Append(seed)

	cases := []struct {
		name string
		f    oplog.QueryFilter
		want []string // IDs
	}{
		{"module=a", oplog.QueryFilter{Module: "a"}, []string{"2", "1"}},
		{"user=u1", oplog.QueryFilter{UserID: "u1"}, []string{"3", "1"}},
		{"status=5xx", oplog.QueryFilter{StatusClass: "5xx"}, []string{"3"}},
		{"status=4xx", oplog.QueryFilter{StatusClass: "4xx"}, []string{"2"}},
		{"since", oplog.QueryFilter{Since: now.Add(-90 * time.Second)}, []string{"4"}},
		{"until", oplog.QueryFilter{Until: now.Add(-150 * time.Second)}, []string{"2", "1"}},
		{"module+status", oplog.QueryFilter{Module: "b", StatusClass: "2xx"}, []string{"4"}},
		{"page2", oplog.QueryFilter{Page: 2, PageSize: 1}, []string{"3"}},
	}
	for _, c := range cases {
		if c.f.Page == 0 {
			c.f.Page = 1
		}
		if c.f.PageSize == 0 {
			c.f.PageSize = 50
		}
		res, err := store.Query(c.f)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		got := make([]string, len(res.Rows))
		for i, e := range res.Rows {
			got[i] = e.ID
		}
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Fatalf("%s: got %v want %v", c.name, got, c.want)
		}
	}

	// 分页 total 与空页边界。
	res, _ := store.Query(oplog.QueryFilter{Page: 9, PageSize: 10})
	if res.Total != 4 || len(res.Rows) != 0 {
		t.Fatalf("empty page: total=%d rows=%d", res.Total, len(res.Rows))
	}
	// pageSize 上限。
	res, _ = store.Query(oplog.QueryFilter{Page: 1, PageSize: 999})
	if res.PageSize != 100 {
		t.Fatalf("page_size cap: %d", res.PageSize)
	}
}

func TestMetricsWindow(t *testing.T) {
	c := oplog.NewCollectorForTest() // 直接暴露 metrics observe 的测试构造
	// 注入已知序列：100 请求（5 个 5xx），延迟 1..100ms。
	for i := 1; i <= 100; i++ {
		status := 200
		if i%20 == 0 {
			status = 500
		}
		c.ObserveForTest(status, time.Duration(i)*time.Millisecond)
	}
	snap := c.SnapshotForTest()
	if snap.Window.Requests != 100 {
		t.Fatalf("requests = %d", snap.Window.Requests)
	}
	if err := snap.Window.ErrorRate - 0.05; err < -0.001 || err > 0.001 {
		t.Fatalf("error_rate = %v", snap.Window.ErrorRate)
	}
	// P95：样本 1..100 的 95 分位 ≈ 95ms（允许 ±2）。
	if snap.Window.P95MS < 93 || snap.Window.P95MS > 97 {
		t.Fatalf("p95 = %v", snap.Window.P95MS)
	}
	if snap.Process.Goroutines <= 0 || snap.Process.HeapAllocMB <= 0 {
		t.Fatalf("process metrics: %+v", snap.Process)
	}
}

// blockingStore 慢 store：验证中间件不被 store 阻塞（异步）。
type blockingStore struct {
	entries atomic.Int64
	query   atomic.Int64
}

func (s *blockingStore) Append(entries []oplog.Entry) error {
	time.Sleep(300 * time.Millisecond) // 模拟慢写入
	s.entries.Add(int64(len(entries)))
	return nil
}
func (s *blockingStore) Query(f oplog.QueryFilter) (oplog.PageResult, error) {
	s.query.Add(1)
	return oplog.PageResult{Rows: []oplog.Entry{}, Total: 0, Page: f.Page, PageSize: f.PageSize}, nil
}

func TestAsyncNoBlock(t *testing.T) {
	bs := &blockingStore{}
	h, m, _ := newFixture(t, oplog.ModeAll, bs)
	tok := adminToken(t, h) // login 也走慢 store 批队列（异步）

	// 快速连发 20 个请求：每个都应立即返回（不等 300ms 慢 store）。
	start := time.Now()
	for i := 0; i < 20; i++ {
		rec := do(h, http.MethodGet, "/api/v1/dict/types", tok)
		if rec.Code == 0 {
			t.Fatal("no response")
		}
	}
	elapsed := time.Since(start)
	// 20 请求若同步等 store（300ms×批量）会远超 2s；异步下 <2s。
	if elapsed > 2*time.Second {
		t.Fatalf("middleware blocked by store: %v", elapsed)
	}
	m.Close() // 优雅关闭应等待慢 store 完成
	if bs.entries.Load() == 0 {
		t.Fatal("entries lost after graceful close")
	}
}

func TestGORMOpLogStore(t *testing.T) {
	st, err := openGORM(t)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	s := st.OpLogStore()
	now := time.Now().UTC()
	batch := make([]oplog.Entry, 0, 5)
	for i := 0; i < 5; i++ {
		batch = append(batch, oplog.Entry{
			ID: fmt.Sprintf("e%d", i), Time: now.Add(-time.Duration(i) * time.Minute),
			Method: "GET", Path: "/api/v1/x/y", Status: 200, Module: "x", UserID: "u1",
		})
	}
	if err := s.Append(batch); err != nil {
		t.Fatal(err)
	}
	res, err := s.Query(oplog.QueryFilter{Module: "x", Page: 1, PageSize: 3})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 5 || len(res.Rows) != 3 {
		t.Fatalf("page1: total=%d rows=%d", res.Total, len(res.Rows))
	}
	res, err = s.Query(oplog.QueryFilter{UserID: "u1", StatusClass: "2xx", Page: 2, PageSize: 3})
	if err != nil || len(res.Rows) != 2 {
		t.Fatalf("page2: %v rows=%d", err, len(res.Rows))
	}
	n, err := st.OpLogStore().(*gormstore.OpLogStore).CleanupBefore(context.Background(), now.Add(-2*time.Minute+100*time.Millisecond))
	if err != nil || n != 3 { // 边界留 100ms 余量：-4/-3/-2 分钟三条早于阈值
		t.Fatalf("cleanup: n=%d err=%v", n, err)
	}
}
