// Package contract 是 L1 合同管理模块（ToB 核心业务）：合同登记、
// 审批（流程定义联动）、生效/完结，以及内置到期扫描任务（临近到期
// 自动记入审计链，供督办）。
package contract

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/basalt-go/basalt/pkg/kernel/app"
	"github.com/basalt-go/basalt/pkg/kernel/web"
	"github.com/basalt-go/basalt/pkg/modules/audit"
	"github.com/basalt-go/basalt/pkg/modules/identity"
	"github.com/basalt-go/basalt/pkg/modules/job"
	"github.com/basalt-go/basalt/pkg/modules/workflow"
)

const Version = "0.1.0"

const DefaultDefKey = "contract"

// 合同状态：draft → reviewing → active → finished；驳回回退可改再提。
const (
	StDraft     = "draft"
	StReviewing = "reviewing"
	StActive    = "active"
	StRejected  = "rejected"
	StFinished  = "finished"
)

const (
	PermManage = "contract:manage"
	PermRead   = "contract:read"
)

var (
	ErrNotFound      = errors.New("contract: not found")
	ErrNotCreator    = errors.New("contract: only creator can submit")
	ErrBadTransition = errors.New("contract: invalid status transition")
)

// Contract 是一份合同。
type Contract struct {
	ID             string    `json:"id" gorm:"primaryKey;size:40"`
	ContractNum    string    `json:"contract_num" gorm:"size:64;index"`
	Title          string    `json:"title" gorm:"size:255"`
	Party          string    `json:"party" gorm:"size:255"` // 对方单位
	Amount         int64     `json:"amount"`                // 分
	SignDate       time.Time `json:"sign_date"`
	ExpireDate     time.Time `json:"expire_date" gorm:"index"`
	Status         string    `json:"status" gorm:"size:16;index"`
	CreatorID      string    `json:"creator_id" gorm:"size:40;index"`
	WorkflowInstID string    `json:"workflow_instance_id,omitempty" gorm:"size:40"`
	CreatedAt      time.Time `json:"created_at"`
}

// Store 抽象合同持久化。
type Store interface {
	Create(ctx context.Context, c Contract) error
	Update(ctx context.Context, c Contract) error
	Get(ctx context.Context, id string) (Contract, error)
	List(ctx context.Context, creatorID string) ([]Contract, error)
	ListExpiring(ctx context.Context, before time.Time) ([]Contract, error)
}

type MemStore struct {
	mu sync.RWMutex
	m  map[string]Contract
}

func NewMemStore() *MemStore { return &MemStore{m: map[string]Contract{}} }

func (s *MemStore) Create(_ context.Context, c Contract) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[c.ID] = c
	return nil
}

func (s *MemStore) Update(_ context.Context, c Contract) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.m[c.ID]; !ok {
		return ErrNotFound
	}
	s.m[c.ID] = c
	return nil
}

func (s *MemStore) Get(_ context.Context, id string) (Contract, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.m[id]
	if !ok {
		return Contract{}, ErrNotFound
	}
	return c, nil
}

func (s *MemStore) List(_ context.Context, creatorID string) ([]Contract, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Contract
	for _, c := range s.m {
		if creatorID == "" || c.CreatorID == creatorID {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (s *MemStore) ListExpiring(_ context.Context, before time.Time) ([]Contract, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Contract
	for _, c := range s.m {
		if c.Status == StActive && !c.ExpireDate.IsZero() && c.ExpireDate.Before(before) {
			out = append(out, c)
		}
	}
	return out, nil
}

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

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

func (*Module) Name() string    { return "contract" }
func (*Module) Version() string { return Version }

func (*Module) Dependencies() []string {
	return []string{"identity", "workflow", "audit", "job"}
}

func (*Module) Describe() app.ModuleInfo {
	return app.ModuleInfo{Name: "contract", Version: Version,
		Permissions: []string{PermManage, PermRead}}
}

func (m *Module) Init(_ context.Context, a *app.App) error {
	idm, ok := a.Registry().MustGet("identity").(*identity.Module)
	if !ok {
		return errors.New("contract: identity module not available")
	}
	wfm, ok := a.Registry().Get("workflow")
	if !ok {
		return errors.New("contract: workflow module not available")
	}
	adm, ok := a.Registry().Get("audit")
	if !ok {
		return errors.New("contract: audit module not available")
	}
	jm, ok := a.Registry().Get("job")
	if !ok {
		return errors.New("contract: job module not available")
	}
	m.facade = idm.Facade()
	m.wf = wfm.(*workflow.Module)
	m.recorder = adm.(*audit.Module).Recorder()
	m.users = idm.Users()
	m.bootAdmin = a.Config().BootstrapAdminUsername

	// 审批终态联动：通过→生效；驳回→退回。
	wfm.(*workflow.Module).OnFinish(func(_ context.Context, in workflow.Instance) {
		prefix := DefaultDefKey + "-"
		if len(in.BusinessKey) <= len(prefix) || in.BusinessKey[:len(prefix)] != prefix {
			return
		}
		c, err := m.store.Get(context.Background(), in.BusinessKey[len(prefix):])
		if err != nil {
			return
		}
		switch in.Status {
		case workflow.StatusApproved:
			c.Status = StActive
		case workflow.StatusRejected, workflow.StatusCanceled:
			c.Status = StRejected
		default:
			return
		}
		_ = m.store.Update(context.Background(), c)
		m.audit("contract."+c.Status, c.ID, "amount", c.Amount)
	})

	// 内置任务：每日扫描 30 天内到期合同，记入审计（督办依据）。
	jm.(*job.Module).Manager().Register("contract.expiry-scan", "合同到期扫描",
		"扫描 30 天内到期的生效合同并记入审计链", 86400,
		func(ctx context.Context) (string, error) {
			expiring, err := m.store.ListExpiring(ctx, time.Now().AddDate(0, 0, 30))
			if err != nil {
				return "", err
			}
			for _, c := range expiring {
				m.audit("contract.expiring", c.ID, "expire_date", c.ExpireDate.Format("2006-01-02"))
			}
			return "expiring contracts: " + itoa(len(expiring)), nil
		})

	auth := m.facade.Auth
	req := m.facade.Require
	g := a.Router().Group("/api/v1/contracts")
	g.Handle(http.MethodPost, "", auth(m.create)) // 登记本人合同：登录即可
	g.Handle(http.MethodGet, "", auth(m.list))    // mine；all 需 read
	g.Handle(http.MethodPost, "/{id}/submit", auth(m.submit))
	g.Handle(http.MethodPost, "/{id}/finish", auth(req(PermManage, m.finish)))
	return nil
}

// Start 注册默认合同审批模板。
func (m *Module) Start(ctx context.Context) error {
	if _, err := m.wf.Defs().GetDefByKey(ctx, DefaultDefKey); err == nil {
		return nil
	}
	approvers := []string{}
	if admin, err := m.users.GetByUsername(ctx, m.bootAdmin); err == nil {
		approvers = append(approvers, admin.ID)
	}
	if len(approvers) == 0 {
		return nil
	}
	return m.wf.Defs().CreateDef(ctx, workflow.Definition{
		ID: newID(), Key: DefaultDefKey, Name: "合同会签",
		Category: "biz", Status: "active",
		Steps: []workflow.Step{{Name: "法务/管理层会签", Approvers: approvers, Mode: workflow.ModeCountersign}},
	})
}

func (*Module) Stop(context.Context) error { return nil }

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
		Module: "contract", Event: event, Resource: resource, Detail: detail,
		Time: time.Now().UTC(),
	})
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func (m *Module) create(w http.ResponseWriter, r *http.Request) {
	u, err := m.facade.User(r)
	if err != nil {
		web.Err(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var req struct {
		ContractNum string `json:"contract_num"`
		Title       string `json:"title"`
		Party       string `json:"party"`
		Amount      int64  `json:"amount"`
		SignDate    string `json:"sign_date"`
		ExpireDate  string `json:"expire_date"`
	}
	if !decode(w, r, &req) || req.Title == "" || req.Party == "" || req.Amount <= 0 {
		web.Err(w, http.StatusBadRequest, "title, party and positive amount are required")
		return
	}
	parse := func(s string) time.Time {
		if t, err := time.Parse("2006-01-02", s); err == nil {
			return t
		}
		return time.Time{}
	}
	c := Contract{
		ID: newID(), ContractNum: req.ContractNum, Title: req.Title, Party: req.Party,
		Amount: req.Amount, SignDate: parse(req.SignDate), ExpireDate: parse(req.ExpireDate),
		Status: StDraft, CreatorID: u.ID, CreatedAt: time.Now().UTC(),
	}
	if err := m.store.Create(r.Context(), c); err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	m.audit("contract.created", c.ID, "party", c.Party, "amount", c.Amount)
	web.Created(w, c)
}

func (m *Module) list(w http.ResponseWriter, r *http.Request) {
	u, err := m.facade.User(r)
	if err != nil {
		web.Err(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	scope := u.ID
	if r.URL.Query().Get("scope") == "all" {
		if !m.facade.Can(r, PermRead) && !m.facade.Can(r, PermManage) {
			web.Err(w, http.StatusForbidden, "forbidden")
			return
		}
		scope = ""
	}
	list, err := m.store.List(r.Context(), scope)
	if err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.OK(w, list)
}

func (m *Module) submit(w http.ResponseWriter, r *http.Request) {
	u, err := m.facade.User(r)
	if err != nil {
		web.Err(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	c, err := m.store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		web.Err(w, http.StatusNotFound, "contract not found")
		return
	}
	if c.CreatorID != u.ID {
		web.Err(w, http.StatusForbidden, "only creator can submit")
		return
	}
	if c.Status != StDraft && c.Status != StRejected {
		web.Err(w, http.StatusConflict, "contract already submitted")
		return
	}
	bk := DefaultDefKey + "-" + c.ID
	summary := "合同会签：" + c.Title + "（" + c.Party + "）"
	in, err := m.wf.StartByDefinition(r.Context(), DefaultDefKey, bk, summary, u.ID, nil)
	if err != nil {
		web.Err(w, http.StatusConflict, "workflow: "+err.Error())
		return
	}
	c.Status, c.WorkflowInstID = StReviewing, in.ID
	if err := m.store.Update(r.Context(), c); err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	m.audit("contract.submitted", c.ID, "wf", in.ID)
	web.OK(w, c)
}

func (m *Module) finish(w http.ResponseWriter, r *http.Request) {
	c, err := m.store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		web.Err(w, http.StatusNotFound, "contract not found")
		return
	}
	if c.Status != StActive {
		web.Err(w, http.StatusConflict, "only active contracts can be finished")
		return
	}
	c.Status = StFinished
	if err := m.store.Update(r.Context(), c); err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	m.audit("contract.finished", c.ID)
	web.OK(w, c)
}
