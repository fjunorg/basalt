package modules_test

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/basalt-go/basalt/pkg/kernel/app"
	"github.com/basalt-go/basalt/pkg/modules/audit"
	"github.com/basalt-go/basalt/pkg/modules/dict"
	"github.com/basalt-go/basalt/pkg/modules/file"
	"github.com/basalt-go/basalt/pkg/modules/identity"
	"github.com/basalt-go/basalt/pkg/modules/job"
	"github.com/basalt-go/basalt/pkg/modules/notice"
	"github.com/basalt-go/basalt/pkg/modules/org"
	"github.com/basalt-go/basalt/pkg/modules/sysconfig"
	"github.com/basalt-go/basalt/pkg/modules/tenant"
)

// newFullApp 装配全部模块（接近 serve 生产装配，内存存储）。
func newFullApp(t *testing.T) http.Handler {
	t.Helper()
	a := app.New(nil)
	storage, err := file.NewLocalStorage(t.TempDir())
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	mods := []app.Module{
		identity.New(), tenant.New(), audit.New(),
		org.New(), dict.New(), sysconfig.New(), notice.New(),
		file.New(storage), job.New(a.Logger()),
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

func adminTok(t *testing.T, h http.Handler) string {
	t.Helper()
	rec, data := do(t, h, http.MethodPost, "/api/v1/identity/login", "", map[string]string{
		"username": "admin", "password": "Basalt@2026",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("admin login: %d", rec.Code)
	}
	var d struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(data, &d)
	return d.Token
}

func TestOrgDeptTreeAndPost(t *testing.T) {
	h := newFullApp(t)
	tok := adminTok(t, h)

	// 根部门 → 子部门
	rec, data := do(t, h, http.MethodPost, "/api/v1/org/depts", tok, map[string]any{"name": "总公司", "sort": 1})
	if rec.Code != http.StatusCreated {
		t.Fatalf("root dept: %d %s", rec.Code, rec.Body.String())
	}
	var root org.Dept
	_ = json.Unmarshal(data, &root)
	rec, data = do(t, h, http.MethodPost, "/api/v1/org/depts", tok, map[string]any{"name": "技术部", "parent_id": root.ID, "sort": 1})
	var child org.Dept
	_ = json.Unmarshal(data, &child)

	// 有子部门的父部门不可删
	if rec, _ := do(t, h, http.MethodDelete, "/api/v1/org/depts/"+root.ID, tok, nil); rec.Code != http.StatusConflict {
		t.Fatalf("delete parent with children: %d, want 409", rec.Code)
	}
	// 树结构
	rec, data = do(t, h, http.MethodGet, "/api/v1/org/depts/tree", tok, nil)
	var tree []*org.DeptNode
	_ = json.Unmarshal(data, &tree)
	if len(tree) != 1 || len(tree[0].Children) != 1 || tree[0].Children[0].ID != child.ID {
		t.Fatalf("tree = %+v", tree)
	}

	// 岗位
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/org/posts", tok, map[string]any{"code": "dev", "name": "研发"}); rec.Code != http.StatusCreated {
		t.Fatalf("post: %d", rec.Code)
	}
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/org/posts", tok, map[string]any{"code": "dev", "name": "重复"}); rec.Code != http.StatusConflict {
		t.Fatalf("dup post: %d", rec.Code)
	}

	// 用户挂部门/岗位
	rec, data = do(t, h, http.MethodPost, "/api/v1/identity/users", tok, map[string]any{
		"username": "employee", "password": "password123", "dept_id": child.ID,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("user with dept: %d", rec.Code)
	}
	var u identity.User
	_ = json.Unmarshal(data, &u)
	if u.DeptID != child.ID {
		t.Fatalf("user dept = %q", u.DeptID)
	}
}

func TestDictLifecycle(t *testing.T) {
	h := newFullApp(t)
	tok := adminTok(t, h)

	_, typeData := do(t, h, http.MethodPost, "/api/v1/dict/types", tok, map[string]any{"code": "gender", "name": "性别"})
	var dt dict.Type
	_ = json.Unmarshal(typeData, &dt)
	rec, data := do(t, h, http.MethodPost, "/api/v1/dict/datas", tok, map[string]any{
		"type_code": "gender", "label": "男", "value": "M", "sort": 1,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("dict data: %d", rec.Code)
	}
	var d dict.Data
	_ = json.Unmarshal(data, &d)

	// 有数据的类型不可删
	if rec, _ := do(t, h, http.MethodDelete, "/api/v1/dict/types/"+dt.ID, tok, nil); rec.Code != http.StatusConflict {
		t.Fatalf("type with data: %d", rec.Code)
	}
	// by-type 登录级读取
	rec, data = do(t, h, http.MethodGet, "/api/v1/dict/datas/by-type/gender", tok, nil)
	var list []dict.Data
	_ = json.Unmarshal(data, &list)
	if len(list) != 1 || list[0].Value != "M" {
		t.Fatalf("by-type = %+v", list)
	}
}

func TestSysConfig(t *testing.T) {
	h := newFullApp(t)
	tok := adminTok(t, h)

	do(t, h, http.MethodPost, "/api/v1/configs", tok, map[string]any{"key": "sys.title", "value": "玄武平台", "name": "系统标题"})
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/configs", tok, map[string]any{"key": "sys.title", "value": "x"}); rec.Code != http.StatusConflict {
		t.Fatalf("dup key: %d", rec.Code)
	}
	rec, data := do(t, h, http.MethodGet, "/api/v1/configs/by-key/sys.title", tok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("by-key: %d", rec.Code)
	}
	var it sysconfig.Item
	_ = json.Unmarshal(data, &it)
	if it.Value != "玄武平台" {
		t.Fatalf("config value = %q", it.Value)
	}
}

func TestNoticePublishFlow(t *testing.T) {
	h := newFullApp(t)
	tok := adminTok(t, h)

	rec, data := do(t, h, http.MethodPost, "/api/v1/notices", tok, map[string]any{"title": "系统升级公告", "content": "今晚 22:00 升级"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d", rec.Code)
	}
	var n notice.Notice
	_ = json.Unmarshal(data, &n)
	if n.Status != notice.StatusDraft {
		t.Fatalf("initial status = %s", n.Status)
	}

	// 已发布不可编辑
	do(t, h, http.MethodPost, "/api/v1/notices/"+n.ID+"/publish", tok, nil)
	if rec, _ := do(t, h, http.MethodPatch, "/api/v1/notices/"+n.ID, tok, map[string]any{"title": "改"}); rec.Code != http.StatusConflict {
		t.Fatalf("edit published: %d", rec.Code)
	}

	// 全员视图只含已发布
	rec, data = do(t, h, http.MethodGet, "/api/v1/notices/published", tok, nil)
	var list []notice.Notice
	_ = json.Unmarshal(data, &list)
	if len(list) != 1 || list[0].ID != n.ID {
		t.Fatalf("published = %+v", list)
	}

	// 撤回后不再可见
	do(t, h, http.MethodPost, "/api/v1/notices/"+n.ID+"/revoke", tok, nil)
	rec, data = do(t, h, http.MethodGet, "/api/v1/notices/published", tok, nil)
	_ = json.Unmarshal(data, &list)
	if len(list) != 0 {
		t.Fatalf("after revoke = %+v", list)
	}
}

func TestFileUploadDownload(t *testing.T) {
	h := newFullApp(t)
	tok := adminTok(t, h)

	// multipart 上传
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "报告.txt")
	_, _ = fw.Write([]byte("玄武测试文件内容"))
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/files", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Data file.Meta `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	meta := env.Data
	if meta.Name != "报告.txt" {
		t.Fatalf("meta name = %q", meta.Name)
	}

	// 下载内容一致 + 强制 attachment
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/files/"+meta.ID+"/download", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "玄武测试文件内容" {
		t.Fatalf("download: %d %q", rec.Code, rec.Body.String())
	}
	if cd := rec.Header().Get("Content-Disposition"); cd != `attachment; filename="报告.txt"` {
		t.Fatalf("disposition = %q", cd)
	}
}

func TestJobManagerTrigger(t *testing.T) {
	h := newFullApp(t)
	tok := adminTok(t, h)

	// 内置任务已注册（audit.stats）
	rec, data := do(t, h, http.MethodGet, "/api/v1/jobs", tok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list jobs: %d", rec.Code)
	}
	var jobs []job.Job
	_ = json.Unmarshal(data, &jobs)
	if len(jobs) == 0 {
		t.Fatal("builtin jobs not registered")
	}

	// 手动触发产生运行记录
	rec, _ = do(t, h, http.MethodPost, "/api/v1/jobs/audit.stats/trigger", tok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("trigger: %d %s", rec.Code, rec.Body.String())
	}
	rec, data = do(t, h, http.MethodGet, "/api/v1/jobs/audit.stats/runs", tok, nil)
	var runs []job.RunRecord
	_ = json.Unmarshal(data, &runs)
	if len(runs) == 0 || runs[0].Result == "" {
		t.Fatalf("runs = %+v", runs)
	}

	// 启停
	if rec, _ := do(t, h, http.MethodPatch, "/api/v1/jobs/audit.stats/status", tok, map[string]bool{"enabled": true}); rec.Code != http.StatusOK {
		t.Fatalf("enable: %d", rec.Code)
	}
}

func TestModuleSelfDescriptionIncludesNewModules(t *testing.T) {
	h := newFullApp(t)
	tok := adminTok(t, h)

	rec, data := do(t, h, http.MethodGet, "/api/v1/platform/modules", tok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("modules: %d", rec.Code)
	}
	var mods []struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(data, &mods)
	want := map[string]bool{"org": false, "dict": false, "sysconfig": false, "notice": false, "file": false, "job": false}
	for _, m := range mods {
		if _, ok := want[m.Name]; ok {
			want[m.Name] = true
		}
	}
	for name, seen := range want {
		if !seen {
			t.Fatalf("module %s not in self-description", name)
		}
	}
}
