package workflow_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/basalt-go/basalt/pkg/kernel/app"
	"github.com/basalt-go/basalt/pkg/modules/audit"
	"github.com/basalt-go/basalt/pkg/modules/identity"
	"github.com/basalt-go/basalt/pkg/modules/workflow"
	"github.com/basalt-go/basalt/pkg/stores/gormstore"
)

// ---------- 六项操作矩阵（内存实现；GORM 一致性由重启用例覆盖） ----------

type wfEnv struct {
	t                           *testing.T
	h                           http.Handler
	admin, bill, ted, eve, fox  string // tokens
	billID, tedID, eveID, foxID string
}

func newWFEnv(t *testing.T) *wfEnv {
	t.Helper()
	a := app.New(nil)
	for _, m := range []app.Module{identity.New(), audit.New(), workflow.New()} {
		if err := a.Registry().Register(m); err != nil {
			t.Fatalf("register: %v", err)
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
	e := &wfEnv{t: t, h: a.Router().Handler()}
	e.admin = loginAs(t, e.h, "admin", "Basalt@2026")
	mk := func(name string) (tok, uid string) {
		rec, data := do(t, e.h, http.MethodPost, "/api/v1/identity/users", e.admin, map[string]any{
			"username": name, "password": "Pass@2026!", "nickname": name,
		})
		if rec.Code != http.StatusCreated {
			t.Fatalf("mk %s: %d %s", name, rec.Code, rec.Body.String())
		}
		var u struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(data, &u)
		return loginAs(t, e.h, name, "Pass@2026!"), u.ID
	}
	e.bill, e.billID = mk("bill")
	e.ted, e.tedID = mk("ted")
	e.eve, e.eveID = mk("eve")
	e.fox, e.foxID = mk("fox")
	return e
}

// startWF 发起实例并返回 id。
func (e *wfEnv) startWF(mode string, approvers []string) string {
	e.t.Helper()
	steps := []map[string]any{
		{"name": "一步", "approvers": approvers, "mode": mode},
	}
	rec, data := do(e.t, e.h, http.MethodPost, "/api/v1/wf/instances", e.admin, map[string]any{
		"business_key": "bk-six", "summary": "六操作测试", "steps": steps,
	})
	if rec.Code != http.StatusCreated {
		e.t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
	}
	var in struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(data, &in)
	return in.ID
}

func (e *wfEnv) post(path, tok string, body any) (*json.RawMessage, int) {
	e.t.Helper()
	rec, data := do(e.t, e.h, http.MethodPost, path, tok, body)
	return &data, rec.Code
}

// TestSequentialApproval 依次审批：非轮到者 403；按序通过；末人过则步进。
func TestSequentialApproval(t *testing.T) {
	e := newWFEnv(t)
	id := e.startWF(workflow.ModeSequential, []string{e.billID, e.tedID, e.eveID})

	// ted 未轮到（轮到 bill）→ 403
	if _, code := e.post("/api/v1/wf/instances/"+id+"/approve", e.ted, nil); code != http.StatusForbidden {
		t.Fatalf("skip turn: %d, want 403", code)
	}
	// bill 过 → 轮 ted；eve 未轮到 403
	if _, code := e.post("/api/v1/wf/instances/"+id+"/approve", e.bill, nil); code != http.StatusOK {
		t.Fatalf("bill: %d", code)
	}
	if _, code := e.post("/api/v1/wf/instances/"+id+"/approve", e.eve, nil); code != http.StatusForbidden {
		t.Fatalf("eve skip: %d", code)
	}
	// ted、eve 依次过 → 全部完成 → approved
	for _, tok := range []string{e.ted, e.eve} {
		if _, code := e.post("/api/v1/wf/instances/"+id+"/approve", tok, nil); code != http.StatusOK {
			t.Fatalf("seq approve: %d", code)
		}
	}
	_, data := do(t, e.h, http.MethodGet, "/api/v1/wf/instances/"+id, e.admin, nil)
	var d struct {
		Instance struct {
			Status string `json:"status"`
		} `json:"instance"`
	}
	_ = json.Unmarshal(data, &d)
	if d.Instance.Status != "approved" {
		t.Fatalf("status = %s, want approved", d.Instance.Status)
	}
}

// TestCCAndList 抄送：幂等去重、被抄送列表可见、非相关人 403。
func TestCCAndList(t *testing.T) {
	e := newWFEnv(t)
	id := e.startWF(workflow.ModeCountersign, []string{e.billID})

	// 无关人（fox）抄送 → 403
	if _, code := e.post("/api/v1/wf/instances/"+id+"/cc", e.fox, map[string]any{
		"user_ids": []string{e.foxID},
	}); code != http.StatusForbidden {
		t.Fatalf("outsider cc: %d", code)
	}
	// 审批人 bill 抄送 fox+ted
	if _, code := e.post("/api/v1/wf/instances/"+id+"/cc", e.bill, map[string]any{
		"user_ids": []string{e.foxID, e.tedID},
	}); code != http.StatusOK {
		t.Fatalf("cc: %d", code)
	}
	// 幂等（重复抄送 200，名单不变）
	if _, code := e.post("/api/v1/wf/instances/"+id+"/cc", e.bill, map[string]any{
		"user_ids": []string{e.foxID},
	}); code != http.StatusOK {
		t.Fatalf("dup cc: %d", code)
	}
	// fox 的被抄送列表含实例
	rec, data := do(t, e.h, http.MethodGet, "/api/v1/wf/cc", e.fox, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("cc list: %d", rec.Code)
	}
	var lst struct {
		Count int `json:"count"`
	}
	_ = json.Unmarshal(data, &lst)
	if lst.Count != 1 {
		t.Fatalf("cc count = %d", lst.Count)
	}
	// 未被抄送的 eve 列表为空
	_, data = do(t, e.h, http.MethodGet, "/api/v1/wf/cc", e.eve, nil)
	_ = json.Unmarshal(data, &lst)
	if lst.Count != 0 {
		t.Fatalf("eve cc count = %d", lst.Count)
	}
}

// TestDelegateActSemantics 委派 vs 转办语义对照：
// 委派后被委托人可审一次（A 名义保留）；转办则 A 失去审批权。
func TestDelegateActSemantics(t *testing.T) {
	e := newWFEnv(t)
	id := e.startWF(workflow.ModeCountersign, []string{e.billID, e.tedID})

	// bill 委派 eve 代审
	if _, code := e.post("/api/v1/wf/instances/"+id+"/delegate-act", e.bill,
		map[string]any{"to_user_id": e.eveID}); code != http.StatusOK {
		t.Fatalf("delegate-act: %d", code)
	}
	// eve（非审批人）代审通过 → 动作记 delegate_from=bill
	_, _ = e.post("/api/v1/wf/instances/"+id+"/approve", e.eve, nil)
	// 双人会签：eve 代 bill 审后 ted 仍未审 → 本步未完，step 应保持 0
	_, d2 := do(t, e.h, http.MethodGet, "/api/v1/wf/instances/"+id, e.admin, nil)
	var det struct {
		Instance struct {
			StepIndex int `json:"step_index"`
		} `json:"instance"`
		Actions []struct {
			Act          string `json:"act"`
			ActorID      string `json:"actor_id"`
			DelegateFrom string `json:"delegate_from"`
		} `json:"actions"`
	}
	_ = json.Unmarshal(d2, &det)
	// 双人会签：eve 代 bill 审后 ted 仍未审 → 本步未完，step 保持 0
	if det.Instance.StepIndex != 0 {
		t.Fatalf("step = %d（会签未全员，不应推进）", det.Instance.StepIndex)
	}
	// ted 补审后本步完成 → 进入第二步
	if _, code := e.post("/api/v1/wf/instances/"+id+"/approve", e.ted, nil); code != http.StatusOK {
		t.Fatalf("ted: %d", code)
	}
	found := false
	for _, a := range det.Actions {
		if a.Act == "approve" && a.ActorID == e.eveID && a.DelegateFrom == e.billID {
			found = true
		}
	}
	if !found {
		t.Fatalf("actions = %+v（缺代审记录 delegate_from）", det.Actions)
	}
	// 无委托者（fox）审批 → 403（未被委托）
	if _, code := e.post("/api/v1/wf/instances/"+id+"/approve", e.fox, nil); code != http.StatusForbidden {
		t.Fatalf("non-delegate approve: %d", code)
	}
	// 委托已消耗：eve 再审下一步（她不是审批人也无委托）→ 403
	if _, code := e.post("/api/v1/wf/instances/"+id+"/approve", e.eve, nil); code != http.StatusForbidden {
		t.Fatalf("consumed delegation: %d", code)
	}
}

// TestAddSignBeforeAfter 加签 before/after：
// before=新步先审（原步骤后移）；after=本步过后先走新步。
func TestAddSignBeforeAfter(t *testing.T) {
	e := newWFEnv(t)
	id := e.startWF(workflow.ModeCountersign, []string{e.billID, e.tedID})

	// before 加签（bill 是当前审批人）：新步（fox）先审
	if _, code := e.post("/api/v1/wf/instances/"+id+"/addsign", e.bill, map[string]any{
		"position": "before", "approvers": []string{e.foxID},
	}); code != http.StatusOK {
		t.Fatalf("addsign before: %d", code)
	}
	// 当前轮到 fox（新步），bill 审 → 403（非当前步审批人）
	if _, code := e.post("/api/v1/wf/instances/"+id+"/approve", e.bill, nil); code != http.StatusForbidden {
		t.Fatalf("before-add bill approve: %d（应轮新步）", code)
	}
	// fox 过 → 回原步（bill）
	if _, code := e.post("/api/v1/wf/instances/"+id+"/approve", e.fox, nil); code != http.StatusOK {
		t.Fatalf("fox: %d", code)
	}
	if _, code := e.post("/api/v1/wf/instances/"+id+"/approve", e.bill, nil); code != http.StatusOK {
		t.Fatalf("bill after new step: %d", code)
	}
	// after 加签（ted 当前审批人）：ted 过后先走新步（eve）再终态
	if _, code := e.post("/api/v1/wf/instances/"+id+"/addsign", e.ted, map[string]any{
		"position": "after", "approvers": []string{e.eveID}, "mode": "or-sign",
	}); code != http.StatusOK {
		t.Fatalf("addsign after: %d", code)
	}
	if _, code := e.post("/api/v1/wf/instances/"+id+"/approve", e.ted, nil); code != http.StatusOK {
		t.Fatalf("ted: %d", code)
	}
	if _, code := e.post("/api/v1/wf/instances/"+id+"/approve", e.eve, nil); code != http.StatusOK {
		t.Fatalf("eve after-sign: %d", code)
	}
	_, data := do(t, e.h, http.MethodGet, "/api/v1/wf/instances/"+id, e.admin, nil)
	var d struct {
		Instance struct {
			Status string `json:"status"`
		} `json:"instance"`
	}
	_ = json.Unmarshal(data, &d)
	if d.Instance.Status != "approved" {
		t.Fatalf("status = %s（after 加签链应 approved）", d.Instance.Status)
	}
	// 非法参数：position 越界 / 空名单
	if _, code := e.post("/api/v1/wf/instances/"+id+"/addsign", e.bill, map[string]any{
		"position": "middle", "approvers": []string{e.foxID},
	}); code != http.StatusBadRequest && code != http.StatusConflict {
		t.Fatalf("bad position: %d", code)
	}
}

// TestRejectToPassedStep 驳回至已过节点：回退重走 + 轮次失效 +
// target 越界 400。
func TestRejectToPassedStep(t *testing.T) {
	e := newWFEnv(t)
	// 两步：bill → ted
	rec, data := do(t, e.h, http.MethodPost, "/api/v1/wf/instances", e.admin, map[string]any{
		"business_key": "bk-rt", "summary": "驳回重走",
		"steps": []map[string]any{
			{"name": "一", "approvers": []string{e.billID}, "mode": "or-sign"},
			{"name": "二", "approvers": []string{e.tedID}, "mode": "or-sign"},
		},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("start: %d", rec.Code)
	}
	var in struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(data, &in)
	id := in.ID

	// 第一步过 → 第二步（ted）
	if _, code := e.post("/api/v1/wf/instances/"+id+"/approve", e.bill, nil); code != http.StatusOK {
		t.Fatalf("bill: %d", code)
	}
	// ted 驳回首步
	if _, code := e.post("/api/v1/wf/instances/"+id+"/reject-to", e.ted, map[string]any{
		"target_step": 0, "comment": "材料不全",
	}); code != http.StatusOK {
		t.Fatalf("reject-to: %d", code)
	}
	// 实例回到 step0 round1；bill 重审（旧动作在 round0 不失效本轮）
	if _, code := e.post("/api/v1/wf/instances/"+id+"/approve", e.bill, nil); code != http.StatusOK {
		t.Fatalf("bill round1: %d（回退后旧轮动作应失效）", code)
	}
	if _, code := e.post("/api/v1/wf/instances/"+id+"/approve", e.ted, nil); code != http.StatusOK {
		t.Fatalf("ted round1: %d", code)
	}
	_, data = do(t, e.h, http.MethodGet, "/api/v1/wf/instances/"+id, e.admin, nil)
	var d struct {
		Instance struct {
			Status string `json:"status"`
		} `json:"instance"`
	}
	_ = json.Unmarshal(data, &d)
	if d.Instance.Status != "approved" {
		t.Fatalf("status = %s", d.Instance.Status)
	}
	// 越界 target（终态实例）→ 409
	if _, code := e.post("/api/v1/wf/instances/"+id+"/reject-to", e.bill, map[string]any{
		"target_step": 0,
	}); code != http.StatusConflict {
		t.Fatalf("finished reject-to: %d, want 409", code)
	}
}

// TestSixOpsAgainstFinishedInstance 终态实例：六操作全部 409。
func TestSixOpsAgainstFinishedInstance(t *testing.T) {
	e := newWFEnv(t)
	id := e.startWF(workflow.ModeOrSign, []string{e.billID})
	if _, code := e.post("/api/v1/wf/instances/"+id+"/approve", e.bill, nil); code != http.StatusOK {
		t.Fatalf("approve: %d", code)
	}
	for name, body := range map[string]any{
		"cc":           map[string]any{"user_ids": []string{e.foxID}},
		"delegate-act": map[string]any{"to_user_id": e.eveID},
		"addsign":      map[string]any{"position": "before", "approvers": []string{e.foxID}},
		"reject-to":    map[string]any{"target_step": 0},
	} {
		if _, code := e.post("/api/v1/wf/instances/"+id+"/"+name, e.bill, body); code != http.StatusConflict {
			t.Fatalf("%s on finished: %d, want 409", name, code)
		}
	}
}

// ---------- GORM 重启恢复：六操作中间态跨重启 ----------

// TestSixOpsRestartRecovery sqlite：cc/delegation/round/加签步骤跨重启
// 完整恢复，操作可继续。
func TestSixOpsRestartRecovery(t *testing.T) {
	dsn := t.TempDir() + "/wfsix.db"
	build := func(s *gormstore.Stores) http.Handler {
		m := workflow.NewWithStores(s.WorkflowStore(), s.WorkflowDefStore())
		a := app.New(nil)
		// identity 也走 GORM（用户跨重启），否则重启后 sx1/sx2 不存在
		for _, mod := range []app.Module{identity.NewWithStores(s.UserStore(), s.RoleStore()), audit.New(), m} {
			if err := a.Registry().Register(mod); err != nil {
				t.Fatalf("register: %v", err)
			}
		}
		mods, _ := a.Registry().Resolve()
		for _, mod := range mods {
			if err := mod.Init(context.Background(), a); err != nil {
				t.Fatalf("init: %v", err)
			}
			_ = mod.Start(context.Background())
		}
		return a.Router().Handler()
	}
	s1, err := gormstore.Open(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	h1 := build(s1)
	admin1 := loginAs(t, h1, "admin", "Basalt@2026")

	// 三步 sequential + cc + 委派中间态
	rec, data := do(t, h1, http.MethodPost, "/api/v1/identity/users", admin1, map[string]any{
		"username": "sx1", "password": "Pass@2026!",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("mkuser: %d", rec.Code)
	}
	var u1 struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(data, &u1)
	tok1 := loginAs(t, h1, "sx1", "Pass@2026!")
	rec, data = do(t, h1, http.MethodPost, "/api/v1/identity/users", admin1, map[string]any{
		"username": "sx2", "password": "Pass@2026!",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("mkuser sx2: %d %s", rec.Code, rec.Body.String())
	}
	var u2 struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(data, &u2)
	tok2 := loginAs(t, h1, "sx2", "Pass@2026!")

	rec, data = do(t, h1, http.MethodPost, "/api/v1/wf/instances", admin1, map[string]any{
		"business_key": "bk-restart", "summary": "重启中间态",
		"steps": []map[string]any{
			{"name": "一", "approvers": []string{u1.ID}, "mode": "or-sign"},
			{"name": "二", "approvers": []string{u2.ID}, "mode": "or-sign"},
		},
	})
	var inst struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(data, &inst)

	// 中间态：第一步过 → 第二步 u2 加签 after + 委派 u1 + 抄送 admin
	_, _ = do(t, h1, http.MethodPost, "/api/v1/wf/instances/"+inst.ID+"/approve", tok1, nil)
	do(t, h1, http.MethodPost, "/api/v1/wf/instances/"+inst.ID+"/addsign", tok2, map[string]any{
		"position": "after", "approvers": []string{u1.ID}, "mode": "or-sign",
	})
	do(t, h1, http.MethodPost, "/api/v1/wf/instances/"+inst.ID+"/delegate-act", tok2, map[string]any{
		"to_user_id": u1.ID,
	})
	do(t, h1, http.MethodPost, "/api/v1/wf/instances/"+inst.ID+"/cc", tok2, map[string]any{
		"user_ids": []string{admin1[:0] + "admin"},
	})
	if sqlDB, err := s1.DB.DB(); err == nil {
		_ = sqlDB.Close()
	}

	// 重启：恢复后继续走完（u1 以被委托身份代审第二步 → 加签步 → approved）
	s2, err := gormstore.Open(dsn)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	h2 := build(s2)
	admin1 = loginAs(t, h2, "admin", "Basalt@2026") // 重启后重新登录
	tok1 = loginAs(t, h2, "sx1", "Pass@2026!")
	tok2 = loginAs(t, h2, "sx2", "Pass@2026!")
	// u1 代审（委托恢复验证）
	if rec, _ := do(t, h2, http.MethodPost, "/api/v1/wf/instances/"+inst.ID+"/approve", tok1, nil); rec.Code != http.StatusOK {
		t.Fatalf("delegate approve after restart: %d %s（委托中间态应恢复）", rec.Code, rec.Body.String())
	}
	// 加签步（u1）→ approved
	if rec, _ := do(t, h2, http.MethodPost, "/api/v1/wf/instances/"+inst.ID+"/approve", tok1, nil); rec.Code != http.StatusOK {
		t.Fatalf("addsign step: %d", rec.Code)
	}
	_, data = do(t, h2, http.MethodGet, "/api/v1/wf/instances/"+inst.ID, admin1, nil)
	var d struct {
		Instance struct {
			Status string `json:"status"`
		} `json:"instance"`
	}
	_ = json.Unmarshal(data, &d)
	if d.Instance.Status != "approved" {
		t.Fatalf("status = %s", d.Instance.Status)
	}
	// cc 列表恢复
	if rec, _ := do(t, h2, http.MethodGet, "/api/v1/wf/cc", admin1, nil); rec.Code != http.StatusOK {
		t.Fatalf("cc after restart: %d", rec.Code)
	}
}
