package workflow_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/basalt-go/basalt/pkg/kernel/app"
	"github.com/basalt-go/basalt/pkg/modules/audit"
	"github.com/basalt-go/basalt/pkg/modules/identity"
	"github.com/basalt-go/basalt/pkg/modules/workflow"
)

func newApp(t *testing.T) *app.App {
	t.Helper()
	a := app.New(nil)
	for _, m := range []app.Module{identity.New(), audit.New(), workflow.New()} {
		if err := a.Registry().Register(m); err != nil {
			t.Fatalf("register %s: %v", m.Name(), err)
		}
	}
	return a
}

func initRouter(t *testing.T, a *app.App) http.Handler {
	t.Helper()
	mods, _ := a.Registry().Resolve()
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

// setup：admin 登录 + 两个审批人（bill/ted，无管理权限）。
func setup(t *testing.T, h http.Handler) (admin, bill, ted, billID, tedID string) {
	t.Helper()
	admin = loginAs(t, h, "admin", "Basalt@2026")
	mk := func(name string) (string, string) {
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
	billID, bill = mk("bill")
	tedID, ted = mk("ted")
	return
}

func startWF(t *testing.T, h http.Handler, admin string, approvers []string, mode string) string {
	t.Helper()
	rec, data := do(t, h, http.MethodPost, "/api/v1/wf/instances", admin, map[string]any{
		"business_key": "EXP-2026-001", "summary": "报销 100 元",
		"approvers": approvers, "mode": mode,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create wf: %d %s", rec.Code, rec.Body.String())
	}
	var in workflow.Instance
	_ = json.Unmarshal(data, &in)
	return in.ID
}

func getWF(t *testing.T, h http.Handler, token, id string) workflow.Instance {
	t.Helper()
	rec, data := do(t, h, http.MethodGet, "/api/v1/wf/instances/"+id, token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get wf: %d", rec.Code)
	}
	var out struct {
		Instance workflow.Instance `json:"instance"`
	}
	_ = json.Unmarshal(data, &out)
	return out.Instance
}

func act(t *testing.T, h http.Handler, token, id, kind string) int {
	t.Helper()
	rec, _ := do(t, h, http.MethodPost, "/api/v1/wf/instances/"+id+"/"+kind, token,
		map[string]string{"comment": "ok"})
	return rec.Code
}

func TestCountersignFullApproval(t *testing.T) {
	h := initRouter(t, newApp(t))
	admin, bill, ted, billID, tedID := setup(t, h)
	id := startWF(t, h, admin, []string{billID, tedID}, workflow.ModeCountersign)

	// bill 先批：仍 pending
	if code := act(t, h, bill, id, "approve"); code != http.StatusOK {
		t.Fatalf("bill approve: %d", code)
	}
	if in := getWF(t, h, admin, id); in.Status != workflow.StatusPending {
		t.Fatalf("after 1/2 approvals = %s, want pending", in.Status)
	}
	// 同一人重复动作：409
	if code := act(t, h, bill, id, "approve"); code != http.StatusConflict {
		t.Fatalf("repeated act: %d, want 409", code)
	}
	// ted 批完：approved
	if code := act(t, h, ted, id, "approve"); code != http.StatusOK {
		t.Fatalf("ted approve: %d", code)
	}
	if in := getWF(t, h, admin, id); in.Status != workflow.StatusApproved {
		t.Fatalf("after 2/2 = %s, want approved", in.Status)
	}
	// 终态后再动作：409
	if code := act(t, h, ted, id, "approve"); code != http.StatusConflict {
		t.Fatalf("act on finished: %d, want 409", code)
	}
}

func TestRejectTerminates(t *testing.T) {
	h := initRouter(t, newApp(t))
	admin, _, ted, _, tedID := setup(t, h)
	id := startWF(t, h, admin, []string{tedID}, workflow.ModeCountersign)

	if code := act(t, h, ted, id, "reject"); code != http.StatusOK {
		t.Fatalf("ted reject: %d", code)
	}
	if in := getWF(t, h, admin, id); in.Status != workflow.StatusRejected {
		t.Fatalf("status = %s, want rejected", in.Status)
	}
}

func TestOrSignFirstActionWins(t *testing.T) {
	h := initRouter(t, newApp(t))
	admin, bill, _, billID, tedID := setup(t, h)
	id := startWF(t, h, admin, []string{billID, tedID}, workflow.ModeOrSign)

	if code := act(t, h, bill, id, "approve"); code != http.StatusOK {
		t.Fatalf("bill approve: %d", code)
	}
	if in := getWF(t, h, admin, id); in.Status != workflow.StatusApproved {
		t.Fatalf("or-sign first approval = %s, want approved", in.Status)
	}
}

func TestTodoListAndGuards(t *testing.T) {
	h := initRouter(t, newApp(t))
	admin, bill, ted, billID, _ := setup(t, h)
	id := startWF(t, h, admin, []string{billID}, workflow.ModeCountersign)

	// bill 的待办包含实例；ted 没有
	rec, data := do(t, h, http.MethodGet, "/api/v1/wf/todo", bill, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("todo: %d", rec.Code)
	}
	var todo struct {
		Items []workflow.Instance `json:"items"`
	}
	_ = json.Unmarshal(data, &todo)
	if len(todo.Items) != 1 || todo.Items[0].ID != id {
		t.Fatalf("bill todo = %+v", todo.Items)
	}
	rec, data = do(t, h, http.MethodGet, "/api/v1/wf/todo", ted, nil)
	_ = json.Unmarshal(data, &todo)
	if len(todo.Items) != 0 {
		t.Fatalf("ted todo should be empty, got %+v", todo.Items)
	}

	// 非审批人操作：403
	if code := act(t, h, ted, id, "approve"); code != http.StatusForbidden {
		t.Fatalf("outsider approve: %d, want 403", code)
	}

	// 非发起人撤回：403；发起人撤回：200
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/wf/instances/"+id+"/cancel", bill, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("non-creator cancel: %d, want 403", rec.Code)
	}
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/wf/instances/"+id+"/cancel", admin, nil); rec.Code != http.StatusOK {
		t.Fatalf("creator cancel: %d", rec.Code)
	}
	if in := getWF(t, h, admin, id); in.Status != workflow.StatusCanceled {
		t.Fatalf("status = %s, want canceled", in.Status)
	}
}

func TestWorkflowEventsInAuditChain(t *testing.T) {
	h := initRouter(t, newApp(t))
	admin, bill, _, billID, _ := setup(t, h)
	id := startWF(t, h, admin, []string{billID}, workflow.ModeOrSign)
	_ = act(t, h, bill, id, "approve")

	rec, data := do(t, h, http.MethodGet, "/api/v1/audit/events?module=workflow", admin, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("audit events: %d", rec.Code)
	}
	var list struct {
		Events []audit.Event `json:"events"`
	}
	_ = json.Unmarshal(data, &list)
	if len(list.Events) < 2 {
		t.Fatalf("workflow audit events = %d, want >= 2", len(list.Events))
	}
}
