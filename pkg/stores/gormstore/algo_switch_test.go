package gormstore

import (
	"context"
	"strings"
	"testing"

	"github.com/basalt-go/basalt/pkg/kernel/crypto"
	"github.com/basalt-go/basalt/pkg/modules/audit"
)

// sha256/sm3 hasher 构造助手。
func algoHasher(algo string) audit.Hasher {
	return crypto.NewProvider(algo)
}

// TestAuditChainAlgoSwitchVerify CRYPTO_ALGO 中途切换（sha256→sm3）
// 后全链核验：历史段自动 fallback 到原算法，不再误报断链；
// 真实篡改仍检出（双算法均不匹配）。
func TestAuditChainAlgoSwitchVerify(t *testing.T) {
	dsn := t.TempDir() + "/algoswitch.db"
	ctx := context.Background()

	// 阶段一：SHA-256 时代写入 2 条
	s1, err := Open(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	c1, err := stAuditChain(s1, algoHasher(crypto.AlgoSHA256))
	if err != nil {
		t.Fatalf("chain: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := c1.Append(ctx, audit.Event{Module: "test", Event: "sha-era"}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	if sqlDB, err := s1.DB.DB(); err == nil {
		_ = sqlDB.Close()
	}

	// 阶段二：切换 SM3 后重开，续链 2 条（新段以 SM3 落链）
	s2, err := Open(dsn)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	c2, err := stAuditChain(s2, algoHasher(crypto.AlgoSM3))
	if err != nil {
		t.Fatalf("chain2: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := c2.Append(ctx, audit.Event{Module: "test", Event: "sm3-era"}); err != nil {
			t.Fatalf("append2: %v", err)
		}
	}

	// 切换后核验：全链通过（历史 SHA-256 段 fallback，新 SM3 段主算法）
	breakAt, err := c2.Verify(ctx)
	if err != nil || breakAt != 0 {
		t.Fatalf("verify after switch: break=%d err=%v（历史段应 fallback 通过）", breakAt, err)
	}

	// 真篡改：改第一条内容 → 双算法均不匹配 → 断链且错误含提示
	if err := s2.DB.Exec("UPDATE events SET module = 'tampered' WHERE seq = 1").Error; err != nil {
		t.Fatalf("tamper: %v", err)
	}
	breakAt, err = c2.Verify(ctx)
	if err == nil || breakAt == 0 {
		t.Fatal("篡改未检出")
	}
	if !strings.Contains(err.Error(), "tamper or unknown legacy algo") {
		t.Fatalf("err = %v（应含篡改语义提示）", err)
	}
}

// stAuditChain 经 Stores 构造链（测试助手）。
func stAuditChain(s *Stores, h audit.Hasher) (*AuditChain, error) {
	return NewAuditChain(s.DB, h)
}
