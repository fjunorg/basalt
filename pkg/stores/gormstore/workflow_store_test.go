package gormstore_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/basalt-go/basalt/pkg/modules/workflow"
	"github.com/basalt-go/basalt/pkg/stores/gormstore"
)

func mkInstance(id, creator string, steps []workflow.Step) workflow.Instance {
	return workflow.Instance{
		ID: id, BusinessKey: "bk-" + id, Summary: "测试流程 " + id,
		Steps: steps, CreatorID: creator,
		Status: workflow.StatusPending, CreatedAt: time.Now().UTC(),
	}
}

// TestWorkflowDefStore 覆盖定义 CRUD、唯一 key 冲突与规范化。
func TestWorkflowDefStore(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	ds := s.WorkflowDefStore()

	d := workflow.Definition{
		ID: "def1", Key: "expense", Name: "报销",
		Steps: []workflow.Step{{Approvers: []string{"a", "b"}}}, // Mode 默认会签
	}
	if err := ds.CreateDef(ctx, d); err != nil {
		t.Fatalf("create def: %v", err)
	}
	if err := ds.CreateDef(ctx, workflow.Definition{ID: "def2", Key: "expense", Name: "重复",
		Steps: []workflow.Step{{Approvers: []string{"a"}}}}); !errors.Is(err, workflow.ErrDefKeyUsed) {
		t.Fatalf("dup key: %v", err)
	}

	got, err := ds.GetDefByKey(ctx, "expense")
	if err != nil || got.ID != "def1" || len(got.Steps) != 1 || got.Steps[0].Mode != workflow.ModeCountersign {
		t.Fatalf("by key: %+v %v（Mode 应规范化为默认会签）", got, err)
	}
	if _, err := ds.GetDefByKey(ctx, "nope"); !errors.Is(err, workflow.ErrDefNotFound) {
		t.Fatalf("missing key: %v", err)
	}

	got.Name = "差旅报销"
	got.Status = "disabled"
	if err := ds.UpdateDef(ctx, got); err != nil {
		t.Fatalf("update def: %v", err)
	}
	got, _ = ds.GetDef(ctx, "def1")
	if got.Name != "差旅报销" || got.Status != "disabled" {
		t.Fatalf("after update: %+v", got)
	}
	// 改 key 撞已有 → 冲突
	got.Key = "taken"
	if err := ds.UpdateDef(ctx, workflow.Definition{ID: "other", Key: "taken"}); err == nil {
		t.Fatal("update with foreign id should not pass")
	}

	list, _ := ds.ListDefs(ctx)
	if len(list) != 1 {
		t.Fatalf("defs = %d", len(list))
	}
	if err := ds.DeleteDef(ctx, "def1"); err != nil {
		t.Fatalf("delete def: %v", err)
	}
	if err := ds.DeleteDef(ctx, "def1"); !errors.Is(err, workflow.ErrDefNotFound) {
		t.Fatalf("delete missing: %v", err)
	}
}

// TestWorkflowStoreStateMachine 覆盖会签中间态/或签定局/终态/去重/
// 转办/待办——GORM 实现与 MemStore 共享 ApplyAction 单一事实来源。
func TestWorkflowStoreStateMachine(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	ws := s.WorkflowStore()

	// 两步：步1 会签 [A,B]，步2 或签 [C]
	steps := []workflow.Step{
		{Name: "初审", Approvers: []string{"A", "B"}, Mode: workflow.ModeCountersign},
		{Name: "终审", Approvers: []string{"C"}, Mode: workflow.ModeOrSign},
	}
	if err := ws.Create(ctx, mkInstance("w1", "u1", steps)); err != nil {
		t.Fatalf("create: %v", err)
	}

	// 非审批人拒绝
	if _, err := ws.Act(ctx, "w1", "X", "approve", ""); !errors.Is(err, workflow.ErrNotApprover) {
		t.Fatalf("outsider act: %v", err)
	}
	// 会签中间态：A 批后不推进
	in, err := ws.Act(ctx, "w1", "A", "approve", "ok")
	if err != nil || in.Status != workflow.StatusPending || in.StepIndex != 0 {
		t.Fatalf("after A approve: %+v %v（会签应停留步0）", in, err)
	}
	// A 重复批 → 已处理
	if _, err := ws.Act(ctx, "w1", "A", "approve", ""); !errors.Is(err, workflow.ErrAlreadyDone) {
		t.Fatalf("dup act: %v", err)
	}
	// 中间态待办：B 有、A 无
	todoB, _ := ws.ListTodo(ctx, "B")
	todoA, _ := ws.ListTodo(ctx, "A")
	if len(todoB) != 1 || len(todoA) != 0 {
		t.Fatalf("todo mid-state: B=%d A=%d（应 B=1 A=0）", len(todoB), len(todoA))
	}
	// B 批 → 步1 完成，进入步2
	in, err = ws.Act(ctx, "w1", "B", "approve", "")
	if err != nil || in.Status != workflow.StatusPending || in.StepIndex != 1 {
		t.Fatalf("after B approve: %+v %v（应推进到步1）", in, err)
	}
	// 步2 或签：C 一批定局
	in, err = ws.Act(ctx, "w1", "C", "approve", "")
	if err != nil || in.Status != workflow.StatusApproved || in.FinishedAt == nil {
		t.Fatalf("after C approve: %+v %v（或签一步定局）", in, err)
	}
	// 完结后动作拒绝
	if _, err := ws.Act(ctx, "w1", "C", "approve", ""); !errors.Is(err, workflow.ErrAlreadyDone) {
		t.Fatalf("act on finished: %v", err)
	}
	// 完结实例不出现在待办
	todoC, _ := ws.ListTodo(ctx, "C")
	if len(todoC) != 0 {
		t.Fatalf("todo after finish = %d", len(todoC))
	}
	// 动作历史完整（3 条 approve）
	_, actions, err := ws.Get(ctx, "w1")
	if err != nil || len(actions) != 3 {
		t.Fatalf("actions = %d %v", len(actions), err)
	}
	// 完结可查：ListAll / ListByCreator
	all, _ := ws.ListAll(ctx)
	mine, _ := ws.ListByCreator(ctx, "u1")
	if len(all) != 1 || len(mine) != 1 {
		t.Fatalf("all=%d mine=%d", len(all), len(mine))
	}

	// cancel 仅创建者；终态实例任何人不可 cancel（creator 判定先行）
	_ = ws.Create(ctx, mkInstance("w2", "u2", []workflow.Step{{Approvers: []string{"A"}}}))
	in, err = ws.Act(ctx, "w2", "A", "reject", "不行")
	if err != nil || in.Status != workflow.StatusRejected {
		t.Fatalf("reject: %+v %v", in, err)
	}
	if _, err := ws.Act(ctx, "w2", "A", "cancel", ""); !errors.Is(err, workflow.ErrNotCreator) {
		t.Fatalf("cancel by non-creator: %v", err)
	}
	if _, err := ws.Act(ctx, "w2", "u2", "cancel", ""); !errors.Is(err, workflow.ErrAlreadyDone) {
		t.Fatalf("cancel on finished: %v", err)
	}
	_ = ws.Create(ctx, mkInstance("w3", "u3", []workflow.Step{{Approvers: []string{"A"}}}))
	if _, err := ws.Act(ctx, "w3", "u2", "cancel", ""); !errors.Is(err, workflow.ErrNotCreator) {
		t.Fatalf("cancel by non-creator: %v", err)
	}
	if in, err = ws.Act(ctx, "w3", "u3", "cancel", ""); err != nil || in.Status != workflow.StatusCanceled {
		t.Fatalf("cancel: %+v %v", in, err)
	}

	// Delegate 转办：A 转给 D 后 D 可批、A 不可
	_ = ws.Create(ctx, mkInstance("w4", "u4", []workflow.Step{{Approvers: []string{"A"}, Mode: workflow.ModeOrSign}}))
	in, err = ws.Delegate(ctx, "w4", "A", "D")
	if err != nil || !containsID(in.Approvers, "D") || containsID(in.Approvers, "A") {
		t.Fatalf("delegate: %+v %v", in, err)
	}
	if _, err := ws.Delegate(ctx, "w4", "A", "E"); !errors.Is(err, workflow.ErrNotApprover) {
		t.Fatalf("delegate by non-approver: %v", err)
	}
	if _, err := ws.Delegate(ctx, "w4", "D", "D"); !errors.Is(err, workflow.ErrBadDelegate) {
		t.Fatalf("delegate to existing approver: %v", err)
	}
	if in, err = ws.Act(ctx, "w4", "D", "approve", ""); err != nil || in.Status != workflow.StatusApproved {
		t.Fatalf("approve after delegate: %+v %v", in, err)
	}
	// 历史含 delegate 记录
	_, actions, _ = ws.Get(ctx, "w4")
	foundDelegate := false
	for _, a := range actions {
		if a.Act == "delegate" && a.Comment == "-> D" {
			foundDelegate = true
		}
	}
	if !foundDelegate {
		t.Fatalf("delegate action missing: %+v", actions)
	}

	// Get：不存在映射 ErrNotFound
	if _, _, err := ws.Get(ctx, "ghost"); !errors.Is(err, workflow.ErrNotFound) {
		t.Fatalf("missing get: %v", err)
	}
	// Create 的兼容包装：无 Steps 但给 Approvers → 单步
	if err := ws.Create(ctx, workflow.Instance{ID: "w5", BusinessKey: "bk", Summary: "s",
		Approvers: []string{"A"}, CreatorID: "u", Status: workflow.StatusPending, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("legacy create: %v", err)
	}
	got, _, _ := ws.Get(ctx, "w5")
	if len(got.Steps) != 1 || len(got.Steps[0].Approvers) != 1 {
		t.Fatalf("legacy wrapped: %+v", got)
	}
}

func containsID(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// TestWorkflowRestartRecovery 文件库重启恢复（与其它模块同范式）：
// 在途实例从会签中间态继续推进、待办恢复、定义与完结实例可查。
func TestWorkflowRestartRecovery(t *testing.T) {
	dsn := t.TempDir() + "/workflow.db"
	ctx := context.Background()

	s1, err := gormstore.Open(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// 定义先行（模拟 finance/document 的 Start 注册路径）
	if err := s1.WorkflowDefStore().CreateDef(ctx, workflow.Definition{
		ID: "def1", Key: "expense", Name: "报销",
		Steps: []workflow.Step{
			{Approvers: []string{"A", "B"}}, // 步1 会签
			{Approvers: []string{"C"}},      // 步2 会签（单人）
		},
	}); err != nil {
		t.Fatalf("create def: %v", err)
	}
	ws1 := s1.WorkflowStore()
	if err := ws1.Create(ctx, mkInstance("wr1", "u1", []workflow.Step{
		{Approvers: []string{"A", "B"}, Mode: workflow.ModeCountersign},
		{Approvers: []string{"C"}, Mode: workflow.ModeCountersign},
	})); err != nil {
		t.Fatalf("create instance: %v", err)
	}
	// 停机前：A 批完（步1 中间态），B/C 未动
	if _, err := ws1.Act(ctx, "wr1", "A", "approve", "初审通过"); err != nil {
		t.Fatalf("A approve: %v", err)
	}
	if sqlDB, err := s1.DB.DB(); err == nil {
		_ = sqlDB.Close()
	}

	// 重启：同一 DSN 重开
	s2, err := gormstore.Open(dsn)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	ws2 := s2.WorkflowStore()

	// ① 定义恢复（业务模块 Start 时的 GetDefByKey 可命中，不重复建模板）
	if _, err := s2.WorkflowDefStore().GetDefByKey(ctx, "expense"); err != nil {
		t.Fatalf("def not restored: %v", err)
	}
	// ② 待办恢复：B 有（未批）、A 无（已批）
	todoB, _ := ws2.ListTodo(ctx, "B")
	todoA, _ := ws2.ListTodo(ctx, "A")
	if len(todoB) != 1 || todoB[0].ID != "wr1" || len(todoA) != 0 {
		t.Fatalf("todo after restart: B=%v A=%d", todoB, len(todoA))
	}
	// ③ 动作历史完整
	in, actions, err := ws2.Get(ctx, "wr1")
	if err != nil || len(actions) != 1 || actions[0].ActorID != "A" {
		t.Fatalf("history after restart: %d actions %v", len(actions), err)
	}
	if in.Status != workflow.StatusPending || in.StepIndex != 0 {
		t.Fatalf("mid-state after restart: %+v", in)
	}
	// ④ 中间态继续推进：B 批 → 步1 完成
	if in, err = ws2.Act(ctx, "wr1", "B", "approve", ""); err != nil || in.StepIndex != 1 || in.Status != workflow.StatusPending {
		t.Fatalf("B approve after restart: %+v %v", in, err)
	}
	// ⑤ 步2 推进至终态
	if in, err = ws2.Act(ctx, "wr1", "C", "approve", ""); err != nil || in.Status != workflow.StatusApproved {
		t.Fatalf("C approve after restart: %+v %v", in, err)
	}
	// ⑥ 完结实例可查（Get/ListAll），动作历史含跨重启的 3 条
	_, actions, _ = ws2.Get(ctx, "wr1")
	if len(actions) != 3 {
		t.Fatalf("full history = %d（应 3 条跨重启）", len(actions))
	}
	all, _ := ws2.ListAll(ctx)
	if len(all) != 1 || all[0].Status != workflow.StatusApproved {
		t.Fatalf("finished instance: %+v", all)
	}
}
