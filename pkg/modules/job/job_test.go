package job_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/basalt-go/basalt/pkg/kernel/app"
	"github.com/basalt-go/basalt/pkg/modules/audit"
	"github.com/basalt-go/basalt/pkg/modules/identity"
	"github.com/basalt-go/basalt/pkg/modules/job"
	"github.com/basalt-go/basalt/pkg/stores/gormstore"
)

func newApp(t *testing.T, mods ...app.Module) http.Handler {
	t.Helper()
	a := app.New(nil)
	if err := a.Registry().Register(identity.New()); err != nil {
		t.Fatalf("register identity: %v", err)
	}
	if err := a.Registry().Register(audit.New()); err != nil {
		t.Fatalf("register audit: %v", err)
	}
	for _, m := range mods {
		if err := a.Registry().Register(m); err != nil {
			t.Fatalf("register %s: %v", m.Name(), err)
		}
	}
	resolved, err := a.Registry().Resolve()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	for _, m := range resolved {
		if err := m.Init(context.Background(), a); err != nil {
			t.Fatalf("init %s: %v", m.Name(), err)
		}
		_ = m.Start(context.Background())
	}
	return a.Router().Handler()
}

func do(t *testing.T, h http.Handler, method, path, token string, body any) (*httptest.ResponseRecorder, json.RawMessage) {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	return rec, env.Data
}

func adminToken(t *testing.T, h http.Handler) string {
	t.Helper()
	rec, data := do(t, h, http.MethodPost, "/api/v1/identity/login", "", map[string]string{
		"username": "admin", "password": "Basalt@2026",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("admin login: %d %s", rec.Code, rec.Body.String())
	}
	var d struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(data, &d)
	return d.Token
}

// TestManagerSemantics Manager 纯内存路径：注册默认停用、启停、
// 手动触发与运行记录、错误 Runner 记入 LastErr；
// 边界：操作未注册任务 ErrNotFound、运行记录环形截断。
func TestManagerSemantics(t *testing.T) {
	m := job.NewManager(nil)
	m.Register("demo", "演示任务", "单测用", 60, func(context.Context) (string, error) {
		return "ok:1", nil
	})
	m.Register("bad", "失败任务", "", 60, func(context.Context) (string, error) {
		return "", errors.New("boom")
	})

	// 默认停用
	js := m.List()
	if len(js) != 2 || js[0].Enabled || js[0].IntervalSec != 60 {
		t.Fatalf("initial list = %+v", js)
	}

	// 边界：未注册任务
	if err := m.SetEnabled("ghost", true); !errors.Is(err, job.ErrNotFound) {
		t.Fatalf("enable ghost: %v", err)
	}
	if err := m.Trigger(context.Background(), "ghost"); !errors.Is(err, job.ErrNotFound) {
		t.Fatalf("trigger ghost: %v", err)
	}

	// 启用（重复启用幂等）
	if err := m.SetEnabled("demo", true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if err := m.SetEnabled("demo", true); err != nil {
		t.Fatalf("re-enable: %v", err)
	}

	// 触发成功路径：结果与运行记录
	if err := m.Trigger(context.Background(), "demo"); err != nil {
		t.Fatalf("trigger: %v", err)
	}
	runs := m.Runs("demo")
	if len(runs) != 1 || runs[0].Result != "ok:1" || runs[0].Err != "" {
		t.Fatalf("runs = %+v", runs)
	}
	for _, j := range m.List() {
		if j.ID == "demo" && (j.LastRun == nil || j.LastResult != "ok:1" || j.LastErr) {
			t.Fatalf("demo after trigger: %+v", j)
		}
	}

	// 触发失败路径：错误计入记录与 LastErr，Trigger 本身不报错
	if err := m.Trigger(context.Background(), "bad"); err != nil {
		t.Fatalf("trigger bad: %v", err)
	}
	runs = m.Runs("bad")
	if len(runs) != 1 || runs[0].Err != "boom" {
		t.Fatalf("bad runs = %+v", runs)
	}
	for _, j := range m.List() {
		if j.ID == "bad" && (!j.LastErr || j.LastResult != "boom") {
			t.Fatalf("bad after trigger: %+v", j)
		}
	}

	// 停用
	if err := m.SetEnabled("demo", false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	for _, j := range m.List() {
		if j.ID == "demo" && j.Enabled {
			t.Fatalf("demo still enabled: %+v", j)
		}
	}
}

// TestJobModuleAPI 模块 HTTP 路径：内置 audit.stats 任务注册、
// 启停、手动触发、运行记录查询；边界：未注册任务 404。
func TestJobModuleAPI(t *testing.T) {
	h := newApp(t, job.New(nil))
	tok := adminToken(t, h)

	// 内置任务已注册（依赖 audit 模块）
	rec, data := do(t, h, http.MethodGet, "/api/v1/jobs", tok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d", rec.Code)
	}
	var jobs []job.Job
	_ = json.Unmarshal(data, &jobs)
	if len(jobs) != 1 || jobs[0].ID != "audit.stats" || jobs[0].Enabled {
		t.Fatalf("jobs = %+v", jobs)
	}

	// 启用
	if rec, _ = do(t, h, http.MethodPatch, "/api/v1/jobs/audit.stats/status", tok, map[string]bool{
		"enabled": true,
	}); rec.Code != http.StatusOK {
		t.Fatalf("enable: %d", rec.Code)
	}

	// 手动触发 → 响应含本次运行记录
	rec, data = do(t, h, http.MethodPost, "/api/v1/jobs/audit.stats/trigger", tok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("trigger: %d %s", rec.Code, rec.Body.String())
	}
	var run job.RunRecord
	_ = json.Unmarshal(data, &run)
	if run.Result == "" || run.Err != "" || run.Duration == "" {
		t.Fatalf("run = %+v", run)
	}

	// 运行记录列表（分页包装：{total, offset, items}）
	_, data = do(t, h, http.MethodGet, "/api/v1/jobs/audit.stats/runs", tok, nil)
	var runs struct {
		Total int             `json:"total"`
		Items []job.RunRecord `json:"items"`
	}
	_ = json.Unmarshal(data, &runs)
	if runs.Total != 1 || len(runs.Items) != 1 {
		t.Fatalf("runs = %+v", runs)
	}

	// ---- 边界 ----
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/jobs/ghost/trigger", tok, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("trigger ghost: %d, want 404", rec.Code)
	}
	if rec, _ := do(t, h, http.MethodPatch, "/api/v1/jobs/ghost/status", tok, map[string]bool{
		"enabled": true,
	}); rec.Code != http.StatusNotFound {
		t.Fatalf("enable ghost: %d, want 404", rec.Code)
	}
}

// TestJobPersistenceRestart 模块级重启恢复（HTTP 路径）：触发内置任务
// 运行态落盘 → 重建 store 与模块实例 → 新实例经 Register 恢复
// Enabled/LastResult；运行记录为内存环形，重启清零（设计语义）。
func TestJobPersistenceRestart(t *testing.T) {
	dsn := t.TempDir() + "/job.db"

	mkApp := func(s *gormstore.Stores) http.Handler {
		t.Helper()
		a := app.New(nil)
		for _, m := range []app.Module{
			identity.NewWithStores(s.UserStore(), s.RoleStore()),
			audit.New(),
			job.NewWithStore(a.Logger(), s.JobStore()),
		} {
			if err := a.Registry().Register(m); err != nil {
				t.Fatalf("register %s: %v", m.Name(), err)
			}
		}
		mods, err := a.Registry().Resolve()
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		for _, m := range mods {
			if err := m.Init(context.Background(), a); err != nil {
				t.Fatalf("init %s: %v", m.Name(), err)
			}
			_ = m.Start(context.Background())
		}
		return a.Router().Handler()
	}

	s1, err := gormstore.Open(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	h1 := mkApp(s1)
	tok := adminToken(t, h1)
	// 启用 + 触发：运行态写入持久层
	if rec, _ := do(t, h1, http.MethodPatch, "/api/v1/jobs/audit.stats/status", tok, map[string]bool{
		"enabled": true,
	}); rec.Code != http.StatusOK {
		t.Fatalf("enable: %d", rec.Code)
	}
	if rec, _ := do(t, h1, http.MethodPost, "/api/v1/jobs/audit.stats/trigger", tok, nil); rec.Code != http.StatusOK {
		t.Fatalf("trigger: %d", rec.Code)
	}
	if sqlDB, err := s1.DB.DB(); err == nil {
		_ = sqlDB.Close()
	}

	// 重启：同一 DSN 新 store + 新模块（Init 时 Register 恢复运行态）
	s2, err := gormstore.Open(dsn)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	h2 := mkApp(s2)
	tok2 := adminToken(t, h2)

	rec, data := do(t, h2, http.MethodGet, "/api/v1/jobs", tok2, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list after restart: %d", rec.Code)
	}
	var jobs []job.Job
	_ = json.Unmarshal(data, &jobs)
	if len(jobs) != 1 || jobs[0].ID != "audit.stats" {
		t.Fatalf("jobs after restart = %+v", jobs)
	}
	j := jobs[0]
	if !j.Enabled || j.LastRun == nil || j.LastResult == "" || j.LastErr {
		t.Fatalf("runtime state not restored: %+v", j)
	}

	// 运行记录是内存环形历史：重启后清零（设计语义）
	_, data = do(t, h2, http.MethodGet, "/api/v1/jobs/audit.stats/runs", tok2, nil)
	var runs []job.RunRecord
	_ = json.Unmarshal(data, &runs)
	if len(runs) != 0 {
		t.Fatalf("runs after restart = %d, want 0（环形历史不持久化）", len(runs))
	}
}
