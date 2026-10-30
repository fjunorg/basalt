package menu_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/basalt-go/basalt/pkg/kernel/app"
	"github.com/basalt-go/basalt/pkg/modules/audit"
	"github.com/basalt-go/basalt/pkg/modules/identity"
	"github.com/basalt-go/basalt/pkg/modules/menu"
)

// newMenuApp 装配 identity+audit+menu（内存路径），返回 handler。
func newMenuApp(t *testing.T) http.Handler {
	t.Helper()
	a := app.New(nil)
	for _, m := range []app.Module{identity.New(), audit.New(), menu.New()} {
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
	return a.Router().Handler()
}

func do(t *testing.T, h http.Handler, method, path, token string, body any) (*httptest.ResponseRecorder, json.RawMessage) {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	return rec, env.Data
}

func login(t *testing.T, h http.Handler, user, pass string) string {
	t.Helper()
	rec, data := do(t, h, http.MethodPost, "/api/v1/identity/login", "", map[string]string{
		"username": user, "password": pass,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("login %s: %d", user, rec.Code)
	}
	var d struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(data, &d)
	return d.Token
}

// TestMenuSeedListAndMyMenus 种子播种 + admin 全量可见 + /me 下发。
func TestMenuSeedListAndMyMenus(t *testing.T) {
	h := newMenuApp(t)
	admin := login(t, h, "admin", "Basalt@2026")

	rec, data := do(t, h, http.MethodGet, "/api/v1/menus", admin, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	var list []struct {
		ID   string `json:"id"`
		Path string `json:"path"`
	}
	_ = json.Unmarshal(data, &list)
	if len(list) < 18 {
		t.Fatalf("seeds = %d（应约 20 项）", len(list))
	}

	rec, data = do(t, h, http.MethodGet, "/api/v1/menus/me", admin, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("me: %d", rec.Code)
	}
	var me struct {
		Menus  []*menu.MenuNode `json:"menus"`
		Perms  []string         `json:"perms"`
		Source string           `json:"source"`
	}
	_ = json.Unmarshal(data, &me)
	if me.Source != "backend" || len(me.Menus) == 0 || len(me.Perms) == 0 {
		t.Fatalf("me = %d menus, %d perms, source=%s", len(me.Menus), len(me.Perms), me.Source)
	}
	// admin 通配权限：可见全部启用种子
	if len(me.Menus) < 18 {
		t.Fatalf("admin visible = %d", len(me.Menus))
	}
}

// TestMenuCRUDAndGuards CRUD + 父校验成环 + 删除保护 + 角色绑定。
func TestMenuCRUDAndGuards(t *testing.T) {
	h := newMenuApp(t)
	admin := login(t, h, "admin", "Basalt@2026")

	// 创建根目录与叶子
	rec, data := do(t, h, http.MethodPost, "/api/v1/menus", admin,
		map[string]any{"name": "分组", "path": "/group", "sort": 5})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create group: %d %s", rec.Code, rec.Body.String())
	}
	var group menu.Menu
	_ = json.Unmarshal(data, &group)

	rec, data = do(t, h, http.MethodPost, "/api/v1/menus", admin,
		map[string]any{"name": "子页", "path": "/group/leaf", "parent_id": group.ID, "perm": "x:y:z"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create leaf: %d %s", rec.Code, rec.Body.String())
	}
	var leaf menu.Menu
	_ = json.Unmarshal(data, &leaf)

	// path 重复 409
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/menus", admin,
		map[string]any{"name": "重复", "path": "/group"}); rec.Code != http.StatusConflict {
		t.Fatalf("dup path: %d", rec.Code)
	}
	// 成环：group 的 parent 设为 leaf
	if rec, _ := do(t, h, http.MethodPatch, "/api/v1/menus/"+group.ID, admin,
		map[string]string{"parent_id": leaf.ID}); rec.Code != http.StatusBadRequest {
		t.Fatalf("cycle: %d", rec.Code)
	}
	// 有子不可删
	if rec, _ := do(t, h, http.MethodDelete, "/api/v1/menus/"+group.ID, admin, nil); rec.Code != http.StatusConflict {
		t.Fatalf("delete with children: %d", rec.Code)
	}

	// 角色绑定（复用 identity:role:manage；admin 建角色）
	rec, data = do(t, h, http.MethodPost, "/api/v1/identity/roles", admin,
		map[string]any{"code": "viewer", "name": "只读"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create role: %d %s", rec.Code, rec.Body.String())
	}
	var role identity.Role
	_ = json.Unmarshal(data, &role)
	if rec, _ := do(t, h, http.MethodPut, "/api/v1/roles/"+role.ID+"/menus", admin,
		map[string]any{"menu_ids": []string{leaf.ID}}); rec.Code != http.StatusOK {
		t.Fatalf("bind: %d %s", rec.Code, rec.Body.String())
	}
	rec, data = do(t, h, http.MethodGet, "/api/v1/roles/"+role.ID+"/menus", admin, nil)
	var bound struct {
		MenuIDs []string `json:"menu_ids"`
	}
	_ = json.Unmarshal(data, &bound)
	if len(bound.MenuIDs) != 1 || bound.MenuIDs[0] != leaf.ID {
		t.Fatalf("bound = %v", bound.MenuIDs)
	}
	// 有角色绑定不可删
	if rec, _ := do(t, h, http.MethodDelete, "/api/v1/menus/"+leaf.ID, admin, nil); rec.Code != http.StatusConflict {
		t.Fatalf("delete bound: %d", rec.Code)
	}
}

// TestVisibleForFiltering 过滤规则单测：无权剪除、纯目录空剪除、
// disabled 全剪、通配放行。
func TestVisibleForFiltering(t *testing.T) {
	menus := []menu.Menu{
		{ID: "a", Name: "目录A", Path: "/a", Sort: 1, Enabled: true},
		{ID: "a1", Name: "页A1", Path: "/a/1", ParentID: "a", Perm: "p:one", Sort: 1, Enabled: true},
		{ID: "b", Name: "目录B", Path: "/b", Sort: 2, Enabled: true},
		{ID: "b1", Name: "页B1", Path: "/b/1", ParentID: "b", Perm: "p:two", Sort: 1, Enabled: true},
		{ID: "c", Name: "禁用", Path: "/c", Perm: "p:one", Sort: 3, Enabled: false},
	}
	// 只有 p:one：见 a/a1；b 整树剪除（无可见子）；c 禁用剪除
	tree := menu.VisibleFor(menus, func(p string) bool { return p == "p:one" })
	if len(tree) != 1 || tree[0].ID != "a" || len(tree[0].Children) != 1 {
		t.Fatalf("filtered tree = %+v", tree)
	}
	// 通配 *：全部启用项可见（c 禁用仍剪）
	tree = menu.VisibleFor(menus, func(string) bool { return true })
	if len(tree) != 2 {
		t.Fatalf("wildcard tree = %d", len(tree))
	}
	// 无任何权限：全部剪除 → 空树（前端回退静态的信号）
	tree = menu.VisibleFor(menus, func(string) bool { return false })
	if len(tree) != 0 {
		t.Fatalf("no-perm tree = %d", len(tree))
	}
}

// TestMenuMemStoreSemantics 内存 Store 语义（重启恢复由 gormstore
// 测试覆盖）。
func TestMenuMemStoreSemantics(t *testing.T) {
	s := menu.NewMemStore()
	ctx := context.Background()
	base := time.Now().UTC()
	_ = s.Create(ctx, menu.Menu{ID: "x", Name: "X", Path: "/x", Enabled: true, CreatedAt: base})
	if _, err := s.Get(ctx, "x"); err != nil {
		t.Fatalf("get: %v", err)
	}
	if _, err := s.Get(ctx, "ghost"); err == nil {
		t.Fatal("missing get should fail")
	}
	if err := s.Delete(ctx, "x"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := s.Delete(ctx, "x"); err == nil {
		t.Fatal("double delete should fail")
	}
}
