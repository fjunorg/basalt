package gormstore

// 全量 dedicated-db 路由型 store：与 RoutedDictStore（dict，见
// tenant_router.go）同一模式——tenantResolver 按请求上下文解析
// 目标库，委托对应固定库 store 的同名方法执行。
//
// 边界（不接入，理由）：
//   - identity / tenant / audit / quota：平台级数据（跨租户认证、
//     租户目录本身、全局审计链、平台配额），按 docs/tenant-isolation.md
//     的库边界定义固定主库
//   - aisafety：Engine 为进程级内存快照（词库/策略变更后整体 Reload），
//     与请求级路由语义冲突（多租户需各自快照），随按租户策略快照
//     演进后接入
//   - file：元数据已路由；文件字节流在共享磁盘 BASALT_UPLOAD_DIR
//     （Storage 接口与库无关），物理字节隔离随对象存储接入

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/basalt-go/basalt/pkg/modules/agent"
	"github.com/basalt-go/basalt/pkg/modules/contract"
	"github.com/basalt-go/basalt/pkg/modules/dict"
	"github.com/basalt-go/basalt/pkg/modules/document"
	"github.com/basalt-go/basalt/pkg/modules/file"
	"github.com/basalt-go/basalt/pkg/modules/finance"
	"github.com/basalt-go/basalt/pkg/modules/identity"
	"github.com/basalt-go/basalt/pkg/modules/job"
	"github.com/basalt-go/basalt/pkg/modules/menu"
	"github.com/basalt-go/basalt/pkg/modules/notice"
	"github.com/basalt-go/basalt/pkg/modules/notify"
	"github.com/basalt-go/basalt/pkg/modules/org"
	"github.com/basalt-go/basalt/pkg/modules/rag"
	"github.com/basalt-go/basalt/pkg/modules/sysconfig"
	"github.com/basalt-go/basalt/pkg/modules/tenant"
	"github.com/basalt-go/basalt/pkg/modules/workflow"
)

// NewRoutedResolver 构造共享路由底座（各 Routed store 内嵌）。
func NewRoutedResolver(master *gorm.DB, users identity.UserStore, tenants tenant.TenantStore, router *TenantRouter) tenantResolver {
	return tenantResolver{master: master, users: users, tenants: tenants, router: router}
}

// ---------- org ----------

// RoutedOrgStore 实现 org.Store（请求级租户路由）。
type RoutedOrgStore struct{ tenantResolver }

var _ org.Store = (*RoutedOrgStore)(nil)

func (s *RoutedOrgStore) CreateDept(ctx context.Context, d org.Dept) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return (&OrgStore{db: db}).CreateDept(ctx, d)
}
func (s *RoutedOrgStore) UpdateDept(ctx context.Context, d org.Dept) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return (&OrgStore{db: db}).UpdateDept(ctx, d)
}
func (s *RoutedOrgStore) DeleteDept(ctx context.Context, id string) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return (&OrgStore{db: db}).DeleteDept(ctx, id)
}
func (s *RoutedOrgStore) ListDepts(ctx context.Context) ([]org.Dept, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return nil, err
	}
	return (&OrgStore{db: db}).ListDepts(ctx)
}
func (s *RoutedOrgStore) CreatePost(ctx context.Context, p org.Post) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return (&OrgStore{db: db}).CreatePost(ctx, p)
}
func (s *RoutedOrgStore) UpdatePost(ctx context.Context, p org.Post) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return (&OrgStore{db: db}).UpdatePost(ctx, p)
}
func (s *RoutedOrgStore) DeletePost(ctx context.Context, id string) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return (&OrgStore{db: db}).DeletePost(ctx, id)
}
func (s *RoutedOrgStore) ListPosts(ctx context.Context) ([]org.Post, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return nil, err
	}
	return (&OrgStore{db: db}).ListPosts(ctx)
}

// ---------- sysconfig ----------

// RoutedSysConfigStore 实现 sysconfig.Store（请求级租户路由）。
type RoutedSysConfigStore struct{ tenantResolver }

var _ sysconfig.Store = (*RoutedSysConfigStore)(nil)

func (s *RoutedSysConfigStore) Create(ctx context.Context, it sysconfig.Item) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return (&SysConfigStore{db: db}).Create(ctx, it)
}
func (s *RoutedSysConfigStore) Update(ctx context.Context, it sysconfig.Item) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return (&SysConfigStore{db: db}).Update(ctx, it)
}
func (s *RoutedSysConfigStore) Delete(ctx context.Context, id string) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return (&SysConfigStore{db: db}).Delete(ctx, id)
}
func (s *RoutedSysConfigStore) List(ctx context.Context) ([]sysconfig.Item, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return nil, err
	}
	return (&SysConfigStore{db: db}).List(ctx)
}
func (s *RoutedSysConfigStore) GetByKey(ctx context.Context, key string) (sysconfig.Item, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return sysconfig.Item{}, err
	}
	return (&SysConfigStore{db: db}).GetByKey(ctx, key)
}

// ---------- notice ----------

// RoutedNoticeStore 实现 notice.Store（请求级租户路由）。
type RoutedNoticeStore struct{ tenantResolver }

var _ notice.Store = (*RoutedNoticeStore)(nil)

func (s *RoutedNoticeStore) Create(ctx context.Context, n notice.Notice) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return (&NoticeStore{db: db}).Create(ctx, n)
}
func (s *RoutedNoticeStore) Update(ctx context.Context, n notice.Notice) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return (&NoticeStore{db: db}).Update(ctx, n)
}
func (s *RoutedNoticeStore) Delete(ctx context.Context, id string) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return (&NoticeStore{db: db}).Delete(ctx, id)
}
func (s *RoutedNoticeStore) Get(ctx context.Context, id string) (notice.Notice, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return notice.Notice{}, err
	}
	return (&NoticeStore{db: db}).Get(ctx, id)
}
func (s *RoutedNoticeStore) List(ctx context.Context, publishedOnly bool) ([]notice.Notice, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return nil, err
	}
	return (&NoticeStore{db: db}).List(ctx, publishedOnly)
}

// ---------- file（元数据；字节流见文件头边界说明） ----------

// RoutedFileStore 实现 file.Store（请求级租户路由）。
type RoutedFileStore struct{ tenantResolver }

var _ file.Store = (*RoutedFileStore)(nil)

func (s *RoutedFileStore) Create(ctx context.Context, m file.Meta) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return (&FileStore{db: db}).Create(ctx, m)
}
func (s *RoutedFileStore) Delete(ctx context.Context, id string) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return (&FileStore{db: db}).Delete(ctx, id)
}
func (s *RoutedFileStore) Get(ctx context.Context, id string) (file.Meta, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return file.Meta{}, err
	}
	return (&FileStore{db: db}).Get(ctx, id)
}
func (s *RoutedFileStore) List(ctx context.Context) ([]file.Meta, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return nil, err
	}
	return (&FileStore{db: db}).List(ctx)
}

// ---------- job ----------

// RoutedJobStore 实现 job.Store（请求级租户路由：运行态按发起者
// 所在租户落库，重启恢复走各自租户库）。
type RoutedJobStore struct{ tenantResolver }

var _ job.Store = (*RoutedJobStore)(nil)

func (s *RoutedJobStore) GetJob(ctx context.Context, id string) (job.Job, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return job.Job{}, err
	}
	return (&JobStore{db: db}).GetJob(ctx, id)
}
func (s *RoutedJobStore) SaveJob(ctx context.Context, j job.Job) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return (&JobStore{db: db}).SaveJob(ctx, j)
}
func (s *RoutedJobStore) ListJobs(ctx context.Context) ([]job.Job, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return nil, err
	}
	return (&JobStore{db: db}).ListJobs(ctx)
}
func (s *RoutedJobStore) DeleteJob(ctx context.Context, id string) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return (&JobStore{db: db}).DeleteJob(ctx, id)
}
func (s *RoutedJobStore) SaveRun(ctx context.Context, rec job.RunRecord) (job.RunRecord, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return job.RunRecord{}, err
	}
	return (&JobStore{db: db}).SaveRun(ctx, rec)
}
func (s *RoutedJobStore) ListRuns(ctx context.Context, jobID string, offset, limit int) ([]job.RunRecord, int, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return nil, 0, err
	}
	return (&JobStore{db: db}).ListRuns(ctx, jobID, offset, limit)
}

// ---------- finance ----------

// RoutedFinanceStore 实现 finance.Store（请求级租户路由）。
type RoutedFinanceStore struct{ tenantResolver }

var _ finance.Store = (*RoutedFinanceStore)(nil)

func (s *RoutedFinanceStore) CreateExpense(ctx context.Context, e finance.Expense) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return (&FinanceStore{db: db}).CreateExpense(ctx, e)
}
func (s *RoutedFinanceStore) UpdateExpense(ctx context.Context, e finance.Expense) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return (&FinanceStore{db: db}).UpdateExpense(ctx, e)
}
func (s *RoutedFinanceStore) GetExpense(ctx context.Context, id string) (finance.Expense, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return finance.Expense{}, err
	}
	return (&FinanceStore{db: db}).GetExpense(ctx, id)
}
func (s *RoutedFinanceStore) ListExpenses(ctx context.Context, applicantID string) ([]finance.Expense, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return nil, err
	}
	return (&FinanceStore{db: db}).ListExpenses(ctx, applicantID)
}
func (s *RoutedFinanceStore) CreateInvoice(ctx context.Context, iv finance.Invoice) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return (&FinanceStore{db: db}).CreateInvoice(ctx, iv)
}
func (s *RoutedFinanceStore) ListInvoices(ctx context.Context) ([]finance.Invoice, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return nil, err
	}
	return (&FinanceStore{db: db}).ListInvoices(ctx)
}
func (s *RoutedFinanceStore) SetBudget(ctx context.Context, b finance.Budget) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return (&FinanceStore{db: db}).SetBudget(ctx, b)
}
func (s *RoutedFinanceStore) GetBudget(ctx context.Context, deptID, period string) (finance.Budget, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return finance.Budget{}, err
	}
	return (&FinanceStore{db: db}).GetBudget(ctx, deptID, period)
}
func (s *RoutedFinanceStore) AddBudgetUsed(ctx context.Context, deptID, period string, delta int64) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return (&FinanceStore{db: db}).AddBudgetUsed(ctx, deptID, period, delta)
}
func (s *RoutedFinanceStore) ListBudgets(ctx context.Context) ([]finance.Budget, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return nil, err
	}
	return (&FinanceStore{db: db}).ListBudgets(ctx)
}
func (s *RoutedFinanceStore) CreatePayment(ctx context.Context, p finance.Payment) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return (&FinanceStore{db: db}).CreatePayment(ctx, p)
}
func (s *RoutedFinanceStore) UpdatePayment(ctx context.Context, p finance.Payment) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return (&FinanceStore{db: db}).UpdatePayment(ctx, p)
}
func (s *RoutedFinanceStore) ListPayments(ctx context.Context) ([]finance.Payment, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return nil, err
	}
	return (&FinanceStore{db: db}).ListPayments(ctx)
}

// ---------- document ----------

// RoutedDocumentStore 实现 document.Store（请求级租户路由）。
type RoutedDocumentStore struct{ tenantResolver }

var _ document.Store = (*RoutedDocumentStore)(nil)

func (s *RoutedDocumentStore) Create(ctx context.Context, d document.Doc) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return (&DocumentStore{db: db}).Create(ctx, d)
}
func (s *RoutedDocumentStore) Update(ctx context.Context, d document.Doc) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return (&DocumentStore{db: db}).Update(ctx, d)
}
func (s *RoutedDocumentStore) Get(ctx context.Context, id string) (document.Doc, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return document.Doc{}, err
	}
	return (&DocumentStore{db: db}).Get(ctx, id)
}
func (s *RoutedDocumentStore) List(ctx context.Context, creatorID string) ([]document.Doc, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return nil, err
	}
	return (&DocumentStore{db: db}).List(ctx, creatorID)
}

// ---------- contract ----------

// RoutedContractStore 实现 contract.Store（请求级租户路由）。
type RoutedContractStore struct{ tenantResolver }

var _ contract.Store = (*RoutedContractStore)(nil)

func (s *RoutedContractStore) Create(ctx context.Context, c contract.Contract) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return (&ContractStore{db: db}).Create(ctx, c)
}
func (s *RoutedContractStore) Update(ctx context.Context, c contract.Contract) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return (&ContractStore{db: db}).Update(ctx, c)
}
func (s *RoutedContractStore) Get(ctx context.Context, id string) (contract.Contract, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return contract.Contract{}, err
	}
	return (&ContractStore{db: db}).Get(ctx, id)
}
func (s *RoutedContractStore) List(ctx context.Context, creatorID string) ([]contract.Contract, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return nil, err
	}
	return (&ContractStore{db: db}).List(ctx, creatorID)
}
func (s *RoutedContractStore) ListExpiring(ctx context.Context, before time.Time) ([]contract.Contract, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return nil, err
	}
	return (&ContractStore{db: db}).ListExpiring(ctx, before)
}

// ---------- workflow（实例 + 定义双接口） ----------

// RoutedWorkflowStore 实现 workflow.Store（请求级租户路由）。
type RoutedWorkflowStore struct{ tenantResolver }

var _ workflow.Store = (*RoutedWorkflowStore)(nil)

func (s *RoutedWorkflowStore) Create(ctx context.Context, in workflow.Instance) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return (&WorkflowStore{db: db}).Create(ctx, in)
}
func (s *RoutedWorkflowStore) Get(ctx context.Context, id string) (workflow.Instance, []workflow.Action, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return workflow.Instance{}, nil, err
	}
	return (&WorkflowStore{db: db}).Get(ctx, id)
}
func (s *RoutedWorkflowStore) Act(ctx context.Context, instanceID, actorID, act, comment string) (workflow.Instance, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return workflow.Instance{}, err
	}
	return (&WorkflowStore{db: db}).Act(ctx, instanceID, actorID, act, comment)
}
func (s *RoutedWorkflowStore) ActBranch(ctx context.Context, instanceID, actorID, act, comment string, branch int) (workflow.Instance, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return workflow.Instance{}, err
	}
	return (&WorkflowStore{db: db}).ActBranch(ctx, instanceID, actorID, act, comment, branch)
}
func (s *RoutedWorkflowStore) Delegate(ctx context.Context, instanceID, fromUserID, toUserID string) (workflow.Instance, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return workflow.Instance{}, err
	}
	return (&WorkflowStore{db: db}).Delegate(ctx, instanceID, fromUserID, toUserID)
}
func (s *RoutedWorkflowStore) ListTodo(ctx context.Context, userID string) ([]workflow.Instance, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return nil, err
	}
	return (&WorkflowStore{db: db}).ListTodo(ctx, userID)
}
func (s *RoutedWorkflowStore) ListByCreator(ctx context.Context, userID string) ([]workflow.Instance, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return nil, err
	}
	return (&WorkflowStore{db: db}).ListByCreator(ctx, userID)
}
func (s *RoutedWorkflowStore) ListAll(ctx context.Context) ([]workflow.Instance, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return nil, err
	}
	return (&WorkflowStore{db: db}).ListAll(ctx)
}

// RoutedWorkflowDefStore 实现 workflow.DefStore（请求级租户路由）。
type RoutedWorkflowDefStore struct{ tenantResolver }

var _ workflow.DefStore = (*RoutedWorkflowDefStore)(nil)

func (s *RoutedWorkflowDefStore) CreateDef(ctx context.Context, d workflow.Definition) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return (&WorkflowDefStore{db: db}).CreateDef(ctx, d)
}
func (s *RoutedWorkflowDefStore) UpdateDef(ctx context.Context, d workflow.Definition) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return (&WorkflowDefStore{db: db}).UpdateDef(ctx, d)
}
func (s *RoutedWorkflowDefStore) DeleteDef(ctx context.Context, id string) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return (&WorkflowDefStore{db: db}).DeleteDef(ctx, id)
}
func (s *RoutedWorkflowDefStore) GetDef(ctx context.Context, id string) (workflow.Definition, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return workflow.Definition{}, err
	}
	return (&WorkflowDefStore{db: db}).GetDef(ctx, id)
}
func (s *RoutedWorkflowDefStore) GetDefByKey(ctx context.Context, key string) (workflow.Definition, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return workflow.Definition{}, err
	}
	return (&WorkflowDefStore{db: db}).GetDefByKey(ctx, key)
}
func (s *RoutedWorkflowDefStore) ListDefs(ctx context.Context) ([]workflow.Definition, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return nil, err
	}
	return (&WorkflowDefStore{db: db}).ListDefs(ctx)
}

// ---------- rag ----------

// RoutedRagStore 实现 rag.Store（请求级租户路由）。
type RoutedRagStore struct{ tenantResolver }

var _ rag.Store = (*RoutedRagStore)(nil)

func (s *RoutedRagStore) CreateKB(ctx context.Context, kb rag.KnowledgeBase) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return (&RagStore{db: db}).CreateKB(ctx, kb)
}
func (s *RoutedRagStore) GetKB(ctx context.Context, id string) (rag.KnowledgeBase, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return rag.KnowledgeBase{}, err
	}
	return (&RagStore{db: db}).GetKB(ctx, id)
}
func (s *RoutedRagStore) ListKBs(ctx context.Context) ([]rag.KnowledgeBase, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return nil, err
	}
	return (&RagStore{db: db}).ListKBs(ctx)
}
func (s *RoutedRagStore) UpdateKB(ctx context.Context, kb rag.KnowledgeBase) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return (&RagStore{db: db}).UpdateKB(ctx, kb)
}
func (s *RoutedRagStore) DeleteKB(ctx context.Context, id string) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return (&RagStore{db: db}).DeleteKB(ctx, id)
}
func (s *RoutedRagStore) CreateDoc(ctx context.Context, d rag.Document) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return (&RagStore{db: db}).CreateDoc(ctx, d)
}
func (s *RoutedRagStore) GetDoc(ctx context.Context, id string) (rag.Document, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return rag.Document{}, err
	}
	return (&RagStore{db: db}).GetDoc(ctx, id)
}
func (s *RoutedRagStore) ListDocs(ctx context.Context, kbID string) ([]rag.Document, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return nil, err
	}
	return (&RagStore{db: db}).ListDocs(ctx, kbID)
}
func (s *RoutedRagStore) UpdateDoc(ctx context.Context, d rag.Document) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return (&RagStore{db: db}).UpdateDoc(ctx, d)
}
func (s *RoutedRagStore) DeleteDoc(ctx context.Context, id string) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return (&RagStore{db: db}).DeleteDoc(ctx, id)
}
func (s *RoutedRagStore) ReplaceChunks(ctx context.Context, chunks []rag.Chunk) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return (&RagStore{db: db}).ReplaceChunks(ctx, chunks)
}
func (s *RoutedRagStore) ListChunks(ctx context.Context, docID string) ([]rag.Chunk, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return nil, err
	}
	return (&RagStore{db: db}).ListChunks(ctx, docID)
}
func (s *RoutedRagStore) VectorsOfKB(ctx context.Context, kbID string) ([]rag.Chunk, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return nil, err
	}
	return (&RagStore{db: db}).VectorsOfKB(ctx, kbID)
}

// ---------- agent ----------

// RoutedAgentStore 实现 agent.Store（请求级租户路由）。
type RoutedAgentStore struct{ tenantResolver }

var _ agent.Store = (*RoutedAgentStore)(nil)

func (s *RoutedAgentStore) CreateDef(ctx context.Context, d agent.Definition) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return (&AgentStore{db: db}).CreateDef(ctx, d)
}
func (s *RoutedAgentStore) GetDef(ctx context.Context, id string) (agent.Definition, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return agent.Definition{}, err
	}
	return (&AgentStore{db: db}).GetDef(ctx, id)
}
func (s *RoutedAgentStore) ListDefs(ctx context.Context) ([]agent.Definition, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return nil, err
	}
	return (&AgentStore{db: db}).ListDefs(ctx)
}
func (s *RoutedAgentStore) UpdateDef(ctx context.Context, d agent.Definition) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return (&AgentStore{db: db}).UpdateDef(ctx, d)
}
func (s *RoutedAgentStore) DeleteDef(ctx context.Context, id string) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return (&AgentStore{db: db}).DeleteDef(ctx, id)
}
func (s *RoutedAgentStore) CreateRun(ctx context.Context, r agent.Run) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return (&AgentStore{db: db}).CreateRun(ctx, r)
}
func (s *RoutedAgentStore) GetRun(ctx context.Context, id string) (agent.Run, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return agent.Run{}, err
	}
	return (&AgentStore{db: db}).GetRun(ctx, id)
}
func (s *RoutedAgentStore) UpdateRun(ctx context.Context, r agent.Run) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return (&AgentStore{db: db}).UpdateRun(ctx, r)
}
func (s *RoutedAgentStore) ListRuns(ctx context.Context, agentID string) ([]agent.Run, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return nil, err
	}
	return (&AgentStore{db: db}).ListRuns(ctx, agentID)
}
func (s *RoutedAgentStore) AppendMsg(ctx context.Context, msg agent.RunMessage) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return (&AgentStore{db: db}).AppendMsg(ctx, msg)
}
func (s *RoutedAgentStore) ListMsgs(ctx context.Context, runID string) ([]agent.RunMessage, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return nil, err
	}
	return (&AgentStore{db: db}).ListMsgs(ctx, runID)
}

// ---------- 聚合工厂 ----------

// RoutedStores 聚合全部路由型 store（对齐 Stores 聚合形态，供
// serve 装配按需取用；平台级模块与 aisafety 不在其中，见文件头
// 边界说明）。
type RoutedStores struct{ res tenantResolver }

// NewRoutedStores 构造聚合。
func NewRoutedStores(res tenantResolver) *RoutedStores { return &RoutedStores{res: res} }

func (rs *RoutedStores) OrgStore() org.Store                 { return &RoutedOrgStore{rs.res} }
func (rs *RoutedStores) DictStore() dict.Store               { return &RoutedDictStore{rs.res} }
func (rs *RoutedStores) SysConfigStore() sysconfig.Store     { return &RoutedSysConfigStore{rs.res} }
func (rs *RoutedStores) NoticeStore() notice.Store           { return &RoutedNoticeStore{rs.res} }
func (rs *RoutedStores) FileStore() file.Store               { return &RoutedFileStore{rs.res} }
func (rs *RoutedStores) JobStore() job.Store                 { return &RoutedJobStore{rs.res} }
func (rs *RoutedStores) FinanceStore() finance.Store         { return &RoutedFinanceStore{rs.res} }
func (rs *RoutedStores) DocumentStore() document.Store       { return &RoutedDocumentStore{rs.res} }
func (rs *RoutedStores) ContractStore() contract.Store       { return &RoutedContractStore{rs.res} }
func (rs *RoutedStores) WorkflowStore() workflow.Store       { return &RoutedWorkflowStore{rs.res} }
func (rs *RoutedStores) WorkflowDefStore() workflow.DefStore { return &RoutedWorkflowDefStore{rs.res} }
func (rs *RoutedStores) RagStore() rag.Store                 { return &RoutedRagStore{rs.res} }
func (rs *RoutedStores) AgentStore() agent.Store             { return &RoutedAgentStore{rs.res} }
func (rs *RoutedStores) NotifyStore() notify.Store           { return &RoutedNotifyStore{rs.res} }
func (rs *RoutedStores) MenuStore() menu.Store               { return &RoutedMenuStore{rs.res} }

// ---------- notify ----------

// RoutedNotifyStore 实现 notify.Store（请求级租户路由）。
type RoutedNotifyStore struct{ tenantResolver }

var _ notify.Store = (*RoutedNotifyStore)(nil)

func (s *RoutedNotifyStore) CreateTemplate(ctx context.Context, t notify.Template) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return (&NotifyStore{db: db}).CreateTemplate(ctx, t)
}
func (s *RoutedNotifyStore) UpdateTemplate(ctx context.Context, t notify.Template) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return (&NotifyStore{db: db}).UpdateTemplate(ctx, t)
}
func (s *RoutedNotifyStore) DeleteTemplate(ctx context.Context, id string) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return (&NotifyStore{db: db}).DeleteTemplate(ctx, id)
}
func (s *RoutedNotifyStore) GetTemplateByCode(ctx context.Context, code string) (notify.Template, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return notify.Template{}, err
	}
	return (&NotifyStore{db: db}).GetTemplateByCode(ctx, code)
}
func (s *RoutedNotifyStore) ListTemplates(ctx context.Context) ([]notify.Template, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return nil, err
	}
	return (&NotifyStore{db: db}).ListTemplates(ctx)
}
func (s *RoutedNotifyStore) CreateSMTP(ctx context.Context, c notify.SMTPConfig) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return (&NotifyStore{db: db}).CreateSMTP(ctx, c)
}
func (s *RoutedNotifyStore) DeleteSMTP(ctx context.Context, id string) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return (&NotifyStore{db: db}).DeleteSMTP(ctx, id)
}
func (s *RoutedNotifyStore) ListSMTPs(ctx context.Context) ([]notify.SMTPConfig, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return nil, err
	}
	return (&NotifyStore{db: db}).ListSMTPs(ctx)
}
func (s *RoutedNotifyStore) CreateSMSChannel(ctx context.Context, c notify.SMSChannel) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return (&NotifyStore{db: db}).CreateSMSChannel(ctx, c)
}
func (s *RoutedNotifyStore) DeleteSMSChannel(ctx context.Context, id string) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return (&NotifyStore{db: db}).DeleteSMSChannel(ctx, id)
}
func (s *RoutedNotifyStore) ListSMSChannels(ctx context.Context) ([]notify.SMSChannel, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return nil, err
	}
	return (&NotifyStore{db: db}).ListSMSChannels(ctx)
}
func (s *RoutedNotifyStore) AppendLog(ctx context.Context, l notify.SendLog) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return (&NotifyStore{db: db}).AppendLog(ctx, l)
}
func (s *RoutedNotifyStore) ListLogs(ctx context.Context, channel string, offset, limit int) ([]notify.SendLog, int64, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return nil, 0, err
	}
	return (&NotifyStore{db: db}).ListLogs(ctx, channel, offset, limit)
}
func (s *RoutedNotifyStore) GetLog(ctx context.Context, id string) (notify.SendLog, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return notify.SendLog{}, err
	}
	return (&NotifyStore{db: db}).GetLog(ctx, id)
}
func (s *RoutedNotifyStore) CreateInbox(ctx context.Context, m notify.InboxMsg) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return (&NotifyStore{db: db}).CreateInbox(ctx, m)
}
func (s *RoutedNotifyStore) ListInbox(ctx context.Context, userID string, unreadOnly bool, offset, limit int) ([]notify.InboxMsg, int64, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return nil, 0, err
	}
	return (&NotifyStore{db: db}).ListInbox(ctx, userID, unreadOnly, offset, limit)
}
func (s *RoutedNotifyStore) MarkRead(ctx context.Context, id, userID string) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return (&NotifyStore{db: db}).MarkRead(ctx, id, userID)
}
func (s *RoutedNotifyStore) UnreadCount(ctx context.Context, userID string) (int64, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return 0, err
	}
	return (&NotifyStore{db: db}).UnreadCount(ctx, userID)
}

// ---------- menu ----------

// RoutedMenuStore 实现 menu.Store（请求级租户路由）。
type RoutedMenuStore struct{ tenantResolver }

var _ menu.Store = (*RoutedMenuStore)(nil)

func (s *RoutedMenuStore) Create(ctx context.Context, m menu.Menu) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return (&MenuStore{db: db}).Create(ctx, m)
}
func (s *RoutedMenuStore) Update(ctx context.Context, m menu.Menu) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return (&MenuStore{db: db}).Update(ctx, m)
}
func (s *RoutedMenuStore) Delete(ctx context.Context, id string) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return (&MenuStore{db: db}).Delete(ctx, id)
}
func (s *RoutedMenuStore) Get(ctx context.Context, id string) (menu.Menu, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return menu.Menu{}, err
	}
	return (&MenuStore{db: db}).Get(ctx, id)
}
func (s *RoutedMenuStore) List(ctx context.Context) ([]menu.Menu, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return nil, err
	}
	return (&MenuStore{db: db}).List(ctx)
}
func (s *RoutedMenuStore) CountByPath(ctx context.Context, path, excludeID string) (int64, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return 0, err
	}
	return (&MenuStore{db: db}).CountByPath(ctx, path, excludeID)
}
func (s *RoutedMenuStore) SetRoleMenus(ctx context.Context, roleID string, menuIDs []string) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return (&MenuStore{db: db}).SetRoleMenus(ctx, roleID, menuIDs)
}
func (s *RoutedMenuStore) GetRoleMenus(ctx context.Context, roleID string) ([]string, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return nil, err
	}
	return (&MenuStore{db: db}).GetRoleMenus(ctx, roleID)
}
func (s *RoutedMenuStore) BoundRoleCount(ctx context.Context, menuID string) (int64, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return 0, err
	}
	return (&MenuStore{db: db}).BoundRoleCount(ctx, menuID)
}

// ---------- RoutedWorkflowStore 六项操作委托 ----------

func (s *RoutedWorkflowStore) CC(ctx context.Context, instanceID, actorID string, userIDs []string) (workflow.Instance, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return workflow.Instance{}, err
	}
	return (&WorkflowStore{db: db}).CC(ctx, instanceID, actorID, userIDs)
}
func (s *RoutedWorkflowStore) DelegateAct(ctx context.Context, instanceID, fromUserID, toUserID string) (workflow.Instance, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return workflow.Instance{}, err
	}
	return (&WorkflowStore{db: db}).DelegateAct(ctx, instanceID, fromUserID, toUserID)
}
func (s *RoutedWorkflowStore) AddSign(ctx context.Context, instanceID, actorID, position string, approvers []string, mode string) (workflow.Instance, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return workflow.Instance{}, err
	}
	return (&WorkflowStore{db: db}).AddSign(ctx, instanceID, actorID, position, approvers, mode)
}
func (s *RoutedWorkflowStore) RejectTo(ctx context.Context, instanceID, actorID string, targetStep int, comment string) (workflow.Instance, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return workflow.Instance{}, err
	}
	return (&WorkflowStore{db: db}).RejectTo(ctx, instanceID, actorID, targetStep, comment)
}
func (s *RoutedWorkflowStore) ListCC(ctx context.Context, userID string) ([]workflow.Instance, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return nil, err
	}
	return (&WorkflowStore{db: db}).ListCC(ctx, userID)
}
