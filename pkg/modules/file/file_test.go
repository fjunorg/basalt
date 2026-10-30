package file_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/basalt-go/basalt/pkg/kernel/app"
	"github.com/basalt-go/basalt/pkg/modules/file"
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

func adminToken(t *testing.T, h http.Handler) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"username": "admin", "password": "Basalt@2026"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/identity/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin login: %d %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	return env.Data.Token
}

// upload 发一次 multipart 上传，返回 (recorder, meta)。
func upload(t *testing.T, h http.Handler, token, filename, content string) (*httptest.ResponseRecorder, file.Meta) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", filename)
	_, _ = fw.Write([]byte(content))
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/files", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var env struct {
		Data file.Meta `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	return rec, env.Data
}

func get(t *testing.T, h http.Handler, token, path string) (*httptest.ResponseRecorder, json.RawMessage) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	return rec, env.Data
}

// TestFileUploadDownloadDelete 上传 → 列表 → 下载内容比对 → 删除 → 404；
// 边界：缺 file 字段 400、路径穿越文件名被清洗、下载缺失 404。
func TestFileUploadDownloadDelete(t *testing.T) {
	root := t.TempDir()
	storage, err := file.NewLocalStorage(root)
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	h := newApp(t, file.NewWithStore(file.NewMemStore(), storage))
	tok := adminToken(t, h)

	rec, meta := upload(t, h, tok, "报告.txt", "机密内容-甲乙丙")
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body.String())
	}
	if meta.Name != "报告.txt" || meta.Size != int64(len("机密内容-甲乙丙")) || meta.ID == "" {
		t.Fatalf("meta = %+v", meta)
	}

	// 列表
	rec, data := get(t, h, tok, "/api/v1/files")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d", rec.Code)
	}
	var metas []file.Meta
	_ = json.Unmarshal(data, &metas)
	if len(metas) != 1 || metas[0].ID != meta.ID {
		t.Fatalf("list = %+v", metas)
	}

	// 下载：内容逐字节比对 + attachment 头（防内联安全基线）
	req := httptest.NewRequest(http.MethodGet, "/api/v1/files/"+meta.ID+"/download", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	drec := httptest.NewRecorder()
	h.ServeHTTP(drec, req)
	if drec.Code != http.StatusOK {
		t.Fatalf("download: %d", drec.Code)
	}
	if got := drec.Body.String(); got != "机密内容-甲乙丙" {
		t.Fatalf("download content = %q", got)
	}
	if cd := drec.Header().Get("Content-Disposition"); cd == "" {
		t.Fatal("Content-Disposition missing (attachment baseline)")
	}

	// ---- 边界：路径穿越文件名清洗 ----
	rec, evil := upload(t, h, tok, "../../etc/passwd", "x")
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload evil name: %d", rec.Code)
	}
	if evil.Name != "passwd" {
		t.Fatalf("evil name sanitized to %q, want passwd", evil.Name)
	}

	// 缺 file 字段 → 400
	req = httptest.NewRequest(http.MethodPost, "/api/v1/files", bytes.NewReader(nil))
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("Authorization", "Bearer "+tok)
	brec := httptest.NewRecorder()
	h.ServeHTTP(brec, req)
	if brec.Code != http.StatusBadRequest {
		t.Fatalf("no multipart: %d, want 400", brec.Code)
	}

	// 下载/删除不存在 → 404
	if rec, _ := get(t, h, tok, "/api/v1/files/ghost/download"); rec.Code != http.StatusNotFound {
		t.Fatalf("download missing: %d, want 404", rec.Code)
	}
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/files/ghost", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	drec = httptest.NewRecorder()
	h.ServeHTTP(drec, req)
	if drec.Code != http.StatusNotFound {
		t.Fatalf("delete missing: %d, want 404", drec.Code)
	}

	// 删除后再下载 → 404
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/files/"+meta.ID, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	drec = httptest.NewRecorder()
	h.ServeHTTP(drec, req)
	if drec.Code != http.StatusOK {
		t.Fatalf("delete: %d", drec.Code)
	}
	if rec, _ := get(t, h, tok, "/api/v1/files/"+meta.ID+"/download"); rec.Code != http.StatusNotFound {
		t.Fatalf("download after delete: %d, want 404", rec.Code)
	}
}

// TestFileMetaPersistenceRestart 重启恢复：上传（sqlite 元信息 + 磁盘字节）
// → 重建 store 与模块实例（同一磁盘根）→ 列表元信息与下载内容均在。
func TestFileMetaPersistenceRestart(t *testing.T) {
	dsn := t.TempDir() + "/file.db"
	root := t.TempDir() // 磁盘字节根目录：两次装配共用

	mkApp := func(s *gormstore.Stores) http.Handler {
		t.Helper()
		storage, err := file.NewLocalStorage(root)
		if err != nil {
			t.Fatalf("storage: %v", err)
		}
		a := app.New(nil)
		for _, m := range []app.Module{
			identity.NewWithStores(s.UserStore(), s.RoleStore()),
			file.NewWithStore(s.FileStore(), storage),
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
	rec, meta := upload(t, h1, tok, "存档文件.bin", "restart-payload")
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload: %d", rec.Code)
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

	// 元信息重启后仍在
	rec, data := get(t, h2, tok2, "/api/v1/files")
	if rec.Code != http.StatusOK {
		t.Fatalf("list after restart: %d", rec.Code)
	}
	var metas []file.Meta
	_ = json.Unmarshal(data, &metas)
	if len(metas) != 1 || metas[0].ID != meta.ID || metas[0].Name != "存档文件.bin" {
		t.Fatalf("metas after restart = %+v", metas)
	}

	// 字节内容经同一磁盘根仍可下载
	req := httptest.NewRequest(http.MethodGet, "/api/v1/files/"+meta.ID+"/download", nil)
	req.Header.Set("Authorization", "Bearer "+tok2)
	drec := httptest.NewRecorder()
	h2.ServeHTTP(drec, req)
	if drec.Code != http.StatusOK {
		t.Fatalf("download after restart: %d", drec.Code)
	}
	body, _ := io.ReadAll(drec.Body)
	if string(body) != "restart-payload" {
		t.Fatalf("content after restart = %q", body)
	}
}
