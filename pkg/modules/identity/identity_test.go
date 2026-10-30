package identity_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/basalt-go/basalt/pkg/kernel/app"
	"github.com/basalt-go/basalt/pkg/kernel/config"
	"github.com/basalt-go/basalt/pkg/modules/identity"
)

// newTestApp 构造已注册 identity 模块的 App（dev 配置）。
func newTestApp(t *testing.T) *app.App {
	t.Helper()
	a := app.New(nil)
	if err := a.Registry().Register(identity.New()); err != nil {
		t.Fatalf("register identity: %v", err)
	}
	return a
}

// initRouter 完成 Init + Start（引导管理员在此创建）并返回可服务的 handler。
func initRouter(t *testing.T, a *app.App) http.Handler {
	t.Helper()
	mods, err := a.Registry().Resolve()
	if err != nil {
		t.Fatalf("resolve: %v", err)
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
	return a.Router().Handler()
}

type resp struct {
	Code int             `json:"code"`
	Data json.RawMessage `json:"data"`
}

func doJSON(t *testing.T, h http.Handler, method, path, token string, body any) (*httptest.ResponseRecorder, resp) {
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
	var env resp
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	return rec, env
}

// adminLogin 用引导管理员拿 token。dev 默认 admin / Basalt@2026。
func adminLogin(t *testing.T, h http.Handler) string {
	t.Helper()
	rec, env := doJSON(t, h, http.MethodPost, "/api/v1/identity/login", "", map[string]string{
		"username": "admin", "password": "Basalt@2026",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("bootstrap admin login: status %d body %s", rec.Code, rec.Body.String())
	}
	var d struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(env.Data, &d); err != nil || d.Token == "" {
		t.Fatalf("admin token: %v", err)
	}
	return d.Token
}

func createUser(t *testing.T, h http.Handler, token, username, password string) (resp, int) {
	t.Helper()
	rec, env := doJSON(t, h, http.MethodPost, "/api/v1/identity/users", token, map[string]string{
		"username": username, "password": password, "email": username + "@example.com",
	})
	return env, rec.Code
}

func loginAs(t *testing.T, h http.Handler, username, password string) (string, int) {
	t.Helper()
	rec, env := doJSON(t, h, http.MethodPost, "/api/v1/identity/login", "", map[string]string{
		"username": username, "password": password,
	})
	if rec.Code != http.StatusOK {
		return "", rec.Code
	}
	var d struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(env.Data, &d)
	return d.Token, rec.Code
}

func TestUserLifecycle(t *testing.T) {
	h := initRouter(t, newTestApp(t))
	admin := adminLogin(t, h)

	env, code := createUser(t, h, admin, "alice", "password123")
	if code != http.StatusCreated {
		t.Fatalf("create user: status %d, envelope code %d", code, env.Code)
	}
	var created identity.User
	if err := json.Unmarshal(env.Data, &created); err != nil {
		t.Fatalf("created user decode: %v", err)
	}
	if created.PasswordHash != "" {
		t.Fatal("password hash leaked in create response")
	}

	// 未认证创建：401
	if rec, _ := doJSON(t, h, http.MethodPost, "/api/v1/identity/users", "", map[string]string{
		"username": "bob", "password": "password123",
	}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated create: status %d, want 401", rec.Code)
	}

	// 重名 / 弱密码
	if _, code := createUser(t, h, admin, "alice", "password456"); code != http.StatusConflict {
		t.Fatalf("duplicate username: status %d, want 409", code)
	}
	if _, code := createUser(t, h, admin, "bob", "short"); code != http.StatusBadRequest {
		t.Fatalf("weak password: status %d, want 400", code)
	}

	// 普通用户访问列表（无角色 → 403）
	_, _ = createUser(t, h, admin, "nobody", "password123")
	nobodyTok, _ := loginAs(t, h, "nobody", "password123")
	if rec, _ := doJSON(t, h, http.MethodGet, "/api/v1/identity/users", nobodyTok, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("list without permission: status %d, want 403", rec.Code)
	}

	// admin 列表 / 单查 / me
	if rec, _ := doJSON(t, h, http.MethodGet, "/api/v1/identity/users", admin, nil); rec.Code != http.StatusOK {
		t.Fatalf("admin list: status %d", rec.Code)
	}
	if rec, _ := doJSON(t, h, http.MethodGet, "/api/v1/identity/users/"+created.ID, admin, nil); rec.Code != http.StatusOK {
		t.Fatalf("get user: status %d", rec.Code)
	}
	if rec, _ := doJSON(t, h, http.MethodGet, "/api/v1/identity/me", admin, nil); rec.Code != http.StatusOK {
		t.Fatalf("me: status %d", rec.Code)
	}
}

func TestRBACRoleAssignment(t *testing.T) {
	h := initRouter(t, newTestApp(t))
	admin := adminLogin(t, h)

	// 创建带只读权限的角色
	rec, env := doJSON(t, h, http.MethodPost, "/api/v1/identity/roles", admin, map[string]any{
		"code": "viewer", "name": "Viewer", "permissions": []string{"identity:user:list", "identity:user:get"},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create role: %d %s", rec.Code, rec.Body.String())
	}
	var role identity.Role
	_ = json.Unmarshal(env.Data, &role)

	// 创建用户并分配角色
	_, _ = createUser(t, h, admin, "viewer1", "password123")
	rec, env = doJSON(t, h, http.MethodGet, "/api/v1/identity/users", admin, nil)
	var users []identity.User
	_ = json.Unmarshal(env.Data, &users)
	var viewer identity.User
	for _, u := range users {
		if u.Username == "viewer1" {
			viewer = u
		}
	}
	rec, _ = doJSON(t, h, http.MethodPut, "/api/v1/identity/users/"+viewer.ID+"/roles", admin, map[string]any{
		"role_ids": []string{role.ID},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("assign roles: %d %s", rec.Code, rec.Body.String())
	}

	tok, _ := loginAs(t, h, "viewer1", "password123")
	// 有权限：列表 200
	if rec, _ := doJSON(t, h, http.MethodGet, "/api/v1/identity/users", tok, nil); rec.Code != http.StatusOK {
		t.Fatalf("viewer list: status %d, want 200", rec.Code)
	}
	// 无权限：创建用户 403
	if rec, _ := doJSON(t, h, http.MethodPost, "/api/v1/identity/users", tok, map[string]string{
		"username": "x", "password": "password123",
	}); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer create: status %d, want 403", rec.Code)
	}

	// me 应携带权限集合
	rec, env = doJSON(t, h, http.MethodGet, "/api/v1/identity/me", tok, nil)
	var me struct {
		Permissions []string `json:"permissions"`
	}
	_ = json.Unmarshal(env.Data, &me)
	if len(me.Permissions) != 2 {
		t.Fatalf("me permissions = %v, want 2 entries", me.Permissions)
	}
}

func TestRoleCRUD(t *testing.T) {
	h := initRouter(t, newTestApp(t))
	admin := adminLogin(t, h)

	rec, env := doJSON(t, h, http.MethodPost, "/api/v1/identity/roles", admin, map[string]any{
		"code": "ops", "name": "Ops", "permissions": []string{"identity:user:status"},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d", rec.Code)
	}
	var role identity.Role
	_ = json.Unmarshal(env.Data, &role)

	// 重码冲突
	if rec, _ := doJSON(t, h, http.MethodPost, "/api/v1/identity/roles", admin, map[string]any{
		"code": "ops", "name": "Dup",
	}); rec.Code != http.StatusConflict {
		t.Fatalf("dup role: %d", rec.Code)
	}

	// 更新权限
	rec, _ = doJSON(t, h, http.MethodPatch, "/api/v1/identity/roles/"+role.ID, admin, map[string]any{
		"permissions": []string{"identity:user:list"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("update role: %d", rec.Code)
	}

	// 删除
	if rec, _ := doJSON(t, h, http.MethodDelete, "/api/v1/identity/roles/"+role.ID, admin, nil); rec.Code != http.StatusOK {
		t.Fatalf("delete role: %d", rec.Code)
	}
}

func TestLoginFailures(t *testing.T) {
	h := initRouter(t, newTestApp(t))
	admin := adminLogin(t, h)
	_, _ = createUser(t, h, admin, "carol", "password123")

	if _, code := loginAs(t, h, "carol", "wrong-password"); code != http.StatusUnauthorized {
		t.Fatalf("bad password: status %d, want 401", code)
	}
	if _, code := loginAs(t, h, "ghost", "password123"); code != http.StatusUnauthorized {
		t.Fatalf("unknown user: status %d, want 401", code)
	}
}

func TestDisabledUserCannotLogin(t *testing.T) {
	a := newTestApp(t)
	h := initRouter(t, a)
	admin := adminLogin(t, h)
	env, _ := createUser(t, h, admin, "dave", "password123")
	var u identity.User
	_ = json.Unmarshal(env.Data, &u)

	rec, _ := doJSON(t, h, http.MethodPatch, "/api/v1/identity/users/"+u.ID+"/status", admin,
		map[string]string{"status": "disabled"})
	if rec.Code != http.StatusOK {
		t.Fatalf("disable user: status %d", rec.Code)
	}
	if _, code := loginAs(t, h, "dave", "password123"); code != http.StatusForbidden {
		t.Fatalf("disabled login: status %d, want 403", code)
	}
}

func TestTamperedAndExpiredTokens(t *testing.T) {
	h := initRouter(t, newTestApp(t))
	if rec, _ := doJSON(t, h, http.MethodGet, "/api/v1/identity/me", "not-a-token", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("tampered token: status %d, want 401", rec.Code)
	}

	a := newTestApp(t)
	h2 := initRouter(t, a)
	admin := adminLogin(t, h2)
	_, env := doJSON(t, h2, http.MethodGet, "/api/v1/identity/me", admin, nil)
	var me struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(env.Data, &me)

	issuer := identity.NewTokenIssuer("basalt-dev-secret", time.Nanosecond)
	token, _, err := issuer.Issue(identity.User{ID: me.ID}, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if rec, _ := doJSON(t, h2, http.MethodGet, "/api/v1/identity/me", token, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("expired token: status %d, want 401", rec.Code)
	}
}

func TestProdRejectsDefaults(t *testing.T) {
	cfg := config.Defaults()
	cfg.Env = "prod"

	check := func(wantErr bool) {
		a := app.New(cfg)
		m := identity.New()
		_ = a.Registry().Register(m)
		mods, err := a.Registry().Resolve()
		if err != nil {
			t.Fatal(err)
		}
		err = mods[0].Init(context.Background(), a)
		if wantErr && err == nil {
			t.Fatal("expected prod rejection, got nil")
		}
		if !wantErr && err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	cfg.JWTSecret = "basalt-dev-secret" // 默认
	check(true)

	cfg.JWTSecret = "real-secret"
	cfg.BootstrapAdminPassword = "Basalt@2026" // 默认口令
	check(true)

	cfg.BootstrapAdminPassword = "real-password"
	check(false)
}

func TestMemStores(t *testing.T) {
	s := identity.NewMemUserStore()
	ctx := context.Background()
	u := identity.User{ID: "u1", Username: "frank", Status: identity.StatusActive}
	if err := s.Create(ctx, u); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.Create(ctx, u); !errors.Is(err, identity.ErrUsernameTaken) {
		t.Fatalf("duplicate create: %v", err)
	}
	if err := s.AssignRoles(ctx, "u1", []string{"r1", "r2"}); err != nil {
		t.Fatalf("assign: %v", err)
	}
	got, _ := s.Get(ctx, "u1")
	if len(got.RoleIDs) != 2 {
		t.Fatalf("role ids = %v", got.RoleIDs)
	}
	if err := s.UpdateStatus(ctx, "u1", "frozen"); !errors.Is(err, identity.ErrBadStatus) {
		t.Fatalf("bad status: %v", err)
	}

	rs := identity.NewMemRoleStore()
	r := identity.Role{ID: "r1", Code: "viewer", Permissions: []string{"identity:*"}}
	if err := rs.Create(ctx, r); err != nil {
		t.Fatalf("role create: %v", err)
	}
	if err := rs.Create(ctx, r); !errors.Is(err, identity.ErrRoleCodeTaken) {
		t.Fatalf("dup role: %v", err)
	}
	g, _ := rs.GetByCode(ctx, "viewer")
	if g.ID != "r1" {
		t.Fatalf("get by code: %+v", g)
	}
}

func TestHasPermissionWildcard(t *testing.T) {
	cases := []struct {
		perms []string
		want  string
		ok    bool
	}{
		{[]string{"*"}, "anything:at:all", true},
		{[]string{"identity:*"}, "identity:user:list", true},
		{[]string{"identity:*"}, "tenant:list", false},
		{[]string{"identity:user:*"}, "identity:user:status", true},
		{[]string{"identity:user:*"}, "identity:role:manage", false},
		{[]string{"identity:user:list"}, "identity:user:list", true},
		{[]string{"identity:user:list"}, "identity:user:get", false},
	}
	for _, c := range cases {
		if got := identity.HasPermission(c.perms, c.want); got != c.ok {
			t.Fatalf("HasPermission(%v, %q) = %v, want %v", c.perms, c.want, got, c.ok)
		}
	}
}
