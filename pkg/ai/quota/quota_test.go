package quota

import (
	"testing"
	"time"
)

func TestQuotaLifecycle(t *testing.T) {
	m := NewManager(100)

	m.SetLimit("t1", 150)
	if !m.Allow("t1", 50) {
		t.Fatal("t1 50 within 150 should be allowed")
	}
	m.Consume("t1", 100)
	if m.Allow("t1", 51) {
		t.Fatal("t1 100+51 exceeds 150, should be denied")
	}
	if !m.Allow("t1", 50) {
		t.Fatal("t1 100+50 within 150 should be allowed")
	}
	if used, limit := m.Usage("t1"); used != 100 || limit != 150 {
		t.Fatalf("usage = %d/%d, want 100/150", used, limit)
	}

	// 未设置租户回落默认限额
	if used, limit := m.Usage("unknown"); used != 0 || limit != 100 {
		t.Fatalf("default = %d/%d, want 0/100", used, limit)
	}
	if !m.Allow("unknown", 100) {
		t.Fatal("boundary: 0+100 should equal limit 100 and be allowed")
	}
	m.Consume("unknown", 100)
	if m.Allow("unknown", 1) {
		t.Fatal("default limit boundary broken after consuming full quota")
	}
}

func TestUnlimited(t *testing.T) {
	m := NewManager(100)
	m.SetLimit("vip", -1)
	for i := 0; i < 5; i++ {
		m.Consume("vip", 1000)
	}
	if !m.Allow("vip", 1_000_000) {
		t.Fatal("unlimited tenant should always be allowed")
	}
	if _, limit := m.Usage("vip"); limit != -1 {
		t.Fatalf("limit = %d, want -1", limit)
	}
}

// TestMonthlyPeriodReset 验证跨账期自动开账：用量归零、限额延续。
func TestMonthlyPeriodReset(t *testing.T) {
	m := NewManager(100)
	now := time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }

	m.SetLimit("t1", 150)
	m.Consume("t1", 150)
	if m.Allow("t1", 1) {
		t.Fatal("quota exhausted within period")
	}

	// 时间推进到下月：新账期自动开启，用量归零、限额延续
	now = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	if !m.Allow("t1", 150) {
		t.Fatal("new period should reset usage (limit carried over)")
	}
	if used, limit := m.Usage("t1"); used != 0 || limit != 150 {
		t.Fatalf("new period = %d/%d, want 0/150", used, limit)
	}
	if m.CurrentPeriod() != "2026-11" {
		t.Fatalf("period = %s, want 2026-11", m.CurrentPeriod())
	}
}

func TestConcurrent(t *testing.T) {
	m := NewManager(-1)
	done := make(chan struct{}, 100)
	for i := 0; i < 100; i++ {
		go func() {
			m.Consume("cc", 10)
			m.Allow("cc", 0)
			done <- struct{}{}
		}()
	}
	for i := 0; i < 100; i++ {
		<-done
	}
	if used, _ := m.Usage("cc"); used != 1000 {
		t.Fatalf("used = %d, want 1000 (race-safe accounting)", used)
	}
}
