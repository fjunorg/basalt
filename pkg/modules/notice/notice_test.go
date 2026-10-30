package notice_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/basalt-go/basalt/pkg/kernel/app"
	"github.com/basalt-go/basalt/pkg/modules/identity"
	"github.com/basalt-go/basalt/pkg/modules/notice"
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

// TestNoticeLifecycle 草稿 → 编辑 → 发布 → 撤回状态机；
// 边界：编辑已发布公告 409、重复发布 409、撤回非发布态 409、缺标题 400。
func TestNoticeLifecycle(t *testing.T) {
	h := newApp(t, notice.New())
	tok := adminToken(t, h)

	rec, data := do(t, h, http.MethodPost, "/api/v1/notices", tok, map[string]any{
		"title": "国庆放假通知", "content": "10 月 1 日至 8 日放假",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var n notice.Notice
	_ = json.Unmarshal(data, &n)
	if n.Status != notice.StatusDraft || n.Type != "notice" || n.PublishedAt != nil {
		t.Fatalf("draft defaults: %+v", n)
	}

	// 草稿可编辑
	if rec, _ = do(t, h, http.MethodPatch, "/api/v1/notices/"+n.ID, tok, map[string]any{
		"title": "国庆放假通知（修订）", "content": "10 月 1 日至 7 日放假",
	}); rec.Code != http.StatusOK {
		t.Fatalf("edit draft: %d", rec.Code)
	}

	// 发布前：全员视图不可见
	_, data = do(t, h, http.MethodGet, "/api/v1/notices/published", tok, nil)
	var pub []notice.Notice
	_ = json.Unmarshal(data, &pub)
	if len(pub) != 0 {
		t.Fatalf("published before publish = %+v", pub)
	}

	// 发布 → 全员视图可见 + PublishedAt 打点
	if rec, _ = do(t, h, http.MethodPost, "/api/v1/notices/"+n.ID+"/publish", tok, nil); rec.Code != http.StatusOK {
		t.Fatalf("publish: %d", rec.Code)
	}
	_, data = do(t, h, http.MethodGet, "/api/v1/notices/published", tok, nil)
	_ = json.Unmarshal(data, &pub)
	if len(pub) != 1 || pub[0].Status != notice.StatusPublished || pub[0].PublishedAt == nil {
		t.Fatalf("published = %+v", pub)
	}

	// ---- 边界：状态机守卫 ----
	if rec, _ := do(t, h, http.MethodPatch, "/api/v1/notices/"+n.ID, tok, map[string]any{
		"title": "改已发布",
	}); rec.Code != http.StatusConflict {
		t.Fatalf("edit published: %d, want 409", rec.Code)
	}
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/notices/"+n.ID+"/publish", tok, nil); rec.Code != http.StatusConflict {
		t.Fatalf("re-publish: %d, want 409", rec.Code)
	}
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/notices/ghost/publish", tok, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("publish missing: %d, want 404", rec.Code)
	}
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/notices", tok, map[string]any{
		"content": "无标题",
	}); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing title: %d, want 400", rec.Code)
	}

	// 撤回 → 全员视图不可见；撤回后不可再撤回/发布
	if rec, _ = do(t, h, http.MethodPost, "/api/v1/notices/"+n.ID+"/revoke", tok, nil); rec.Code != http.StatusOK {
		t.Fatalf("revoke: %d", rec.Code)
	}
	_, data = do(t, h, http.MethodGet, "/api/v1/notices/published", tok, nil)
	_ = json.Unmarshal(data, &pub)
	if len(pub) != 0 {
		t.Fatalf("published after revoke = %+v", pub)
	}
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/notices/"+n.ID+"/revoke", tok, nil); rec.Code != http.StatusConflict {
		t.Fatalf("re-revoke: %d, want 409", rec.Code)
	}

	// 管理视图（含撤回）
	_, data = do(t, h, http.MethodGet, "/api/v1/notices", tok, nil)
	var all []notice.Notice
	_ = json.Unmarshal(data, &all)
	if len(all) != 1 || all[0].Status != notice.StatusRevoked {
		t.Fatalf("manage view = %+v", all)
	}

	// 删除
	if rec, _ = do(t, h, http.MethodDelete, "/api/v1/notices/"+n.ID, tok, nil); rec.Code != http.StatusOK {
		t.Fatalf("delete: %d", rec.Code)
	}
}

// TestNoticePersistenceRestart 重启恢复：草稿 + 已发布各一条写入文件库 →
// 重建 store 与模块实例 → 状态与 PublishedAt 延续、发布过滤仍生效。
func TestNoticePersistenceRestart(t *testing.T) {
	dsn := t.TempDir() + "/notice.db"

	mkApp := func(s *gormstore.Stores) http.Handler {
		t.Helper()
		a := app.New(nil)
		for _, m := range []app.Module{
			identity.NewWithStores(s.UserStore(), s.RoleStore()),
			notice.NewWithStore(s.NoticeStore()),
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
	// 草稿
	if rec, _ := do(t, h1, http.MethodPost, "/api/v1/notices", tok, map[string]string{
		"title": "内部筹备说明",
	}); rec.Code != http.StatusCreated {
		t.Fatalf("create draft: %d", rec.Code)
	}
	// 已发布
	rec, data := do(t, h1, http.MethodPost, "/api/v1/notices", tok, map[string]string{
		"title": "系统维护公告",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create to-publish: %d", rec.Code)
	}
	var pub notice.Notice
	_ = json.Unmarshal(data, &pub)
	if rec, _ = do(t, h1, http.MethodPost, "/api/v1/notices/"+pub.ID+"/publish", tok, nil); rec.Code != http.StatusOK {
		t.Fatalf("publish: %d", rec.Code)
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

	_, data = do(t, h2, http.MethodGet, "/api/v1/notices/published", tok2, nil)
	var published []notice.Notice
	_ = json.Unmarshal(data, &published)
	if len(published) != 1 || published[0].Title != "系统维护公告" || published[0].PublishedAt == nil {
		t.Fatalf("published after restart = %+v", published)
	}
	_, data = do(t, h2, http.MethodGet, "/api/v1/notices", tok2, nil)
	var all []notice.Notice
	_ = json.Unmarshal(data, &all)
	if len(all) != 2 {
		t.Fatalf("all after restart = %+v", all)
	}
}
