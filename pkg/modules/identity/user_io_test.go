package identity_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/basalt-go/basalt/pkg/kernel/app"
	"github.com/basalt-go/basalt/pkg/kernel/xlsx"
	"github.com/basalt-go/basalt/pkg/modules/identity"
)

// newIdentityApp 最小装配（identity 单模块）。
func newIdentityApp(t *testing.T) http.Handler {
	t.Helper()
	a := app.New(nil)
	if err := a.Registry().Register(identity.New()); err != nil {
		t.Fatalf("register: %v", err)
	}
	mods, _ := a.Registry().Resolve()
	for _, m := range mods {
		if err := m.Init(context.Background(), a); err != nil {
			t.Fatalf("init: %v", err)
		}
		_ = m.Start(context.Background())
	}
	return a.Router().Handler()
}

func loginAdmin(t *testing.T, h http.Handler) string {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/identity/login",
		bytes.NewReader([]byte(`{"username":"admin","password":"Basalt@2026"}`)))
	h.ServeHTTP(rec, req)
	var env struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	return env.Data.Token
}

// TestUserExportImportRoundTrip 导出→导入→比对：admin 导出含自身；
// 导入（含成功行/重复行/空用户名行/坏角色行）错误行报告精确到行号。
func TestUserExportImportRoundTrip(t *testing.T) {
	h := newIdentityApp(t)
	tok := loginAdmin(t, h)

	// 导出
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/identity/users/export", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("export: %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" {
		t.Fatalf("content-type = %q", ct)
	}
	grid, err := xlsx.Read(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("read exported: %v", err)
	}
	if len(grid) < 2 || grid[0][0] != "username" || grid[1][0] != "admin" {
		t.Fatalf("export grid = %+v", grid[:2])
	}

	// 导入：4 行（好行 / 重复 admin / 空 username / 坏角色）
	data, _ := xlsx.Build(
		[]string{"username", "email", "dept_id", "role_codes", "password"},
		[][]string{
			{"imported-user", "u@x.com", "", "", "Passw0rd!123"},
			{"admin", "dup@x.com", "", "", "x"},
			{"", "nousername@x.com", "", "", "x"},
			{"bad-role-user", "r@x.com", "", "no-such-role", "x"},
		})
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/identity/users/import", bytes.NewReader(data))
	req.Header.Set("Authorization", "Bearer "+tok)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("import: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data struct {
			Imported int `json:"imported"`
			Failed   int `json:"failed"`
			Errors   []struct {
				Row    int    `json:"row"`
				Reason string `json:"reason"`
			} `json:"errors"`
		} `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Data.Imported != 1 || out.Data.Failed != 3 || len(out.Data.Errors) != 3 {
		t.Fatalf("import result = %+v", out.Data)
	}
	// 错误行行号精确（xlsx 行号：表头=1，数据从 2 起）
	byRow := map[int]string{}
	for _, e := range out.Data.Errors {
		byRow[e.Row] = e.Reason
	}
	if byRow[3] == "" || byRow[4] == "" || byRow[5] == "" {
		t.Fatalf("error rows = %+v（应 3/4/5）", byRow)
	}
	if r := byRow[3]; r == "" || !contains(r, "已存在") {
		t.Fatalf("row 3 reason = %q", r)
	}

	// 非法文件 400
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/identity/users/import",
		bytes.NewReader([]byte("not an xlsx")))
	req.Header.Set("Authorization", "Bearer "+tok)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad file: %d", rec.Code)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && bytes.Contains([]byte(s), []byte(sub)))
}

// TestUserExportRequiresPerm 未认证 401。
func TestUserExportRequiresPerm(t *testing.T) {
	h := newIdentityApp(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/identity/users/export", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauth export: %d", rec.Code)
	}
	_ = io.Discard
}
