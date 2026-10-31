package file_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/basalt-go/basalt/pkg/kernel/app"
	"github.com/basalt-go/basalt/pkg/modules/file"
	"github.com/basalt-go/basalt/pkg/modules/identity"
)

// ---------- 混合存量：S3 切换后旧本地文件继续可访问 ----------

func newFileApp(t *testing.T, m *file.Module) http.Handler {
	t.Helper()
	a := app.New(nil)
	for _, mod := range []app.Module{identity.New(), m} {
		if err := a.Registry().Register(mod); err != nil {
			t.Fatalf("register %s: %v", mod.Name(), err)
		}
	}
	resolved, err := a.Registry().Resolve()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	for _, mod := range resolved {
		if err := mod.Init(context.Background(), a); err != nil {
			t.Fatalf("init %s: %v", mod.Name(), err)
		}
		_ = mod.Start(context.Background())
	}
	return a.Router().Handler()
}

func adminTok(t *testing.T, h http.Handler) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"username": "admin", "password": "Basalt@2026"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/identity/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var env struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	return env.Data.Token
}

func uploadFor(t *testing.T, h http.Handler, tok, filename, content string) (*httptest.ResponseRecorder, file.Meta) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", filename)
	_, _ = fw.Write([]byte(content))
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/files", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var env struct {
		Data file.Meta `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	return rec, env.Data
}

func getFor(t *testing.T, h http.Handler, tok, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// miniStub 最小 S3 stub（PUT/GET/DELETE 存储；不验签——签名正确性
// 已由包内 TestSigV4AWSDocumentVector 与双向验签 stub 锁定，本文件
// 聚焦混合存量路由语义）。
func miniStub(t *testing.T) string {
	t.Helper()
	objs := map[string][]byte{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/testbucket"), "/")
		switch r.Method {
		case http.MethodPut:
			b, _ := io.ReadAll(r.Body)
			objs[key] = b
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			if v, ok := objs[key]; ok {
				w.Write(v)
				return
			}
			w.WriteHeader(http.StatusNotFound)
		case http.MethodDelete:
			delete(objs, key)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestHybridStorageSwitch 混合存量语义：S3 启用后——
// 旧本地文件（storage 空）继续从本地下载/删除；新上传落 S3；
// /url 端点按后端能力分流（s3→预签名 / local→501）。
func TestHybridStorageSwitch(t *testing.T) {
	// 阶段一：纯本地时代上传旧文件
	root := t.TempDir()
	local, err := file.NewLocalStorage(root)
	if err != nil {
		t.Fatalf("local: %v", err)
	}
	store := file.NewMemStore()
	m1 := file.NewWithStore(store, local)
	h1 := newFileApp(t, m1)
	tok := adminTok(t, h1)
	rec, oldMeta := uploadFor(t, h1, tok, "存量文件.txt", "local-era-content")
	if rec.Code != http.StatusCreated {
		t.Fatalf("old upload: %d", rec.Code)
	}

	// 阶段二：切换 S3 为主（同一 store 保留元数据；本地为回退）
	s3, err := file.NewS3Storage(miniStub(t), "us-east-1", "testbucket", "testak", "testsk", "", true)
	if err != nil {
		t.Fatalf("s3: %v", err)
	}
	m2 := file.NewWithStorages(store, s3, local)
	h2 := newFileApp(t, m2)
	tok2 := adminTok(t, h2) // h2 72ec7acb identity 5b9e4f8bFf0c91cd65b0767b5f55

	// 旧文件（storage 空）→ 本地路由，内容可达
	rec = getFor(t, h2, tok2, "/api/v1/files/"+oldMeta.ID+"/download")
	if rec.Code != http.StatusOK || rec.Body.String() != "local-era-content" {
		t.Fatalf("old download: %d %q（混合存量应走本地回退）", rec.Code, rec.Body.String())
	}
	// 旧文件 /url → 本地后端无预签名能力 → 501
	if rec := getFor(t, h2, tok2, "/api/v1/files/"+oldMeta.ID+"/url"); rec.Code != http.StatusNotImplemented {
		t.Fatalf("old url: %d, want 501", rec.Code)
	}

	// 新上传 → 落 S3（meta.Storage=s3，stub 侧有对象）
	rec, newMeta := uploadFor(t, h2, tok2, "新文件.txt", "s3-era-content")
	if rec.Code != http.StatusCreated {
		t.Fatalf("new upload: %d %s", rec.Code, rec.Body.String())
	}
	if newMeta.Storage != "s3" {
		t.Fatalf("new meta.Storage = %q", newMeta.Storage)
	}
	// 新文件下载走 S3
	rec = getFor(t, h2, tok2, "/api/v1/files/"+newMeta.ID+"/download")
	if rec.Code != http.StatusOK || rec.Body.String() != "s3-era-content" {
		t.Fatalf("new download: %d %q", rec.Code, rec.Body.String())
	}
	// 新文件 /url → 预签名（stub 可直接 GET）
	rec = getFor(t, h2, tok2, "/api/v1/files/"+newMeta.ID+"/url")
	if rec.Code != http.StatusOK {
		t.Fatalf("new url: %d %s", rec.Code, rec.Body.String())
	}
	var urlResp struct {
		Data struct {
			URL       string `json:"url"`
			ExpiresIn int    `json:"expires_in"`
		} `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &urlResp)
	if !strings.Contains(urlResp.Data.URL, "X-Amz-Signature=") || urlResp.Data.ExpiresIn != 900 {
		t.Fatalf("url resp = %+v", urlResp.Data)
	}
	resp, err := http.Get(urlResp.Data.URL)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("presign fetch: %v status=%d", err, resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "s3-era-content" {
		t.Fatalf("presign content = %q", string(b))
	}

	// 删除路由：旧文件删本地成功；新文件删 S3 成功
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/files/"+oldMeta.ID, nil)
	req.Header.Set("Authorization", "Bearer "+tok2)
	rec2 := httptest.NewRecorder()
	h2.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusOK {
		t.Fatalf("delete old: %d", rec2.Code)
	}
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/files/"+newMeta.ID, nil)
	req.Header.Set("Authorization", "Bearer "+tok2)
	rec2 = httptest.NewRecorder()
	h2.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusOK {
		t.Fatalf("delete new: %d", rec2.Code)
	}
}
