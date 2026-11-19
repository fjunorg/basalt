package workflow

import (
	"context"
	"errors"
	"testing"
)

// ---------- 条件网关 ----------

func conditionSteps() []Step {
	return []Step{
		{
			Name: "金额判断",
			Type: StepTypeCondition,
			Rules: []Rule{
				{To: 2, Cond: []Cond{{Field: "amount", Op: "gt", Value: "5000"}}},
				{To: 1}, // 默认分支
			},
		},
		{Name: "主管审批", Approvers: []string{"mgr"}, Mode: ModeCountersign},
		{Name: "总经理审批", Approvers: []string{"gm"}, Mode: ModeCountersign},
	}
}

func TestConditionGateway_BranchByVars(t *testing.T) {
	cases := []struct {
		name   string
		amount string
		want   string
	}{
		{"高额走总经理", "8000", "总经理审批"},
		{"低额走主管", "800", "主管审批"},
		{"相等不命中 gt", "5000", "主管审批"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := NewMemStore()
			defs := NewMemDefStore()
			m := NewWithStores(store, defs)
			if err := defs.CreateDef(context.Background(), Definition{
				ID: "d1", Key: "expense", Name: "报销", Status: "active",
				Steps: conditionSteps(),
			}); err != nil {
				t.Fatal(err)
			}
			in, err := m.StartByDefinitionWithVars(context.Background(),
				"expense", "biz-1", "测试", "creator", nil,
				map[string]string{"amount": tc.amount})
			if err != nil {
				t.Fatal(err)
			}
			step := in.CurrentStep()
			if step == nil || step.Name != tc.want {
				t.Fatalf("current step = %v, want %s", step, tc.want)
			}
		})
	}
}

func TestConditionGateway_ApproverSnapshotResolved(t *testing.T) {
	store := NewMemStore()
	defs := NewMemDefStore()
	m := NewWithStores(store, defs)
	_ = defs.CreateDef(context.Background(), Definition{
		ID: "d1", Key: "expense", Name: "报销", Status: "active",
		Steps: conditionSteps(),
	})
	in, err := m.StartByDefinitionWithVars(context.Background(),
		"expense", "biz-1", "测试", "creator", nil,
		map[string]string{"amount": "9000"})
	if err != nil {
		t.Fatal(err)
	}
	if len(in.Approvers) != 1 || in.Approvers[0] != "gm" {
		t.Fatalf("approvers = %v, want [gm]", in.Approvers)
	}
}

func TestConditionGateway_NoDefaultRejected(t *testing.T) {
	_, err := NormalizeSteps([]Step{
		{Type: StepTypeCondition, Rules: []Rule{
			{To: 1, Cond: []Cond{{Field: "amount", Op: "gt", Value: "10"}}},
		}},
		{Name: "审批", Approvers: []string{"u1"}},
	})
	if !errors.Is(err, ErrBadGateway) {
		t.Fatalf("err = %v, want ErrBadGateway", err)
	}
}

func TestConditionGateway_BackwardTargetRejected(t *testing.T) {
	_, err := NormalizeSteps([]Step{
		{Name: "审批", Approvers: []string{"u1"}},
		{Type: StepTypeCondition, Rules: []Rule{{To: 0}}},
	})
	if !errors.Is(err, ErrBadGateway) {
		t.Fatalf("err = %v, want ErrBadGateway", err)
	}
}

func TestConditionGateway_OperatorSet(t *testing.T) {
	vars := map[string]string{"amount": "120", "dept": "finance", "note": "紧急采购"}
	cases := []struct {
		cond Cond
		want bool
	}{
		{Cond{Field: "amount", Op: "eq", Value: "120"}, true},
		{Cond{Field: "amount", Op: "ne", Value: "120"}, false},
		{Cond{Field: "amount", Op: "ge", Value: "120"}, true},
		{Cond{Field: "amount", Op: "lt", Value: "120"}, false},
		{Cond{Field: "amount", Op: "le", Value: "120"}, true},
		{Cond{Field: "dept", Op: "in", Value: "hr,finance"}, true},
		{Cond{Field: "dept", Op: "in", Value: "hr,legal"}, false},
		{Cond{Field: "note", Op: "contains", Value: "紧急"}, true},
		{Cond{Field: "amount", Op: "gt", Value: "abc"}, false},
		{Cond{Field: "missing", Op: "eq", Value: ""}, true},
	}
	for _, tc := range cases {
		if got := matchOne(tc.cond, vars[tc.cond.Field]); got != tc.want {
			t.Fatalf("%+v = %v, want %v", tc.cond, got, tc.want)
		}
	}
}

// ---------- 并行网关 ----------

func parallelSteps() []Step {
	return []Step{
		{
			Name: "并行会签",
			Type: StepTypeParallel,
			Branches: [][]Step{
				{{Name: "法务", Approvers: []string{"legal"}, Mode: ModeCountersign}},
				{{Name: "财务", Approvers: []string{"fin"}, Mode: ModeCountersign}},
			},
		},
		{Name: "归档", Approvers: []string{"archiver"}, Mode: ModeCountersign},
	}
}

func newParallelInstance(t *testing.T) (*Module, Store, Instance) {
	t.Helper()
	store := NewMemStore()
	defs := NewMemDefStore()
	m := NewWithStores(store, defs)
	if err := defs.CreateDef(context.Background(), Definition{
		ID: "p1", Key: "parallel", Name: "并行流程", Status: "active",
		Steps: parallelSteps(),
	}); err != nil {
		t.Fatal(err)
	}
	in, err := m.StartByDefinition(context.Background(), "parallel", "biz-1", "测试", "creator", nil)
	if err != nil {
		t.Fatal(err)
	}
	return m, store, in
}

func TestParallelGateway_InitBranches(t *testing.T) {
	_, _, in := newParallelInstance(t)
	if in.StepIndex != 0 || len(in.Branches) != 2 {
		t.Fatalf("stepIndex=%d branches=%d", in.StepIndex, len(in.Branches))
	}
	if len(in.Approvers) != 2 {
		t.Fatalf("merged approvers = %v", in.Approvers)
	}
}

func TestParallelGateway_BranchProgressAndJoin(t *testing.T) {
	_, store, in := newParallelInstance(t)
	ctx := context.Background()

	// 法务分支通过：仅该分支完成，流程仍停在并行步
	got, err := store.ActBranch(ctx, in.ID, "legal", "approve", "ok", 0)
	if err != nil {
		t.Fatal(err)
	}
	if got.StepIndex != 0 || !got.Branches[0].Done || got.Branches[1].Done {
		t.Fatalf("after legal: %+v", got)
	}

	// 财务分支通过：全部完成 → 进入归档步
	got, err = store.ActBranch(ctx, in.ID, "fin", "approve", "ok", 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.StepIndex != 1 || len(got.Branches) != 0 {
		t.Fatalf("after join: stepIndex=%d branches=%v", got.StepIndex, got.Branches)
	}
	if step := got.CurrentStep(); step == nil || step.Name != "归档" {
		t.Fatalf("current step = %v", step)
	}

	// 末步通过 → 终态
	got, err = store.Act(ctx, in.ID, "archiver", "approve", "ok")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusApproved {
		t.Fatalf("status = %s", got.Status)
	}
}

func TestParallelGateway_BranchInferredFromActor(t *testing.T) {
	_, store, in := newParallelInstance(t)
	ctx := context.Background()
	// 未传 branch 时按操作者推断所属分支
	got, err := store.Act(ctx, in.ID, "legal", "approve", "ok")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Branches[0].Done {
		t.Fatalf("branch0 = %+v", got.Branches[0])
	}
	// 非本流程审批人 → ErrNotApprover
	_, err = store.Act(ctx, in.ID, "nobody", "approve", "ok")
	if !errors.Is(err, ErrNotApprover) {
		t.Fatalf("err = %v, want ErrNotApprover", err)
	}
}

func TestParallelGateway_InvalidBranchIndex(t *testing.T) {
	_, store, in := newParallelInstance(t)
	_, err := store.ActBranch(context.Background(), in.ID, "legal", "approve", "ok", 9)
	if !errors.Is(err, ErrBadBranch) {
		t.Fatalf("err = %v, want ErrBadBranch", err)
	}
}

func TestParallelGateway_BranchApproverScoped(t *testing.T) {
	_, store, in := newParallelInstance(t)
	// 财务人员不能替法务分支审批
	_, err := store.ActBranch(context.Background(), in.ID, "fin", "approve", "ok", 0)
	if !errors.Is(err, ErrNotApprover) {
		t.Fatalf("err = %v, want ErrNotApprover", err)
	}
}

func TestParallelGateway_MultiStepBranchSequential(t *testing.T) {
	store := NewMemStore()
	defs := NewMemDefStore()
	m := NewWithStores(store, defs)
	_ = defs.CreateDef(context.Background(), Definition{
		ID: "p2", Key: "nested", Name: "分支多步", Status: "active",
		Steps: []Step{{
			Name: "并行",
			Type: StepTypeParallel,
			Branches: [][]Step{
				{{Name: "法务初审", Approvers: []string{"l1"}}, {Name: "法务终审", Approvers: []string{"l2"}}},
				{{Name: "财务", Approvers: []string{"f1"}}},
			},
		}},
	})
	in, err := m.StartByDefinition(context.Background(), "nested", "biz-1", "测试", "creator", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := store.ActBranch(ctx, in.ID, "l1", "approve", "", 0); err != nil {
		t.Fatal(err)
	}
	// 分支内推进到第二个子步（l2），实例仍停在并行步
	got, _, _ := store.Get(ctx, in.ID)
	if got.Branches[0].StepIndex != 1 || got.Branches[0].Done {
		t.Fatalf("branch0 = %+v", got.Branches[0])
	}
	if sub := got.BranchStep(&got.Branches[0]); sub == nil || sub.Name != "法务终审" {
		t.Fatalf("branch0 sub = %v", sub)
	}
	if _, err := store.ActBranch(ctx, in.ID, "l2", "approve", "", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ActBranch(ctx, in.ID, "f1", "approve", "", 1); err != nil {
		t.Fatal(err)
	}
	got, _, _ = store.Get(ctx, in.ID)
	if got.Status != StatusApproved {
		t.Fatalf("status = %s", got.Status)
	}
}

func TestParallelGateway_RejectEndsInstance(t *testing.T) {
	_, store, in := newParallelInstance(t)
	got, err := store.ActBranch(context.Background(), in.ID, "fin", "reject", "不通过", 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusRejected {
		t.Fatalf("status = %s", got.Status)
	}
}

func TestParallelGateway_NestedParallelRejected(t *testing.T) {
	_, err := NormalizeSteps([]Step{{
		Type: StepTypeParallel,
		Branches: [][]Step{{
			{Type: StepTypeParallel, Branches: [][]Step{{{Name: "x", Approvers: []string{"u"}}}}},
		}},
	}})
	if !errors.Is(err, ErrBadGateway) {
		t.Fatalf("err = %v, want ErrBadGateway", err)
	}
}

func TestParallelGateway_TodoScopedToBranch(t *testing.T) {
	_, store, in := newParallelInstance(t)
	ctx := context.Background()

	todo, err := store.ListTodo(ctx, "legal")
	if err != nil || len(todo) != 1 {
		t.Fatalf("legal todo = %d err=%v", len(todo), err)
	}
	if _, err := store.ActBranch(ctx, in.ID, "legal", "approve", "", 0); err != nil {
		t.Fatal(err)
	}
	todo, _ = store.ListTodo(ctx, "legal")
	if len(todo) != 0 {
		t.Fatalf("legal todo after approve = %d", len(todo))
	}
	todo, _ = store.ListTodo(ctx, "fin")
	if len(todo) != 1 {
		t.Fatalf("fin todo = %d", len(todo))
	}
}
