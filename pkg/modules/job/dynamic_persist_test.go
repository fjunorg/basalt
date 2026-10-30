package job_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/basalt-go/basalt/pkg/modules/job"
	"github.com/basalt-go/basalt/pkg/stores/gormstore"
)

// TestDynamicJobPersistRestartSQLite sqlite 真实持久层：动态任务定义
// 与执行日志跨重启延续；cron 型 NextRun 重算（不补跑）。
func TestDynamicJobPersistRestartSQLite(t *testing.T) {
	dsn := t.TempDir() + "/jobdyn.db"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("hi"))
	}))
	defer srv.Close()
	ctx := context.Background()

	// 第一次进程：注册动态任务（cron 型）+ 启用 + 触发两次（日志入库）
	s1, err := gormstore.Open(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	m1 := job.NewManagerWithStore(nil, s1.JobStore())
	if err := m1.RegisterHTTP("dyn-sqlite", "跨重启", "", "*/7 * * * *", 0, srv.URL, "POST", `{"x":1}`); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := m1.SetEnabled("dyn-sqlite", true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	_ = m1.Trigger(ctx, "dyn-sqlite")
	_ = m1.Trigger(ctx, "dyn-sqlite")
	recs, total, err := m1.RunsPage(ctx, "dyn-sqlite", 0, 10)
	if err != nil || total != 2 || len(recs) != 2 {
		t.Fatalf("runs total=%d err=%v", total, err)
	}
	if sqlDB, err := s1.DB.DB(); err == nil {
		_ = sqlDB.Close()
	}

	// 重启：同一 dsn 重开，动态任务定义/运行态/日志全恢复
	s2, err := gormstore.Open(dsn)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	m2 := job.NewManagerWithStore(nil, s2.JobStore())
	if err := m2.LoadDynamic(ctx); err != nil {
		t.Fatalf("load: %v", err)
	}
	list := m2.List()
	var restored *job.Job
	for i := range list {
		if list[i].ID == "dyn-sqlite" {
			restored = &list[i]
		}
	}
	if restored == nil {
		t.Fatal("动态任务定义未恢复")
	}
	if !restored.Enabled || restored.CronExpr != "*/7 * * * *" || restored.Method != http.MethodPost {
		t.Fatalf("restored = %+v", restored)
	}
	if !restored.NextRun.After(time.Now().UTC().Add(-time.Minute)) {
		t.Fatalf("NextRun = %s（应重算为未来）", restored.NextRun)
	}
	// 执行日志跨重启延续（分页读取）
	_, total2, err := m2.RunsPage(ctx, "dyn-sqlite", 0, 10)
	if err != nil || total2 != 2 {
		t.Fatalf("runs after restart total=%d err=%v", total2, err)
	}
	// 触发仍可用且日志追加
	_ = m2.Trigger(ctx, "dyn-sqlite")
	_, total3, _ := m2.RunsPage(ctx, "dyn-sqlite", 0, 10)
	if total3 != 3 {
		t.Fatalf("after trigger total=%d, want 3", total3)
	}
	// 删除 → 持久层同步删除（再重启不复活）
	if err := m2.Delete("dyn-sqlite"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if sqlDB, err := s2.DB.DB(); err == nil {
		_ = sqlDB.Close()
	}
	s3, _ := gormstore.Open(dsn)
	m3 := job.NewManagerWithStore(nil, s3.JobStore())
	_ = m3.LoadDynamic(ctx)
	for _, j := range m3.List() {
		if j.ID == "dyn-sqlite" {
			t.Fatal("已删除任务不应在重启后复活")
		}
	}
}
