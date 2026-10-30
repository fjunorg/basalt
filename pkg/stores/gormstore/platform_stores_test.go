package gormstore

import (
	"context"
	"testing"
	"time"

	"github.com/basalt-go/basalt/pkg/modules/errcode"
	"github.com/basalt-go/basalt/pkg/modules/identity"
	"github.com/basalt-go/basalt/pkg/modules/tenantpkg"
)

// OnlineStore 持久化：登记/登出/踢下线 + 重启延续（GORM 路径）。
func TestOnlineStorePersist(t *testing.T) {
	st, err := Open("file:online_persist?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() {
		if db, e := st.DB.DB(); e == nil {
			_ = db.Close()
		}
	}()
	ctx := context.Background()
	s := st.OnlineStore()
	now := time.Now().UTC()

	// 登记 A/B 两个会话。
	_ = s.Register(ctx, identity.OnlineSession{TokenID: "fpA", UserID: "u1", Username: "alice", LoginAt: now, ExpiresAt: now.Add(time.Hour)})
	_ = s.Register(ctx, identity.OnlineSession{TokenID: "fpB", UserID: "u1", Username: "alice", LoginAt: now, ExpiresAt: now.Add(time.Hour)})

	// 登出 A：B 仍有效。
	_ = s.Revoke(ctx, "fpA", now.Add(time.Second))
	if !s.CheckToken(ctx, "u1", "fpB", now) {
		t.Fatal("fpB must stay valid after fpA logout")
	}
	if s.CheckToken(ctx, "u1", "fpA", now) {
		t.Fatal("fpA must be rejected")
	}

	// 踢下线：iat 早于 kicked_at 的旧令牌拒绝；新登录（Register 清 kicks）恢复。
	if n, err := s.RevokeByUser(ctx, "u1", now.Add(time.Minute)); err != nil || n != 1 {
		t.Fatalf("kick: n=%d err=%v", n, err)
	}
	if s.CheckToken(ctx, "u1", "fpB", now) {
		t.Fatal("kicked user old token must be rejected")
	}
	_ = s.Register(ctx, identity.OnlineSession{TokenID: "fpC", UserID: "u1", Username: "alice", LoginAt: now.Add(2 * time.Minute), ExpiresAt: now.Add(2 * time.Hour)})
	if !s.CheckToken(ctx, "u1", "fpC", now.Add(2*time.Minute)) {
		t.Fatal("re-login must clear kick")
	}

	// 重启（关闭共享缓存库重开同名即新库，改用文件 DSN 验证延续）。
	dsn := t.TempDir() + "/online_restart.db"
	st2, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	s2 := st2.OnlineStore()
	_ = s2.Register(ctx, identity.OnlineSession{TokenID: "fpD", UserID: "u2", Username: "bob", LoginAt: now, ExpiresAt: now.Add(time.Hour)})
	n, _ := s2.RevokeByUser(ctx, "u2", now.Add(time.Minute))
	if n != 1 {
		t.Fatalf("revoke: %d", n)
	}
	if db, e := st2.DB.DB(); e == nil {
		_ = db.Close()
	}
	st3, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if db, e := st3.DB.DB(); e == nil {
			_ = db.Close()
		}
	}()
	s3 := st3.OnlineStore()
	// 重启后踢出标记延续（kicked_at 持久化）。
	if s3.CheckToken(ctx, "u2", "fpD", now) {
		t.Fatal("kick must survive restart (file DSN)")
	}
	// 在线列表不含被吊销会话。
	online, _ := s3.ListOnline(ctx, now.Add(time.Minute))
	for _, ses := range online {
		if ses.TokenID == "fpD" {
			t.Fatal("revoked session must not be online after restart")
		}
	}
}

// TenantPkg + ErrCode store 持久化语义。
func TestPlatformStoresPersist(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// 套餐：CRUD + 绑定 + 计数。
	tp := st.TenantPkgStore()
	p := tenantpkg.Package{ID: "p1", Name: "基础版", Status: "active",
		MenuIDs: []string{"m1"}, Perms: []string{"a:b"}, CreatedAt: time.Now()}
	if err := tp.CreatePkg(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := tp.CreatePkg(ctx, p); err != tenantpkg.ErrPkgNameTaken {
		t.Fatalf("dup: %v", err)
	}
	if err := tp.Bind(ctx, "t1", "p1", time.Now()); err != nil {
		t.Fatal(err)
	}
	if n, _ := tp.CountBound(ctx, "p1"); n != 1 {
		t.Fatalf("bound = %d", n)
	}
	got, bound, _ := tp.BoundPkg(ctx, "t1")
	if !bound || got.ID != "p1" {
		t.Fatalf("binding lost: %+v bound=%v", got, bound)
	}
	if err := tp.Unbind(ctx, "t1"); err != nil {
		t.Fatal(err)
	}
	if _, bound, _ := tp.BoundPkg(ctx, "t1"); bound {
		t.Fatal("unbind failed")
	}

	// 错误码：CRUD + 唯一键。
	ec := st.ErrCodeStore()
	e := errcode.Entry{ID: "e1", Module: "m", Code: "C1", Message: "msg", HTTPStatus: 400, CreatedAt: time.Now()}
	if err := ec.Create(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err := ec.Create(ctx, e); err != errcode.ErrDup {
		t.Fatalf("dup: %v", err)
	}
	out, _ := ec.List(ctx, "m")
	if len(out) != 1 {
		t.Fatalf("list = %d", len(out))
	}
}
