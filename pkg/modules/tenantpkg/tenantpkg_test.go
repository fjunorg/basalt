package tenantpkg_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/basalt-go/basalt/pkg/kernel/app"
	"github.com/basalt-go/basalt/pkg/modules/identity"
	"github.com/basalt-go/basalt/pkg/modules/menu"
	"github.com/basalt-go/basalt/pkg/modules/tenant"
	"github.com/basalt-go/basalt/pkg/modules/tenantpkg"
)

// 装配 identity + tenant + menu + tenantpkg 四模块（套餐裁剪 × 菜单联动验证）。
func newApp(t *testing.T) (http.Handler, *identity.Module) {
	t.Helper()
	a := app.New(nil)
	idm := identity.New()
	_ = a.Registry().Register(idm)
	_ = a.Registry().Register(tenant.New())
	_ = a.Registry().Register(menu.NewWithStore(seededMenuStore(t)))
	_ = a.Registry().Register(tenantpkg.New())
	mods, err := a.Registry().Resolve()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range mods {
		if err := m.Init(context.Background(), a); err != nil {
			t.Fatalf("init %s: %v", m.Name(), err)
		}
	}
	for _, m := range mods {
		if err := m.Start(context.Background()); err != nil {
			t.Fatalf("start %s: %v", m.Name(), err)
		}
	}
	return a.Router().Handler(), idm
}

// seededMenuStore 预置两条均带权限点的菜单（裁剪验证口径干净；
// 无权限点的「登录可见页」按 menu.VisibleFor 既有语义不参与裁剪，
// 运营如需整页按套餐隐藏应为其配置权限点——见 tenantpkg.go 语义说明）。
func seededMenuStore(t *testing.T) menu.Store {
	t.Helper()
	s := menu.NewMemStore()
	ctx := context.Background()
	_ = s.Create(ctx, menu.Menu{ID: "m-users", Name: "用户管理", Path: "/users", Perm: "identity:user:list", Sort: 10, Enabled: true})
	_ = s.Create(ctx, menu.Menu{ID: "m-dict", Name: "字典管理", Path: "/dict", Perm: "dict:type:list", Sort: 20, Enabled: true})
	return s
}

func doJSON(t *testing.T, h http.Handler, method, path, token string, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var rdr *strings.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = strings.NewReader(string(b))
	} else {
		rdr = strings.NewReader("")
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
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	return rec, m
}

func adminLogin(t *testing.T, h http.Handler) string {
	t.Helper()
	rec, _ := doJSON(t, h, http.MethodPost, "/api/v1/identity/login", "", map[string]string{
		"username": "admin", "password": "Basalt@2026",
	})
	var env struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	if env.Data.Token == "" {
		t.Fatalf("admin login: %d %s", rec.Code, rec.Body.String())
	}
	return env.Data.Token
}

// ---------- IntersectPerms 纯函数 ----------

func TestIntersectPerms(t *testing.T) {
	cases := []struct {
		name             string
		perms, pkg, want []string
	}{
		{"精确交集", []string{"a:b:c", "x:y:z"}, []string{"a:b:c"}, []string{"a:b:c"}},
		{"套餐前缀通配", []string{"a:b:c", "a:b:d", "x:y"}, []string{"a:b:*"}, []string{"a:b:c", "a:b:d"}},
		{"套餐全量放行", []string{"a", "b"}, []string{"*"}, []string{"a", "b"}},
		{"角色通配收窄为套餐集", []string{"*"}, []string{"p:1", "p:2"}, []string{"p:1", "p:2"}},
		{"空套餐清空", []string{"a", "b"}, []string{"c"}, []string{}},
		{"去重", []string{"a:b", "a:b"}, []string{"a:b"}, []string{"a:b"}},
		{"前缀模式不覆盖更短项", []string{"a", "a:b"}, []string{"a:*"}, []string{"a:b"}},
	}
	for _, c := range cases {
		got := tenantpkg.IntersectPerms(c.perms, c.pkg)
		if len(got) != len(c.want) {
			t.Fatalf("%s: got %v want %v", c.name, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("%s: got %v want %v", c.name, got, c.want)
			}
		}
	}
}

// ---------- CRUD + 绑定 ----------

func TestPackageCRUDAndBinding(t *testing.T) {
	h, _ := newApp(t)
	admin := adminLogin(t, h)

	// 创建套餐。
	rec, env := doJSON(t, h, http.MethodPost, "/api/v1/tenantpkg/packages", admin, map[string]any{
		"name": "基础版", "perms": []string{"identity:user:list", "dict:type:*"},
		"menu_ids": []string{"m-users"},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create pkg: %d %s", rec.Code, rec.Body.String())
	}
	data, _ := env["data"].(map[string]any)
	pkgID := data["id"].(string)

	// 重名冲突。
	if rec, _ := doJSON(t, h, http.MethodPost, "/api/v1/tenantpkg/packages", admin, map[string]any{
		"name": "基础版",
	}); rec.Code != http.StatusConflict {
		t.Fatalf("dup pkg: %d", rec.Code)
	}

	// 列表含绑定计数。
	rec, env = doJSON(t, h, http.MethodGet, "/api/v1/tenantpkg/packages", admin, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d", rec.Code)
	}
	list, _ := env["data"].([]any)
	if len(list) != 1 {
		t.Fatalf("list len = %d", len(list))
	}

	// 绑定租户（租户先建）。
	rec, env = doJSON(t, h, http.MethodPost, "/api/v1/tenant/tenants", admin, map[string]any{
		"code": "acme", "name": "ACME",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create tenant: %d %s", rec.Code, rec.Body.String())
	}
	td, _ := env["data"].(map[string]any)
	tenantID := td["id"].(string)

	if rec, _ := doJSON(t, h, http.MethodPut, "/api/v1/tenantpkg/tenants/"+tenantID+"/package", admin,
		map[string]any{"package_id": pkgID}); rec.Code != http.StatusOK {
		t.Fatalf("bind: %d %s", rec.Code, rec.Body.String())
	}
	rec, env = doJSON(t, h, http.MethodGet, "/api/v1/tenantpkg/tenants/"+tenantID+"/package", admin, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get binding: %d", rec.Code)
	}
	bd, _ := env["data"].(map[string]any)
	if bd["package_id"] != pkgID {
		t.Fatalf("binding mismatch: %v", bd)
	}

	// 绑定中禁删。
	if rec, _ := doJSON(t, h, http.MethodDelete, "/api/v1/tenantpkg/packages/"+pkgID, admin, nil); rec.Code != http.StatusConflict {
		t.Fatalf("delete bound pkg: %d", rec.Code)
	}

	// 更新停用。
	rec, _ = doJSON(t, h, http.MethodPatch, "/api/v1/tenantpkg/packages/"+pkgID, admin, map[string]any{"status": "disabled"})
	if rec.Code != http.StatusOK {
		t.Fatalf("update pkg: %d", rec.Code)
	}

	// 解绑后可删。
	if rec, _ := doJSON(t, h, http.MethodPut, "/api/v1/tenantpkg/tenants/"+tenantID+"/package", admin,
		map[string]any{"package_id": ""}); rec.Code != http.StatusOK {
		t.Fatalf("unbind: %d", rec.Code)
	}
	if rec, _ := doJSON(t, h, http.MethodDelete, "/api/v1/tenantpkg/packages/"+pkgID, admin, nil); rec.Code != http.StatusOK {
		t.Fatalf("delete pkg: %d", rec.Code)
	}
}

// ---------- 裁剪生效（API + 菜单联动端到端） ----------

func TestPackageTrimmingE2E(t *testing.T) {
	h, idm := newApp(t)
	admin := adminLogin(t, h)

	// 建租户 + 租户用户（角色持全部用户域权限）。
	_, env := doJSON(t, h, http.MethodPost, "/api/v1/tenant/tenants", admin, map[string]any{"code": "beta", "name": "Beta"})
	td, _ := env["data"].(map[string]any)
	tenantID := td["id"].(string)

	rec, env := doJSON(t, h, http.MethodPost, "/api/v1/identity/roles", admin, map[string]any{
		"code": "tenant-full", "name": "租户全量", "permissions": []string{"identity:user:list", "dict:type:list"},
	})
	var role struct {
		ID string `json:"id"`
	}
	rd, _ := env["data"].(map[string]any)
	role.ID = rd["id"].(string)
	_ = rec

	rec, _ = doJSON(t, h, http.MethodPost, "/api/v1/identity/users", admin, map[string]any{
		"username": "betauser", "password": "password123", "tenant_id": tenantID,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create tenant user: %d %s", rec.Code, rec.Body.String())
	}
	_, env = doJSON(t, h, http.MethodGet, "/api/v1/identity/users", admin, nil)
	var users []identity.User
	raw, _ := json.Marshal(env["data"])
	_ = json.Unmarshal(raw, &users)
	var betaUser identity.User
	for _, u := range users {
		if u.Username == "betauser" {
			betaUser = u
		}
	}
	if rec, _ := doJSON(t, h, http.MethodPut, "/api/v1/identity/users/"+betaUser.ID+"/roles", admin,
		map[string]any{"role_ids": []string{role.ID}}); rec.Code != http.StatusOK {
		t.Fatalf("assign role: %d", rec.Code)
	}

	// 租户用户登录（绑定前：全量权限，两项菜单均可见）。
	rec, _ = doJSON(t, h, http.MethodPost, "/api/v1/identity/login", "", map[string]string{"username": "betauser", "password": "password123"})
	var lr struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &lr)
	betaTok := lr.Data.Token

	if rec, _ := doJSON(t, h, http.MethodGet, "/api/v1/identity/users", betaTok, nil); rec.Code != http.StatusOK {
		t.Fatalf("before bind: tenant user must pass: %d", rec.Code)
	}
	rec, env = doJSON(t, h, http.MethodGet, "/api/v1/menus/me", betaTok, nil)
	var me struct {
		Data struct {
			Menus []map[string]any `json:"menus"`
		} `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &me)
	if len(me.Data.Menus) != 2 {
		t.Fatalf("before bind: menus = %d, want 2", len(me.Data.Menus))
	}

	// 建套餐（仅 identity:user:list）并绑定。
	rec, env = doJSON(t, h, http.MethodPost, "/api/v1/tenantpkg/packages", admin, map[string]any{
		"name": "裁剪版", "perms": []string{"identity:user:list"},
	})
	pd, _ := env["data"].(map[string]any)
	pkgID := pd["id"].(string)
	if rec, _ := doJSON(t, h, http.MethodPut, "/api/v1/tenantpkg/tenants/"+tenantID+"/package", admin,
		map[string]any{"package_id": pkgID}); rec.Code != http.StatusOK {
		t.Fatalf("bind: %d", rec.Code)
	}

	// 绑定后：API 权限保留（套餐内），菜单只剩 identity:user:list 关联项。
	if rec, _ := doJSON(t, h, http.MethodGet, "/api/v1/identity/users", betaTok, nil); rec.Code != http.StatusOK {
		t.Fatalf("after bind: in-package perm must pass: %d", rec.Code)
	}
	rec, env = doJSON(t, h, http.MethodGet, "/api/v1/menus/me", betaTok, nil)
	_ = json.Unmarshal(rec.Body.Bytes(), &me)
	if len(me.Data.Menus) != 1 {
		t.Fatalf("after bind: menus = %d, want 1 (trimmed): %v", len(me.Data.Menus), env["data"])
	}

	// 平台级用户（admin）不受套餐约束。
	rec, _ = doJSON(t, h, http.MethodGet, "/api/v1/menus/me", admin, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("platform admin: %d", rec.Code)
	}

	_ = idm
	_ = time.Now
}
