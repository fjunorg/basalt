package gormstore

import (
	"context"
	"testing"
	"time"

	"github.com/basalt-go/basalt/pkg/modules/identity"
)

// SSOStore 基础语义：Client CRUD 冲突、授权码单次消费、
// refresh 轮换/重放/吊链（重启恢复由 TestSSOStoreRestartFileDSN 覆盖）。
func TestSSOStoreBasics(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx := context.Background()
	s := st.SSOStore()

	// 1. Client CRUD + secret 哈希不回读泄漏。
	c := identity.Client{
		ID: "c-1", ClientID: "cid-100", SecretHash: identity.HashSecret("s3cret"),
		Name: "Demo", RedirectURIs: []string{"https://demo.example.com/cb"},
		Scopes: []string{"profile"}, Grants: []string{"authorization_code"},
		IsPublic: false, Status: identity.StatusActive, TenantID: "t-9",
	}
	if err := s.CreateClient(ctx, c); err != nil {
		t.Fatalf("create client: %v", err)
	}
	if err := s.CreateClient(ctx, c); err != identity.ErrClientIDTaken {
		t.Fatalf("dup client: %v", err)
	}

	// 2. 授权码：单次消费原子性。
	ac := identity.AuthCode{
		Code: "code-abc", ClientID: "cid-100", UserID: "u-1",
		RedirectURI: "https://demo.example.com/cb", Scopes: []string{"profile"},
		ExpiresAt: time.Now().Add(5 * time.Minute), CreatedAt: time.Now(),
	}
	if err := s.SaveAuthCode(ctx, ac); err != nil {
		t.Fatalf("save code: %v", err)
	}
	got, err := s.ConsumeAuthCode(ctx, "code-abc")
	if err != nil || !got.Used {
		t.Fatalf("first consume: %v used=%v", err, got.Used)
	}
	if _, err := s.ConsumeAuthCode(ctx, "code-abc"); err != identity.ErrAuthCodeUsed {
		t.Fatalf("second consume must be ErrAuthCodeUsed: %v", err)
	}
	if _, err := s.ConsumeAuthCode(ctx, "code-none"); err != identity.ErrAuthCodeNotFound {
		t.Fatalf("unknown code: %v", err)
	}

	// 3. refresh：轮换 + 重放 + 链吊销。
	rt1 := identity.RefreshToken{
		TokenHash: identity.HashSecret("rt-raw-1"), ChainID: "chain-1",
		ClientID: "cid-100", UserID: "u-1", Scopes: []string{"profile"},
		ExpiresAt: time.Now().Add(14 * 24 * time.Hour), CreatedAt: time.Now(),
	}
	rt2 := rt1
	rt2.TokenHash = identity.HashSecret("rt-raw-2")
	for _, rt := range []identity.RefreshToken{rt1, rt2} {
		if err := s.SaveRefresh(ctx, rt); err != nil {
			t.Fatalf("save refresh: %v", err)
		}
	}
	if _, err := s.RotateRefresh(ctx, rt1.TokenHash); err != nil {
		t.Fatalf("rotate rt1: %v", err)
	}
	if _, err := s.RotateRefresh(ctx, rt1.TokenHash); err != identity.ErrRefreshReplayed {
		t.Fatalf("replay rt1: %v", err)
	}
	if err := s.RevokeChain(ctx, "chain-1"); err != nil {
		t.Fatalf("revoke chain: %v", err)
	}
	// 链吊销后 rt2 轮换 → 重放错误。
	if _, err := s.RotateRefresh(ctx, rt2.TokenHash); err != identity.ErrRefreshReplayed {
		t.Fatalf("revoked rt2: %v", err)
	}
}

// TestSSOStoreRestartFileDSN 文件 DSN 重启恢复：授权码已用状态、
// refresh 吊销状态、应用注册跨进程延续。
func TestSSOStoreRestartFileDSN(t *testing.T) {
	dsn := t.TempDir() + "/sso_restart.db"
	st, err := Open(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx := context.Background()
	s := st.SSOStore()

	c := identity.Client{
		ID: "c-1", ClientID: "cid-100", Name: "Demo",
		RedirectURIs: []string{"https://demo.example.com/cb"},
		Grants:       []string{"client_credentials"}, Status: identity.StatusActive,
	}
	if err := s.CreateClient(ctx, c); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveAuthCode(ctx, identity.AuthCode{
		Code: "code-xyz", ClientID: "cid-100", UserID: "u-1",
		RedirectURI: "https://demo.example.com/cb",
		ExpiresAt:   time.Now().Add(5 * time.Minute), CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConsumeAuthCode(ctx, "code-xyz"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveRefresh(ctx, identity.RefreshToken{
		TokenHash: identity.HashSecret("rt-file-1"), ChainID: "chain-f",
		ClientID: "cid-100", UserID: "u-1",
		ExpiresAt: time.Now().Add(24 * time.Hour), CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RotateRefresh(ctx, identity.HashSecret("rt-file-1")); err != nil {
		t.Fatal(err)
	}

	// 重启（关旧连接、同 DSN 重开）。
	sqlDB, _ := st.DB.DB()
	_ = sqlDB.Close()
	st2, err := Open(dsn)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() {
		if db, e := st2.DB.DB(); e == nil {
			_ = db.Close()
		}
	}()
	s2 := st2.SSOStore()

	// 应用延续。
	if _, err := s2.GetClientByClientID(ctx, "cid-100"); err != nil {
		t.Fatalf("client lost after restart: %v", err)
	}
	// 已用授权码仍是已用（单次使用跨重启）。
	if _, err := s2.ConsumeAuthCode(ctx, "code-xyz"); err != identity.ErrAuthCodeUsed {
		t.Fatalf("code must stay used: %v", err)
	}
	// 已轮换 refresh 重放仍被识别。
	if _, err := s2.RotateRefresh(ctx, identity.HashSecret("rt-file-1")); err != identity.ErrRefreshReplayed {
		t.Fatalf("refresh must stay revoked: %v", err)
	}
}

// 并发双花：同一授权码并发消费仅一方成功（条件更新行级原子性）。
// glebarez/sqlite :memory: 每连接独立库——并发路径用共享缓存 DSN，
// 保证 goroutine 间同一物理库。
func TestSSOStoreConsumeConcurrent(t *testing.T) {
	st, err := Open("file:sso_race?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() {
		if db, e := st.DB.DB(); e == nil {
			_ = db.Close()
		}
	}()
	ctx := context.Background()
	s := st.SSOStore()
	if err := s.SaveAuthCode(ctx, identity.AuthCode{
		Code: "code-race", ClientID: "cid", UserID: "u",
		ExpiresAt: time.Now().Add(time.Minute), CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	const n = 8
	res := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			_, err := s.ConsumeAuthCode(ctx, "code-race")
			res <- err
		}()
	}
	ok, used := 0, 0
	for i := 0; i < n; i++ {
		if err := <-res; err == nil {
			ok++
		} else if err == identity.ErrAuthCodeUsed {
			used++
		} else {
			t.Fatalf("unexpected: %v", err)
		}
	}
	if ok != 1 || used != n-1 {
		t.Fatalf("single-use violated: ok=%d used=%d", ok, used)
	}
}
