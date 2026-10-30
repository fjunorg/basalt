package tenant_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/basalt-go/basalt/pkg/kernel/app"
	"github.com/basalt-go/basalt/pkg/modules/identity"
	"github.com/basalt-go/basalt/pkg/modules/tenant"
)

// newApp 注册 identity + tenant（tenant 依赖 identity，验证依赖通道）。
func newApp(t *testing.T) *app.App {
	t.Helper()
	a := app.New(nil)
	for _, m := range []app.Module{identity.New(), tenant.New()} {
		if err := a.Registry().Register(m); err != nil {
			t.Fatalf("register %s: %v", m.Name(), err)
		}
	}
	return a
}

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
		Code int             `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	return rec, env.Data
}

func adminToken(t *testing.T, h http.Handler) string {
	t.Helper()
	rec, data := do(t, h, http.MethodPost, "/api/v1/identity/login", "", map[string]string{
		"username": "admin", "password": "Basalt@2026",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("admin login: %d %s", rec.Code, rec.Body.String())
	}
	var d struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(data, &d)
	return d.Token
}

func loginAs(t *testing.T, h http.Handler, user, pass string) string {
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

// createTenantWithUser 建租户 + 在租户内建普通用户，返回 (tenantID, token)。
func createTenantWithUser(t *testing.T, h http.Handler, admin, code, username string) (string, string) {
	t.Helper()
	rec, data := do(t, h, http.MethodPost, "/api/v1/tenant/tenants", admin, map[string]string{
		"code": code, "name": "Tenant " + code,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create tenant: %d %s", rec.Code, rec.Body.String())
	}
	var tn tenant.Tenant
	_ = json.Unmarshal(data, &tn)

	rec, _ = do(t, h, http.MethodPost, "/api/v1/identity/users", admin, map[string]string{
		"username": username, "password": "password123", "tenant_id": tn.ID,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create tenant user: %d %s", rec.Code, rec.Body.String())
	}
	return tn.ID, loginAs(t, h, username, "password123")
}

func TestTenantCRUD(t *testing.T) {
	h := initRouter(t, newApp(t))
	admin := adminToken(t, h)

	rec, data := do(t, h, http.MethodPost, "/api/v1/tenant/tenants", admin, map[string]string{
		"code": "gov-a", "name": "委办局A",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var tn tenant.Tenant
	_ = json.Unmarshal(data, &tn)
	if tn.IsolationMode != tenant.IsolationRow {
		t.Fatalf("default isolation = %q, want shared-schema", tn.IsolationMode)
	}

	// 重码 / 非法隔离模式
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/tenant/tenants", admin, map[string]string{
		"code": "gov-a", "name": "dup",
	}); rec.Code != http.StatusConflict {
		t.Fatalf("dup code: %d", rec.Code)
	}
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/tenant/tenants", admin, map[string]string{
		"code": "bad", "name": "x", "isolation_mode": "quantum",
	}); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad isolation: %d", rec.Code)
	}

	// 列表 / 单查 / 更新
	if rec, _ := do(t, h, http.MethodGet, "/api/v1/tenant/tenants", admin, nil); rec.Code != http.StatusOK {
		t.Fatalf("list: %d", rec.Code)
	}
	if rec, _ := do(t, h, http.MethodGet, "/api/v1/tenant/tenants/"+tn.ID, admin, nil); rec.Code != http.StatusOK {
		t.Fatalf("get: %d", rec.Code)
	}
	if rec, _ := do(t, h, http.MethodPatch, "/api/v1/tenant/tenants/"+tn.ID, admin, map[string]string{
		"name": "委办局A改",
	}); rec.Code != http.StatusOK {
		t.Fatalf("update: %d", rec.Code)
	}
}

func TestTenantPermissionDenied(t *testing.T) {
	h := initRouter(t, newApp(t))
	admin := adminToken(t, h)
	_, tok := createTenantWithUser(t, h, admin, "gov-b", "b-user")

	// 普通租户用户无 tenant:create 权限
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/tenant/tenants", tok, map[string]string{
		"code": "sneaky", "name": "x",
	}); rec.Code != http.StatusForbidden {
		t.Fatalf("create without perm: %d, want 403", rec.Code)
	}
}

func TestRowLevelIsolation(t *testing.T) {
	h := initRouter(t, newApp(t))
	admin := adminToken(t, h)

	_, tokA := createTenantWithUser(t, h, admin, "gov-a", "user-a")
	_, tokB := createTenantWithUser(t, h, admin, "gov-b", "user-b")

	// /tenant/users：各自只看到本租户成员
	assertTenantUsers := func(tok, wantUser string) {
		t.Helper()
		rec, data := do(t, h, http.MethodGet, "/api/v1/tenant/users", tok, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("tenant users: %d", rec.Code)
		}
		var users []identity.User
		_ = json.Unmarshal(data, &users)
		if len(users) != 1 {
			t.Fatalf("tenant users = %d entries, want 1 (row-level isolation broken)", len(users))
		}
		if users[0].Username != wantUser {
			t.Fatalf("tenant member = %q, want %q", users[0].Username, wantUser)
		}
	}
	assertTenantUsers(tokA, "user-a")
	assertTenantUsers(tokB, "user-b")

	// /tenant/current：返回所属租户
	rec, data := do(t, h, http.MethodGet, "/api/v1/tenant/current", tokA, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("current: %d", rec.Code)
	}
	var tn tenant.Tenant
	_ = json.Unmarshal(data, &tn)
	if tn.Code != "gov-a" {
		t.Fatalf("current tenant code = %q, want gov-a", tn.Code)
	}
}

func TestPlatformUserHasNoTenant(t *testing.T) {
	h := initRouter(t, newApp(t))
	admin := adminToken(t, h) // 引导管理员是平台级（无租户）

	if rec, _ := do(t, h, http.MethodGet, "/api/v1/tenant/current", admin, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("platform user current: %d, want 404", rec.Code)
	}
}
