package errcode_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/basalt-go/basalt/pkg/kernel/app"
	"github.com/basalt-go/basalt/pkg/modules/errcode"
	"github.com/basalt-go/basalt/pkg/modules/identity"
)

func newApp(t *testing.T) (http.Handler, *errcode.Module) {
	t.Helper()
	a := app.New(nil)
	ec := errcode.New()
	_ = a.Registry().Register(identity.New())
	_ = a.Registry().Register(ec)
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
	return a.Router().Handler(), ec
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

func TestErrCodeCRUDAndFilter(t *testing.T) {
	h, _ := newApp(t)
	admin := adminLogin(t, h)

	// 创建。
	rec, env := doJSON(t, h, http.MethodPost, "/api/v1/errcodes", admin, map[string]any{
		"module": "custom", "code": "E001", "message": "自定义错误", "http_status": 422,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	d, _ := env["data"].(map[string]any)
	id := d["id"].(string)

	// module+code 唯一冲突。
	if rec, _ := doJSON(t, h, http.MethodPost, "/api/v1/errcodes", admin, map[string]any{
		"module": "custom", "code": "E001", "message": "重复",
	}); rec.Code != http.StatusConflict {
		t.Fatalf("dup: %d", rec.Code)
	}

	// 按模块过滤（custom 1 条；identity 种子多条）。
	rec, env = doJSON(t, h, http.MethodGet, "/api/v1/errcodes?module=custom", admin, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("filter: %d", rec.Code)
	}
	list, _ := env["data"].([]any)
	if len(list) != 1 {
		t.Fatalf("custom filter = %d", len(list))
	}

	// 更新。
	if rec, _ := doJSON(t, h, http.MethodPatch, "/api/v1/errcodes/"+id, admin,
		map[string]any{"message": "改后"}); rec.Code != http.StatusOK {
		t.Fatalf("update: %d", rec.Code)
	}

	// 删除。
	if rec, _ := doJSON(t, h, http.MethodDelete, "/api/v1/errcodes/"+id, admin, nil); rec.Code != http.StatusOK {
		t.Fatalf("delete: %d", rec.Code)
	}
	if rec, _ := doJSON(t, h, http.MethodGet, "/api/v1/errcodes/"+id, admin, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("get after delete: %d", rec.Code)
	}
}

func TestErrCodeSeedIdempotent(t *testing.T) {
	h, ec := newApp(t)
	admin := adminLogin(t, h)

	rec, env := doJSON(t, h, http.MethodGet, "/api/v1/errcodes", admin, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d", rec.Code)
	}
	list, _ := env["data"].([]any)
	if len(list) == 0 {
		t.Fatal("seeds not populated on first start")
	}
	// 幂等：重复 Start 不重复播种。
	if err := ec.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	rec, _ = doJSON(t, h, http.MethodGet, "/api/v1/errcodes", admin, nil)
	var env2 struct {
		Data []errcode.Entry `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env2)
	if len(env2.Data) != len(list) {
		t.Fatalf("re-seed must be no-op: %d -> %d", len(list), len(env2.Data))
	}
}

func TestErrCodePermissionBoundary(t *testing.T) {
	h, _ := newApp(t)
	admin := adminLogin(t, h)
	_, _ = doJSON(t, h, http.MethodPost, "/api/v1/identity/users", admin, map[string]string{
		"username": "nobody", "password": "password123",
	})
	rec, _ := doJSON(t, h, http.MethodPost, "/api/v1/identity/login", "", map[string]string{
		"username": "nobody", "password": "password123",
	})
	var env struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	if rec, _ := doJSON(t, h, http.MethodGet, "/api/v1/errcodes", env.Data.Token, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("plain user must 403: %d", rec.Code)
	}
}
