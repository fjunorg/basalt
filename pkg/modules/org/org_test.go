package org_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/basalt-go/basalt/pkg/kernel/app"
	"github.com/basalt-go/basalt/pkg/modules/identity"
	"github.com/basalt-go/basalt/pkg/modules/org"
	"github.com/basalt-go/basalt/pkg/stores/gormstore"
)

func newApp(t *testing.T, mods ...app.Module) http.Handler {
	t.Helper()
	a := app.New(nil)
	if err := a.Registry().Register(identity.New()); err != nil {
		t.Fatalf("register identity: %v", err)
	}
	for _, m := range mods {
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

// TestOrgDeptAndPostCRUD 部门树与岗位 CRUD：父子层级、更新、删除；
// 边界：删除有子部门的父部门 409、岗位重码 409、缺失资源 404。
func TestOrgDeptAndPostCRUD(t *testing.T) {
	h := newApp(t, org.New())
	tok := adminToken(t, h)

	// 根 → 子部门
	rec, data := do(t, h, http.MethodPost, "/api/v1/org/depts", tok, map[string]any{
		"name": "总公司", "sort": 1,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create root: %d %s", rec.Code, rec.Body.String())
	}
	var root org.Dept
	_ = json.Unmarshal(data, &root)
	if root.Status != "active" || root.ParentID != "" {
		t.Fatalf("root defaults: %+v", root)
	}

	rec, data = do(t, h, http.MethodPost, "/api/v1/org/depts", tok, map[string]any{
		"parent_id": root.ID, "name": "技术部", "sort": 2,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create child: %d %s", rec.Code, rec.Body.String())
	}
	var child org.Dept
	_ = json.Unmarshal(data, &child)

	// 树查询：父节点挂子节点
	_, data = do(t, h, http.MethodGet, "/api/v1/org/depts/tree", tok, nil)
	var tree []*org.DeptNode
	_ = json.Unmarshal(data, &tree)
	if len(tree) != 1 || len(tree[0].Children) != 1 || tree[0].Children[0].ID != child.ID {
		t.Fatalf("tree = %+v", tree)
	}

	// 更新部门名
	if rec, _ = do(t, h, http.MethodPatch, "/api/v1/org/depts/"+root.ID, tok, map[string]any{
		"name": "集团总部",
	}); rec.Code != http.StatusOK {
		t.Fatalf("update dept: %d", rec.Code)
	}

	// 岗位 CRUD
	rec, data = do(t, h, http.MethodPost, "/api/v1/org/posts", tok, map[string]any{
		"code": "dev", "name": "研发", "sort": 1,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create post: %d %s", rec.Code, rec.Body.String())
	}
	var post org.Post
	_ = json.Unmarshal(data, &post)
	if rec, _ = do(t, h, http.MethodPatch, "/api/v1/org/posts/"+post.ID, tok, map[string]any{
		"name": "研发工程师", "status": "disabled",
	}); rec.Code != http.StatusOK {
		t.Fatalf("update post: %d", rec.Code)
	}
	_, data = do(t, h, http.MethodGet, "/api/v1/org/posts", tok, nil)
	var posts []org.Post
	_ = json.Unmarshal(data, &posts)
	if len(posts) != 1 || posts[0].Name != "研发工程师" || posts[0].Status != "disabled" {
		t.Fatalf("posts = %+v", posts)
	}

	// ---- 边界 ----
	// 删除有子部门的父部门 → 409
	if rec, _ := do(t, h, http.MethodDelete, "/api/v1/org/depts/"+root.ID, tok, nil); rec.Code != http.StatusConflict {
		t.Fatalf("delete parent with children: %d, want 409", rec.Code)
	}
	// 岗位重码 → 409
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/org/posts", tok, map[string]any{
		"code": "dev", "name": "重复岗位",
	}); rec.Code != http.StatusConflict {
		t.Fatalf("dup post code: %d, want 409", rec.Code)
	}
	// 删除不存在的部门/岗位 → 404
	if rec, _ := do(t, h, http.MethodDelete, "/api/v1/org/depts/ghost", tok, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("delete missing dept: %d, want 404", rec.Code)
	}
	if rec, _ := do(t, h, http.MethodDelete, "/api/v1/org/posts/ghost", tok, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("delete missing post: %d, want 404", rec.Code)
	}
	// 部门名为空 → 400
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/org/depts", tok, map[string]any{
		"name": "", "sort": 9,
	}); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty name: %d, want 400", rec.Code)
	}

	// 删除叶子部门成功后，父部门才可删
	if rec, _ := do(t, h, http.MethodDelete, "/api/v1/org/depts/"+child.ID, tok, nil); rec.Code != http.StatusOK {
		t.Fatalf("delete leaf: %d", rec.Code)
	}
	if rec, _ := do(t, h, http.MethodDelete, "/api/v1/org/depts/"+root.ID, tok, nil); rec.Code != http.StatusOK {
		t.Fatalf("delete root after leaf removed: %d", rec.Code)
	}
}

// TestOrgPersistenceRestart 重启恢复：部门/岗位经 HTTP 写入文件库 →
// 重建 store 与模块实例（identity 同库持久化）→ 新实例登录后读回验证。
func TestOrgPersistenceRestart(t *testing.T) {
	dsn := t.TempDir() + "/org.db"

	mkApp := func(s *gormstore.Stores) http.Handler {
		t.Helper()
		a := app.New(nil)
		for _, m := range []app.Module{
			identity.NewWithStores(s.UserStore(), s.RoleStore()),
			org.NewWithStore(s.OrgStore()),
		} {
			if err := a.Registry().Register(m); err != nil {
				t.Fatalf("register %s: %v", m.Name(), err)
			}
		}
		mods, err := a.Registry().Resolve()
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		for _, m := range mods {
			if err := m.Init(context.Background(), a); err != nil {
				t.Fatalf("init %s: %v", m.Name(), err)
			}
			_ = m.Start(context.Background())
		}
		return a.Router().Handler()
	}

	s1, err := gormstore.Open(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	h1 := mkApp(s1)
	tok := adminToken(t, h1)
	rec, _ := do(t, h1, http.MethodPost, "/api/v1/org/depts", tok, map[string]any{"name": "总公司", "sort": 1})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create dept: %d", rec.Code)
	}
	rec, _ = do(t, h1, http.MethodPost, "/api/v1/org/posts", tok, map[string]any{"code": "ceo", "name": "总经理"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create post: %d", rec.Code)
	}
	if sqlDB, err := s1.DB.DB(); err == nil {
		_ = sqlDB.Close()
	}

	// 重启：同一 DSN 重建 store 实例与模块
	s2, err := gormstore.Open(dsn)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	h2 := mkApp(s2)
	tok2 := adminToken(t, h2) // 管理员账号同样来自持久层

	_, data := do(t, h2, http.MethodGet, "/api/v1/org/depts/tree", tok2, nil)
	var tree []*org.DeptNode
	_ = json.Unmarshal(data, &tree)
	if len(tree) != 1 || tree[0].Name != "总公司" {
		t.Fatalf("depts after restart = %+v", tree)
	}
	_, data = do(t, h2, http.MethodGet, "/api/v1/org/posts", tok2, nil)
	var posts []org.Post
	_ = json.Unmarshal(data, &posts)
	if len(posts) != 1 || posts[0].Code != "ceo" {
		t.Fatalf("posts after restart = %+v", posts)
	}
}
