package dict_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/basalt-go/basalt/pkg/kernel/app"
	"github.com/basalt-go/basalt/pkg/modules/dict"
	"github.com/basalt-go/basalt/pkg/modules/identity"
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

// TestDictTypeAndDataCRUD 类型与数据项 CRUD：by-type 激活过滤；
// 边界：类型重码 409、有数据的类型删除 409、必填缺失 400。
func TestDictTypeAndDataCRUD(t *testing.T) {
	h := newApp(t, dict.New())
	tok := adminToken(t, h)

	rec, data := do(t, h, http.MethodPost, "/api/v1/dict/types", tok, map[string]string{
		"code": "gender", "name": "性别", "remark": "表单枚举",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create type: %d %s", rec.Code, rec.Body.String())
	}
	var tp dict.Type
	_ = json.Unmarshal(data, &tp)
	if tp.Status != "active" {
		t.Fatalf("type defaults: %+v", tp)
	}

	// 数据项：男/女 + 一条停用项
	if rec, _ = do(t, h, http.MethodPost, "/api/v1/dict/datas", tok, map[string]any{
		"type_code": "gender", "label": "男", "value": "M", "sort": 1,
	}); rec.Code != http.StatusCreated {
		t.Fatalf("create data M: %d", rec.Code)
	}
	rec, data = do(t, h, http.MethodPost, "/api/v1/dict/datas", tok, map[string]any{
		"type_code": "gender", "label": "女", "value": "F", "sort": 2,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create data F: %d", rec.Code)
	}
	var d2 dict.Data
	_ = json.Unmarshal(data, &d2)
	if rec, _ = do(t, h, http.MethodPatch, "/api/v1/dict/datas/"+d2.ID, tok, map[string]any{
		"status": "disabled",
	}); rec.Code != http.StatusOK {
		t.Fatalf("disable data F: %d", rec.Code)
	}

	// 管理视图（按 type 过滤）：含停用项
	_, data = do(t, h, http.MethodGet, "/api/v1/dict/datas?type=gender", tok, nil)
	var all []dict.Data
	_ = json.Unmarshal(data, &all)
	if len(all) != 2 {
		t.Fatalf("manage view = %+v", all)
	}
	// 表单下拉视图：仅激活项
	_, data = do(t, h, http.MethodGet, "/api/v1/dict/datas/by-type/gender", tok, nil)
	var active []dict.Data
	_ = json.Unmarshal(data, &active)
	if len(active) != 1 || active[0].Value != "M" {
		t.Fatalf("active view = %+v", active)
	}

	// 更新类型
	if rec, _ = do(t, h, http.MethodPatch, "/api/v1/dict/types/"+tp.ID, tok, map[string]any{
		"name": "性别枚举", "status": "disabled",
	}); rec.Code != http.StatusOK {
		t.Fatalf("update type: %d", rec.Code)
	}

	// ---- 边界 ----
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/dict/types", tok, map[string]string{
		"code": "gender", "name": "重复",
	}); rec.Code != http.StatusConflict {
		t.Fatalf("dup type code: %d, want 409", rec.Code)
	}
	if rec, _ := do(t, h, http.MethodDelete, "/api/v1/dict/types/"+tp.ID, tok, nil); rec.Code != http.StatusConflict {
		t.Fatalf("delete type with data: %d, want 409", rec.Code)
	}
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/dict/types", tok, map[string]string{
		"code": "", "name": "x",
	}); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing code: %d, want 400", rec.Code)
	}
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/dict/datas", tok, map[string]any{
		"type_code": "gender", "label": "", "value": "X",
	}); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing label: %d, want 400", rec.Code)
	}

	// 清掉数据后类型可删
	_, data = do(t, h, http.MethodGet, "/api/v1/dict/datas?type=gender", tok, nil)
	var items []dict.Data
	_ = json.Unmarshal(data, &items)
	for _, it := range items {
		if rec, _ := do(t, h, http.MethodDelete, "/api/v1/dict/datas/"+it.ID, tok, nil); rec.Code != http.StatusOK {
			t.Fatalf("delete data %s: %d", it.ID, rec.Code)
		}
	}
	if rec, _ := do(t, h, http.MethodDelete, "/api/v1/dict/types/"+tp.ID, tok, nil); rec.Code != http.StatusOK {
		t.Fatalf("delete empty type: %d", rec.Code)
	}
}

// TestDictPersistenceRestart 重启恢复：类型 + 数据项写入文件库 →
// 重建 store 与模块实例 → 读回验证（含激活过滤语义延续）。
func TestDictPersistenceRestart(t *testing.T) {
	dsn := t.TempDir() + "/dict.db"

	mkApp := func(s *gormstore.Stores) http.Handler {
		t.Helper()
		a := app.New(nil)
		for _, m := range []app.Module{
			identity.NewWithStores(s.UserStore(), s.RoleStore()),
			dict.NewWithStore(s.DictStore()),
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
	if rec, _ := do(t, h1, http.MethodPost, "/api/v1/dict/types", tok, map[string]string{
		"code": "notice_type", "name": "公告类型",
	}); rec.Code != http.StatusCreated {
		t.Fatalf("create type: %d", rec.Code)
	}
	if rec, _ := do(t, h1, http.MethodPost, "/api/v1/dict/datas", tok, map[string]any{
		"type_code": "notice_type", "label": "通知", "value": "notice",
	}); rec.Code != http.StatusCreated {
		t.Fatalf("create data: %d", rec.Code)
	}
	if sqlDB, err := s1.DB.DB(); err == nil {
		_ = sqlDB.Close()
	}

	s2, err := gormstore.Open(dsn)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	h2 := mkApp(s2)
	tok2 := adminToken(t, h2)

	_, data := do(t, h2, http.MethodGet, "/api/v1/dict/datas/by-type/notice_type", tok2, nil)
	var items []dict.Data
	_ = json.Unmarshal(data, &items)
	if len(items) != 1 || items[0].Value != "notice" {
		t.Fatalf("datas after restart = %+v", items)
	}
	_, data = do(t, h2, http.MethodGet, "/api/v1/dict/types", tok2, nil)
	var types []dict.Type
	_ = json.Unmarshal(data, &types)
	if len(types) != 1 || types[0].Code != "notice_type" {
		t.Fatalf("types after restart = %+v", types)
	}
}
