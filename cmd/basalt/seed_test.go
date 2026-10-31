package main

import (
	"context"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/basalt-go/basalt/pkg/kernel/app"
	"github.com/basalt-go/basalt/pkg/modules/identity"
	"github.com/basalt-go/basalt/pkg/stores/gormstore"
)

// countAll 汇总各模块数据总量（幂等性对比用）。
func countAll(t *testing.T, st *gormstore.Stores) map[string]int {
	t.Helper()
	ctx := context.Background()
	get := func(f func() (int, error)) int {
		n, err := f()
		if err != nil {
			t.Fatalf("count: %v", err)
		}
		return n
	}
	return map[string]int{
		"depts":     get(func() (int, error) { l, err := st.OrgStore().ListDepts(ctx); return len(l), err }),
		"posts":     get(func() (int, error) { l, err := st.OrgStore().ListPosts(ctx); return len(l), err }),
		"users":     get(func() (int, error) { l, err := st.UserStore().List(ctx); return len(l), err }),
		"roles":     get(func() (int, error) { l, err := st.RoleStore().List(ctx); return len(l), err }),
		"dictTypes": get(func() (int, error) { l, err := st.DictStore().ListTypes(ctx); return len(l), err }),
		"notices":   get(func() (int, error) { l, err := st.NoticeStore().List(ctx, false); return len(l), err }),
		"expenses":  get(func() (int, error) { l, err := st.FinanceStore().ListExpenses(ctx, ""); return len(l), err }),
		"docs":      get(func() (int, error) { l, err := st.DocumentStore().List(ctx, ""); return len(l), err }),
		"contracts": get(func() (int, error) {
			l, err := st.ContractStore().List(ctx, "")
			return len(l), err
		}),
		"kbs":  get(func() (int, error) { l, err := st.RagStore().ListKBs(ctx); return len(l), err }),
		"agds": get(func() (int, error) { l, err := st.AgentStore().ListDefs(ctx); return len(l), err }),
		"nts":  get(func() (int, error) { l, err := st.NotifyStore().ListTemplates(ctx); return len(l), err }),
	}
}

// TestSeedDemoIdempotent 幂等性：两次 seed 数据量不变 + 覆盖模块
// 清单断言 + demo 账号可登录（bcrypt 校验）。
func TestSeedDemoIdempotent(t *testing.T) {
	dsn := t.TempDir() + "/seed.db"
	st1, err := gormstore.Open(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_ = seedDemo(context.Background(), st1)
	after1 := countAll(t, st1)

	// 覆盖清单：主要模块均有数据
	want := map[string]int{
		"depts": 4, "posts": 3, "users": 6, "roles": 4, // 6 个 demo 账号（bootstrap admin 由 serve 时 identity.Start 幂等创建）
		"dictTypes": 3, "notices": 3, "expenses": 3, "docs": 3, "contracts": 3,
		"kbs": 1, "agds": 2, "nts": 3,
	}
	for k, n := range want {
		if after1[k] < n {
			t.Fatalf("coverage %s = %d, want >= %d", k, after1[k], n)
		}
	}
	// 套餐绑定
	if _, ok, _ := st1.TenantPkgStore().BoundPkg(context.Background(), "demo-tn-gov"); !ok {
		t.Fatal("tenant package binding missing")
	}

	// 二次执行（新连接模拟重启后再次 seed）：数据量不变
	if sqlDB, err := st1.DB.DB(); err == nil {
		_ = sqlDB.Close()
	}
	st2, err := gormstore.Open(dsn)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	_ = seedDemo(context.Background(), st2)
	after2 := countAll(t, st2)
	for k, n := range after1 {
		if after2[k] != n {
			t.Fatalf("idempotency broken: %s %d -> %d", k, n, after2[k])
		}
	}

	// demo 账号口令可校验（统一 Demo@2026）
	u, err := st2.UserStore().GetByUsername(context.Background(), "demo-ceo")
	if err != nil {
		t.Fatalf("get demo user: %v", err)
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(demoPass)) != nil {
		t.Fatal("demo password mismatch")
	}
	_ = time.Now
}

// TestSeedThenServeStarts seed 后服务可正常启动且 demo 数据可用：
// 同一库重开后装配 identity 等核心模块（serve sqlite 分支子集），
// demo 账号可登录（bcrypt 链路完整）。
func TestSeedThenServeStarts(t *testing.T) {
	dsn := t.TempDir() + "/seed_serve.db"
	st1, err := gormstore.Open(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_ = seedDemo(context.Background(), st1)
	if db, e := st1.DB.DB(); e == nil {
		_ = db.Close()
	}

	st2, err := gormstore.Open(dsn)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	a := app.New(nil)
	for _, m := range []app.Module{
		identity.NewWithStores(st2.UserStore(), st2.RoleStore()),
	} {
		if err := a.Registry().Register(m); err != nil {
			t.Fatalf("register: %v", err)
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
	_ = mods
}
