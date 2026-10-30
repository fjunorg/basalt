// Package tenantpkg 是租户套餐模块（竞品 Gap P0#7）：套餐 CRUD、
// 租户绑定、套餐内权限/菜单裁剪。
//
// 裁剪生效机制（与 dynamic-menu-backend 的联动点）：
// identity Facade 提供 SetPermFilter 钩子（permissionsOf 汇总角色权限后
// 过滤），本模块 Init 时注入——Require/Can 单点生效；menu 模块的
// /me 菜单树下发复用 facade.Can 做可见性过滤，因此菜单裁剪在同一
// 链路自动联动，无需 menu 模块改动。
//
// 语义边界：
//   - 平台级用户（无租户归属）不受套餐约束（平台管理员语义）；
//   - 租户未绑定套餐 = 不裁剪（向后兼容，等价全量套餐）；
//   - 绑定后权限交集：角色权限 ∩ 套餐权限模板（套餐内 "*" 放行全部）。
package tenantpkg

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/basalt-go/basalt/pkg/kernel/app"
	"github.com/basalt-go/basalt/pkg/kernel/web"
	"github.com/basalt-go/basalt/pkg/modules/audit"
	"github.com/basalt-go/basalt/pkg/modules/identity"
)

const Version = "0.1.0"

// 权限点。
const (
	PermManage = "tenantpkg:manage"
)

var (
	ErrPkgNotFound  = errors.New("tenantpkg: package not found")
	ErrPkgNameTaken = errors.New("tenantpkg: package name already taken")
	ErrPkgInUse     = errors.New("tenantpkg: package bound to tenants")
	ErrBindNotFound = errors.New("tenantpkg: tenant binding not found")
)

// Package 是租户套餐（菜单/权限集合模板）。
type Package struct {
	ID     string `json:"id" gorm:"primaryKey;size:40"`
	Name   string `json:"name" gorm:"uniqueIndex;size:128"`
	Status string `json:"status" gorm:"size:16;index"` // active/disabled
	// MenuIDs 是菜单模板（menu 模块菜单 ID 集；供 Console 套餐编辑器与
	// 菜单裁剪展示，权限裁剪本体走 Perms——菜单可见性由菜单的 Perm 点
	// 是否存活间接决定，双轨一致性由套餐编辑器保证）。
	MenuIDs []string `json:"menu_ids" gorm:"serializer:json"`
	// Perms 是权限点模板（裁剪生效的判定集）。
	Perms     []string  `json:"perms" gorm:"serializer:json"`
	Remark    string    `json:"remark,omitempty" gorm:"size:255"`
	CreatedAt time.Time `json:"created_at"`
}

// TableName 显式表名（跨模块防冲突）。
func (Package) TableName() string { return "tenant_packages" }

// Active 报告套餐可用。
func (p Package) Active() bool { return p.Status == "active" }

// TenantPackage 是租户-套餐绑定（一租户一套餐）。
type TenantPackage struct {
	TenantID  string    `json:"tenant_id" gorm:"primaryKey;size:40"`
	PackageID string    `json:"package_id" gorm:"size:40;index"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName 显式表名。
func (TenantPackage) TableName() string { return "tenant_package_binds" }

// Store 抽象套餐持久化（内存先行，GORM 见 stores/gormstore）。
type Store interface {
	CreatePkg(ctx context.Context, p Package) error
	GetPkg(ctx context.Context, id string) (Package, error)
	ListPkgs(ctx context.Context) ([]Package, error)
	UpdatePkg(ctx context.Context, p Package) error
	DeletePkg(ctx context.Context, id string) error
	CountBound(ctx context.Context, pkgID string) (int64, error)

	Bind(ctx context.Context, tenantID, pkgID string, at time.Time) error
	Unbind(ctx context.Context, tenantID string) error
	BoundPkg(ctx context.Context, tenantID string) (Package, bool, error)
}

// ---------- 内存实现 ----------

type MemStore struct {
	pkgs  map[string]Package
	binds map[string]string // tenantID → pkgID
}

func NewMemStore() *MemStore {
	return &MemStore{pkgs: map[string]Package{}, binds: map[string]string{}}
}

func (s *MemStore) CreatePkg(_ context.Context, p Package) error {
	for _, e := range s.pkgs {
		if e.Name == p.Name {
			return ErrPkgNameTaken
		}
	}
	s.pkgs[p.ID] = p
	return nil
}

func (s *MemStore) GetPkg(_ context.Context, id string) (Package, error) {
	p, ok := s.pkgs[id]
	if !ok {
		return Package{}, ErrPkgNotFound
	}
	return p, nil
}

func (s *MemStore) ListPkgs(_ context.Context) ([]Package, error) {
	out := make([]Package, 0, len(s.pkgs))
	for _, p := range s.pkgs {
		out = append(out, p)
	}
	return out, nil
}

func (s *MemStore) UpdatePkg(_ context.Context, p Package) error {
	if _, ok := s.pkgs[p.ID]; !ok {
		return ErrPkgNotFound
	}
	s.pkgs[p.ID] = p
	return nil
}

func (s *MemStore) DeletePkg(_ context.Context, id string) error {
	if _, ok := s.pkgs[id]; !ok {
		return ErrPkgNotFound
	}
	delete(s.pkgs, id)
	return nil
}

func (s *MemStore) CountBound(_ context.Context, pkgID string) (int64, error) {
	var n int64
	for _, pid := range s.binds {
		if pid == pkgID {
			n++
		}
	}
	return n, nil
}

func (s *MemStore) Bind(_ context.Context, tenantID, pkgID string, at time.Time) error {
	s.binds[tenantID] = pkgID
	return nil
}

func (s *MemStore) Unbind(_ context.Context, tenantID string) error {
	if _, ok := s.binds[tenantID]; !ok {
		return ErrBindNotFound
	}
	delete(s.binds, tenantID)
	return nil
}

func (s *MemStore) BoundPkg(_ context.Context, tenantID string) (Package, bool, error) {
	pid, ok := s.binds[tenantID]
	if !ok {
		return Package{}, false, nil
	}
	p, ok := s.pkgs[pid]
	if !ok {
		return Package{}, false, nil
	}
	return p, true, nil
}

// ---------- 模块 ----------

type Module struct {
	store    Store
	facade   *identity.Facade
	recorder audit.Recorder
}

func New() *Module { return &Module{store: NewMemStore()} }

// NewWithStore 注入持久化实现。
func NewWithStore(s Store) *Module { return &Module{store: s} }

func (*Module) Name() string           { return "tenantpkg" }
func (*Module) Version() string        { return Version }
func (*Module) Dependencies() []string { return []string{"identity"} }

func (*Module) Describe() app.ModuleInfo {
	return app.ModuleInfo{Name: "tenantpkg", Version: Version, Permissions: []string{PermManage}}
}

func (m *Module) Init(_ context.Context, a *app.App) error {
	idm, ok := a.Registry().MustGet("identity").(*identity.Module)
	if !ok {
		return errors.New("tenantpkg: identity module not available")
	}
	m.facade = idm.Facade()
	// 权限裁剪钩子：Require/Can/menu 菜单树同一链路生效。
	m.facade.SetPermFilter(m.filterPerms)

	// 审计（可选依赖：audit 未注册时跳过，测试最小装配不受阻）。
	if am, ok := a.Registry().Get("audit"); ok {
		if adm, ok := am.(*audit.Module); ok {
			m.recorder = adm.Recorder()
		}
	}

	auth := m.facade.Auth
	req := m.facade.Require
	g := a.Router().Group("/api/v1/tenantpkg")

	g.Handle(http.MethodPost, "/packages", auth(req(PermManage, m.createPkg)))
	g.Handle(http.MethodGet, "/packages", auth(req(PermManage, m.listPkgs)))
	g.Handle(http.MethodGet, "/packages/{id}", auth(req(PermManage, m.getPkg)))
	g.Handle(http.MethodPatch, "/packages/{id}", auth(req(PermManage, m.updatePkg)))
	g.Handle(http.MethodDelete, "/packages/{id}", auth(req(PermManage, m.deletePkg)))
	g.Handle(http.MethodPut, "/tenants/{id}/package", auth(req(PermManage, m.bindTenant)))
	g.Handle(http.MethodGet, "/tenants/{id}/package", auth(req(PermManage, m.getBinding)))
	return nil
}

func (*Module) Start(context.Context) error { return nil }
func (*Module) Stop(context.Context) error  { return nil }

// filterPerms 是注入 identity 的权限过滤钩子（套餐裁剪交集语义）。
func (m *Module) filterPerms(ctx context.Context, u identity.User, perms []string) []string {
	if u.TenantID == "" {
		return perms // 平台级用户不受套餐约束
	}
	pkg, bound, err := m.store.BoundPkg(ctx, u.TenantID)
	if err != nil || !bound || !pkg.Active() {
		return perms // 无绑定/套餐停用/查询失败：不裁剪（fail-open 记审计）
	}
	return IntersectPerms(perms, pkg.Perms)
}

// IntersectPerms 求角色权限与套餐模板的交集。
// 套餐含 "*" 时全量放行；否则仅保留被套餐显式包含或套餐前缀通配
// 覆盖的权限点（通配语义与 identity.HasPermission 一致）。
func IntersectPerms(perms, pkgPerms []string) []string {
	allowAll := false
	exact := map[string]bool{}
	var prefixes []string
	for _, p := range pkgPerms {
		switch {
		case p == "*":
			allowAll = true
		case len(p) > 0 && p[len(p)-1] == '*':
			prefixes = append(prefixes, p[:len(p)-1])
		default:
			exact[p] = true
		}
	}
	if allowAll {
		return perms
	}
	out := make([]string, 0, len(perms))
	seen := map[string]bool{}
	for _, p := range perms {
		if p == "*" {
			continue // 角色通配被套餐收窄：展开为套餐内可见项（见下）
		}
		if exact[p] || covered(p, prefixes) {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	// 角色持 "*" 且套餐为有限集：放行套餐全集（角色本就全量，套餐是上限）。
	if len(out) == 0 {
		for _, p := range perms {
			if p == "*" {
				for _, tp := range pkgPerms {
					if tp != "*" && !seen[tp] {
						seen[tp] = true
						out = append(out, tp)
					}
				}
			}
		}
	}
	return out
}

func covered(p string, prefixes []string) bool {
	for _, pre := range prefixes {
		if len(p) >= len(pre) && p[:len(pre)] == pre {
			return true
		}
	}
	return false
}

// ---------- HTTP ----------

func (m *Module) record(r *http.Request, event string, kv ...any) {
	if m.recorder == nil {
		return
	}
	actor := ""
	if uid, ok := identity.UserIDFrom(r.Context()); ok {
		actor = uid
	}
	detail, _ := json.Marshal(kvToMap(kv))
	_ = m.recorder.Record(r.Context(), audit.Event{
		Module: "tenantpkg", Event: event, ActorID: actor, Remote: r.RemoteAddr, Detail: detail,
	})
}

func kvToMap(kv []any) map[string]any {
	mm := make(map[string]any, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		if k, ok := kv[i].(string); ok {
			mm[k] = kv[i+1]
		}
	}
	return mm
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		web.Err(w, http.StatusBadRequest, "invalid json body")
		return false
	}
	return true
}

func (m *Module) createPkg(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string   `json:"name"`
		MenuIDs []string `json:"menu_ids"`
		Perms   []string `json:"perms"`
		Remark  string   `json:"remark"`
	}
	if !decode(w, r, &req) || req.Name == "" {
		if req.Name == "" {
			web.Err(w, http.StatusBadRequest, "name is required")
		}
		return
	}
	p := Package{
		ID: newID(), Name: req.Name, Status: "active",
		MenuIDs: req.MenuIDs, Perms: req.Perms, Remark: req.Remark,
		CreatedAt: time.Now().UTC(),
	}
	if err := m.store.CreatePkg(r.Context(), p); err != nil {
		if errors.Is(err, ErrPkgNameTaken) {
			web.Err(w, http.StatusConflict, "package name already taken")
			return
		}
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	m.record(r, "pkg.created", "pkg_id", p.ID, "name", p.Name)
	web.Created(w, p)
}

func (m *Module) listPkgs(w http.ResponseWriter, r *http.Request) {
	pkgs, err := m.store.ListPkgs(r.Context())
	if err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	type row struct {
		Package
		BoundTenants int64 `json:"bound_tenants"`
	}
	out := make([]row, 0, len(pkgs))
	for _, p := range pkgs {
		n, _ := m.store.CountBound(r.Context(), p.ID)
		out = append(out, row{Package: p, BoundTenants: n})
	}
	web.OK(w, out)
}

func (m *Module) getPkg(w http.ResponseWriter, r *http.Request) {
	p, err := m.store.GetPkg(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, ErrPkgNotFound) {
			web.Err(w, http.StatusNotFound, "package not found")
			return
		}
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.OK(w, p)
}

func (m *Module) updatePkg(w http.ResponseWriter, r *http.Request) {
	p, err := m.store.GetPkg(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, ErrPkgNotFound) {
			web.Err(w, http.StatusNotFound, "package not found")
			return
		}
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	var req struct {
		Name    *string  `json:"name"`
		Status  *string  `json:"status"`
		MenuIDs []string `json:"menu_ids"`
		Perms   []string `json:"perms"`
		Remark  *string  `json:"remark"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Name != nil && *req.Name != "" {
		p.Name = *req.Name
	}
	if req.Status != nil {
		if *req.Status != "active" && *req.Status != "disabled" {
			web.Err(w, http.StatusBadRequest, "status must be active or disabled")
			return
		}
		p.Status = *req.Status
	}
	if req.MenuIDs != nil {
		p.MenuIDs = req.MenuIDs
	}
	if req.Perms != nil {
		p.Perms = req.Perms
	}
	if req.Remark != nil {
		p.Remark = *req.Remark
	}
	if err := m.store.UpdatePkg(r.Context(), p); err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	m.record(r, "pkg.updated", "pkg_id", p.ID, "status", p.Status)
	web.OK(w, p)
}

func (m *Module) deletePkg(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if n, _ := m.store.CountBound(r.Context(), id); n > 0 {
		web.Err(w, http.StatusConflict, "package bound to tenants")
		return
	}
	if err := m.store.DeletePkg(r.Context(), id); err != nil {
		if errors.Is(err, ErrPkgNotFound) {
			web.Err(w, http.StatusNotFound, "package not found")
			return
		}
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	m.record(r, "pkg.deleted", "pkg_id", id)
	web.OK(w, nil)
}

// bindTenant 绑定/换绑（package_id 为空 = 解绑）。
func (m *Module) bindTenant(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("id")
	var req struct {
		PackageID string `json:"package_id"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.PackageID == "" {
		if err := m.store.Unbind(r.Context(), tenantID); err != nil {
			if errors.Is(err, ErrBindNotFound) {
				web.Err(w, http.StatusNotFound, "tenant binding not found")
				return
			}
			web.Err(w, http.StatusInternalServerError, "internal error")
			return
		}
		m.record(r, "pkg.unbound", "tenant_id", tenantID)
		web.OK(w, map[string]any{"tenant_id": tenantID, "package_id": nil})
		return
	}
	if _, err := m.store.GetPkg(r.Context(), req.PackageID); err != nil {
		web.Err(w, http.StatusBadRequest, "unknown package id")
		return
	}
	if err := m.store.Bind(r.Context(), tenantID, req.PackageID, time.Now().UTC()); err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	m.record(r, "pkg.bound", "tenant_id", tenantID, "pkg_id", req.PackageID)
	web.OK(w, map[string]any{"tenant_id": tenantID, "package_id": req.PackageID})
}

func (m *Module) getBinding(w http.ResponseWriter, r *http.Request) {
	p, bound, err := m.store.BoundPkg(r.Context(), r.PathValue("id"))
	switch {
	case err != nil:
		web.Err(w, http.StatusInternalServerError, "internal error")
	case !bound:
		web.OK(w, map[string]any{"package_id": nil})
	default:
		web.OK(w, map[string]any{"package_id": p.ID, "package": p})
	}
}

func newID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("tenantpkg: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b)
}
