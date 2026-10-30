package finance_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/basalt-go/basalt/pkg/kernel/app"
	"github.com/basalt-go/basalt/pkg/modules/audit"
	"github.com/basalt-go/basalt/pkg/modules/finance"
	"github.com/basalt-go/basalt/pkg/modules/identity"
	"github.com/basalt-go/basalt/pkg/modules/workflow"
)

func newApp(t *testing.T) http.Handler {
	t.Helper()
	a := app.New(nil)
	for _, m := range []app.Module{
		identity.New(), audit.New(), workflow.New(), finance.New(),
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

// TestExpenseFullLoop 报销全闭环：建单→（超预算拦截）→提交→审批→
// 预算占用→付款。
func TestExpenseFullLoop(t *testing.T) {
	h := newApp(t)
	admin := loginAs(t, h, "admin", "Basalt@2026")
	_, emp := mkUser(t, h, admin, "emp1")

	// 部门 + 预算 ¥100（10000 分）
	deptID := "dept-finance-test"
	rec, _ := do(t, h, http.MethodPut, "/api/v1/finance/budgets/"+deptID, admin,
		map[string]any{"limit": 10000})
	if rec.Code != http.StatusOK {
		t.Fatalf("set budget: %d", rec.Code)
	}

	// 报销单 ¥50
	rec, data := do(t, h, http.MethodPost, "/api/v1/finance/expenses", emp, map[string]any{
		"dept_id": deptID, "reason": "差旅",
		"items": []map[string]any{{"name": "高铁", "amount": 5000}},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create expense: %d %s", rec.Code, rec.Body.String())
	}
	var e finance.Expense
	_ = json.Unmarshal(data, &e)

	// 提交 → admin 审批通过
	rec, data = do(t, h, http.MethodPost, "/api/v1/finance/expenses/"+e.ID+"/submit", emp, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("submit: %d %s", rec.Code, rec.Body.String())
	}
	_ = json.Unmarshal(data, &e)
	if e.Status != finance.ExpSubmitted || e.WorkflowInstID == "" {
		t.Fatalf("after submit: %+v", e)
	}
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/wf/instances/"+e.WorkflowInstID+"/approve", admin,
		map[string]string{"comment": "ok"}); rec.Code != http.StatusOK {
		t.Fatalf("approve: %d", rec.Code)
	}

	// 联动：单据 approved + 预算占用 5000
	rec, data = do(t, h, http.MethodGet, "/api/v1/finance/expenses?scope=all", admin, nil)
	var list []finance.Expense
	_ = json.Unmarshal(data, &list)
	if len(list) != 1 || list[0].Status != finance.ExpApproved {
		t.Fatalf("after approve: %+v", list)
	}
	rec, data = do(t, h, http.MethodGet, "/api/v1/finance/budgets", admin, nil)
	var budgets []finance.Budget
	_ = json.Unmarshal(data, &budgets)
	found := false
	for _, b := range budgets {
		if b.DeptID == deptID && b.Used == 5000 {
			found = true
		}
	}
	if !found {
		t.Fatalf("budget not consumed: %+v", budgets)
	}

	// 第二单 ¥60：超预算拦截（5000 已用 + 6000 > 10000）
	rec, data = do(t, h, http.MethodPost, "/api/v1/finance/expenses", emp, map[string]any{
		"dept_id": deptID, "reason": "超预算报销",
		"items": []map[string]any{{"name": "住宿", "amount": 6000}},
	})
	var e2 finance.Expense
	_ = json.Unmarshal(data, &e2)
	rec, _ = do(t, h, http.MethodPost, "/api/v1/finance/expenses/"+e2.ID+"/submit", emp, nil)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("over-budget submit: %d, want 422", rec.Code)
	}

	// 付款闭环
	rec, data = do(t, h, http.MethodPost, "/api/v1/finance/payments", admin, map[string]string{
		"expense_id": e.ID, "payee": "张三",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("payment: %d %s", rec.Code, rec.Body.String())
	}
	rec, data = do(t, h, http.MethodGet, "/api/v1/finance/expenses?scope=all", admin, nil)
	_ = json.Unmarshal(data, &list)
	// 重新解包（不复用旧 slice：json.Unmarshal 对复用元素的缺失字段保留旧值）
	list = nil
	_ = json.Unmarshal(data, &list)
	var paid int
	for _, x := range list {
		if x.ID == e.ID && x.Status == finance.ExpPaid {
			paid++
		}
	}
	if paid != 1 {
		t.Fatalf("after payment: %+v", list)
	}
	// 重复付款：非 approved → 409
	rec, _ = do(t, h, http.MethodPost, "/api/v1/finance/payments", admin, map[string]string{
		"expense_id": e.ID, "payee": "张三",
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("re-pay: %d, want 409", rec.Code)
	}
}

// TestMultiStepDefinitionAndDelegate 多级流程定义 + 转办。
func TestMultiStepDefinitionAndDelegate(t *testing.T) {
	h := newApp(t)
	admin := loginAs(t, h, "admin", "Basalt@2026")
	l1ID, l1 := mkUser(t, h, admin, "leader1")
	l2ID, l2 := mkUser(t, h, admin, "leader2")
	_, emp := mkUser(t, h, admin, "emp2")

	// 定义两级流程：leader1 会签 → leader2 或签
	rec, data := do(t, h, http.MethodPost, "/api/v1/wf/definitions", admin, map[string]any{
		"key": "contract", "name": "合同审批", "category": "biz",
		"steps": []map[string]any{
			{"name": "部门审核", "approvers": []string{l1ID}, "mode": "countersign"},
			{"name": "分管领导", "approvers": []string{l1ID}, "mode": "or-sign"},
		},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create def: %d %s", rec.Code, rec.Body.String())
	}
	var def workflow.Definition
	_ = json.Unmarshal(data, &def)

	// emp 按定义发起
	rec, data = do(t, h, http.MethodPost, "/api/v1/wf/instances/by-definition/contract", emp, map[string]string{
		"business_key": "HT-001", "summary": "采购合同",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("start by def: %d %s", rec.Code, rec.Body.String())
	}
	var in workflow.Instance
	_ = json.Unmarshal(data, &in)

	// 第一步 leader1 审批 → 推进第二步（仍 leader1，但属于步骤 2）
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/wf/instances/"+in.ID+"/approve", l1,
		map[string]string{"comment": "一审通过"}); rec.Code != http.StatusOK {
		t.Fatalf("step1 approve: %d", rec.Code)
	}
	rec, data = do(t, h, http.MethodGet, "/api/v1/wf/instances/"+in.ID, admin, nil)
	var detail struct {
		Instance workflow.Instance `json:"instance"`
	}
	_ = json.Unmarshal(data, &detail)
	if detail.Instance.Status != workflow.StatusPending || detail.Instance.StepIndex != 1 {
		t.Fatalf("after step1: status=%s step=%d", detail.Instance.Status, detail.Instance.StepIndex)
	}

	// 第二步转办给 leader2 → leader2 通过 → 终态 approved
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/wf/instances/"+in.ID+"/delegate", l1,
		map[string]string{"to_user_id": l2ID}); rec.Code != http.StatusOK {
		t.Fatalf("delegate: %d", rec.Code)
	}
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/wf/instances/"+in.ID+"/approve", l2,
		map[string]string{"comment": "终审通过"}); rec.Code != http.StatusOK {
		t.Fatalf("step2 approve: %d", rec.Code)
	}
	rec, data = do(t, h, http.MethodGet, "/api/v1/wf/instances/"+in.ID, admin, nil)
	_ = json.Unmarshal(data, &detail)
	if detail.Instance.Status != workflow.StatusApproved {
		t.Fatalf("final: %s", detail.Instance.Status)
	}
}

// TestInvoiceDupCode 发票重号。
func TestInvoiceDupCode(t *testing.T) {
	h := newApp(t)
	admin := loginAs(t, h, "admin", "Basalt@2026")
	body := map[string]any{"code": "INV-001", "title": "某某科技有限公司", "tax_no": "91110000XX", "amount": 5000}
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/finance/invoices", admin, body); rec.Code != http.StatusCreated {
		t.Fatalf("invoice: %d", rec.Code)
	}
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/finance/invoices", admin, body); rec.Code != http.StatusConflict {
		t.Fatalf("dup invoice: %d, want 409", rec.Code)
	}
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

func uidOf(t *testing.T, h http.Handler, admin, name string) string {
	t.Helper()
	_, data := do(t, h, http.MethodGet, "/api/v1/identity/users", admin, nil)
	var users []identity.User
	_ = json.Unmarshal(data, &users)
	for _, u := range users {
		if u.Username == name {
			return u.ID
		}
	}
	t.Fatalf("user %s not found", name)
	return ""
}
