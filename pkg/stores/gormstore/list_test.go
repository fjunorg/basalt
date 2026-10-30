package gormstore

import (
	"context"
	"encoding/json"
	"testing"
)

// TestListNeverNull 空结果序列化为 [] 而非 null（Console 消费一致性，
// qa v1.4.0 终验遗留修复的行为锚点）。
func TestListNeverNull(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx := context.Background()

	// 抽三个代表性 List（Routed 视图底层同源）
	jobs, err := st.JobStore().ListJobs(ctx)
	if err != nil {
		t.Fatalf("jobs: %v", err)
	}
	if b, _ := json.Marshal(jobs); string(b) != "[]" {
		t.Fatalf("empty jobs json = %s, want []", b)
	}
	users, err := st.UserStore().List(ctx)
	if err != nil {
		t.Fatalf("users: %v", err)
	}
	if b, _ := json.Marshal(users); string(b) != "[]" {
		t.Fatalf("empty users json = %s, want []", b)
	}
	kbs, err := st.RagStore().ListKBs(ctx)
	if err != nil {
		t.Fatalf("kbs: %v", err)
	}
	if b, _ := json.Marshal(kbs); string(b) != "[]" {
		t.Fatalf("empty kbs json = %s, want []", b)
	}
}
