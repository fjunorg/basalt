package sysconfig_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/basalt-go/basalt/pkg/kernel/app"
	"github.com/basalt-go/basalt/pkg/modules/identity"
	"github.com/basalt-go/basalt/pkg/modules/sysconfig"
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

// TestConfigCRUD 参数 KV：创建/按 key 读/更新/删除；
// 边界：key 重号 409、按 key 读不存在 404、缺 key 400。
func TestConfigCRUD(t *testing.T) {
	h := newApp(t, sysconfig.New())
	tok := adminToken(t, h)

	rec, data := do(t, h, http.MethodPost, "/api/v1/configs", tok, map[string]string{
		"key": "sys.title", "value": "玄武办公", "name": "系统标题",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var it sysconfig.Item
	_ = json.Unmarshal(data, &it)

	// 按 key 只读（业务代码取参路径）
	rec, data = do(t, h, http.MethodGet, "/api/v1/configs/by-key/sys.title", tok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("by-key: %d", rec.Code)
	}
	_ = json.Unmarshal(data, &it)
	if it.Value != "玄武办公" {
		t.Fatalf("by-key value = %q", it.Value)
	}

	// 更新 value
	if rec, _ = do(t, h, http.MethodPatch, "/api/v1/configs/"+it.ID, tok, map[string]string{
		"value": "玄武政务", "name": "系统标题V2",
	}); rec.Code != http.StatusOK {
		t.Fatalf("update: %d", rec.Code)
	}
	_, data = do(t, h, http.MethodGet, "/api/v1/configs/by-key/sys.title", tok, nil)
	_ = json.Unmarshal(data, &it)
	if it.Value != "玄武政务" || it.Name != "系统标题V2" {
		t.Fatalf("after update: %+v", it)
	}

	// 列表
	_, data = do(t, h, http.MethodGet, "/api/v1/configs", tok, nil)
	var list []sysconfig.Item
	_ = json.Unmarshal(data, &list)
	if len(list) != 1 {
		t.Fatalf("list = %+v", list)
	}

	// ---- 边界 ----
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/configs", tok, map[string]string{
		"key": "sys.title", "value": "dup",
	}); rec.Code != http.StatusConflict {
		t.Fatalf("dup key: %d, want 409", rec.Code)
	}
	if rec, _ := do(t, h, http.MethodGet, "/api/v1/configs/by-key/nope", tok, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("missing key: %d, want 404", rec.Code)
	}
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/configs", tok, map[string]string{
		"value": "no-key",
	}); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing key on create: %d, want 400", rec.Code)
	}
	if rec, _ := do(t, h, http.MethodDelete, "/api/v1/configs/ghost", tok, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("delete missing: %d, want 404", rec.Code)
	}

	// 删除后 key 可复用
	if rec, _ := do(t, h, http.MethodDelete, "/api/v1/configs/"+it.ID, tok, nil); rec.Code != http.StatusOK {
		t.Fatalf("delete: %d", rec.Code)
	}
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/configs", tok, map[string]string{
		"key": "sys.title", "value": "reuse",
	}); rec.Code != http.StatusCreated {
		t.Fatalf("key reuse after delete: %d", rec.Code)
	}
}

// TestConfigPersistenceRestart 重启恢复：参数写入文件库 →
// 重建 store 与模块实例 → 按 key 读回验证新值延续。
func TestConfigPersistenceRestart(t *testing.T) {
	dsn := t.TempDir() + "/config.db"

	mkApp := func(s *gormstore.Stores) http.Handler {
		t.Helper()
		a := app.New(nil)
		for _, m := range []app.Module{
			identity.NewWithStores(s.UserStore(), s.RoleStore()),
			sysconfig.NewWithStore(s.SysConfigStore()),
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
	if rec, _ := do(t, h1, http.MethodPost, "/api/v1/configs", tok, map[string]string{
		"key": "sys.motd", "value": "v1",
	}); rec.Code != http.StatusCreated {
		t.Fatalf("create: %d", rec.Code)
	}
	if sqlDB, err := s1.DB.DB(); err == nil {
		_ = sqlDB.Close()
	}

	// 重启前窗口外再改一次值，验证重启读到的是最后值
	s1b, err := gormstore.Open(dsn)
	if err != nil {
		t.Fatalf("reopen for update: %v", err)
	}
	h1b := mkApp(s1b)
	tok1b := adminToken(t, h1b)
	_, data := do(t, h1b, http.MethodGet, "/api/v1/configs/by-key/sys.motd", tok1b, nil)
	var it sysconfig.Item
	_ = json.Unmarshal(data, &it)
	if rec, _ := do(t, h1b, http.MethodPatch, "/api/v1/configs/"+it.ID, tok1b, map[string]string{
		"value": "v2-final",
	}); rec.Code != http.StatusOK {
		t.Fatalf("update before restart: %d", rec.Code)
	}
	if sqlDB, err := s1b.DB.DB(); err == nil {
		_ = sqlDB.Close()
	}

	s2, err := gormstore.Open(dsn)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	h2 := mkApp(s2)
	tok2 := adminToken(t, h2)
	rec, data := do(t, h2, http.MethodGet, "/api/v1/configs/by-key/sys.motd", tok2, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("by-key after restart: %d", rec.Code)
	}
	_ = json.Unmarshal(data, &it)
	if it.Value != "v2-final" {
		t.Fatalf("value after restart = %q, want v2-final", it.Value)
	}
}
