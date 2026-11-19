package workflow

import (
	"context"
	"sync"
	"testing"
)

// mockNotifier 记录通知事件用于断言。
type mockNotifier struct {
	mu     sync.Mutex
	events []NotifyEvent
}

func (n *mockNotifier) Notify(_ context.Context, ev NotifyEvent) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.events = append(n.events, ev)
	return nil
}

func (n *mockNotifier) get() []NotifyEvent {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]NotifyEvent(nil), n.events...)
}

func (n *mockNotifier) reset() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.events = nil
}

func TestNotifyOnStartByDefinition(t *testing.T) {
	store := NewMemStore()
	defs := NewMemDefStore()
	m := NewWithStores(store, defs)
	notifier := &mockNotifier{}
	m.SetNotifier(notifier)

	// 注册带通知的定义
	_ = defs.CreateDef(context.Background(), Definition{
		ID: "def1", Key: "test-flow", Name: "测试流程", Status: "active",
		Steps:  []Step{{Name: "初审", Approvers: []string{"u1"}, Mode: ModeCountersign}},
		Notify: NotifySpec{StepEnter: true},
	})

	// 发起实例
	in, err := m.StartByDefinition(context.Background(), "test-flow", "biz-1", "测试报销", "creator1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if in.Status != StatusPending {
		t.Fatalf("status = %s", in.Status)
	}

	// 断言通知事件
	events := notifier.get()
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	ev := events[0]
	if ev.Type != NotifyStepEnter {
		t.Fatalf("type = %s", ev.Type)
	}
	if len(ev.Recipients) != 1 || ev.Recipients[0] != "u1" {
		t.Fatalf("recipients = %v", ev.Recipients)
	}
	if ev.StepName != "初审" {
		t.Fatalf("step = %s", ev.StepName)
	}
	if ev.CreatorID != "creator1" {
		t.Fatalf("creator = %s", ev.CreatorID)
	}
}

func TestNotifyOnApprove_StepAdvance(t *testing.T) {
	store := NewMemStore()
	defs := NewMemDefStore()
	m := NewWithStores(store, defs)
	notifier := &mockNotifier{}
	m.SetNotifier(notifier)

	_ = defs.CreateDef(context.Background(), Definition{
		ID: "def1", Key: "two-step", Name: "两步流程", Status: "active",
		Steps: []Step{
			{Name: "初审", Approvers: []string{"u1"}, Mode: ModeCountersign},
			{Name: "终审", Approvers: []string{"u2"}, Mode: ModeCountersign},
		},
		Notify: NotifySpec{StepEnter: true, Approved: true},
	})

	in, _ := m.StartByDefinition(context.Background(), "two-step", "biz-1", "测试", "creator1", nil)
	notifier.reset() // 清空发起通知

	// 第一步通过 → 应通知第二步审批人
	_, err := store.Act(context.Background(), in.ID, "u1", "approve", "ok")
	if err != nil {
		t.Fatal(err)
	}
	// 手动触发通知（模拟 module.act 里的 notifyAfterAct）
	updated, _, _ := store.Get(context.Background(), in.ID)
	m.notifyAfterAct(context.Background(), updated, "u1", "approve", "ok", in, nil)

	events := notifier.get()
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1 (step advance)", len(events))
	}
	if events[0].Type != NotifyStepEnter || events[0].StepName != "终审" {
		t.Fatalf("event = %+v", events[0])
	}
	if len(events[0].Recipients) != 1 || events[0].Recipients[0] != "u2" {
		t.Fatalf("recipients = %v", events[0].Recipients)
	}
}

func TestNotifyOnApprove_FinalApproved(t *testing.T) {
	store := NewMemStore()
	defs := NewMemDefStore()
	m := NewWithStores(store, defs)
	notifier := &mockNotifier{}
	m.SetNotifier(notifier)

	_ = defs.CreateDef(context.Background(), Definition{
		ID: "def1", Key: "one-step", Name: "单步流程", Status: "active",
		Steps:  []Step{{Name: "审批", Approvers: []string{"u1"}, Mode: ModeCountersign}},
		Notify: NotifySpec{Approved: true},
	})

	in, _ := m.StartByDefinition(context.Background(), "one-step", "biz-1", "测试", "creator1", nil)
	_, _ = store.Act(context.Background(), in.ID, "u1", "approve", "同意")
	updated, _, _ := store.Get(context.Background(), in.ID)
	m.notifyAfterAct(context.Background(), updated, "u1", "approve", "同意", in, nil)

	events := notifier.get()
	// 过滤 approved 事件
	var approved []NotifyEvent
	for _, ev := range events {
		if ev.Type == NotifyApproved {
			approved = append(approved, ev)
		}
	}
	if len(approved) != 1 {
		t.Fatalf("approved events = %d, want 1", len(approved))
	}
	// 通知发起人
	found := false
	for _, r := range approved[0].Recipients {
		if r == "creator1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("recipients = %v, should include creator1", approved[0].Recipients)
	}
}

func TestNotifyOnReject(t *testing.T) {
	store := NewMemStore()
	defs := NewMemDefStore()
	m := NewWithStores(store, defs)
	notifier := &mockNotifier{}
	m.SetNotifier(notifier)

	_ = defs.CreateDef(context.Background(), Definition{
		ID: "def1", Key: "reject-flow", Name: "驳回测试", Status: "active",
		Steps:  []Step{{Name: "审批", Approvers: []string{"u1"}, Mode: ModeCountersign}},
		Notify: NotifySpec{Rejected: true},
	})

	in, _ := m.StartByDefinition(context.Background(), "reject-flow", "biz-1", "测试", "creator1", nil)
	_, _ = store.Act(context.Background(), in.ID, "u1", "reject", "金额不对")
	updated, _, _ := store.Get(context.Background(), in.ID)
	m.notifyAfterAct(context.Background(), updated, "u1", "reject", "金额不对", in, nil)

	events := notifier.get()
	var rejected []NotifyEvent
	for _, ev := range events {
		if ev.Type == NotifyRejected {
			rejected = append(rejected, ev)
		}
	}
	if len(rejected) != 1 {
		t.Fatalf("rejected events = %d, want 1", len(rejected))
	}
	if rejected[0].Comment != "金额不对" {
		t.Fatalf("comment = %s", rejected[0].Comment)
	}
}

func TestNotifyDisabled_NoEvents(t *testing.T) {
	store := NewMemStore()
	defs := NewMemDefStore()
	m := NewWithStores(store, defs)
	notifier := &mockNotifier{}
	m.SetNotifier(notifier)

	// Notify 零值 = 全部关闭
	_ = defs.CreateDef(context.Background(), Definition{
		ID: "def1", Key: "silent", Name: "静默流程", Status: "active",
		Steps: []Step{{Name: "审批", Approvers: []string{"u1"}, Mode: ModeCountersign}},
	})

	_, _ = m.StartByDefinition(context.Background(), "silent", "biz-1", "测试", "creator1", nil)
	events := notifier.get()
	if len(events) != 0 {
		t.Fatalf("events = %d, want 0 (notify disabled)", len(events))
	}
}

func TestNotifyNilNotifier_NoPanic(t *testing.T) {
	store := NewMemStore()
	defs := NewMemDefStore()
	m := NewWithStores(store, defs)
	// 不设置 notifier

	_ = defs.CreateDef(context.Background(), Definition{
		ID: "def1", Key: "no-notifier", Name: "无通知器", Status: "active",
		Steps:  []Step{{Name: "审批", Approvers: []string{"u1"}, Mode: ModeCountersign}},
		Notify: AllNotify(),
	})

	// 不应 panic
	_, err := m.StartByDefinition(context.Background(), "no-notifier", "biz-1", "测试", "creator1", nil)
	if err != nil {
		t.Fatal(err)
	}
}

func TestNotifySpecSnapshot(t *testing.T) {
	store := NewMemStore()
	defs := NewMemDefStore()
	m := NewWithStores(store, defs)
	notifier := &mockNotifier{}
	m.SetNotifier(notifier)

	_ = defs.CreateDef(context.Background(), Definition{
		ID: "def1", Key: "snap", Name: "快照测试", Status: "active",
		Steps:  []Step{{Name: "审批", Approvers: []string{"u1"}, Mode: ModeCountersign}},
		Notify: NotifySpec{StepEnter: true},
	})

	in, _ := m.StartByDefinition(context.Background(), "snap", "biz-1", "测试", "creator1", nil)
	if !in.Notify.StepEnter {
		t.Fatal("instance should snapshot NotifySpec from definition")
	}

	// 修改定义的通知配置
	_ = defs.UpdateDef(context.Background(), Definition{
		ID: "def1", Key: "snap", Name: "快照测试", Status: "active",
		Steps:  []Step{{Name: "审批", Approvers: []string{"u1"}, Mode: ModeCountersign}},
		Notify: NotifySpec{}, // 全关
	})

	// 已创建的实例仍用旧配置（快照化）
	if !in.Notify.StepEnter {
		t.Fatal("instance NotifySpec should not change after definition update")
	}
}

func TestNotifySpec_shouldNotify(t *testing.T) {
	spec := NotifySpec{StepEnter: true, Approved: false}
	if !spec.shouldNotify(NotifyStepEnter) {
		t.Fatal("step_enter should be enabled")
	}
	if spec.shouldNotify(NotifyApproved) {
		t.Fatal("approved should be disabled")
	}
	if spec.shouldNotify(NotifyRejected) {
		t.Fatal("rejected should be disabled (zero)")
	}
}
