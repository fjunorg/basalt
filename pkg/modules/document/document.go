// Package document 是 L1 公文流转模块（ToG 标志性业务）：
// 发文拟稿与收文登记、密级/缓急标注、审批流转（流程定义联动）、归档。
// 联动模式与 finance 相同：提交即按定义发起审批，终态经
// workflow.OnFinish 回写（通过→待归档，驳回→退回）。
package document

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
	"github.com/basalt-go/basalt/pkg/kernel/dataperm"
	"github.com/basalt-go/basalt/pkg/kernel/web"
	"github.com/basalt-go/basalt/pkg/modules/audit"
	"github.com/basalt-go/basalt/pkg/modules/identity"
	"github.com/basalt-go/basalt/pkg/modules/org"
	"github.com/basalt-go/basalt/pkg/modules/workflow"
)

const Version = "0.1.0"

const DefaultDefKey = "document"

// 公文状态：draft → circulating → approved(待归档) → archived；驳回回退。
const (
	StDraft       = "draft"
	StCirculating = "circulating"
	StApproved    = "approved" // 审批通过待归档
	StRejected    = "rejected"
	StArchived    = "archived"
)

// 密级（等保场景敏感字段：公文密级进入审计链）。
const (
	SecPublic = "普通"
	SecSecret = "秘密"
	SecTop    = "机密"
)

// 权限点。
const (
	PermManage = "document:manage" // 登记/归档/全量查看
	PermRead   = "document:read"
)

var (
	ErrNotFound      = errors.New("document: not found")
	ErrNotCreator    = errors.New("document: only creator can submit")
	ErrBadTransition = errors.New("document: invalid status transition")
	ErrBadSecurity   = errors.New("document: invalid security level")
)

// Doc 是一条公文（发文或收文）。
type Doc struct {
	ID             string    `json:"id" gorm:"primaryKey;size:40"`
	DocNum         string    `json:"doc_num" gorm:"size:64;index"` // 文号，如 玄武〔2026〕12号
	Type           string    `json:"type" gorm:"size:16"`          // outbound 发文 | inbound 收文
	Title          string    `json:"title" gorm:"size:255"`
	Security       string    `json:"security" gorm:"size:16"`    // 普通|秘密|机密
	Urgency        string    `json:"urgency" gorm:"size:16"`     // 平急|加急|特急
	Correspond     string    `json:"correspond" gorm:"size:255"` // 发文单位（收文）/主送单位（发文）
	Content        string    `json:"content" gorm:"type:text"`
	Status         string    `json:"status" gorm:"size:16;index"`
	CreatorID      string    `json:"creator_id" gorm:"size:40;index"`
	DeptID         string    `json:"dept_id,omitempty" gorm:"size:40;index"` // 数据权限维度：创建时取拟稿人部门
	WorkflowInstID string    `json:"workflow_instance_id,omitempty" gorm:"size:40"`
	CreatedAt      time.Time `json:"created_at"`
}

// Store 抽象公文持久化。
type Store interface {
	Create(ctx context.Context, d Doc) error
	Update(ctx context.Context, d Doc) error
	Get(ctx context.Context, id string) (Doc, error)
	List(ctx context.Context, creatorID string) ([]Doc, error)
}

type MemStore struct {
	mu sync.RWMutex
	m  map[string]Doc
}

func NewMemStore() *MemStore { return &MemStore{m: map[string]Doc{}} }

func (s *MemStore) Create(_ context.Context, d Doc) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[d.ID] = d
	return nil
}

func (s *MemStore) Update(_ context.Context, d Doc) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.m[d.ID]; !ok {
		return ErrNotFound
	}
	s.m[d.ID] = d
	return nil
}

func (s *MemStore) Get(_ context.Context, id string) (Doc, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.m[id]
	if !ok {
		return Doc{}, ErrNotFound
	}
	return d, nil
}

func (s *MemStore) List(_ context.Context, creatorID string) ([]Doc, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Doc
	for _, d := range s.m {
		if creatorID == "" || d.CreatorID == creatorID {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
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

func (*Module) Name() string    { return "document" }
func (*Module) Version() string { return Version }

func (*Module) Dependencies() []string {
	return []string{"identity", "workflow", "audit"}
}

func (*Module) Describe() app.ModuleInfo {
	return app.ModuleInfo{Name: "document", Version: Version,
		Permissions: []string{PermManage, PermRead}}
}

func (m *Module) Init(_ context.Context, a *app.App) error {
	idm, ok := a.Registry().MustGet("identity").(*identity.Module)
	if !ok {
		return errors.New("document: identity module not available")
	}
	wfm, ok := a.Registry().MustGet("workflow").(*workflow.Module)
	if !ok {
		return errors.New("document: workflow module not available")
	}
	adm, ok := a.Registry().MustGet("audit").(*audit.Module)
	if !ok {
		return errors.New("document: audit module not available")
	}
	m.facade = idm.Facade()
	m.wf = wfm
	m.recorder = adm.Recorder()
	m.users = idm.Users()
	m.bootAdmin = a.Config().BootstrapAdminUsername

	// 审批终态联动：通过→待归档；驳回/撤回→退回。
	wfm.OnFinish(func(_ context.Context, in workflow.Instance) {
		prefix := DefaultDefKey + "-"
		if len(in.BusinessKey) <= len(prefix) || in.BusinessKey[:len(prefix)] != prefix {
			return
		}
		d, err := m.store.Get(context.Background(), in.BusinessKey[len(prefix):])
		if err != nil {
			return
		}
		switch in.Status {
		case workflow.StatusApproved:
			d.Status = StApproved
		case workflow.StatusRejected, workflow.StatusCanceled:
			d.Status = StRejected
		default:
			return
		}
		_ = m.store.Update(context.Background(), d)
		m.audit("doc."+d.Status, d.ID, "security", d.Security)
	})

	auth := m.facade.Auth
	req := m.facade.Require
	// 数据权限：可选获取 org 部门树（缺失时子树档降级本部门）。
	var deptResolver identity.DeptResolver
	if mod, ok := a.Registry().Get("org"); ok {
		if orgM, ok := mod.(*org.Module); ok {
			deptResolver = orgM
		}
	}
	dp := identity.NewDataScopeResolver(m.facade, deptResolver)
	g := a.Router().Group("/api/v1/documents")
	g.Handle(http.MethodPost, "", auth(m.create))         // 拟稿/登记：登录即可
	g.Handle(http.MethodGet, "", auth(dp.Inject(m.list))) // mine；all 需 read + 数据范围
	g.Handle(http.MethodPost, "/{id}/submit", auth(m.submit))
	g.Handle(http.MethodPost, "/{id}/archive", auth(req(PermManage, m.archive)))
	return nil
}

// Start 注册默认公文流程模板（时机同 finance：引导用户已就绪）。
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
		ID: newID(), Key: DefaultDefKey, Name: "公文审签",
		Category: "gov", Status: "active",
		Steps: []workflow.Step{{Name: "核稿签发", Approvers: approvers, Mode: workflow.ModeCountersign}},
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
		Module: "document", Event: event, Resource: resource, Detail: detail,
		Time: time.Now().UTC(),
	})
}

func validSecurity(s string) bool { return s == SecPublic || s == SecSecret || s == SecTop }

func (m *Module) create(w http.ResponseWriter, r *http.Request) {
	u, err := m.facade.User(r)
	if err != nil {
		web.Err(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var req struct {
		DocNum     string `json:"doc_num"`
		Type       string `json:"type"`
		Title      string `json:"title"`
		Security   string `json:"security"`
		Urgency    string `json:"urgency"`
		Correspond string `json:"correspond"`
		Content    string `json:"content"`
	}
	if !decode(w, r, &req) || req.Title == "" {
		web.Err(w, http.StatusBadRequest, "title is required")
		return
	}
	if req.Type != "outbound" && req.Type != "inbound" {
		req.Type = "outbound"
	}
	if req.Security == "" {
		req.Security = SecPublic
	}
	if !validSecurity(req.Security) {
		web.Err(w, http.StatusBadRequest, "security must be one of 普通/秘密/机密")
		return
	}
	if req.Urgency == "" {
		req.Urgency = "平急"
	}
	d := Doc{
		ID: newID(), DocNum: req.DocNum, Type: req.Type, Title: req.Title,
		Security: req.Security, Urgency: req.Urgency, Correspond: req.Correspond,
		Content: req.Content, Status: StDraft, CreatorID: u.ID, DeptID: u.DeptID,
		CreatedAt: time.Now().UTC(),
	}
	if err := m.store.Create(r.Context(), d); err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	m.audit("doc.created", d.ID, "type", d.Type, "security", d.Security)
	web.Created(w, d)
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
	// 数据权限裁剪：all 分支按部门 + 本人叠加过滤（mine 天然本人）
	if dp, ok := dataperm.FromContext(r.Context()); ok && scope == "" {
		kept := list[:0]
		for _, d := range list {
			if dp.VisibleRow(d.DeptID, d.CreatorID) {
				kept = append(kept, d)
			}
		}
		list = kept
	}
	web.OK(w, list)
}

func (m *Module) submit(w http.ResponseWriter, r *http.Request) {
	u, err := m.facade.User(r)
	if err != nil {
		web.Err(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	d, err := m.store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		web.Err(w, http.StatusNotFound, "document not found")
		return
	}
	if d.CreatorID != u.ID {
		web.Err(w, http.StatusForbidden, "only creator can submit")
		return
	}
	if d.Status != StDraft && d.Status != StRejected {
		web.Err(w, http.StatusConflict, "document already submitted")
		return
	}
	bk := DefaultDefKey + "-" + d.ID
	summary := fmt.Sprintf("公文《%s》（%s·%s）", d.Title, d.Type, d.Security)
	in, err := m.wf.StartByDefinition(r.Context(), DefaultDefKey, bk, summary, u.ID, nil)
	if err != nil {
		web.Err(w, http.StatusConflict, "workflow: "+err.Error())
		return
	}
	d.Status, d.WorkflowInstID = StCirculating, in.ID
	if err := m.store.Update(r.Context(), d); err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	m.audit("doc.submitted", d.ID, "wf", in.ID)
	web.OK(w, d)
}

func (m *Module) archive(w http.ResponseWriter, r *http.Request) {
	d, err := m.store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		web.Err(w, http.StatusNotFound, "document not found")
		return
	}
	if d.Status != StApproved {
		web.Err(w, http.StatusConflict, "only approved documents can be archived")
		return
	}
	d.Status = StArchived
	if err := m.store.Update(r.Context(), d); err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	m.audit("doc.archived", d.ID, "doc_num", d.DocNum)
	web.OK(w, d)
}
