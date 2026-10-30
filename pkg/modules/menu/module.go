package menu

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/basalt-go/basalt/pkg/kernel/app"
	"github.com/basalt-go/basalt/pkg/kernel/web"
	"github.com/basalt-go/basalt/pkg/modules/identity"
)

type Module struct {
	store  Store
	facade *identity.Facade
}

func New() *Module { return &Module{store: NewMemStore()} }

// NewWithStore 注入持久化实现；New() 内存路径保留。
func NewWithStore(s Store) *Module { return &Module{store: s} }

func (*Module) Name() string           { return "menu" }
func (*Module) Version() string        { return Version }
func (*Module) Dependencies() []string { return []string{"identity"} }

func (*Module) Describe() app.ModuleInfo {
	return app.ModuleInfo{Name: "menu", Version: Version,
		Permissions: []string{PermList, PermManage}}
}

func (m *Module) Init(_ context.Context, a *app.App) error {
	idm, ok := a.Registry().MustGet("identity").(*identity.Module)
	if !ok {
		return errors.New("menu: identity module not available")
	}
	m.facade = idm.Facade()

	auth := m.facade.Auth
	req := m.facade.Require
	g := a.Router().Group("/api/v1/menus")

	g.Handle(http.MethodGet, "/me", auth(m.myMenus))
	g.Handle(http.MethodGet, "", auth(req(PermList, m.list)))
	g.Handle(http.MethodPost, "", auth(req(PermManage, m.create)))
	g.Handle(http.MethodPatch, "/{id}", auth(req(PermManage, m.update)))
	g.Handle(http.MethodDelete, "/{id}", auth(req(PermManage, m.delete)))

	// 角色绑定端点按契约挂 /api/v1/roles/{id}/menus（权限点复用
	// identity:role:manage，与 identity 该组既有路由共存）。
	rg := a.Router().Group("/api/v1/roles")
	rg.Handle(http.MethodGet, "/{id}/menus", auth(req("identity:role:manage", m.getRoleMenus)))
	rg.Handle(http.MethodPut, "/{id}/menus", auth(req("identity:role:manage", m.setRoleMenus)))
	return nil
}

// Start 幂等播种：库中无任何菜单时初始化 22 页种子（语义化 ID）；
// 已有数据（手工配置/前次播种）不覆盖。
func (m *Module) Start(ctx context.Context) error {
	existing, err := m.store.List(ctx)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		return nil
	}
	for _, s := range Seeds {
		if err := m.store.Create(ctx, s); err != nil {
			return err
		}
	}
	return nil
}

func (*Module) Stop(context.Context) error { return nil }

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		web.Err(w, http.StatusBadRequest, "invalid json body")
		return false
	}
	return true
}

// myMenus 当前用户菜单树（契约 §2.1）：按角色权限过滤；
// 空菜单时 menus=[] 前端回退静态。
func (m *Module) myMenus(w http.ResponseWriter, r *http.Request) {
	u, err := m.facade.User(r)
	if err != nil {
		web.Err(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	all, err := m.store.List(r.Context())
	if err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	roles, _ := m.facade.RolesOf(r.Context(), u)
	permSet := map[string]bool{}
	var perms []string
	for _, role := range roles {
		for _, p := range role.Permissions {
			if !permSet[p] {
				permSet[p] = true
				perms = append(perms, p)
			}
		}
	}
	// 过滤谓词复用 identity 通配规则（与 /me perms 一致）
	hasPerm := func(p string) bool { return m.facade.Can(r, p) }
	web.OK(w, map[string]any{
		"menus":  VisibleFor(all, hasPerm),
		"perms":  perms,
		"source": "backend",
	})
}

func (m *Module) list(w http.ResponseWriter, r *http.Request) {
	all, err := m.store.List(r.Context())
	if err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	// 附角色绑定计数（管理页展示）
	type row struct {
		Menu
		RoleCount int64 `json:"role_count"`
	}
	out := make([]row, 0, len(all))
	for _, mn := range all {
		n, _ := m.store.BoundRoleCount(r.Context(), mn.ID)
		out = append(out, row{Menu: mn, RoleCount: n})
	}
	web.OK(w, out)
}

// validateParent 校验父节点存在且不成环（parent 链不得包含自身）。
func (m *Module) validateParent(ctx context.Context, id, parentID string) error {
	if parentID == "" {
		return nil
	}
	if parentID == id {
		return ErrBadParent
	}
	cur := parentID
	for i := 0; i < 64; i++ { // 深度上限保护
		if cur == "" {
			return nil
		}
		if cur == id {
			return ErrBadParent
		}
		p, err := m.store.Get(ctx, cur)
		if err != nil {
			return ErrNoParent
		}
		cur = p.ParentID
	}
	return ErrBadParent
}

func (m *Module) create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ParentID string `json:"parent_id"`
		Name     string `json:"name"`
		Path     string `json:"path"`
		Icon     string `json:"icon"`
		Perm     string `json:"perm"`
		Sort     int    `json:"sort"`
		Enabled  *bool  `json:"enabled"`
	}
	if !decode(w, r, &req) || req.Name == "" || req.Path == "" {
		web.Err(w, http.StatusBadRequest, "name and path are required")
		return
	}
	if err := m.validateParent(r.Context(), "", req.ParentID); err != nil {
		web.Err(w, http.StatusBadRequest, err.Error())
		return
	}
	if n, _ := m.store.CountByPath(r.Context(), req.Path, ""); n > 0 {
		web.Err(w, http.StatusConflict, "path already taken")
		return
	}
	mn := Menu{ID: newID(), ParentID: req.ParentID, Name: req.Name, Path: req.Path,
		Icon: req.Icon, Perm: req.Perm, Sort: req.Sort,
		Enabled: req.Enabled == nil || *req.Enabled, CreatedAt: time.Now().UTC()}
	if err := m.store.Create(r.Context(), mn); err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.Created(w, mn)
}

func (m *Module) update(w http.ResponseWriter, r *http.Request) {
	cur, err := m.store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		web.Err(w, http.StatusNotFound, "menu not found")
		return
	}
	var req struct {
		ParentID *string `json:"parent_id"`
		Name     *string `json:"name"`
		Path     *string `json:"path"`
		Icon     *string `json:"icon"`
		Perm     *string `json:"perm"`
		Sort     *int    `json:"sort"`
		Enabled  *bool   `json:"enabled"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.ParentID != nil {
		if err := m.validateParent(r.Context(), cur.ID, *req.ParentID); err != nil {
			web.Err(w, http.StatusBadRequest, err.Error())
			return
		}
		cur.ParentID = *req.ParentID
	}
	if req.Name != nil && *req.Name != "" {
		cur.Name = *req.Name
	}
	if req.Path != nil && *req.Path != "" {
		if n, _ := m.store.CountByPath(r.Context(), *req.Path, cur.ID); n > 0 {
			web.Err(w, http.StatusConflict, "path already taken")
			return
		}
		cur.Path = *req.Path
	}
	if req.Icon != nil {
		cur.Icon = *req.Icon
	}
	if req.Perm != nil {
		cur.Perm = *req.Perm
	}
	if req.Sort != nil {
		cur.Sort = *req.Sort
	}
	if req.Enabled != nil {
		cur.Enabled = *req.Enabled
	}
	if err := m.store.Update(r.Context(), cur); err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.OK(w, cur)
}

func (m *Module) delete(w http.ResponseWriter, r *http.Request) {
	err := m.store.Delete(r.Context(), r.PathValue("id"))
	switch {
	case errors.Is(err, ErrNotFound):
		web.Err(w, http.StatusNotFound, "menu not found")
	case errors.Is(err, ErrHasChildren):
		web.Err(w, http.StatusConflict, "menu has child menus")
	case errors.Is(err, ErrRoleBound):
		web.Err(w, http.StatusConflict, "menu bound to roles")
	case err != nil:
		web.Err(w, http.StatusInternalServerError, "internal error")
	default:
		web.OK(w, nil)
	}
}

func (m *Module) getRoleMenus(w http.ResponseWriter, r *http.Request) {
	ids, err := m.store.GetRoleMenus(r.Context(), r.PathValue("id"))
	if err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	if ids == nil {
		ids = []string{}
	}
	web.OK(w, map[string]any{"menu_ids": ids})
}

func (m *Module) setRoleMenus(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MenuIDs []string `json:"menu_ids"`
	}
	if !decode(w, r, &req) {
		return
	}
	if err := m.store.SetRoleMenus(r.Context(), r.PathValue("id"), req.MenuIDs); err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.OK(w, map[string]any{"menu_ids": req.MenuIDs})
}

// Seeds 是 22 页静态路由的种子菜单（与 console/src/main.ts 一一对应；
// 菜单管理页不入种子——前端固定渲染防死锁，见契约 §4）。
var Seeds = []Menu{
	{ID: "m-overview", Icon: "Odometer", Name: "概览", Path: "/overview", Sort: 10, Enabled: true},
	{ID: "m-users", Icon: "User", Name: "用户管理", Path: "/users", Perm: "identity:user:list", Sort: 20, Enabled: true},
	{ID: "m-roles", Icon: "Key", Name: "角色管理", Path: "/roles", Perm: "identity:role:manage", Sort: 30, Enabled: true},
	{ID: "m-tenants", Icon: "OfficeBuilding", Name: "租户管理", Path: "/tenants", Perm: "tenant:list", Sort: 40, Enabled: true},
	{ID: "m-org", Icon: "Share", Name: "部门与岗位", Path: "/org", Perm: "org:dept:list", Sort: 50, Enabled: true},
	{ID: "m-dict", Icon: "Collection", Name: "字典管理", Path: "/dict", Perm: "dict:type:list", Sort: 60, Enabled: true},
	{ID: "m-config", Icon: "Setting", Name: "参数配置", Path: "/config", Perm: "config:list", Sort: 70, Enabled: true},
	{ID: "m-notices", Icon: "Bell", Name: "通知公告", Path: "/notices", Perm: "notice:list", Sort: 80, Enabled: true},
	{ID: "m-notify", Icon: "Message", Name: "通知中心", Path: "/notify", Perm: "notify:list", Sort: 90, Enabled: true},
	{ID: "m-files", Icon: "Files", Name: "文件管理", Path: "/files", Perm: "file:list", Sort: 100, Enabled: true},
	{ID: "m-jobs", Icon: "Timer", Name: "定时任务", Path: "/jobs", Perm: "job:list", Sort: 110, Enabled: true},
	{ID: "m-wfdefs", Icon: "Operation", Name: "流程定义", Path: "/wfdefs", Perm: "workflow:def:manage", Sort: 120, Enabled: true},
	{ID: "m-approvals", Icon: "List", Name: "审批中心", Path: "/approvals", Sort: 130, Enabled: true},
	{ID: "m-finance", Icon: "Wallet", Name: "财务中心", Path: "/finance", Sort: 140, Enabled: true},
	{ID: "m-documents", Icon: "Document", Name: "公文中心", Path: "/documents", Sort: 150, Enabled: true},
	{ID: "m-contracts", Icon: "Briefcase", Name: "合同管理", Path: "/contracts", Sort: 160, Enabled: true},
	{ID: "m-audit", Icon: "DataAnalysis", Name: "审计链", Path: "/audit", Perm: "audit:read", Sort: 170, Enabled: true},
	{ID: "m-ai", Icon: "Cpu", Name: "AI 控制台", Path: "/ai", Sort: 180, Enabled: true},
	{ID: "m-agent-defs", Icon: "UserFilled", Name: "Agent 管理", Path: "/agent-defs", Perm: "agent:read", Sort: 190, Enabled: true},
	{ID: "m-agent-run", Icon: "Platform", Name: "Agent 运行", Path: "/agent-run", Perm: "agent:run", Sort: 200, Enabled: true},
}
