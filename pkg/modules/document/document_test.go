package document_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/basalt-go/basalt/pkg/kernel/app"
	"github.com/basalt-go/basalt/pkg/modules/audit"
	"github.com/basalt-go/basalt/pkg/modules/document"
	"github.com/basalt-go/basalt/pkg/modules/identity"
	"github.com/basalt-go/basalt/pkg/modules/workflow"
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

// newDocApp 装配 identity + workflow + audit + document（document 的完整依赖）。
func newDocApp(t *testing.T) http.Handler {
	t.Helper()
	return newApp(t, workflow.New(), audit.New(), document.New())
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

func mkUser(t *testing.T, h http.Handler, admin, name string) (string, string) {
	t.Helper()
	rec, data := do(t, h, http.MethodPost, "/api/v1/identity/users", admin, map[string]string{
		"username": name, "password": "password123",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create %s: %d %s", name, rec.Code, rec.Body.String())
	}
	var u identity.User
	_ = json.Unmarshal(data, &u)
	return u.ID, loginAs(t, h, name, "password123")
}

// TestDocumentCRUDAndBoundaries 拟稿默认值、mine/all 视图与权限；
// 边界：非法密级 400、缺标题 400、非创建人提交 403、
// 重复提交 409、未 approved 归档 409、普通用户 scope=all 403。
func TestDocumentCRUDAndBoundaries(t *testing.T) {
	h := newDocApp(t)
	admin := loginAs(t, h, "admin", "Basalt@2026")
	_, staff := mkUser(t, h, admin, "clerk1")

	// 创建：默认值 outbound/普通/平急
	rec, data := do(t, h, http.MethodPost, "/api/v1/documents", staff, map[string]any{
		"doc_num": "玄武〔2026〕1号", "title": "关于开展自查的通知",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var d document.Doc
	_ = json.Unmarshal(data, &d)
	if d.Type != "outbound" || d.Security != document.SecPublic || d.Urgency != "平急" || d.Status != document.StDraft {
		t.Fatalf("defaults: %+v", d)
	}

	// mine 视图：创建人可见
	_, data = do(t, h, http.MethodGet, "/api/v1/documents", staff, nil)
	var mine []document.Doc
	_ = json.Unmarshal(data, &mine)
	if len(mine) != 1 || mine[0].ID != d.ID {
		t.Fatalf("mine = %+v", mine)
	}

	// scope=all：admin 有 PermManage 可全量
	if rec, _ = do(t, h, http.MethodGet, "/api/v1/documents?scope=all", admin, nil); rec.Code != http.StatusOK {
		t.Fatalf("admin scope=all: %d", rec.Code)
	}

	// ---- 边界 ----
	// 非法密级
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/documents", staff, map[string]any{
		"title": "x", "security": "绝密",
	}); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad security: %d, want 400", rec.Code)
	}
	// 缺标题
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/documents", staff, map[string]any{
		"doc_num": "n",
	}); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing title: %d, want 400", rec.Code)
	}
	// 非创建人提交
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/documents/"+d.ID+"/submit", admin, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("submit by non-creator: %d, want 403", rec.Code)
	}
	// 归档 draft → 409（未 approved）
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/documents/"+d.ID+"/archive", admin, nil); rec.Code != http.StatusConflict {
		t.Fatalf("archive draft: %d, want 409", rec.Code)
	}
	// 提交（默认流程存在，Start 注册）→ circulating；重复提交 409
	if rec, _ = do(t, h, http.MethodPost, "/api/v1/documents/"+d.ID+"/submit", staff, nil); rec.Code != http.StatusOK {
		t.Fatalf("submit: %d %s", rec.Code, rec.Body.String())
	}
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/documents/"+d.ID+"/submit", staff, nil); rec.Code != http.StatusConflict {
		t.Fatalf("re-submit: %d, want 409", rec.Code)
	}
	// circulating 仍未 approved → 归档 409
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/documents/"+d.ID+"/archive", admin, nil); rec.Code != http.StatusConflict {
		t.Fatalf("archive circulating: %d, want 409", rec.Code)
	}
	// 不存在的公文
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/documents/ghost/submit", staff, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("submit ghost: %d, want 404", rec.Code)
	}
}

// TestDocumentPersistenceRestart 重启恢复：两份公文（不同密级）写入文件库 →
// 重建 store 与模块实例 → mine 列表与状态、密级、文号读回一致。
func TestDocumentPersistenceRestart(t *testing.T) {
	dsn := t.TempDir() + "/document.db"

	mkApp := func(s *gormstore.Stores) http.Handler {
		t.Helper()
		a := app.New(nil)
		for _, m := range []app.Module{
			identity.NewWithStores(s.UserStore(), s.RoleStore()),
			workflow.New(),
			audit.New(),
			document.NewWithStore(s.DocumentStore()),
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
	admin := loginAs(t, h1, "admin", "Basalt@2026")
	_, staff := mkUser(t, h1, admin, "archivist")
	for _, doc := range []map[string]any{
		{"doc_num": "玄武〔2026〕10号", "title": "普通公文", "security": "普通"},
		{"doc_num": "玄武〔2026〕11号", "title": "秘密公文", "security": "秘密"},
	} {
		if rec, _ := do(t, h1, http.MethodPost, "/api/v1/documents", staff, doc); rec.Code != http.StatusCreated {
			t.Fatalf("create %v: %d", doc["title"], rec.Code)
		}
	}
	if sqlDB, err := s1.DB.DB(); err == nil {
		_ = sqlDB.Close()
	}

	s2, err := gormstore.Open(dsn)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	h2 := mkApp(s2)
	admin2 := loginAs(t, h2, "admin", "Basalt@2026")
	staff2 := loginAs(t, h2, "archivist", "password123")

	// 全量视图（admin）读回：密级与文号保留
	_, data := do(t, h2, http.MethodGet, "/api/v1/documents?scope=all", admin2, nil)
	var docs []document.Doc
	_ = json.Unmarshal(data, &docs)
	if len(docs) != 2 {
		t.Fatalf("docs after restart = %+v", docs)
	}
	byTitle := map[string]document.Doc{}
	for _, d := range docs {
		byTitle[d.Title] = d
	}
	if byTitle["秘密公文"].Security != "秘密" || byTitle["秘密公文"].DocNum != "玄武〔2026〕11号" {
		t.Fatalf("secret doc after restart: %+v", byTitle["秘密公文"])
	}
	if byTitle["普通公文"].Status != document.StDraft {
		t.Fatalf("status after restart: %+v", byTitle["普通公文"])
	}

	// mine 视图（原创建人）重启后仍归其名下
	_, data = do(t, h2, http.MethodGet, "/api/v1/documents", staff2, nil)
	var mine []document.Doc
	_ = json.Unmarshal(data, &mine)
	if len(mine) != 2 {
		t.Fatalf("mine after restart = %+v", mine)
	}
}
