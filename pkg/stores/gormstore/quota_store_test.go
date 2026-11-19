package gormstore_test

import (
	"context"
	"testing"
	"time"

	"github.com/basalt-go/basalt/pkg/ai/quota"
	"github.com/basalt-go/basalt/pkg/stores/gormstore"
)

func TestQuotaStorePersistence(t *testing.T) {
	dsn := t.TempDir() + "/quota.db"

	// 第一次运行：设限额、消耗
	db1, err := gormstore.Open(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	qs1, err := db1.QuotaStore()
	if err != nil {
		t.Fatalf("quota store: %v", err)
	}
	m1 := quota.NewManagerWithStore(qs1, 100)
	m1.SetLimit(context.Background(), "t1", 150)
	m1.Consume(context.Background(), "t1", 60)
	if used, _ := m1.Usage(context.Background(), "t1"); used != 60 {
		t.Fatalf("used = %d", used)
	}

	// 重启：限额与用量均延续
	db2, err := gormstore.Open(dsn)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	qs2, err := db2.QuotaStore()
	if err != nil {
		t.Fatalf("quota store 2: %v", err)
	}
	m2 := quota.NewManagerWithStore(qs2, 100)
	if used, limit := m2.Usage(context.Background(), "t1"); used != 60 || limit != 150 {
		t.Fatalf("after restart = %d/%d, want 60/150", used, limit)
	}
	// 重启后续接执行：消耗至限额后拒绝
	if !m2.Allow(context.Background(), "t1", 90) {
		t.Fatal("60+90 should equal limit 150 and be allowed")
	}
	m2.Consume(context.Background(), "t1", 90)
	if m2.Allow(context.Background(), "t1", 1) {
		t.Fatal("limit enforcement across restart broken")
	}
}

func TestQuotaStorePeriodIsolation(t *testing.T) {
	db, err := gormstore.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	qs, err := db.QuotaStore()
	if err != nil {
		t.Fatalf("quota store: %v", err)
	}
	m := quota.NewManagerWithStore(qs, 100)
	now := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	m.SetClock(func() time.Time { return now })

	m.Consume(context.Background(), "t1", 100)
	if m.Allow(context.Background(), "t1", 1) {
		t.Fatal("period 2026-10 exhausted")
	}
	now = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC) // 跨账期
	if !m.Allow(context.Background(), "t1", 100) {
		t.Fatal("new period must reset usage on persisted store")
	}
	if used, limit := m.Usage(context.Background(), "t1"); used != 0 || limit != 100 {
		t.Fatalf("new period = %d/%d, want 0/100", used, limit)
	}
}
