// Package finance 是 L1 财务业务模块（ToB/ToG 核心）：报销、付款、
// 发票登记与部门预算。核心联动：报销提交即按流程定义发起审批
// （预算预检）→ 审批通过回写单据并占用预算 → 付款闭环。
// 金额一律 int64 分，避免浮点误差。
package finance

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/basalt-go/basalt/pkg/kernel/app"
	"github.com/basalt-go/basalt/pkg/kernel/web"
	"github.com/basalt-go/basalt/pkg/modules/audit"
	"github.com/basalt-go/basalt/pkg/modules/identity"
	"github.com/basalt-go/basalt/pkg/modules/workflow"
)

const Version = "0.1.0"

// 默认流程定义键（finance Init 时若无则注册单步 admin 审批模板，
// 实际部署经流程定义管理页编排多级步骤）。
const DefaultExpenseDefKey = "expense"

// 报销单状态机：draft → submitted → approved/rejected → paid。
const (
	ExpDraft     = "draft"
	ExpSubmitted = "submitted"
	ExpApproved  = "approved"
	ExpRejected  = "rejected"
	ExpPaid      = "paid"
)

// 权限点。
const (
	PermExpenseList  = "finance:expense:list"
	PermExpenseApply = "finance:expense:apply"
	PermInvoice      = "finance:invoice:manage"
	PermBudget       = "finance:budget:manage"
	PermPayment      = "finance:payment:manage"
)

var (
	ErrNotFound      = errors.New("finance: not found")
	ErrBadTransition = errors.New("finance: invalid status transition")
	ErrOverBudget    = errors.New("finance: over department budget")
	ErrNotApplicant  = errors.New("finance: only applicant can submit")
	ErrDupInvoice    = errors.New("finance: invoice code already registered")
	ErrNotApproved   = errors.New("finance: expense not approved")
)

// ExpenseItem 是报销明细行。
type ExpenseItem struct {
	Name   string `json:"name"`
	Amount int64  `json:"amount"` // 分
	Memo   string `json:"memo,omitempty"`
}

// Expense 是报销单。
type Expense struct {
	ID             string        `json:"id" gorm:"primaryKey;size:40"`
	BusinessKey    string        `json:"business_key" gorm:"size:64"`
	ApplicantID    string        `json:"applicant_id" gorm:"size:40;index"`
	DeptID         string        `json:"dept_id" gorm:"size:40;index"`
	Amount         int64         `json:"amount"` // 分（明细合计）
	Reason         string        `json:"reason" gorm:"size:512"`
	Items          []ExpenseItem `json:"items" gorm:"serializer:json"`
	InvoiceIDs     []string      `json:"invoice_ids" gorm:"serializer:json"`
	Status         string        `json:"status" gorm:"size:16;index"`
	WorkflowInstID string        `json:"workflow_instance_id,omitempty" gorm:"size:40"`
	CreatedAt      time.Time     `json:"created_at"`
}

// Invoice 是发票登记。
type Invoice struct {
	ID        string    `json:"id" gorm:"primaryKey;size:40"`
	Code      string    `json:"code" gorm:"uniqueIndex;size:64"`
	Title     string    `json:"title" gorm:"size:255"`
	TaxNo     string    `json:"tax_no" gorm:"size:64"`
	Amount    int64     `json:"amount"`
	Date      time.Time `json:"date"`
	CreatedAt time.Time `json:"created_at"`
}

// Budget 是部门账期预算（单位分）。
type Budget struct {
	DeptID  string    `json:"dept_id" gorm:"primaryKey;size:40"`
	Period  string    `json:"period" gorm:"primaryKey;size:7"` // YYYY-MM
	Limit   int64     `json:"limit"`                           // -1 不限
	Used    int64     `json:"used"`
	Updated time.Time `json:"updated_at"`
}

// Payment 是付款记录。
type Payment struct {
	ID        string    `json:"id" gorm:"primaryKey;size:40"`
	ExpenseID string    `json:"expense_id" gorm:"size:40;index"`
	Payee     string    `json:"payee" gorm:"size:255"`
	Amount    int64     `json:"amount"`
	Status    string    `json:"status" gorm:"size:16"`
	CreatedAt time.Time `json:"created_at"`
}

// Store 抽象财务持久化。
type Store interface {
	CreateExpense(ctx context.Context, e Expense) error
	UpdateExpense(ctx context.Context, e Expense) error
	GetExpense(ctx context.Context, id string) (Expense, error)
	ListExpenses(ctx context.Context, applicantID string) ([]Expense, error)
	CreateInvoice(ctx context.Context, iv Invoice) error
	ListInvoices(ctx context.Context) ([]Invoice, error)
	SetBudget(ctx context.Context, b Budget) error
	GetBudget(ctx context.Context, deptID, period string) (Budget, error)
	AddBudgetUsed(ctx context.Context, deptID, period string, delta int64) error
	ListBudgets(ctx context.Context) ([]Budget, error)
	CreatePayment(ctx context.Context, p Payment) error
	UpdatePayment(ctx context.Context, p Payment) error
	ListPayments(ctx context.Context) ([]Payment, error)
}

// MemStore 内存实现。
type MemStore struct {
	mu       sync.RWMutex
	expenses map[string]Expense
	invoices map[string]Invoice
	budgets  map[string]Budget // dept\x00period
	payments map[string]Payment
}

func NewMemStore() *MemStore {
	return &MemStore{
		expenses: map[string]Expense{}, invoices: map[string]Invoice{},
		budgets: map[string]Budget{}, payments: map[string]Payment{},
	}
}

func (s *MemStore) CreateExpense(_ context.Context, e Expense) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expenses[e.ID] = e
	return nil
}

func (s *MemStore) UpdateExpense(_ context.Context, e Expense) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.expenses[e.ID]; !ok {
		return ErrNotFound
	}
	s.expenses[e.ID] = e
	return nil
}

func (s *MemStore) GetExpense(_ context.Context, id string) (Expense, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.expenses[id]
	if !ok {
		return Expense{}, ErrNotFound
	}
	return e, nil
}

func (s *MemStore) ListExpenses(_ context.Context, applicantID string) ([]Expense, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Expense
	for _, e := range s.expenses {
		if applicantID == "" || e.ApplicantID == applicantID {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (s *MemStore) CreateInvoice(_ context.Context, iv Invoice) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.invoices {
		if e.Code == iv.Code {
			return ErrDupInvoice
		}
	}
	s.invoices[iv.ID] = iv
	return nil
}

func (s *MemStore) ListInvoices(_ context.Context) ([]Invoice, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Invoice, 0, len(s.invoices))
	for _, iv := range s.invoices {
		out = append(out, iv)
	}
	return out, nil
}

func bkey(deptID, period string) string { return deptID + "\x00" + period }

func (s *MemStore) SetBudget(_ context.Context, b Budget) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.budgets[bkey(b.DeptID, b.Period)] = b
	return nil
}

func (s *MemStore) GetBudget(_ context.Context, deptID, period string) (Budget, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, ok := s.budgets[bkey(deptID, period)]
	if !ok {
		return Budget{}, ErrNotFound
	}
	return b, nil
}

func (s *MemStore) AddBudgetUsed(_ context.Context, deptID, period string, delta int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := bkey(deptID, period)
	b, ok := s.budgets[k]
	if !ok {
		return ErrNotFound
	}
	b.Used += delta
	b.Updated = time.Now().UTC()
	s.budgets[k] = b
	return nil
}

func (s *MemStore) ListBudgets(_ context.Context) ([]Budget, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Budget, 0, len(s.budgets))
	for _, b := range s.budgets {
		out = append(out, b)
	}
	return out, nil
}

func (s *MemStore) CreatePayment(_ context.Context, p Payment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.payments[p.ID] = p
	return nil
}

func (s *MemStore) UpdatePayment(_ context.Context, p Payment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.payments[p.ID]; !ok {
		return ErrNotFound
	}
	s.payments[p.ID] = p
	return nil
}

func (s *MemStore) ListPayments(_ context.Context) ([]Payment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Payment, 0, len(s.payments))
	for _, p := range s.payments {
		out = append(out, p)
	}
	return out, nil
}

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// currentPeriod 当前账期（UTC 月）。
func currentPeriod() string { return time.Now().UTC().Format("2006-01") }

type Module struct {
	store     Store
	facade    *identity.Facade
	wf        *workflow.Module
	recorder  audit.Recorder
	users     identity.UserStore
	bootAdmin string
}

func New() *Module                 { return &Module{store: NewMemStore()} }
func NewWithStore(s Store) *Module { return &Module{store: s} }

func (*Module) Name() string    { return "finance" }
func (*Module) Version() string { return Version }

func (*Module) Dependencies() []string {
	return []string{"identity", "workflow", "audit"}
}

func (*Module) Describe() app.ModuleInfo {
	return app.ModuleInfo{Name: "finance", Version: Version, Permissions: []string{
		PermExpenseList, PermExpenseApply, PermInvoice, PermBudget, PermPayment,
	}}
}

func (m *Module) Init(_ context.Context, a *app.App) error {
	idm, ok := a.Registry().MustGet("identity").(*identity.Module)
	if !ok {
		return errors.New("finance: identity module not available")
	}
	wfm, ok := a.Registry().MustGet("workflow").(*workflow.Module)
	if !ok {
		return errors.New("finance: workflow module not available")
	}
	adm, ok := a.Registry().MustGet("audit").(*audit.Module)
	if !ok {
		return errors.New("finance: audit module not available")
	}
	m.facade = idm.Facade()
	m.wf = wfm
	m.recorder = adm.Recorder()
	m.users = idm.Users()
	m.bootAdmin = a.Config().BootstrapAdminUsername

	// 审批终态联动：通过→占用预算+单据 approved；驳回/撤回→回草稿。
	wfm.OnFinish(func(_ context.Context, in workflow.Instance) {
		prefix := DefaultExpenseDefKey + "-"
		if len(in.BusinessKey) <= len(prefix) || in.BusinessKey[:len(prefix)] != prefix {
			return
		}
		expID := in.BusinessKey[len(prefix):]
		e, err := m.store.GetExpense(context.Background(), expID)
		if err != nil {
			return
		}
		switch in.Status {
		case workflow.StatusApproved:
			e.Status = ExpApproved
			_ = m.store.UpdateExpense(context.Background(), e)
			if e.DeptID != "" {
				_ = m.store.AddBudgetUsed(context.Background(), e.DeptID, currentPeriod(), e.Amount)
			}
			m.audit("expense.approved", e.ID, e.Amount)
		case workflow.StatusRejected, workflow.StatusCanceled:
			e.Status = ExpRejected
			_ = m.store.UpdateExpense(context.Background(), e)
		}
	})

	auth := m.facade.Auth
	req := m.facade.Require
	g := a.Router().Group("/api/v1/finance")

	g.Handle(http.MethodPost, "/expenses", auth(m.createExpense))             // 申请类：登录即可
	g.Handle(http.MethodGet, "/expenses", auth(m.listExpenses))               // mine；all 需 list 权限
	g.Handle(http.MethodPost, "/expenses/{id}/submit", auth(m.submitExpense)) // 申请类：仅本人

	g.Handle(http.MethodGet, "/invoices", auth(req(PermInvoice, m.listInvoices)))
	g.Handle(http.MethodPost, "/invoices", auth(req(PermInvoice, m.createInvoice)))

	g.Handle(http.MethodGet, "/budgets", auth(req(PermBudget, m.listBudgets)))
	g.Handle(http.MethodPut, "/budgets/{dept}", auth(req(PermBudget, m.setBudget)))

	g.Handle(http.MethodGet, "/payments", auth(req(PermPayment, m.listPayments)))
	g.Handle(http.MethodPost, "/payments", auth(req(PermPayment, m.createPayment)))
	return nil
}

// Start 注册内置默认报销流程模板。时机在 identity.Start 之后——
// 引导管理员用户已存在，审批人取用户 ID（Init 期用户尚未创建）。
func (m *Module) Start(ctx context.Context) error {
	if _, err := m.wf.Defs().GetDefByKey(ctx, DefaultExpenseDefKey); err == nil {
		return nil // 已有定义（含管理员自定义编排），不覆盖
	}
	approvers := []string{}
	if admin, err := m.users.GetByUsername(ctx, m.bootAdmin); err == nil {
		approvers = append(approvers, admin.ID)
	}
	if len(approvers) == 0 {
		return nil // 无引导用户的环境（测试自备定义）
	}
	return m.wf.Defs().CreateDef(ctx, workflow.Definition{
		ID: newID(), Key: DefaultExpenseDefKey, Name: "差旅报销审批",
		Category: "finance", Status: "active",
		Steps: []workflow.Step{{Name: "审批", Approvers: approvers, Mode: workflow.ModeCountersign}},
	})
}
func (*Module) Stop(context.Context) error { return nil }

// Store 供联动扩展。
func (m *Module) Store() Store { return m.store }

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		web.Err(w, http.StatusBadRequest, "invalid json body")
		return false
	}
	return true
}

func (m *Module) audit(event, resource string, kv ...any) {
	if m.recorder == nil {
		return
	}
	detail, _ := json.Marshal(map[string]any{"kv": kv})
	_ = m.recorder.Record(context.Background(), audit.Event{
		Module: "finance", Event: event, Resource: resource, Detail: detail,
		Time: time.Now().UTC(),
	})
}

func (m *Module) createExpense(w http.ResponseWriter, r *http.Request) {
	u, err := m.facade.User(r)
	if err != nil {
		web.Err(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var req struct {
		DeptID     string        `json:"dept_id"`
		Reason     string        `json:"reason"`
		Items      []ExpenseItem `json:"items"`
		InvoiceIDs []string      `json:"invoice_ids"`
	}
	if !decode(w, r, &req) || req.Reason == "" || len(req.Items) == 0 {
		web.Err(w, http.StatusBadRequest, "reason and items are required")
		return
	}
	var total int64
	for _, it := range req.Items {
		if it.Amount <= 0 {
			web.Err(w, http.StatusBadRequest, "item amounts must be positive (in fen)")
			return
		}
		total += it.Amount
	}
	e := Expense{
		ID: newID(), ApplicantID: u.ID, DeptID: req.DeptID,
		Amount: total, Reason: req.Reason, Items: req.Items, InvoiceIDs: req.InvoiceIDs,
		Status: ExpDraft, CreatedAt: time.Now().UTC(),
	}
	e.BusinessKey = fmt.Sprintf("%s-%s", DefaultExpenseDefKey, e.ID)
	if err := m.store.CreateExpense(r.Context(), e); err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.Created(w, e)
}

func (m *Module) listExpenses(w http.ResponseWriter, r *http.Request) {
	u, err := m.facade.User(r)
	if err != nil {
		web.Err(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	scope := r.URL.Query().Get("scope")
	if scope == "all" {
		if !m.facade.Can(r, PermExpenseList) {
			web.Err(w, http.StatusForbidden, "forbidden")
			return
		}
		scope = "" // 全量
	} else {
		scope = u.ID // 我的
	}
	list, err := m.store.ListExpenses(r.Context(), scope)
	if err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.OK(w, list)
}

func (m *Module) submitExpense(w http.ResponseWriter, r *http.Request) {
	u, err := m.facade.User(r)
	if err != nil {
		web.Err(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	e, err := m.store.GetExpense(r.Context(), r.PathValue("id"))
	if err != nil {
		web.Err(w, http.StatusNotFound, "expense not found")
		return
	}
	if e.ApplicantID != u.ID {
		web.Err(w, http.StatusForbidden, "only applicant can submit")
		return
	}
	if e.Status != ExpDraft {
		web.Err(w, http.StatusConflict, "expense already submitted")
		return
	}
	// 预算预检（部门+账期；未设预算视为不限）
	if e.DeptID != "" {
		if b, err := m.store.GetBudget(r.Context(), e.DeptID, currentPeriod()); err == nil && b.Limit >= 0 && b.Used+e.Amount > b.Limit {
			web.WriteJSON(w, http.StatusUnprocessableEntity, http.StatusUnprocessableEntity,
				fmt.Sprintf("over budget: used %d + %d > limit %d (fen)", b.Used, e.Amount, b.Limit), nil)
			return
		}
	}
	in, err := m.wf.StartByDefinition(r.Context(), DefaultExpenseDefKey, e.BusinessKey,
		fmt.Sprintf("报销 %s（¥%.2f）", e.Reason, float64(e.Amount)/100), u.ID, nil)
	if err != nil {
		web.Err(w, http.StatusConflict, "workflow: "+err.Error())
		return
	}
	e.Status, e.WorkflowInstID = ExpSubmitted, in.ID
	if err := m.store.UpdateExpense(r.Context(), e); err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	m.audit("expense.submitted", e.ID, "amount", e.Amount, "wf", in.ID)
	web.OK(w, e)
}

func (m *Module) listInvoices(w http.ResponseWriter, r *http.Request) {
	list, _ := m.store.ListInvoices(r.Context())
	web.OK(w, list)
}

func (m *Module) createInvoice(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code   string `json:"code"`
		Title  string `json:"title"`
		TaxNo  string `json:"tax_no"`
		Amount int64  `json:"amount"`
		Date   string `json:"date"`
	}
	if !decode(w, r, &req) || req.Code == "" || req.Amount <= 0 {
		web.Err(w, http.StatusBadRequest, "code and positive amount are required")
		return
	}
	d := time.Now().UTC()
	if req.Date != "" {
		if parsed, err := time.Parse("2006-01-02", req.Date); err == nil {
			d = parsed
		}
	}
	iv := Invoice{ID: newID(), Code: req.Code, Title: req.Title, TaxNo: req.TaxNo,
		Amount: req.Amount, Date: d, CreatedAt: time.Now().UTC()}
	if err := m.store.CreateInvoice(r.Context(), iv); err != nil {
		if err == ErrDupInvoice {
			web.Err(w, http.StatusConflict, "invoice code already registered")
			return
		}
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.Created(w, iv)
}

func (m *Module) listBudgets(w http.ResponseWriter, r *http.Request) {
	list, _ := m.store.ListBudgets(r.Context())
	web.OK(w, list)
}

func (m *Module) setBudget(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Period string `json:"period"`
		Limit  int64  `json:"limit"` // -1 不限
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Period == "" {
		req.Period = currentPeriod()
	}
	b := Budget{DeptID: r.PathValue("dept"), Period: req.Period,
		Limit: req.Limit, Updated: time.Now().UTC()}
	if old, err := m.store.GetBudget(r.Context(), b.DeptID, b.Period); err == nil {
		b.Used = old.Used
	}
	if err := m.store.SetBudget(r.Context(), b); err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	m.audit("budget.set", b.DeptID, "period", b.Period, "limit", b.Limit)
	web.OK(w, b)
}

func (m *Module) listPayments(w http.ResponseWriter, r *http.Request) {
	list, _ := m.store.ListPayments(r.Context())
	web.OK(w, list)
}

func (m *Module) createPayment(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ExpenseID string `json:"expense_id"`
		Payee     string `json:"payee"`
	}
	if !decode(w, r, &req) || req.ExpenseID == "" || req.Payee == "" {
		web.Err(w, http.StatusBadRequest, "expense_id and payee are required")
		return
	}
	e, err := m.store.GetExpense(r.Context(), req.ExpenseID)
	if err != nil {
		web.Err(w, http.StatusNotFound, "expense not found")
		return
	}
	if e.Status != ExpApproved {
		web.Err(w, http.StatusConflict, "expense not approved")
		return
	}
	p := Payment{ID: newID(), ExpenseID: e.ID, Payee: req.Payee,
		Amount: e.Amount, Status: "paid", CreatedAt: time.Now().UTC()}
	if err := m.store.CreatePayment(r.Context(), p); err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	e.Status = ExpPaid
	_ = m.store.UpdateExpense(r.Context(), e)
	m.audit("payment.created", p.ID, "expense", e.ID, "amount", e.Amount)
	web.Created(w, p)
}
