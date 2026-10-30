package gormstore_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/basalt-go/basalt/pkg/kernel/app"
	"github.com/basalt-go/basalt/pkg/modules/identity"
	"github.com/basalt-go/basalt/pkg/modules/tenant"
	"github.com/basalt-go/basalt/pkg/stores/gormstore"
)

func openMem(t *testing.T) *gormstore.Stores {
	t.Helper()
	s, err := gormstore.Open(":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	return s
}

func TestUserStoreSemantics(t *testing.T) {
	s := openMem(t)
	us := s.UserStore()
	ctx := context.Background()

	u := identity.User{ID: "u1", Username: "alice", Status: identity.StatusActive, RoleIDs: []string{"r1"}}
	if err := us.Create(ctx, u); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := us.Create(ctx, u); !errors.Is(err, identity.ErrUsernameTaken) {
		t.Fatalf("dup create: %v", err)
	}
	got, err := us.GetByUsername(ctx, "alice")
	if err != nil || got.ID != "u1" {
		t.Fatalf("get by username: %+v %v", got, err)
	}
	if _, err := us.Get(ctx, "ghost"); !errors.Is(err, identity.ErrUserNotFound) {
		t.Fatalf("missing get: %v", err)
	}
	if err := us.AssignRoles(ctx, "u1", []string{"r1", "r2"}); err != nil {
		t.Fatalf("assign: %v", err)
	}
	got, _ = us.Get(ctx, "u1")
	if len(got.RoleIDs) != 2 || got.RoleIDs[1] != "r2" {
		t.Fatalf("role ids after assign: %v (serializer:json roundtrip)", got.RoleIDs)
	}
	if err := us.UpdateStatus(ctx, "u1", identity.StatusDisabled); err != nil {
		t.Fatalf("update status: %v", err)
	}
	got, _ = us.Get(ctx, "u1")
	if got.Status != identity.StatusDisabled {
		t.Fatalf("status = %s", got.Status)
	}
	if err := us.UpdateStatus(ctx, "u1", "frozen"); !errors.Is(err, identity.ErrBadStatus) {
		t.Fatalf("bad status: %v", err)
	}

	// 租户过滤（行级隔离读取路径）
	_ = us.Create(ctx, identity.User{ID: "p1", Username: "platform-user", Status: identity.StatusActive})
	_ = us.Create(ctx, identity.User{ID: "t1u", Username: "tenant-user", Status: identity.StatusActive, TenantID: "tn1"})
	inTenant, _ := us.ListByTenant(ctx, "tn1")
	if len(inTenant) != 1 || inTenant[0].ID != "t1u" {
		t.Fatalf("ListByTenant = %+v", inTenant)
	}
	all, _ := us.List(ctx)
	if len(all) != 3 {
		t.Fatalf("List = %d, want 3", len(all))
	}
}

func TestRoleAndTenantStores(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	rs, ts := s.RoleStore(), s.TenantStore()

	r := identity.Role{ID: "r1", Code: "viewer", Permissions: []string{"identity:*"}}
	if err := rs.Create(ctx, r); err != nil {
		t.Fatalf("role create: %v", err)
	}
	if err := rs.Create(ctx, r); !errors.Is(err, identity.ErrRoleCodeTaken) {
		t.Fatalf("dup role: %v", err)
	}
	got, _ := rs.GetByCode(ctx, "viewer")
	if got.ID != "r1" || len(got.Permissions) != 1 {
		t.Fatalf("role roundtrip: %+v", got)
	}
	r.Name = "Viewer 2"
	r.Permissions = []string{"*"}
	if err := rs.Update(ctx, r); err != nil {
		t.Fatalf("role update: %v", err)
	}
	got, _ = rs.Get(ctx, "r1")
	if got.Name != "Viewer 2" || got.Permissions[0] != "*" {
		t.Fatalf("after update: %+v", got)
	}
	if err := rs.Delete(ctx, "r1"); err != nil {
		t.Fatalf("role delete: %v", err)
	}
	if _, err := rs.Get(ctx, "r1"); !errors.Is(err, identity.ErrRoleNotFound) {
		t.Fatalf("deleted role: %v", err)
	}

	tn := tenant.Tenant{ID: "tn1", Code: "gov", Name: "G", Status: "active", IsolationMode: tenant.IsolationRow}
	if err := ts.Create(ctx, tn); err != nil {
		t.Fatalf("tenant create: %v", err)
	}
	if err := ts.Create(ctx, tn); !errors.Is(err, tenant.ErrCodeTaken) {
		t.Fatalf("dup tenant: %v", err)
	}
	tn.Name = "G2"
	if err := ts.Update(ctx, tn); err != nil {
		t.Fatalf("tenant update: %v", err)
	}
	g, _ := ts.Get(ctx, "tn1")
	if g.Name != "G2" {
		t.Fatalf("tenant after update: %+v", g)
	}
	list, _ := ts.List(ctx)
	if len(list) != 1 {
		t.Fatalf("tenant list = %d", len(list))
	}
}

// TestModuleRunWithGORMStores 证明存储可插拔：GORM 实现 + SQLite
// 注入 identity/tenant 模块后，完整登录 + 租户隔离流程通过。
func TestModuleRunWithGORMStores(t *testing.T) {
	s := openMem(t)
	a := app.New(nil)
	for _, m := range []app.Module{
		identity.NewWithStores(s.UserStore(), s.RoleStore()),
		tenant.NewWithStore(s.TenantStore()),
	} {
		if err := a.Registry().Register(m); err != nil {
			t.Fatalf("register: %v", err)
		}
	}
	mods, _ := a.Registry().Resolve()
	for _, m := range mods {
		if err := m.Init(context.Background(), a); err != nil {
			t.Fatalf("init %s: %v", m.Name(), err)
		}
		_ = m.Start(context.Background())
	}
	h := a.Router().Handler()

	do := func(method, path, token string, body any) (*httptest.ResponseRecorder, json.RawMessage) {
		var rdr *bytes.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rdr = bytes.NewReader(b)
		} else {
			rdr = bytes.NewReader(nil)
		}
		req := httptest.NewRequest(method, path, rdr)
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

	// 引导管理员登录（GORM 持久化的用户）
	rec, data := do(http.MethodPost, "/api/v1/identity/login", "", map[string]string{
		"username": "admin", "password": "Basalt@2026",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("admin login on gorm stores: %d", rec.Code)
	}
	var d struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(data, &d)

	// 建租户 + 租户用户 + 行级隔离仍然成立
	rec, data = do(http.MethodPost, "/api/v1/tenant/tenants", d.Token, map[string]string{"code": "g1", "name": "G1"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("tenant create: %d", rec.Code)
	}
	var tn tenant.Tenant
	_ = json.Unmarshal(data, &tn)
	rec, _ = do(http.MethodPost, "/api/v1/identity/users", d.Token, map[string]string{
		"username": "member1", "password": "password123", "tenant_id": tn.ID,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("tenant user: %d", rec.Code)
	}
	rec, data = do(http.MethodPost, "/api/v1/identity/login", "", map[string]string{
		"username": "member1", "password": "password123",
	})
	_ = json.Unmarshal(data, &d)
	rec, data = do(http.MethodGet, "/api/v1/tenant/users", d.Token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("tenant users: %d", rec.Code)
	}
	var users []identity.User
	_ = json.Unmarshal(data, &users)
	if len(users) != 1 || users[0].Username != "member1" {
		t.Fatalf("row-level isolation on gorm: %+v", users)
	}
}
