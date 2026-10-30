package gormstore_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/basalt-go/basalt/pkg/modules/agent"
	"github.com/basalt-go/basalt/pkg/modules/contract"
	"github.com/basalt-go/basalt/pkg/modules/dict"
	"github.com/basalt-go/basalt/pkg/modules/document"
	"github.com/basalt-go/basalt/pkg/modules/file"
	"github.com/basalt-go/basalt/pkg/modules/finance"
	"github.com/basalt-go/basalt/pkg/modules/identity"
	"github.com/basalt-go/basalt/pkg/modules/job"
	"github.com/basalt-go/basalt/pkg/modules/notice"
	"github.com/basalt-go/basalt/pkg/modules/org"
	"github.com/basalt-go/basalt/pkg/modules/rag"
	"github.com/basalt-go/basalt/pkg/modules/sysconfig"
	"github.com/basalt-go/basalt/pkg/modules/tenant"
	"github.com/basalt-go/basalt/pkg/modules/workflow"
	"github.com/basalt-go/basalt/pkg/stores/gormstore"
)

// setupTenantFixture 主库 + 两个 dedicated 租户（独立文件库）+ 两个
// shared-schema 租户的用户与租户记录。返回 (master, userA, userB, userRow)。
func setupTenantFixture(t *testing.T, dir string) (*gormstore.Stores, string, string, string) {
	t.Helper()
	master, err := gormstore.Open(dir + "/master.db")
	if err != nil {
		t.Fatalf("open master: %v", err)
	}
	ctx := context.Background()

	mkTenant := func(code, mode, dbRef string) tenant.Tenant {
		return tenant.Tenant{ID: "tn-" + code, Code: code, Name: code,
			Status: "active", IsolationMode: mode, DBRef: dbRef, CreatedAt: time.Now().UTC()}
	}
	mkUser := func(id, tnID string) identity.User {
		return identity.User{ID: id, Username: id, Status: identity.StatusActive, TenantID: tnID}
	}
	ts := master.TenantStore()
	_ = ts.Create(ctx, mkTenant("gov-a", tenant.IsolationDedicatedDB, "db-a"))
	_ = ts.Create(ctx, mkTenant("gov-b", tenant.IsolationDedicatedDB, "db-b"))
	_ = ts.Create(ctx, mkTenant("row-c", tenant.IsolationRow, ""))
	us := master.UserStore()
	_ = us.Create(ctx, mkUser("user-a", "tn-gov-a"))
	_ = us.Create(ctx, mkUser("user-b", "tn-gov-b"))
	_ = us.Create(ctx, mkUser("user-row", "tn-row-c"))
	_ = us.Create(ctx, mkUser("user-plain", ""))
	return master, "user-a", "user-b", "user-row"
}

// dsnMap 构造测试 DSN 解析（ref → 文件库 DSN）。
func dsnMap(base, ref string) func(string) string {
	return func(r string) string {
		if r == ref {
			return base + "/" + ref + ".db"
		}
		return ""
	}
}

// ctxAsUser 构造带用户身份的请求上下文（模拟 AuthMiddleware 注入）。
func ctxAsUser(ctx context.Context, userID string) context.Context {
	return identity.WithUserID(ctx, userID)
}

// TestTenantRouterRoutingAndIsolation 路由正确性 + 隔离性：
// 租户 A/B 各自的专属库互不可见；shared-schema 与平台用户走主库。
func TestTenantRouterRoutingAndIsolation(t *testing.T) {
	dir := t.TempDir()
	master, userA, userB, userRow := setupTenantFixture(t, dir)
	router := gormstore.NewTenantRouterWithDSN(func(ref string) string {
		if ref == "db-a" || ref == "db-b" {
			return dir + "/" + ref + ".db"
		}
		return ""
	})
	defer router.Close()
	ctx := context.Background()

	rs := gormstore.NewRoutedDictStore(master.DB, master.UserStore(), master.TenantStore(), router)

	// 租户 A 与 B 各建同 code 字典类型（独立库，互不冲突）
	for _, uid := range []string{userA, userB} {
		if err := rs.CreateType(ctxAsUser(ctx, uid), dict.Type{
			ID: "t1", Code: "gender", Name: "性别", Status: "active", CreatedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("create by %s: %v", uid, err)
		}
	}
	// 同 code 在两库各自创建成功即分库证明；数据互不可见再验证
	typesA, _ := rs.ListTypes(ctxAsUser(ctx, userA))
	typesB, _ := rs.ListTypes(ctxAsUser(ctx, userB))
	if len(typesA) != 1 || len(typesB) != 1 || typesA[0].Code != "gender" || typesB[0].Code != "gender" {
		t.Fatalf("A=%v B=%v（各库应各有一条同 code 记录——分库互不冲突）", typesA, typesB)
	}
	// A 库再建第二条，B 不可见
	_ = rs.CreateType(ctxAsUser(ctx, userA), dict.Type{ID: "t2", Code: "level", Name: "级别"})
	typesA, _ = rs.ListTypes(ctxAsUser(ctx, userA))
	typesB, _ = rs.ListTypes(ctxAsUser(ctx, userB))
	if len(typesA) != 2 || len(typesB) != 1 {
		t.Fatalf("isolation broken: A=%d B=%d", len(typesA), len(typesB))
	}

	// shared-schema 租户与平台用户：主库（现状行为）
	if err := rs.CreateType(ctxAsUser(ctx, userRow), dict.Type{ID: "t3", Code: "common", Name: "主库"}); err != nil {
		t.Fatalf("row tenant create: %v", err)
	}
	typesRow, _ := rs.ListTypes(ctxAsUser(ctx, userRow))
	if len(typesRow) != 1 || typesRow[0].Code != "common" {
		t.Fatalf("row tenant should hit master: %v", typesRow)
	}
	typesPlain, _ := rs.ListTypes(ctx) // 无认证上下文
	if len(typesPlain) != 1 {
		t.Fatalf("no-tenant ctx should hit master: %v", typesPlain)
	}
}

// TestTenantRouterRestartRecovery 重启恢复：路由器重建后缓存冷启动，
// 租户库数据延续（文件库）；主库租户目录亦延续。
func TestTenantRouterRestartRecovery(t *testing.T) {
	dir := t.TempDir()
	master, userA, _, _ := setupTenantFixture(t, dir)
	ctx := context.Background()

	r1 := gormstore.NewTenantRouterWithDSN(dsnMap(dir, "db-a"))
	rs1 := gormstore.NewRoutedDictStore(master.DB, master.UserStore(), master.TenantStore(), r1)
	if err := rs1.CreateType(ctxAsUser(ctx, userA), dict.Type{
		ID: "keep", Code: "persist", Name: "持久", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	r1.Close()

	// 模拟重启：全新路由器 + 全新主库连接
	master2, err := gormstore.Open(dir + "/master.db")
	if err != nil {
		t.Fatalf("reopen master: %v", err)
	}
	r2 := gormstore.NewTenantRouterWithDSN(dsnMap(dir, "db-a"))
	defer r2.Close()
	rs2 := gormstore.NewRoutedDictStore(master2.DB, master2.UserStore(), master2.TenantStore(), r2)

	types, err := rs2.ListTypes(ctxAsUser(ctx, userA))
	if err != nil || len(types) != 1 || types[0].Code != "persist" {
		t.Fatalf("after restart: %v %v（租户库数据应延续）", types, err)
	}
}

// TestTenantRouterErrorSemantics 错误语义：DSN 未配置/DBRef 缺失/
// 连接失败均返回 ErrTenantDBUnavailable，绝不静默回退主库。
func TestTenantRouterErrorSemantics(t *testing.T) {
	dir := t.TempDir()
	master, userA, _, _ := setupTenantFixture(t, dir)
	ctx := context.Background()

	// db-a 的 DSN 未配置
	router := gormstore.NewTenantRouterWithDSN(func(string) string { return "" })
	rs := gormstore.NewRoutedDictStore(master.DB, master.UserStore(), master.TenantStore(), router)
	_, err := rs.ListTypes(ctxAsUser(ctx, userA))
	if !errors.Is(err, gormstore.ErrTenantDBUnavailable) {
		t.Fatalf("dsn missing: %v（应明确报错而非回退主库）", err)
	}

	// dedicated 租户缺 DBRef（数据配置错误）
	_ = master.TenantStore().Create(ctx, tenant.Tenant{
		ID: "tn-bad", Code: "bad", Status: "active",
		IsolationMode: tenant.IsolationDedicatedDB, CreatedAt: time.Now().UTC(),
	})
	_ = master.UserStore().Create(ctx, identity.User{ID: "user-bad", Username: "user-bad", Status: identity.StatusActive, TenantID: "tn-bad"})
	_, err = rs.ListTypes(ctxAsUser(ctx, "user-bad"))
	if !errors.Is(err, gormstore.ErrTenantDBUnavailable) {
		t.Fatalf("db_ref missing: %v", err)
	}

	// 连接失败（不可达路径）
	router2 := gormstore.NewTenantRouterWithDSN(func(string) string {
		return "/nonexistent-dir-no-perm/x.db?_pragma=nope(1)"
	})
	rs2 := gormstore.NewRoutedDictStore(master.DB, master.UserStore(), master.TenantStore(), router2)
	if _, err := rs2.ListTypes(ctxAsUser(ctx, userA)); !errors.Is(err, gormstore.ErrTenantDBUnavailable) {
		t.Fatalf("conn fail: %v", err)
	}
}

// TestRoutedStoresFullIsolation 全量隔离：双 dedicated 租户跨全部
// 接入模块——租户 A 写入后租户 B 列表不可见（每模块一个写+读探针）。
func TestRoutedStoresFullIsolation(t *testing.T) {
	dir := t.TempDir()
	master, userA, userB, _ := setupTenantFixture(t, dir)
	router := gormstore.NewTenantRouterWithDSN(func(ref string) string {
		if ref == "db-a" || ref == "db-b" {
			return dir + "/" + ref + ".db"
		}
		return ""
	})
	defer router.Close()
	ctx := context.Background()
	rs := gormstore.NewRoutedStores(gormstore.NewRoutedResolver(
		master.DB, master.UserStore(), master.TenantStore(), router))

	now := time.Now().UTC()
	probes := []struct {
		name  string
		write func(uid string) error
		count func(uid string) (int, error)
	}{
		{"org", func(uid string) error {
			return rs.OrgStore().CreateDept(ctxAsUser(ctx, uid), org.Dept{ID: "d-" + uid, Name: "部", Sort: 1, Status: "active", CreatedAt: now})
		}, func(uid string) (int, error) {
			l, err := rs.OrgStore().ListDepts(ctxAsUser(ctx, uid))
			return len(l), err
		}},
		{"dict", func(uid string) error {
			return rs.DictStore().CreateType(ctxAsUser(ctx, uid), dict.Type{ID: "t-" + uid, Code: "c", Name: "类型", Status: "active", CreatedAt: now})
		}, func(uid string) (int, error) {
			l, err := rs.DictStore().ListTypes(ctxAsUser(ctx, uid))
			return len(l), err
		}},
		{"sysconfig", func(uid string) error {
			return rs.SysConfigStore().Create(ctxAsUser(ctx, uid), sysconfig.Item{ID: "k-" + uid, Key: "sys.title", Value: "v"})
		}, func(uid string) (int, error) {
			l, err := rs.SysConfigStore().List(ctxAsUser(ctx, uid))
			return len(l), err
		}},
		{"notice", func(uid string) error {
			return rs.NoticeStore().Create(ctxAsUser(ctx, uid), notice.Notice{ID: "n-" + uid, Title: "公告", Status: notice.StatusDraft, CreatedAt: now})
		}, func(uid string) (int, error) {
			l, err := rs.NoticeStore().List(ctxAsUser(ctx, uid), false)
			return len(l), err
		}},
		{"file", func(uid string) error {
			return rs.FileStore().Create(ctxAsUser(ctx, uid), file.Meta{ID: "f-" + uid, Name: "a.txt", StoragePath: "p", CreatedAt: now})
		}, func(uid string) (int, error) {
			l, err := rs.FileStore().List(ctxAsUser(ctx, uid))
			return len(l), err
		}},
		{"job", func(uid string) error {
			return rs.JobStore().SaveJob(ctxAsUser(ctx, uid), job.Job{ID: "j-" + uid, Name: "任务", Enabled: true, NextRun: now})
		}, func(uid string) (int, error) {
			l, err := rs.JobStore().ListJobs(ctxAsUser(ctx, uid))
			return len(l), err
		}},
		{"finance", func(uid string) error {
			return rs.FinanceStore().CreateExpense(ctxAsUser(ctx, uid), finance.Expense{ID: "e-" + uid, ApplicantID: uid, Amount: 1, Status: finance.ExpDraft, CreatedAt: now})
		}, func(uid string) (int, error) {
			l, err := rs.FinanceStore().ListExpenses(ctxAsUser(ctx, uid), "")
			return len(l), err
		}},
		{"document", func(uid string) error {
			return rs.DocumentStore().Create(ctxAsUser(ctx, uid), document.Doc{ID: "doc-" + uid, Title: "公文", Status: document.StDraft, CreatorID: uid, CreatedAt: now})
		}, func(uid string) (int, error) {
			l, err := rs.DocumentStore().List(ctxAsUser(ctx, uid), "")
			return len(l), err
		}},
		{"contract", func(uid string) error {
			return rs.ContractStore().Create(ctxAsUser(ctx, uid), contract.Contract{ID: "c-" + uid, Title: "合同", Status: contract.StDraft, CreatorID: uid, CreatedAt: now})
		}, func(uid string) (int, error) {
			l, err := rs.ContractStore().List(ctxAsUser(ctx, uid), "")
			return len(l), err
		}},
		{"workflow", func(uid string) error {
			return rs.WorkflowStore().Create(ctxAsUser(ctx, uid), workflow.Instance{ID: "w-" + uid, BusinessKey: "bk-" + uid, Summary: "流程", Steps: []workflow.Step{{Approvers: []string{uid}}}, CreatorID: uid, Status: workflow.StatusPending, CreatedAt: now})
		}, func(uid string) (int, error) {
			l, err := rs.WorkflowStore().ListAll(ctxAsUser(ctx, uid))
			return len(l), err
		}},
		{"rag", func(uid string) error {
			return rs.RagStore().CreateKB(ctxAsUser(ctx, uid), rag.KnowledgeBase{ID: "kb-" + uid, Name: "知识库"})
		}, func(uid string) (int, error) {
			l, err := rs.RagStore().ListKBs(ctxAsUser(ctx, uid))
			return len(l), err
		}},
		{"agent", func(uid string) error {
			return rs.AgentStore().CreateDef(ctxAsUser(ctx, uid), agent.Definition{ID: "ad-" + uid, Name: "智能体-" + uid, Model: "m"})
		}, func(uid string) (int, error) {
			l, err := rs.AgentStore().ListDefs(ctxAsUser(ctx, uid))
			return len(l), err
		}},
	}

	for _, p := range probes {
		p := p
		t.Run(p.name, func(t *testing.T) {
			if err := p.write(userA); err != nil {
				t.Fatalf("A write: %v", err)
			}
			nA, err := p.count(userA)
			if err != nil || nA != 1 {
				t.Fatalf("A count = %d %v（应只见自己的 1 条）", nA, err)
			}
			nB, err := p.count(userB)
			if err != nil || nB != 0 {
				t.Fatalf("B count = %d %v（A 的写入对 B 必须不可见）", nB, err)
			}
			// B 也写一条：各自 1 条，互不串扰
			if err := p.write(userB); err != nil {
				t.Fatalf("B write: %v", err)
			}
			if nA, _ := p.count(userA); nA != 1 {
				t.Fatalf("A count after B write = %d（B 写入不得进入 A 库）", nA)
			}
		})
	}
}
