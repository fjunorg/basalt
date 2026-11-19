// Package tenant 是 L1 多租户模块。
//
// 当前交付：租户 CRUD、租户上下文（登录用户归属）、行级隔离读取演示
// （/api/v1/tenant/users 仅返回本租户用户）。三模式中的 shared-schema
// （行级）为默认实装；shared-db / dedicated-db 的存储路由随 M1 后期
// GORM 方言层落地（枚举与校验已就位）。
package tenant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/basalt-go/basalt/pkg/kernel/app"
	"github.com/basalt-go/basalt/pkg/kernel/crypto/randutil"
	"github.com/basalt-go/basalt/pkg/kernel/web"
	"github.com/basalt-go/basalt/pkg/modules/identity"
)

const Version = "0.2.0"

// 隔离模式（docs/01-architecture.md mod-tenant）。
const (
	// IsolationRow：共享库共享表，行级 TenantID 列隔离（默认，实装中）。
	IsolationRow = "shared-schema"
	// IsolationSharedDB：共享实例独立库（M1 后期随 GORM 落地）。
	IsolationSharedDB = "shared-db"
	// IsolationDedicatedDB：独立实例独占库（M1 后期）。
	IsolationDedicatedDB = "dedicated-db"
)

// tenant 权限点（复用 identity 的通配匹配规则，admin 角色全量通过）。
const (
	PermCreate = "tenant:create"
	PermGet    = "tenant:get"
	PermList   = "tenant:list"
	PermUpdate = "tenant:update"
)

var (
	ErrCodeTaken = errors.New("tenant: code already taken")
	ErrNotFound  = errors.New("tenant: not found")
	ErrBadIsol   = errors.New("tenant: invalid isolation mode")
)

// Tenant 是租户主体。Code 用于对外标识（如子域名、发票抬头），
// ID 为内部主键，两者均唯一。gorm 标签供 pkg/stores/gormstore 复用。
type Tenant struct {
	ID            string    `json:"id" gorm:"primaryKey;size:40"`
	Code          string    `json:"code" gorm:"uniqueIndex;size:64"`
	Name          string    `json:"name" gorm:"size:128"`
	Status        string    `json:"status" gorm:"size:16;index"`
	IsolationMode string    `json:"isolation_mode" gorm:"size:32"`
	// DBRef 是 dedicated-db 模式下租户库的引用名（如 "gov-db"），
	// 实际连接串经环境变量 BASALT_TENANT_DSN_<REF大写> 提供——
	// 凭据不落库，见 docs/tenant-isolation.md。其余模式留空。
	DBRef     string    `json:"db_ref,omitempty" gorm:"size:64"`
	CreatedAt time.Time `json:"created_at"`
}

// TenantStore 抽象租户持久化（内存实现先行，GORM + 国产数据库方言随后）。
type TenantStore interface {
	Create(ctx context.Context, t Tenant) error
	Get(ctx context.Context, id string) (Tenant, error)
	List(ctx context.Context) ([]Tenant, error)
	Update(ctx context.Context, t Tenant) error
}

type MemTenantStore struct {
	mu     sync.RWMutex
	byID   map[string]Tenant
	byCode map[string]string
}

func NewMemTenantStore() *MemTenantStore {
	return &MemTenantStore{byID: map[string]Tenant{}, byCode: map[string]string{}}
}

func (s *MemTenantStore) Create(_ context.Context, t Tenant) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byCode[t.Code]; ok {
		return ErrCodeTaken
	}
	s.byID[t.ID] = t
	s.byCode[t.Code] = t.ID
	return nil
}

func (s *MemTenantStore) Get(_ context.Context, id string) (Tenant, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.byID[id]
	if !ok {
		return Tenant{}, ErrNotFound
	}
	return t, nil
}

func (s *MemTenantStore) List(_ context.Context) ([]Tenant, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Tenant, 0, len(s.byID))
	for _, t := range s.byID {
		out = append(out, t)
	}
	return out, nil
}

func (s *MemTenantStore) Update(_ context.Context, t Tenant) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.byID[t.ID]
	if !ok {
		return ErrNotFound
	}
	delete(s.byCode, old.Code)
	s.byID[t.ID] = t
	s.byCode[t.Code] = t.ID
	return nil
}

// ValidateIsolation 校验隔离模式枚举。
func ValidateIsolation(mode string) bool {
	return mode == IsolationRow || mode == IsolationSharedDB || mode == IsolationDedicatedDB
}

func NewTenantID() string { return randutil.Hex16() }

type Module struct {
	store  TenantStore
	facade *identity.Facade // 由依赖模块 identity 提供
	users  identity.UserStore
}

func decodeJSON(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}

func New() *Module { return &Module{store: NewMemTenantStore()} }

// NewWithStore 注入自定义租户存储（GORM 实现等），模块逻辑零改动。
func NewWithStore(store TenantStore) *Module { return &Module{store: store} }

func (*Module) Name() string           { return "tenant" }
func (*Module) Version() string        { return Version }
func (*Module) Dependencies() []string { return []string{"identity"} }

func (m *Module) Init(_ context.Context, a *app.App) error {
	// 依赖通道：Registry 保证 identity 已完成 Init，facade 就绪。
	idm, ok := a.Registry().MustGet("identity").(*identity.Module)
	if !ok {
		return errors.New("tenant: identity module not available")
	}
	m.facade = idm.Facade()
	m.users = idm.Users()

	auth := m.facade.Auth
	req := m.facade.Require
	g := a.Router().Group("/api/v1/tenant")

	g.Handle(http.MethodPost, "/tenants", auth(req(PermCreate, m.createTenant)))
	g.Handle(http.MethodGet, "/tenants", auth(req(PermList, m.listTenants)))
	g.Handle(http.MethodGet, "/tenants/{id}", auth(req(PermGet, m.getTenant)))
	g.Handle(http.MethodPatch, "/tenants/{id}", auth(req(PermUpdate, m.updateTenant)))
	// 登录用户视角：仅需认证。
	g.Handle(http.MethodGet, "/current", auth(m.currentTenant))
	g.Handle(http.MethodGet, "/users", auth(m.tenantUsers))
	return nil
}

func (*Module) Start(context.Context) error { return nil }
func (*Module) Stop(context.Context) error  { return nil }

type createTenantReq struct {
	Code          string `json:"code"`
	Name          string `json:"name"`
	IsolationMode string `json:"isolation_mode"`
	DBRef         string `json:"db_ref"` // dedicated-db 模式下的库引用名
}

func (m *Module) createTenant(w http.ResponseWriter, r *http.Request) {
	var req createTenantReq
	if err := decodeJSON(r, &req); err != nil {
		web.Err(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if req.Code == "" || req.Name == "" {
		web.Err(w, http.StatusBadRequest, "code and name are required")
		return
	}
	if req.IsolationMode == "" {
		req.IsolationMode = IsolationRow
	}
	if !ValidateIsolation(req.IsolationMode) {
		web.Err(w, http.StatusBadRequest, "isolation_mode must be shared-schema, shared-db or dedicated-db")
		return
	}
	if req.DBRef != "" && req.IsolationMode != IsolationDedicatedDB {
		web.Err(w, http.StatusBadRequest, "db_ref only applies to dedicated-db isolation")
		return
	}
	if req.IsolationMode == IsolationDedicatedDB && req.DBRef == "" {
		web.Err(w, http.StatusBadRequest, "db_ref is required for dedicated-db isolation")
		return
	}
	t := Tenant{
		ID: NewTenantID(), Code: req.Code, Name: req.Name,
		Status: "active", IsolationMode: req.IsolationMode, DBRef: req.DBRef,
		CreatedAt: time.Now().UTC(),
	}
	if err := m.store.Create(r.Context(), t); err != nil {
		if errors.Is(err, ErrCodeTaken) {
			web.Err(w, http.StatusConflict, "tenant code already taken")
			return
		}
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.Created(w, t)
}

func (m *Module) listTenants(w http.ResponseWriter, r *http.Request) {
	ts, err := m.store.List(r.Context())
	if err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.OK(w, ts)
}

func (m *Module) getTenant(w http.ResponseWriter, r *http.Request) {
	t, err := m.store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			web.Err(w, http.StatusNotFound, "tenant not found")
			return
		}
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.OK(w, t)
}

type updateTenantReq struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

func (m *Module) updateTenant(w http.ResponseWriter, r *http.Request) {
	var req updateTenantReq
	if err := decodeJSON(r, &req); err != nil {
		web.Err(w, http.StatusBadRequest, "invalid json body")
		return
	}
	t, err := m.store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			web.Err(w, http.StatusNotFound, "tenant not found")
			return
		}
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	if req.Name != "" {
		t.Name = req.Name
	}
	if req.Status != "" {
		if req.Status != "active" && req.Status != "disabled" {
			web.Err(w, http.StatusBadRequest, "status must be active or disabled")
			return
		}
		t.Status = req.Status
	}
	if err := m.store.Update(r.Context(), t); err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.OK(w, t)
}

// currentTenant 返回当前登录用户所属租户（从用户记录实时读取，
// 不走令牌快照——用户被移动租户后立即生效）。
func (m *Module) currentTenant(w http.ResponseWriter, r *http.Request) {
	u, err := m.facade.User(r)
	if err != nil {
		web.Err(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if u.TenantID == "" {
		web.Err(w, http.StatusNotFound, "user does not belong to a tenant")
		return
	}
	t, err := m.store.Get(r.Context(), u.TenantID)
	if err != nil {
		web.Err(w, http.StatusNotFound, "tenant not found")
		return
	}
	web.OK(w, t)
}

// tenantUsers 是行级隔离的读取演示：仅返回当前用户所属租户的成员。
func (m *Module) tenantUsers(w http.ResponseWriter, r *http.Request) {
	u, err := m.facade.User(r)
	if err != nil {
		web.Err(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if u.TenantID == "" {
		web.Err(w, http.StatusBadRequest, "user does not belong to a tenant")
		return
	}
	users, err := m.users.ListByTenant(r.Context(), u.TenantID)
	if err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.OK(w, users)
}

// Describe 实现模块自描述。
func (*Module) Describe() app.ModuleInfo {
	return app.ModuleInfo{
		Name:        "tenant",
		Version:     Version,
		Permissions: []string{PermCreate, PermGet, PermList, PermUpdate},
	}
}
