package gormstore_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/basalt-go/basalt/pkg/kernel/app"
	"github.com/basalt-go/basalt/pkg/kernel/crypto"
	"github.com/basalt-go/basalt/pkg/modules/audit"
	"github.com/basalt-go/basalt/pkg/modules/contract"
	"github.com/basalt-go/basalt/pkg/modules/dict"
	"github.com/basalt-go/basalt/pkg/modules/document"
	"github.com/basalt-go/basalt/pkg/modules/file"
	"github.com/basalt-go/basalt/pkg/modules/finance"
	"github.com/basalt-go/basalt/pkg/modules/identity"
	"github.com/basalt-go/basalt/pkg/modules/job"
	"github.com/basalt-go/basalt/pkg/modules/notice"
	"github.com/basalt-go/basalt/pkg/modules/org"
	"github.com/basalt-go/basalt/pkg/modules/sysconfig"
	"github.com/basalt-go/basalt/pkg/modules/tenant"
	"github.com/basalt-go/basalt/pkg/modules/workflow"
	"github.com/basalt-go/basalt/pkg/stores/gormstore"
)

// TestOrgDictSysConfigStores 覆盖 org/dict/sysconfig 三个 store 的
// 关键语义：CRUD、唯一键冲突、保护删除（引用存在时拒绝）。
func TestOrgDictSysConfigStores(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()

	// ---- org：部门树 + 岗位 ----
	os := s.OrgStore()
	root := org.Dept{ID: "d1", Name: "总公司", Sort: 1, Status: "active", CreatedAt: time.Now().UTC()}
	child := org.Dept{ID: "d2", ParentID: "d1", Name: "技术部", Sort: 2, Status: "active", CreatedAt: time.Now().UTC()}
	if err := os.CreateDept(ctx, root); err != nil {
		t.Fatalf("create root: %v", err)
	}
	if err := os.CreateDept(ctx, child); err != nil {
		t.Fatalf("create child: %v", err)
	}
	if err := os.DeleteDept(ctx, "d1"); !errors.Is(err, org.ErrHasChildren) {
		t.Fatalf("delete parent with children: %v", err)
	}
	depts, _ := os.ListDepts(ctx)
	if len(depts) != 2 || depts[0].ID != "d1" {
		t.Fatalf("depts = %+v", depts)
	}
	root.Name = "集团总部"
	if err := os.UpdateDept(ctx, root); err != nil {
		t.Fatalf("update dept: %v", err)
	}
	if err := os.DeleteDept(ctx, "ghost"); !errors.Is(err, org.ErrNotFound) {
		t.Fatalf("delete missing dept: %v", err)
	}

	p1 := org.Post{ID: "p1", Code: "dev", Name: "研发", Sort: 1, Status: "active"}
	if err := os.CreatePost(ctx, p1); err != nil {
		t.Fatalf("create post: %v", err)
	}
	if err := os.CreatePost(ctx, org.Post{ID: "p2", Code: "dev", Name: "重复"}); !errors.Is(err, org.ErrDupCode) {
		t.Fatalf("dup post code: %v", err)
	}
	p1.Name = "研发工程师"
	if err := os.UpdatePost(ctx, p1); err != nil {
		t.Fatalf("update post: %v", err)
	}
	posts, _ := os.ListPosts(ctx)
	if len(posts) != 1 || posts[0].Name != "研发工程师" {
		t.Fatalf("posts = %+v", posts)
	}

	// ---- dict：类型 + 数据，类型有数据不可删 ----
	ds := s.DictStore()
	dt := dict.Type{ID: "t1", Code: "gender", Name: "性别", Status: "active", CreatedAt: time.Now().UTC()}
	if err := ds.CreateType(ctx, dt); err != nil {
		t.Fatalf("create type: %v", err)
	}
	if err := ds.CreateType(ctx, dt); !errors.Is(err, dict.ErrTypeCodeUsed) {
		t.Fatalf("dup type: %v", err)
	}
	if err := ds.CreateData(ctx, dict.Data{ID: "dd1", TypeCode: "gender", Label: "男", Value: "M", Sort: 1, Status: "active"}); err != nil {
		t.Fatalf("create data: %v", err)
	}
	if err := ds.DeleteType(ctx, "t1"); !errors.Is(err, dict.ErrTypeHasData) {
		t.Fatalf("delete type with data: %v", err)
	}
	got, _ := ds.ListDataByType(ctx, "gender")
	if len(got) != 1 || got[0].Value != "M" {
		t.Fatalf("data by type = %+v", got)
	}

	// ---- sysconfig：KV 唯一键 + GetByKey ----
	ss := s.SysConfigStore()
	if err := ss.Create(ctx, sysconfig.Item{ID: "c1", Key: "sys.title", Value: "玄武", Name: "标题"}); err != nil {
		t.Fatalf("create config: %v", err)
	}
	if err := ss.Create(ctx, sysconfig.Item{ID: "c2", Key: "sys.title", Value: "x"}); !errors.Is(err, sysconfig.ErrKeyUsed) {
		t.Fatalf("dup key: %v", err)
	}
	it, err := ss.GetByKey(ctx, "sys.title")
	if err != nil || it.Value != "玄武" {
		t.Fatalf("get by key: %+v %v", it, err)
	}
	if _, err := ss.GetByKey(ctx, "nope"); !errors.Is(err, sysconfig.ErrNotFound) {
		t.Fatalf("missing key: %v", err)
	}
}

// TestNoticeFileStores 覆盖 notice 发布过滤与 file 元信息 roundtrip。
func TestNoticeFileStores(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()

	ns := s.NoticeStore()
	draft := notice.Notice{ID: "n1", Title: "草稿", Status: notice.StatusDraft, CreatorID: "u1", CreatedAt: time.Now().UTC()}
	pub := notice.Notice{ID: "n2", Title: "已发布", Status: notice.StatusPublished, CreatorID: "u1", CreatedAt: time.Now().UTC()}
	_ = ns.Create(ctx, draft)
	_ = ns.Create(ctx, pub)
	published, _ := ns.List(ctx, true)
	if len(published) != 1 || published[0].ID != "n2" {
		t.Fatalf("published only = %+v", published)
	}
	all, _ := ns.List(ctx, false)
	if len(all) != 2 {
		t.Fatalf("all = %+v", all)
	}
	// 发布动作 = 状态迁移 + PublishedAt
	got, _ := ns.Get(ctx, "n1")
	now := time.Now().UTC()
	got.Status, got.PublishedAt = notice.StatusPublished, &now
	if err := ns.Update(ctx, got); err != nil {
		t.Fatalf("publish update: %v", err)
	}
	got, _ = ns.Get(ctx, "n1")
	if got.Status != notice.StatusPublished || got.PublishedAt == nil {
		t.Fatalf("after publish: %+v", got)
	}

	fs := s.FileStore()
	meta := file.Meta{ID: "f1", Name: "报告.txt", Size: 42, ContentType: "text/plain",
		StoragePath: "20261030/f1-报告.txt", UploaderID: "u1", CreatedAt: time.Now().UTC()}
	if err := fs.Create(ctx, meta); err != nil {
		t.Fatalf("create meta: %v", err)
	}
	gotM, err := fs.Get(ctx, "f1")
	if err != nil || gotM.StoragePath != meta.StoragePath || gotM.Size != 42 {
		t.Fatalf("meta roundtrip: %+v %v", gotM, err)
	}
	if _, err := fs.Get(ctx, "ghost"); !errors.Is(err, file.ErrNotFound) {
		t.Fatalf("missing meta: %v", err)
	}
	if err := fs.Delete(ctx, "f1"); err != nil {
		t.Fatalf("delete meta: %v", err)
	}
}

// TestFinanceStores 覆盖报销明细 serializer 往返、发票唯一键、
// 预算 UPSERT 与原子累加、按申请人过滤。
func TestFinanceStores(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	fs := s.FinanceStore()

	e := finance.Expense{
		ID: "e1", ApplicantID: "u1", DeptID: "d1", Amount: 15000,
		Reason: "差旅", Status: finance.ExpDraft,
		Items:      []finance.ExpenseItem{{Name: "机票", Amount: 10000}, {Name: "住宿", Amount: 5000}},
		InvoiceIDs: []string{"i1", "i2"},
		CreatedAt:  time.Now().UTC(),
	}
	if err := fs.CreateExpense(ctx, e); err != nil {
		t.Fatalf("create expense: %v", err)
	}
	got, err := fs.GetExpense(ctx, "e1")
	if err != nil || len(got.Items) != 2 || got.Items[0].Name != "机票" || len(got.InvoiceIDs) != 2 {
		t.Fatalf("expense serializer roundtrip: %+v %v", got, err)
	}
	_ = fs.CreateExpense(ctx, finance.Expense{ID: "e2", ApplicantID: "u2", Amount: 1, Status: finance.ExpDraft, CreatedAt: time.Now().UTC()})
	mine, _ := fs.ListExpenses(ctx, "u1")
	if len(mine) != 1 || mine[0].ID != "e1" {
		t.Fatalf("list by applicant = %+v", mine)
	}
	got.Status = finance.ExpSubmitted
	if err := fs.UpdateExpense(ctx, got); err != nil {
		t.Fatalf("update expense: %v", err)
	}
	got, _ = fs.GetExpense(ctx, "e1")
	if got.Status != finance.ExpSubmitted {
		t.Fatalf("after update: %s", got.Status)
	}

	if err := fs.CreateInvoice(ctx, finance.Invoice{ID: "iv1", Code: "INV-001", Amount: 100, Date: time.Now().UTC(), CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("create invoice: %v", err)
	}
	err = fs.CreateInvoice(ctx, finance.Invoice{ID: "iv2", Code: "INV-001", Amount: 1, Date: time.Now().UTC(), CreatedAt: time.Now().UTC()})
	if !errors.Is(err, finance.ErrDupInvoice) {
		t.Fatalf("dup invoice: %v", err)
	}

	period := "2026-10"
	if err := fs.SetBudget(ctx, finance.Budget{DeptID: "d1", Period: period, Limit: 100000}); err != nil {
		t.Fatalf("set budget: %v", err)
	}
	// 复合主键 UPSERT：保留 Used
	if err := fs.SetBudget(ctx, finance.Budget{DeptID: "d1", Period: period, Limit: 200000}); err != nil {
		t.Fatalf("re-set budget: %v", err)
	}
	if err := fs.AddBudgetUsed(ctx, "d1", period, 30000); err != nil {
		t.Fatalf("add used: %v", err)
	}
	b, err := fs.GetBudget(ctx, "d1", period)
	if err != nil || b.Limit != 200000 || b.Used != 30000 {
		t.Fatalf("budget = %+v %v", b, err)
	}
	if err := fs.AddBudgetUsed(ctx, "ghost", period, 1); !errors.Is(err, finance.ErrNotFound) {
		t.Fatalf("add used on missing budget: %v", err)
	}

	p := finance.Payment{ID: "pay1", ExpenseID: "e1", Payee: "张三", Amount: 15000, Status: "paid", CreatedAt: time.Now().UTC()}
	if err := fs.CreatePayment(ctx, p); err != nil {
		t.Fatalf("create payment: %v", err)
	}
	pays, _ := fs.ListPayments(ctx)
	if len(pays) != 1 {
		t.Fatalf("payments = %+v", pays)
	}
}

// TestDocumentContractStores 覆盖创建人过滤与到期窗口查询。
func TestDocumentContractStores(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()

	docs := s.DocumentStore()
	d1 := document.Doc{ID: "doc1", Title: "A", Status: document.StDraft, CreatorID: "u1", CreatedAt: time.Now().UTC()}
	d2 := document.Doc{ID: "doc2", Title: "B", Status: document.StArchived, CreatorID: "u2", CreatedAt: time.Now().UTC()}
	_ = docs.Create(ctx, d1)
	_ = docs.Create(ctx, d2)
	mine, _ := docs.List(ctx, "u1")
	if len(mine) != 1 || mine[0].ID != "doc1" {
		t.Fatalf("docs by creator = %+v", mine)
	}
	got, err := docs.Get(ctx, "doc1")
	if err != nil || got.Title != "A" {
		t.Fatalf("get doc: %+v %v", got, err)
	}
	got.Status = document.StCirculating
	got.WorkflowInstID = "wf-1"
	if err := docs.Update(ctx, got); err != nil {
		t.Fatalf("update doc: %v", err)
	}
	got, _ = docs.Get(ctx, "doc1")
	if got.Status != document.StCirculating || got.WorkflowInstID != "wf-1" {
		t.Fatalf("after update: %+v", got)
	}

	cs := s.ContractStore()
	base := time.Now().UTC()
	_ = cs.Create(ctx, contract.Contract{ID: "c1", Title: "快到期", Status: contract.StActive,
		ExpireDate: base.AddDate(0, 0, 10), CreatorID: "u1", CreatedAt: base})
	_ = cs.Create(ctx, contract.Contract{ID: "c2", Title: "远期", Status: contract.StActive,
		ExpireDate: base.AddDate(1, 0, 0), CreatorID: "u1", CreatedAt: base})
	_ = cs.Create(ctx, contract.Contract{ID: "c3", Title: "未生效", Status: contract.StDraft,
		ExpireDate: base.AddDate(0, 0, 5), CreatorID: "u1", CreatedAt: base})
	_ = cs.Create(ctx, contract.Contract{ID: "c4", Title: "无到期日", Status: contract.StActive,
		CreatorID: "u1", CreatedAt: base})
	expiring, err := cs.ListExpiring(ctx, base.AddDate(0, 0, 30))
	if err != nil {
		t.Fatalf("list expiring: %v", err)
	}
	if len(expiring) != 1 || expiring[0].ID != "c1" {
		t.Fatalf("expiring = %+v（应只含 c1：30 天窗口内生效合同，排除远期/草稿/未填到期日）", expiring)
	}
}

// TestSQLiteFilePersistence 用文件库验证真实落盘：同一 DSN 关闭重开
// 后数据仍在（重启续存的最小证明）。
func TestSQLiteFilePersistence(t *testing.T) {
	dsn := t.TempDir() + "/restart.db"
	ctx := context.Background()

	s1, err := gormstore.Open(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := s1.SysConfigStore().Create(ctx, sysconfig.Item{ID: "k1", Key: "persist", Value: "yes"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if sqlDB, err := s1.DB.DB(); err == nil {
		_ = sqlDB.Close()
	}

	s2, err := gormstore.Open(dsn)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	it, err := s2.SysConfigStore().GetByKey(ctx, "persist")
	if err != nil || it.Value != "yes" {
		t.Fatalf("after restart: %+v %v（数据应仍在）", it, err)
	}
}

// TestJobManagerRestartRecovery 验证 job 调度的重启恢复语义：
// Enabled/LastRun 恢复；停机期间过期的 NextRun 重排为 now+interval（不补跑）。
func TestJobManagerRestartRecovery(t *testing.T) {
	dsn := t.TempDir() + "/jobs.db"
	ctx := context.Background()

	s1, err := gormstore.Open(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	m1 := job.NewManagerWithStore(nil, s1.JobStore())
	m1.Register("demo", "演示", "", 60, func(context.Context) (string, error) { return "ok", nil })
	if err := m1.SetEnabled("demo", true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if err := m1.Trigger(ctx, "demo"); err != nil {
		t.Fatalf("trigger: %v", err)
	}
	jobs := m1.List()
	if len(jobs) != 1 || !jobs[0].Enabled || jobs[0].LastRun == nil || jobs[0].LastResult != "ok" {
		t.Fatalf("before restart: %+v", jobs)
	}
	if sqlDB, err := s1.DB.DB(); err == nil {
		_ = sqlDB.Close()
	}

	// 重启：新 Manager 同一持久层
	s2, err := gormstore.Open(dsn)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	m2 := job.NewManagerWithStore(nil, s2.JobStore())
	m2.Register("demo", "演示", "", 60, func(context.Context) (string, error) { return "ok", nil })
	jobs = m2.List()
	if len(jobs) != 1 {
		t.Fatalf("after restart: %+v", jobs)
	}
	j := jobs[0]
	if !j.Enabled || j.LastRun == nil || j.LastResult != "ok" {
		t.Fatalf("runtime state not restored: %+v", j)
	}
	// 运行记录是内存环形：重启后清零（设计语义）
	if runs := m2.Runs("demo"); len(runs) != 0 {
		t.Fatalf("runs after restart = %d（应为 0：内存环形历史不持久化）", len(runs))
	}
}

// TestAllModulesWithGORMStores 模拟 serve.go sqlite 分支的全量装配
//（identity/tenant/audit + 9 业务模块 + workflow），验证注入缝正确：
// 登录后各模块关键 API 在 GORM store 上可用。
func TestAllModulesWithGORMStores(t *testing.T) {
	s := openMem(t)
	a := app.New(nil)
	storage, err := file.NewLocalStorage(t.TempDir())
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	chain, err := s.AuditChainStore(struct{ crypto.Provider }{crypto.Default()})
	if err != nil {
		t.Fatalf("audit chain: %v", err)
	}
	mods := []app.Module{
		identity.NewWithStores(s.UserStore(), s.RoleStore()),
		tenant.NewWithStore(s.TenantStore()),
		audit.NewWithStore(chain),
		org.NewWithStore(s.OrgStore()),
		dict.NewWithStore(s.DictStore()),
		sysconfig.NewWithStore(s.SysConfigStore()),
		notice.NewWithStore(s.NoticeStore()),
		file.NewWithStore(s.FileStore(), storage),
		job.NewWithStore(a.Logger(), s.JobStore()),
		workflow.NewWithStores(s.WorkflowStore(), s.WorkflowDefStore()),
		finance.NewWithStore(s.FinanceStore()),
		document.NewWithStore(s.DocumentStore()),
		contract.NewWithStore(s.ContractStore()),
	}
	for _, m := range mods {
		if err := a.Registry().Register(m); err != nil {
			t.Fatalf("register %s: %v", m.Name(), err)
		}
	}
	resolved, err := a.Registry().Resolve()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	for _, m := range resolved {
		if err := m.Init(context.Background(), a); err != nil {
			t.Fatalf("init %s: %v", m.Name(), err)
		}
		_ = m.Start(context.Background())
	}
	h := a.Router().Handler()

	// 登录引导管理员
	login := func() string {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/identity/login",
			strings.NewReader(`{"username":"admin","password":"Basalt@2026"}`))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var env struct {
			Data struct {
				Token string `json:"token"`
			} `json:"data"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &env)
		return env.Data.Token
	}
	tok := login()
	if tok == "" {
		t.Fatal("admin login failed")
	}

	// 各模块关键 API 一发（创建即走 GORM 写路径）
	steps := []struct {
		method, path string
		body         string
		wantCode     int
	}{
		{http.MethodPost, "/api/v1/org/depts", `{"name":"总公司","sort":1}`, http.StatusCreated},
		{http.MethodPost, "/api/v1/dict/types", `{"code":"gender","name":"性别"}`, http.StatusCreated},
		{http.MethodPost, "/api/v1/configs", `{"key":"sys.title","value":"玄武"}`, http.StatusCreated},
		{http.MethodPost, "/api/v1/notices", `{"title":"公告"}`, http.StatusCreated},
		{http.MethodPost, "/api/v1/finance/invoices", `{"code":"INV-1","amount":100}`, http.StatusCreated},
		{http.MethodPost, "/api/v1/documents", `{"title":"测试公文"}`, http.StatusCreated},
		{http.MethodPost, "/api/v1/contracts", `{"title":"测试合同","party":"某公司","amount":1000}`, http.StatusCreated},
	}
	for _, st := range steps {
		req := httptest.NewRequest(st.method, st.path, strings.NewReader(st.body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != st.wantCode {
			t.Fatalf("%s %s: %d %s（want %d）", st.method, st.path, rec.Code, rec.Body.String(), st.wantCode)
		}
	}

	// job：触发内置 audit.stats（Manager 持久化路径）
	req := httptest.NewRequest(http.MethodPost, "/api/v1/jobs/audit.stats/trigger", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("job trigger: %d %s", rec.Code, rec.Body.String())
	}
	// job 状态已落库：直接从 store 读回验证
	js := s.JobStore()
	j, err := js.GetJob(context.Background(), "audit.stats")
	if err != nil || j.LastResult == "" {
		t.Fatalf("job persisted: %+v %v", j, err)
	}

	// file：multipart 上传走 GORM 元数据
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "报告.txt")
	_, _ = fw.Write([]byte("内容"))
	_ = mw.Close()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/files", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+tok)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("file upload: %d %s", rec.Code, rec.Body.String())
	}
}
