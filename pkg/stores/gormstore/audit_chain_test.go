package gormstore_test

import (
	"context"
	"testing"

	"github.com/basalt-go/basalt/pkg/kernel/crypto"
	"github.com/basalt-go/basalt/pkg/modules/audit"
	"github.com/basalt-go/basalt/pkg/stores/gormstore"
)

// sm3Hasher 供测试注入国密哈希，验证链算法可插拔。
type sm3Hasher struct{ p crypto.Provider }

func (h sm3Hasher) Hash(ctx context.Context, data []byte) ([]byte, error) {
	return h.p.Hash(ctx, data)
}
func (h sm3Hasher) Algorithm() string { return h.p.Algorithm() }

func TestAuditChainPersistAcrossRestart(t *testing.T) {
	ctx := context.Background()

	// 模拟第一次运行：文件库 + 追加 3 条
	dsn := t.TempDir() + "/audit.db"
	db1, err := gormstore.Open(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	chain1, err := gormstore.NewAuditChain(db1.DB, struct{ crypto.Provider }{crypto.Default()})
	if err != nil {
		t.Fatalf("new chain: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := chain1.Append(ctx, audit.Event{Module: "test", Event: "e"}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	// 模拟重启：同一 DSN 重新打开，链应完整且可续写
	db2, err := gormstore.Open(dsn)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	chain2, err := gormstore.NewAuditChain(db2.DB, struct{ crypto.Provider }{crypto.Default()})
	if err != nil {
		t.Fatalf("new chain 2: %v", err)
	}
	if brk, err := chain2.Verify(ctx); err != nil || brk != 0 {
		t.Fatalf("verify after restart: brk=%d err=%v", brk, err)
	}
	if err := chain2.Append(ctx, audit.Event{Module: "test", Event: "after-restart"}); err != nil {
		t.Fatalf("append after restart: %v", err)
	}
	if brk, err := chain2.Verify(ctx); err != nil || brk != 0 {
		t.Fatalf("verify after续写: brk=%d err=%v", brk, err)
	}
	events, _ := chain2.List(ctx, "", 0)
	if len(events) != 4 {
		t.Fatalf("events = %d, want 4 (restart must not reset chain)", len(events))
	}
	if events[0].Event != "after-restart" {
		t.Fatalf("newest = %q", events[0].Event)
	}
}

func TestAuditChainDetectsDBTampering(t *testing.T) {
	ctx := context.Background()
	db, err := gormstore.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	chain, err := gormstore.NewAuditChain(db.DB, struct{ crypto.Provider }{crypto.Default()})
	if err != nil {
		t.Fatalf("chain: %v", err)
	}
	for i := 0; i < 4; i++ {
		_ = chain.Append(ctx, audit.Event{Module: "m", Event: "e"})
	}
	// 直接改库行（绕过链逻辑），模拟 DBA 篡改
	if err := db.DB.Model(&audit.Event{}).Where("seq = ?", 2).Update("event", "hacked").Error; err != nil {
		t.Fatalf("tamper: %v", err)
	}
	brk, err := chain.Verify(ctx)
	if err == nil || brk != 2 {
		t.Fatalf("tampered row: brk=%d err=%v, want brk=2", brk, err)
	}
}

func TestAuditChainWithSM3(t *testing.T) {
	ctx := context.Background()
	db, err := gormstore.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	chain, err := gormstore.NewAuditChain(db.DB, sm3Hasher{crypto.NewProvider(crypto.AlgoSM3)})
	if err != nil {
		t.Fatalf("chain: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := chain.Append(ctx, audit.Event{Module: "sm3", Event: "e"}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	if brk, err := chain.Verify(ctx); err != nil || brk != 0 {
		t.Fatalf("sm3 chain verify: brk=%d err=%v", brk, err)
	}
	events, _ := chain.List(ctx, "", 1)
	// SM3 摘要 32 字节 → hex 64 字符
	if len(events[0].Hash) != 64 {
		t.Fatalf("sm3 hash len = %d, want 64", len(events[0].Hash))
	}
}
