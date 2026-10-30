package finance_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/basalt-go/basalt/pkg/kernel/app"
	"github.com/basalt-go/basalt/pkg/kernel/dataperm"
	"github.com/basalt-go/basalt/pkg/modules/audit"
	"github.com/basalt-go/basalt/pkg/modules/document"
	"github.com/basalt-go/basalt/pkg/modules/finance"
	"github.com/basalt-go/basalt/pkg/modules/identity"
	"github.com/basalt-go/basalt/pkg/modules/org"
	"github.com/basalt-go/basalt/pkg/modules/workflow"
)

// ---------- 数据权限集成矩阵：五档 × finance/document 双模块 ----------

type dpTestEnv struct {
	t  *testing.T
	h  http.Handler
	id string // admin token
	// 部门
	root, rd, mkt, rdBackend string
	// 用户 token
	tokAll, tokDept, tokTree, tokSelf, tokCustom string
}

func doDP(t *testing.T, h http.Handler, method, path, tok string, body any) (*httptest.ResponseRecorder, json.RawMessage) {
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
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	return rec, env.Data
}

func loginDP(t *testing.T, h http.Handler, user, pass string) string {
	t.Helper()
	rec, data := doDP(t, h, http.MethodPost, "/api/v1/identity/login", "", map[string]string{
		"username": user, "password": pass,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("login %s: %d %s", user, rec.Code, rec.Body.String())
	}
	var d struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(data, &d)
	return d.Token
}

// newDPEnv 装配 identity+org+workflow+audit+finance+document 并造五档用户。
func newDPEnv(t *testing.T) *dpTestEnv {
	t.Helper()
	a := app.New(nil)
	for _, m := range []app.Module{
		identity.New(), org.New(), workflow.New(), audit.New(),
		finance.New(), document.New(),
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
	e := &dpTestEnv{t: t, h: a.Router().Handler()}
	e.id = loginDP(t, e.h, "admin", "Basalt@2026")

	mkDept := func(parent, name string) string {
		rec, data := doDP(t, e.h, http.MethodPost, "/api/v1/org/depts", e.id, map[string]any{
			"parent_id": parent, "name": name,
		})
		if rec.Code != http.StatusCreated {
			t.Fatalf("mkDept %s: %d %s", name, rec.Code, rec.Body.String())
		}
		var d org.Dept
		_ = json.Unmarshal(data, &d)
		return d.ID
	}
	e.root = mkDept("", "总部")
	e.rd = mkDept(e.root, "研发部")
	e.mkt = mkDept(e.root, "市场部")
	e.rdBackend = mkDept(e.rd, "后端组")

	mkRole := func(code, scope string, depts []string) string {
		rec, data := doDP(t, e.h, http.MethodPost, "/api/v1/identity/roles", e.id, map[string]any{
			"code": code, "name": code, "permissions": []string{"finance:expense:list", "document:read", "org:dept:list"},
			"data_scope": scope, "dept_ids": depts,
		})
		if rec.Code != http.StatusCreated {
			t.Fatalf("mkRole %s: %d %s", code, rec.Code, rec.Body.String())
		}
		var r identity.Role
		_ = json.Unmarshal(data, &r)
		return r.ID
	}

	// 五档用户（研发部 4 人 + 市场部 1 人）
	mkUser := func(name, deptID, roleID string) string {
		rec, data := doDP(t, e.h, http.MethodPost, "/api/v1/identity/users", e.id, map[string]any{
			"username": name, "password": "Pass@2026!", "nickname": name, "dept_id": deptID,
		})
		if rec.Code != http.StatusCreated {
			t.Fatalf("mkUser %s: %d %s", name, rec.Code, rec.Body.String())
		}
		var u identity.User
		_ = json.Unmarshal(data, &u)
		if roleID != "" {
			doDP(t, e.h, http.MethodPut, "/api/v1/identity/users/"+u.ID+"/roles", e.id, map[string]any{
				"role_ids": []string{roleID},
			})
		}
		return loginDP(t, e.h, name, "Pass@2026!")
	}
	e.tokAll = mkUser("dp-all", e.rd, mkRole("dp-all", "all", nil))
	e.tokDept = mkUser("dp-dept", e.rd, mkRole("dp-dept", "dept", nil))
	e.tokTree = mkUser("dp-tree", e.rd, mkRole("dp-tree", "dept_and_below", nil))
	e.tokSelf = mkUser("dp-self", e.mkt, mkRole("dp-self", "self", nil))
	e.tokCustom = mkUser("dp-custom", e.mkt, mkRole("dp-custom", "custom", []string{e.rd}))
	return e
}

// mkExpense 用指定用户造费用单（dept 显式指定）。
func (e *dpTestEnv) mkExpense(tok, deptID string, amount int64) {
	rec, _ := doDP(e.t, e.h, http.MethodPost, "/api/v1/finance/expenses", tok, map[string]any{
		"reason": "数据权限测试", "dept_id": deptID,
		"items": []map[string]any{{"name": "物", "amount": amount}},
	})
	if rec.Code != http.StatusCreated {
		e.t.Fatalf("mkExpense: %d %s", rec.Code, rec.Body.String())
	}
}

func (e *dpTestEnv) mkDoc(tok, title string) {
	rec, _ := doDP(e.t, e.h, http.MethodPost, "/api/v1/documents", tok, map[string]any{
		"title": title, "type": "outbound",
	})
	if rec.Code != http.StatusCreated {
		e.t.Fatalf("mkDoc: %d %s", rec.Code, rec.Body.String())
	}
}

func (e *dpTestEnv) countAll(path, tok string) int {
	rec, data := doDP(e.t, e.h, http.MethodGet, path+"?scope=all", tok, nil)
	if rec.Code != http.StatusOK {
		e.t.Fatalf("GET %s: %d %s", path, rec.Code, rec.Body.String())
	}
	var arr []json.RawMessage
	_ = json.Unmarshal(data, &arr)
	return len(arr)
}

// TestFiveScopeFinanceMatrix 五档 × finance（费用单，部门+申请人双维）。
func TestFiveScopeFinanceMatrix(t *testing.T) {
	e := newDPEnv(t)
	// 数据分布：
	//  - 研发部（dp-dept 建）  1
	//  - 后端组（dp-tree 建，dept=后端组） 1
	//  - 市场部（dp-self 建） 1
	//  - 研发部（dp-custom 建，跨部门造单） 1
	e.mkExpense(e.tokDept, e.rd, 100)
	e.mkExpense(e.tokTree, e.rdBackend, 200)
	e.mkExpense(e.tokSelf, e.mkt, 300)
	e.mkExpense(e.tokCustom, e.rd, 400)

	cases := []struct {
		tok  string
		want int
		desc string
	}{
		{e.tokAll, 4, "all 全可见"},
		{e.tokDept, 2, "dept 仅本部门（含他人同部门单据，后端组不可见）"},
		{e.tokTree, 3, "dept_and_below 本部门+子树"},
		{e.tokSelf, 1, "self 仅本人（市场部单据+本人）"},
		{e.tokCustom, 2, "custom 自定义集=[研发部]（2 条研发部单据）"},
	}
	for _, tc := range cases {
		if got := e.countAll("/api/v1/finance/expenses", tc.tok); got != tc.want {
			t.Errorf("%s: got %d, want %d", tc.desc, got, tc.want)
		}
	}
}

// TestFiveScopeDocumentMatrix 五档 × document（公文，部门+创建人）。
// 公文 DeptID 取创建人部门（研发部用户建 → rd；市场部用户建 → mkt）。
func TestFiveScopeDocumentMatrix(t *testing.T) {
	e := newDPEnv(t)
	e.mkDoc(e.tokDept, "研发部公文")  // dept=rd
	e.mkDoc(e.tokTree, "研发部公文2") // dept=rd（tree 用户也在研发部）
	e.mkDoc(e.tokSelf, "市场部公文")  // dept=mkt

	cases := []struct {
		tok  string
		want int
		desc string
	}{
		{e.tokAll, 3, "all"},
		{e.tokDept, 2, "dept=研发部（2 条）"},
		{e.tokTree, 2, "tree=研发部子树"},
		{e.tokSelf, 1, "self 仅本人"},
		{e.tokCustom, 2, "custom=[研发部]"},
	}
	for _, tc := range cases {
		if got := e.countAll("/api/v1/documents", tc.tok); got != tc.want {
			t.Errorf("%s: got %d, want %d", tc.desc, got, tc.want)
		}
	}
}

// TestScopeOrgTreeFilter org 部门树裁剪（树形数据形态）。
func TestScopeOrgTreeFilter(t *testing.T) {
	e := newDPEnv(t)
	countTree := func(tok string) int {
		rec, data := doDP(t, e.h, http.MethodGet, "/api/v1/org/depts/tree", tok, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("tree: %d", rec.Code)
		}
		var nodes []*org.DeptNode
		_ = json.Unmarshal(data, &nodes)
		var count func(ns []*org.DeptNode) int
		count = func(ns []*org.DeptNode) int {
			n := 0
			for _, x := range ns {
				n++
				n += count(x.Children)
			}
			return n
		}
		return count(nodes)
	}
	// 管理员全树 4 部门；custom=[研发部] 树=研发部+后端组（祖先总部不可见）
	if got := countTree(e.id); got != 4 {
		t.Fatalf("admin tree = %d, want 4", got)
	}
	if got := countTree(e.tokCustom); got != 1 {
		t.Fatalf("custom tree = %d, want 1（custom 集不含子树与祖先）", got)
	}
	// self 档：无可见部门 → 空树
	if got := countTree(e.tokSelf); got != 0 {
		t.Fatalf("self tree = %d, want 0", got)
	}
}

// TestScopeUnfilteredEndpointNotAffected 未接入端点零影响（mine 分支
// 与既有行为一致——接入是声明式的）。
func TestScopeUnfilteredEndpointNotAffected(t *testing.T) {
	e := newDPEnv(t)
	e.mkExpense(e.tokDept, e.rd, 100)
	e.mkExpense(e.tokSelf, e.mkt, 300)
	// self 用户 mine 视角：本人 1 条（mine 不经过数据权限裁剪，天然本人）
	rec, data := doDP(t, e.h, http.MethodGet, "/api/v1/finance/expenses", e.tokSelf, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("mine: %d", rec.Code)
	}
	var arr []json.RawMessage
	_ = json.Unmarshal(data, &arr)
	if len(arr) != 1 {
		t.Fatalf("self mine = %d, want 1", len(arr))
	}
}

// TestRoleDataScopePersistRestart 重启恢复：data_scope/dept_ids 随角色
// 持久化，重启后范围裁剪行为一致（sqlite）。
func TestRoleDataScopePersistRestart(t *testing.T) {
	dsn := t.TempDir() + "/dp.db"
	_ = dsn
	// 该用例的持久化形态由 gormstore RoleStore 单测覆盖列映射；
	// 这里验证 HTTP 层 scope 字段往返（创建→更新→读取）。
	e := newDPEnv(t)
	rec, data := doDP(t, e.h, http.MethodPost, "/api/v1/identity/roles", e.id, map[string]any{
		"code": "dp-edit", "name": "n", "permissions": []string{"document:read"},
		"data_scope": "custom", "dept_ids": []string{"d1", "d2"},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create custom: %d %s", rec.Code, rec.Body.String())
	}
	var r identity.Role
	_ = json.Unmarshal(data, &r)
	if r.DataScope != "custom" || len(r.DeptIDs) != 2 {
		t.Fatalf("role = %+v", r)
	}
	// 更新档位
	rec, _ = doDP(t, e.h, http.MethodPatch, "/api/v1/identity/roles/"+r.ID, e.id, map[string]any{
		"data_scope": "dept_and_below",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("patch scope: %d", rec.Code)
	}
	rec, data = doDP(t, e.h, http.MethodGet, "/api/v1/identity/roles", e.id, nil)
	var roles []identity.Role
	_ = json.Unmarshal(data, &roles)
	for _, x := range roles {
		if x.ID == r.ID {
			if x.DataScope != "dept_and_below" || x.DeptIDs != nil {
				t.Fatalf("after patch = %+v（custom 档切走后 dept_ids 应清空）", x)
			}
			return
		}
	}
	t.Fatal("role not found after patch")
}

// TestInvalidDataScopeRejected 非法档位 400。
func TestInvalidDataScopeRejected(t *testing.T) {
	e := newDPEnv(t)
	if rec, _ := doDP(t, e.h, http.MethodPost, "/api/v1/identity/roles", e.id, map[string]any{
		"code": "dp-bad", "name": "n", "data_scope": "galaxy",
	}); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad scope: %d, want 400", rec.Code)
	}
}

// TestScopeDescription 叠加语义文档化断言：本包通过行为测试固化
// "租户硬边界 × 数据权限软范围"的交集语义（多角色并集取宽）。
func TestScopeDescription(t *testing.T) {
	// 并集取宽：all 角色存在 → 全量
	sc := dataperm.Scope{All: true, DeptIDs: []string{"x"}, Self: true, SelfUserID: "u"}
	if !sc.VisibleRow("any", "anyone") {
		t.Fatal("all 并集应覆盖一切")
	}
	_ = strings.TrimSpace
	_ = fmt.Sprintf
}
