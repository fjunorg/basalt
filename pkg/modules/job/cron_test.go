package job

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ---------- cron 解析矩阵 ----------

func TestParseCronValid(t *testing.T) {
	valid := []string{
		"* * * * *",     // 每分钟
		"*/5 * * * *",   // 每 5 分钟
		"0 9 * * *",     // 每天 9:00
		"30 8 1 * *",    // 每月 1 日 8:30
		"0 0 * * 0",     // 每周日 0:00
		"15 10 * * 1-5", // 工作日 10:15
		"0 0 1 1 *",     // 每年 1 月 1 日
		"1,15,30 * * * *",
		"10-20/5 8-18 * * 1,3,5",
		"59 23 31 12 *", // 跨年边界（12-31 23:59）
	}
	for _, expr := range valid {
		if _, err := ParseCron(expr); err != nil {
			t.Errorf("ParseCron(%q) = %v, want ok", expr, err)
		}
	}
}

func TestParseCronInvalid(t *testing.T) {
	invalid := []string{
		"",            // 空
		"* * * *",     // 4 段
		"* * * * * *", // 6 段
		"60 * * * *",  // 分越界
		"* 24 * * *",  // 时越界
		"* * 32 * *",  // 日越界
		"* * * 13 *",  // 月越界
		"* * * * 7",   // 周越界
		"a * * * *",   // 非数字
		"*/0 * * * *", // step 0
		"5-1 * * * *", // 反向区间
		"1,,2 * * * *",
	}
	for _, expr := range invalid {
		if _, err := ParseCron(expr); err == nil {
			t.Errorf("ParseCron(%q) 应报错", expr)
		}
	}
}

// ---------- Next 触发矩阵（固定时刻断言） ----------

func TestCronNext(t *testing.T) {
	base := time.Date(2026, 10, 30, 23, 45, 30, 0, time.UTC) // 周五
	cases := []struct {
		expr string
		want time.Time
	}{
		{"* * * * *", time.Date(2026, 10, 30, 23, 46, 0, 0, time.UTC)},
		{"*/15 * * * *", time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)}, // 下一刻度（23:45 严格大于 → 24:00）
		{"0 9 * * *", time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)},
		{"30 8 1 * *", time.Date(2026, 11, 1, 8, 30, 0, 0, time.UTC)},
		{"0 0 * * 0", time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)},       // 下个周日（10-31 是周六→11-1 周日）
		{"15 10 * * 1-5", time.Date(2026, 11, 2, 10, 15, 0, 0, time.UTC)}, // 下周一（周六凌晨起）
		{"59 23 31 12 *", time.Date(2026, 12, 31, 23, 59, 0, 0, time.UTC)},
	}
	for _, tc := range cases {
		c, err := ParseCron(tc.expr)
		if err != nil {
			t.Fatalf("parse %q: %v", tc.expr, err)
		}
		if got := c.Next(base); !got.Equal(tc.want) {
			t.Errorf("Next(%q) = %s, want %s", tc.expr, got, tc.want)
		}
	}
}

// TestCronNextDomDowOR dom 与 dow 同时受限的 Vixie OR 语义：
// "0 0 1 * 1" = 每月 1 日 或 每周一 的 0 点。
func TestCronNextDomDowOR(t *testing.T) {
	c, err := ParseCron("0 0 1 * 1")
	if err != nil {
		t.Fatal(err)
	}
	// 2026-10-30（周五）之后：11-01 是周日（非 1 日？11-01 恰是 1 日！）
	got := c.Next(time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC))
	if got.Month() != time.November || got.Day() != 1 {
		t.Fatalf("Next = %s, want 11-01（既是 1 日）", got)
	}
	// 从 11-02 起：下一个命中 = 周一 11-02（dow OR）
	got = c.Next(time.Date(2026, 11, 1, 23, 59, 0, 0, time.UTC))
	if got.Month() != time.November || got.Day() != 2 || got.Weekday() != time.Monday {
		t.Fatalf("Next = %s, want 11-02 周一（dow OR 语义）", got)
	}
}

// TestCronNextStrictlyAfter 严格大于语义（同一分钟内不再触发）。
func TestCronNextStrictlyAfter(t *testing.T) {
	c, _ := ParseCron("* * * * *")
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	next := c.Next(at)
	if !next.After(at) || next.Sub(at) != time.Minute {
		t.Fatalf("next = %s（应严格 +1 分钟）", next)
	}
}

// ---------- scheduleNext 统一重排 ----------

func TestScheduleNext(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	ji := &Job{IntervalSec: 90}
	if got := ji.scheduleNext(now); !got.Equal(now.Add(90 * time.Second)) {
		t.Fatalf("interval next = %s", got)
	}
	jc := &Job{CronExpr: "30 9 * * *"} // cron 优先（interval 被忽略）
	jc.IntervalSec = 60
	if got := jc.scheduleNext(now); got.Hour() != 9 || got.Minute() != 30 {
		t.Fatalf("cron next = %s", got)
	}
}

// ---------- 动态任务生命周期（httptest 回调） ----------

func TestDynamicJobLifecycle(t *testing.T) {
	var hits int
	var lastBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.Method == http.MethodPost {
			b := make([]byte, 128)
			n, _ := r.Body.Read(b)
			lastBody = string(b[:n])
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	m := NewManager(nil)
	ctx := context.Background()
	m.Register("builtin", "内置", "", 60, func(context.Context) (string, error) { return "ok", nil })

	// 创建：cron 优先 / 必填校验 / 目标校验
	if err := m.RegisterHTTP("dyn", "动态", "", "", 0, srv.URL, "POST", `{"k":1}`); err == nil {
		t.Fatal("无调度应拒绝")
	}
	if err := m.RegisterHTTP("dyn", "动态", "", "", 30, "ftp://x", "", ""); err == nil {
		t.Fatal("非 http(s) 应拒绝")
	}
	if err := m.RegisterHTTP("dyn", "动态", "", "*/5 * * * *", 0, srv.URL, "POST", `{"k":1}`); err != nil {
		t.Fatalf("创建: %v", err)
	}
	if err := m.RegisterHTTP("dyn", "重复", "", "", 30, srv.URL, "", ""); err == nil {
		t.Fatal("重复 id 应拒绝")
	}

	// 列表含动态任务（cron 型）。注意：List() 每次返回新切片
	// （map 迭代序随机），必须单次取值再索引——二次调用会因顺序
	// 不同而指向错误元素（race 模式下暴露的真实测试 bug）。
	list := m.List()
	var found *Job
	for i := range list {
		if list[i].ID == "dyn" {
			found = &list[i]
		}
	}
	if found == nil || found.Kind != KindHTTP || found.CronExpr != "*/5 * * * *" {
		t.Fatalf("dyn job = %+v", found)
	}

	// 手动触发 → 回调收到 + 运行记录成功
	if err := m.Trigger(ctx, "dyn"); err != nil {
		t.Fatalf("trigger: %v", err)
	}
	if hits != 1 || lastBody != `{"k":1}` {
		t.Fatalf("callback hits=%d body=%q", hits, lastBody)
	}
	runs := m.Runs("dyn")
	if len(runs) != 1 || runs[0].Err != "" || !strings.Contains(runs[0].Result, "HTTP 200") {
		t.Fatalf("run = %+v", runs)
	}

	// 非法 id 删除 → 404；内置任务删除 → ErrBuiltinNoDel
	if err := m.Delete("ghost"); err != ErrNotFound {
		t.Fatalf("ghost del = %v", err)
	}
	if err := m.Delete("builtin"); err != ErrBuiltinNoDel {
		t.Fatalf("builtin del = %v", err)
	}
	// 动态任务删除成功
	if err := m.Delete("dyn"); err != nil {
		t.Fatalf("del: %v", err)
	}
	if err := m.Trigger(ctx, "dyn"); err != ErrNotFound {
		t.Fatal("deleted job trigger should 404")
	}
}

// TestDynamicJobHTTPTargetBoundary 目标与方法边界。
func TestDynamicJobHTTPTargetBoundary(t *testing.T) {
	m := NewManager(nil)
	bad := []struct{ url, method string }{
		{"", "POST"},
		{"not-a-url", "POST"},
		{"file:///etc/passwd", "POST"},
		{"/relative", "POST"},
		{"http://", "POST"}, // 无 host
		{"http://ok.example", "TRACE"},
	}
	for _, b := range bad {
		if err := m.RegisterHTTP("x"+b.url+b.method, "n", "", "", 30, b.url, b.method, ""); err == nil {
			t.Errorf("应拒绝 url=%q method=%q", b.url, b.method)
		}
	}
	if err := m.RegisterHTTP("g1", "n", "", "", 30, "http://ok.example", "PUT", `{"a":1}`); err != nil {
		t.Fatalf("合法 PUT 应通过: %v", err)
	}
}

// ---------- 重启恢复（内存 stub store 模拟持久层） ----------

type memStore struct {
	jobs map[string]Job
	runs []RunRecord
	seq  int64
}

func (s *memStore) GetJob(_ context.Context, id string) (Job, error) {
	if j, ok := s.jobs[id]; ok {
		return j, nil
	}
	return Job{}, ErrNotFound
}
func (s *memStore) SaveJob(_ context.Context, j Job) error {
	s.jobs[j.ID] = j
	return nil
}
func (s *memStore) ListJobs(context.Context) ([]Job, error) {
	var out []Job
	for _, j := range s.jobs {
		out = append(out, j)
	}
	return out, nil
}
func (s *memStore) DeleteJob(_ context.Context, id string) error {
	delete(s.jobs, id)
	return nil
}
func (s *memStore) SaveRun(_ context.Context, rec RunRecord) (RunRecord, error) {
	s.seq++
	rec.Seq = s.seq
	s.runs = append(s.runs, rec)
	return rec, nil
}
func (s *memStore) ListRuns(_ context.Context, jobID string, offset, limit int) ([]RunRecord, int, error) {
	var matched []RunRecord
	for i := len(s.runs) - 1; i >= 0; i-- { // 倒序
		if s.runs[i].JobID == jobID {
			matched = append(matched, s.runs[i])
		}
	}
	if offset > len(matched) {
		offset = len(matched)
	}
	end := offset + limit
	if limit <= 0 || end > len(matched) {
		end = len(matched)
	}
	return matched[offset:end], len(matched), nil
}

func newMemStore() *memStore { return &memStore{jobs: map[string]Job{}} }

// TestRestartRecoveryDynamic 重启恢复：动态任务定义自持久层重建，
// NextRun 从当前重算（不补跑），运行态（enabled）延续。
func TestRestartRecoveryDynamic(t *testing.T) {
	store := newMemStore()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("pong"))
	}))
	defer srv.Close()

	m1 := NewManagerWithStore(nil, store)
	_ = m1.RegisterHTTP("dyn-restart", "重启任务", "", "*/10 * * * *", 0, srv.URL, "GET", "")
	_ = m1.SetEnabled("dyn-restart", true)

	// 模拟重启：新 Manager（同一 store），LoadDynamic 重建
	m2 := NewManagerWithStore(nil, store)
	if err := m2.LoadDynamic(context.Background()); err != nil {
		t.Fatalf("load: %v", err)
	}
	var restored *Job
	list := m2.List()
	for i := range list {
		if list[i].ID == "dyn-restart" {
			restored = &list[i]
		}
	}
	if restored == nil {
		t.Fatal("动态任务未恢复")
	}
	if !restored.Enabled || restored.URL != srv.URL || restored.CronExpr != "*/10 * * * *" {
		t.Fatalf("restored = %+v（定义与运行态应延续）", restored)
	}
	// NextRun 重算：为未来时刻（cron 从当前对齐，不补跑）
	if !restored.NextRun.After(time.Now().UTC().Add(-time.Minute)) {
		t.Fatalf("NextRun = %s（应为重算后的未来时刻）", restored.NextRun)
	}
	// 触发仍可用（runner 恢复）
	if err := m2.Trigger(context.Background(), "dyn-restart"); err != nil {
		t.Fatalf("trigger after restart: %v", err)
	}

	// 内置任务在重启后仍以代码注册为准（不会被持久层的动态行覆盖）
	m2.Register("builtin", "内置", "", 60, func(context.Context) (string, error) { return "ok", nil })
	for _, j := range m2.List() {
		if j.ID == "builtin" && j.Kind != KindBuiltin {
			t.Fatalf("builtin kind = %s", j.Kind)
		}
	}
}

// TestRunsPage 分页与总数（持久层路径）。
func TestRunsPage(t *testing.T) {
	store := newMemStore()
	m := NewManagerWithStore(nil, store)
	_ = m.RegisterHTTP("p", "分页", "", "", 30, "http://ok.example", "POST", "")
	for i := 0; i < 5; i++ {
		_ = m.Trigger(context.Background(), "p")
	}
	page1, total, err := m.RunsPage(context.Background(), "p", 0, 2)
	if err != nil || total != 5 || len(page1) != 2 {
		t.Fatalf("page1 = %d 条 total=%d err=%v", len(page1), total, err)
	}
	page3, _, _ := m.RunsPage(context.Background(), "p", 4, 2)
	if len(page3) != 1 {
		t.Fatalf("page3 = %d 条（尾页）", len(page3))
	}
	// 倒序（最新在前）：page1[0].Seq > page1[1].Seq
	if page1[0].Seq <= page1[1].Seq {
		t.Fatalf("未按倒序: %d %d", page1[0].Seq, page1[1].Seq)
	}
	// 其他任务隔离
	_ = m.RegisterHTTP("q", "他任务", "", "", 30, "http://ok.example", "POST", "")
	_, qTotal, _ := m.RunsPage(context.Background(), "q", 0, 10)
	if qTotal != 0 {
		t.Fatalf("q total = %d（应隔离为 0）", qTotal)
	}
}

// TestCronJobTickScheduling cron 型到期执行（tick 驱动）。
func TestCronJobTickScheduling(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	m := NewManager(nil)
	_ = m.RegisterHTTP("tick", "到期", "", "* * * * *", 0, srv.URL, "GET", "")
	// 手动把 NextRun 拨到过去 → tick 应执行并按 cron 重排到未来分钟
	tickStart := time.Now().UTC() // 断言下界：race 开销下断言时刻可能
	// 越过紧邻的分钟刻度（cron 重排最小粒度），用 tick 前时刻判定
	// 才无竞态——重排值恒 > exec 时刻 ≥ tickStart。
	m.mu.Lock()
	if j, ok := m.jobs["tick"]; ok {
		j.Enabled = true
		j.NextRun = tickStart.Add(-time.Minute)
	}
	m.mu.Unlock()
	m.tick(context.Background())
	if len(m.Runs("tick")) != 1 {
		t.Fatal("到期任务应执行一次")
	}
	m.mu.Lock()
	next := m.jobs["tick"].NextRun
	m.mu.Unlock()
	if !next.After(tickStart) {
		t.Fatalf("重排 NextRun = %s（应晚于 tick 起点 %s）", next, tickStart)
	}
}
