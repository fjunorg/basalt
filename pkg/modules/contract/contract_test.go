package contract_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/basalt-go/basalt/pkg/kernel/app"
	"github.com/basalt-go/basalt/pkg/modules/audit"
	"github.com/basalt-go/basalt/pkg/modules/contract"
	"github.com/basalt-go/basalt/pkg/modules/identity"
	"github.com/basalt-go/basalt/pkg/modules/job"
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

// newContractApp 装配 contract 完整依赖（identity/workflow/audit/job）。
func newContractApp(t *testing.T) http.Handler {
	t.Helper()
	return newApp(t, workflow.New(), audit.New(), job.New(nil), contract.New())
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
		t.Fatalf("create %s: %d", name, rec.Code)
	}
	var u identity.User
	_ = json.Unmarshal(data, &u)
	return u.ID, loginAs(t, h, name, "password123")
}

// TestContractCRUDAndBoundaries 登记默认草稿、金额/日期解析、mine 视图；
// 边界：缺必填/非正金额 400、非创建人提交 403、重复提交 409、
// 草稿完结 409、普通用户 scope=all 403。
func TestContractCRUDAndBoundaries(t *testing.T) {
	h := newContractApp(t)
	admin := loginAs(t, h, "admin", "Basalt@2026")
	_, biz := mkUser(t, h, admin, "biz1")

	rec, data := do(t, h, http.MethodPost, "/api/v1/contracts", biz, map[string]any{
		"contract_num": "HT-2026-001", "title": "设备采购合同", "party": "某某科技有限公司",
		"amount": 25800000, "sign_date": "2026-10-01", "expire_date": "2027-09-30",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var c contract.Contract
	_ = json.Unmarshal(data, &c)
	if c.Status != contract.StDraft || c.Amount != 25800000 {
		t.Fatalf("created: %+v", c)
	}
	if got := c.ExpireDate.Format("2006-01-02"); got != "2027-09-30" {
		t.Fatalf("expire date parse: %s", got)
	}

	// mine 视图
	_, data = do(t, h, http.MethodGet, "/api/v1/contracts", biz, nil)
	var mine []contract.Contract
	_ = json.Unmarshal(data, &mine)
	if len(mine) != 1 || mine[0].ID != c.ID {
		t.Fatalf("mine = %+v", mine)
	}
	// 普通用户 scope=all → 403
	if rec, _ := do(t, h, http.MethodGet, "/api/v1/contracts?scope=all", biz, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("staff scope=all: %d, want 403", rec.Code)
	}

	// ---- 边界 ----
	badBodies := []map[string]any{
		{"title": "缺对方"},                                                        // 缺 party
		{"party": "某公司"},                                                        // 缺 title
		{"title": "零额", "party": "某公司", "amount": 0},                            // 非正金额
		{"title": "负额", "party": "某公司", "amount": -1},                           // 负金额
	}
	for i, body := range badBodies {
		if rec, _ := do(t, h, http.MethodPost, "/api/v1/contracts", biz, body); rec.Code != http.StatusBadRequest {
			t.Fatalf("bad body #%d: %d, want 400", i, rec.Code)
		}
	}
	// 非创建人提交
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/contracts/"+c.ID+"/submit", admin, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("submit by non-creator: %d, want 403", rec.Code)
	}
	// 提交 → reviewing；重复提交 409
	if rec, _ = do(t, h, http.MethodPost, "/api/v1/contracts/"+c.ID+"/submit", biz, nil); rec.Code != http.StatusOK {
		t.Fatalf("submit: %d %s", rec.Code, rec.Body.String())
	}
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/contracts/"+c.ID+"/submit", biz, nil); rec.Code != http.StatusConflict {
		t.Fatalf("re-submit: %d, want 409", rec.Code)
	}
	// reviewing 非 active → finish 409
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/contracts/"+c.ID+"/finish", admin, nil); rec.Code != http.StatusConflict {
		t.Fatalf("finish reviewing: %d, want 409", rec.Code)
	}
	// ghost
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/contracts/ghost/submit", biz, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("submit ghost: %d, want 404", rec.Code)
	}
}

// TestContractPersistenceRestart 重启恢复：合同（金额/到期日）写入文件库 →
// 重建 store 与模块实例 → mine/全量视图读回金额、日期、状态一致。
func TestContractPersistenceRestart(t *testing.T) {
	dsn := t.TempDir() + "/contract.db"

	mkApp := func(s *gormstore.Stores) http.Handler {
		t.Helper()
		a := app.New(nil)
		for _, m := range []app.Module{
			identity.NewWithStores(s.UserStore(), s.RoleStore()),
			workflow.New(),
			audit.New(),
			job.NewWithStore(a.Logger(), s.JobStore()),
			contract.NewWithStore(s.ContractStore()),
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
	_, biz := mkUser(t, h1, admin, "biz2")
	if rec, _ := do(t, h1, http.MethodPost, "/api/v1/contracts", biz, map[string]any{
		"contract_num": "HT-2026-002", "title": "年度服务合同", "party": "乙方公司",
		"amount": 9900000, "sign_date": "2026-10-15", "expire_date": "2026-11-15",
	}); rec.Code != http.StatusCreated {
		t.Fatalf("create: %d", rec.Code)
	}
	if sqlDB, err := s1.DB.DB(); err == nil {
		_ = sqlDB.Close()
	}

	s2, err := gormstore.Open(dsn)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	h2 := mkApp(s2)
	biz2 := loginAs(t, h2, "biz2", "password123")

	rec, data := do(t, h2, http.MethodGet, "/api/v1/contracts", biz2, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("mine after restart: %d", rec.Code)
	}
	var mine []contract.Contract
	_ = json.Unmarshal(data, &mine)
	if len(mine) != 1 {
		t.Fatalf("mine after restart = %+v", mine)
	}
	got := mine[0]
	if got.Amount != 9900000 || got.Status != contract.StDraft ||
		got.ContractNum != "HT-2026-002" || got.ExpireDate.Format("2006-01-02") != "2026-11-15" {
		t.Fatalf("contract after restart: %+v", got)
	}
}
