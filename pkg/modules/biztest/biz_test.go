package biz_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/basalt-go/basalt/pkg/kernel/app"
	"github.com/basalt-go/basalt/pkg/modules/audit"
	"github.com/basalt-go/basalt/pkg/modules/contract"
	"github.com/basalt-go/basalt/pkg/modules/document"
	"github.com/basalt-go/basalt/pkg/modules/identity"
	"github.com/basalt-go/basalt/pkg/modules/job"
	"github.com/basalt-go/basalt/pkg/modules/workflow"
)

func newApp(t *testing.T) http.Handler {
	t.Helper()
	a := app.New(nil)
	for _, m := range []app.Module{
		identity.New(), audit.New(), job.New(nil), workflow.New(),
		document.New(), contract.New(),
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
		t.Fatalf("login: %d", rec.Code)
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

// TestDocumentLifecycle 发文全流程：拟稿 → 提交（默认流程）→ 核稿通过
// → 待归档 → 归档；密级入审计链。
func TestDocumentLifecycle(t *testing.T) {
	h := newApp(t)
	admin := loginAs(t, h, "admin", "Basalt@2026")
	_, staff := mkUser(t, h, admin, "clerk1")

	rec, data := do(t, h, http.MethodPost, "/api/v1/documents", staff, map[string]any{
		"doc_num": "玄武〔2026〕12号", "type": "outbound", "title": "关于开展等保自查的通知",
		"security": "秘密", "urgency": "加急", "correspond": "各处室",
		"content": "请各单位于本周五前完成自查。",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create doc: %d %s", rec.Code, rec.Body.String())
	}
	var d document.Doc
	_ = json.Unmarshal(data, &d)

	// 非法密级拒绝
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/documents", staff, map[string]any{
		"title": "x", "security": "绝密",
	}); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad security: %d", rec.Code)
	}

	// 提交 → 默认流程（admin 核稿）
	rec, data = do(t, h, http.MethodPost, "/api/v1/documents/"+d.ID+"/submit", staff, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("submit: %d %s", rec.Code, rec.Body.String())
	}
	_ = json.Unmarshal(data, &d)
	if d.Status != document.StCirculating || d.WorkflowInstID == "" {
		t.Fatalf("after submit: %+v", d)
	}
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/wf/instances/"+d.WorkflowInstID+"/approve", admin,
		map[string]string{"comment": "同意印发"}); rec.Code != http.StatusOK {
		t.Fatalf("approve: %d", rec.Code)
	}

	// 联动回写：待归档；然后归档
	var list []document.Doc
	_, data = do(t, h, http.MethodGet, "/api/v1/documents", staff, nil)
	_ = json.Unmarshal(data, &list)
	if list[0].Status != document.StApproved {
		t.Fatalf("after approve: %s", list[0].Status)
	}
	// 未通过审批前归档应 409——此刻已 approved，直接归档
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/documents/"+d.ID+"/archive", admin, nil); rec.Code != http.StatusOK {
		t.Fatalf("archive: %d", rec.Code)
	}
	_, data = do(t, h, http.MethodGet, "/api/v1/documents", staff, nil)
	_ = json.Unmarshal(data, &list)
	if list[0].Status != document.StArchived {
		t.Fatalf("after archive: %s", list[0].Status)
	}

	// 审计链含密级事件
	_, data = do(t, h, http.MethodGet, "/api/v1/audit/events?module=document&limit=10", admin, nil)
	var ev struct {
		Events []audit.Event `json:"events"`
	}
	_ = json.Unmarshal(data, &ev)
	if len(ev.Events) < 3 {
		t.Fatalf("document audit events = %d, want >= 3", len(ev.Events))
	}
}

// TestContractLifecycle 合同全流程：登记 → 提交会签 → 生效 → 完结；
// 到期扫描任务联动。
func TestContractLifecycle(t *testing.T) {
	h := newApp(t)
	admin := loginAs(t, h, "admin", "Basalt@2026")
	_, staff := mkUser(t, h, admin, "biz1")

	rec, data := do(t, h, http.MethodPost, "/api/v1/contracts", staff, map[string]any{
		"contract_num": "HT-2026-001", "title": "办公设备采购合同",
		"party": "某某科技有限公司", "amount": 25800000,
		"sign_date": "2026-10-01", "expire_date": time.Now().AddDate(0, 0, 10).Format("2006-01-02"),
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var c contract.Contract
	_ = json.Unmarshal(data, &c)
	if c.Amount != 25800000 {
		t.Fatalf("amount = %d", c.Amount)
	}

	// 提交会签 → 通过 → 生效
	rec, data = do(t, h, http.MethodPost, "/api/v1/contracts/"+c.ID+"/submit", staff, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("submit: %d", rec.Code)
	}
	_ = json.Unmarshal(data, &c)
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/wf/instances/"+c.WorkflowInstID+"/approve", admin, nil); rec.Code != http.StatusOK {
		t.Fatalf("approve: %d", rec.Code)
	}
	var list []contract.Contract
	_, data = do(t, h, http.MethodGet, "/api/v1/contracts", staff, nil)
	_ = json.Unmarshal(data, &list)
	if list[0].Status != contract.StActive {
		t.Fatalf("after approve: %s", list[0].Status)
	}

	// 到期扫描任务（合同 10 天后到期 → 命中 30 天窗口）
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/jobs/contract.expiry-scan/trigger", admin, nil); rec.Code != http.StatusOK {
		t.Fatalf("expiry scan: %d", rec.Code)
	}
	_, data = do(t, h, http.MethodGet, "/api/v1/audit/events?module=contract&limit=20", admin, nil)
	var ev struct {
		Events []audit.Event `json:"events"`
	}
	_ = json.Unmarshal(data, &ev)
	found := false
	for _, e := range ev.Events {
		if e.Event == "contract.expiring" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expiry event missing: %+v", ev.Events)
	}

	// 完结
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/contracts/"+c.ID+"/finish", admin, nil); rec.Code != http.StatusOK {
		t.Fatalf("finish: %d", rec.Code)
	}
	_, data = do(t, h, http.MethodGet, "/api/v1/contracts", staff, nil)
	_ = json.Unmarshal(data, &list)
	if list[0].Status != contract.StFinished {
		t.Fatalf("after finish: %s", list[0].Status)
	}
}

// TestDocumentRejectLoop 驳回后可修改重提。
func TestDocumentRejectLoop(t *testing.T) {
	h := newApp(t)
	admin := loginAs(t, h, "admin", "Basalt@2026")
	_, staff := mkUser(t, h, admin, "clerk2")

	_, data := do(t, h, http.MethodPost, "/api/v1/documents", staff, map[string]any{
		"title": "测试退回重拟", "type": "inbound", "security": "普通",
	})
	var d document.Doc
	_ = json.Unmarshal(data, &d)
	_, data = do(t, h, http.MethodPost, "/api/v1/documents/"+d.ID+"/submit", staff, nil)
	_ = json.Unmarshal(data, &d)

	// 驳回 → 状态 rejected → 可重提
	do(t, h, http.MethodPost, "/api/v1/wf/instances/"+d.WorkflowInstID+"/reject", admin,
		map[string]string{"comment": "格式不符"})
	_, data = do(t, h, http.MethodGet, "/api/v1/documents", staff, nil)
	var list []document.Doc
	_ = json.Unmarshal(data, &list)
	if list[0].Status != document.StRejected {
		t.Fatalf("after reject: %s", list[0].Status)
	}
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/documents/"+d.ID+"/submit", staff, nil); rec.Code != http.StatusOK {
		t.Fatalf("resubmit: %d", rec.Code)
	}
}
