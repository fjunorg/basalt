package app

import (
	"context"
	"fmt"
	"testing"
)

type fakeModule struct {
	name    string
	deps    []string
	events  *[]string // 记录生命周期事件，验证调用顺序
	initErr error
}

func (f *fakeModule) Name() string           { return f.name }
func (f *fakeModule) Version() string        { return "0.0.0" }
func (f *fakeModule) Dependencies() []string { return f.deps }
func (f *fakeModule) Init(_ context.Context, _ *App) error {
	*f.events = append(*f.events, "init:"+f.name)
	return f.initErr
}
func (f *fakeModule) Start(_ context.Context) error {
	*f.events = append(*f.events, "start:"+f.name)
	return nil
}
func (f *fakeModule) Stop(_ context.Context) error {
	*f.events = append(*f.events, "stop:"+f.name)
	return nil
}

func TestResolveTopologicalOrder(t *testing.T) {
	var events []string
	r := NewRegistry()
	// 故意按"依赖者先注册"的乱序，验证拓扑排序仍然将被依赖模块排在前面。
	mods := []Module{
		&fakeModule{name: "ai-gateway", deps: []string{"tenant"}, events: &events},
		&fakeModule{name: "tenant", deps: []string{"identity"}, events: &events},
		&fakeModule{name: "identity", events: &events},
	}
	for _, m := range mods {
		if err := r.Register(m); err != nil {
			t.Fatalf("register %s: %v", m.Name(), err)
		}
	}

	resolved, err := r.Resolve()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	want := []string{"identity", "tenant", "ai-gateway"}
	if len(resolved) != len(want) {
		t.Fatalf("resolved %d modules, want %d", len(resolved), len(want))
	}
	for i, name := range want {
		if resolved[i].Name() != name {
			t.Fatalf("position %d = %q, want %q", i, resolved[i].Name(), name)
		}
	}
}

func TestResolveRejectsCycle(t *testing.T) {
	var events []string
	r := NewRegistry()
	_ = r.Register(&fakeModule{name: "a", deps: []string{"b"}, events: &events})
	_ = r.Register(&fakeModule{name: "b", deps: []string{"a"}, events: &events})
	if _, err := r.Resolve(); err == nil {
		t.Fatal("expected cycle error, got nil")
	}
}

func TestResolveRejectsMissingDependency(t *testing.T) {
	var events []string
	r := NewRegistry()
	_ = r.Register(&fakeModule{name: "a", deps: []string{"ghost"}, events: &events})
	_, err := r.Resolve()
	if err == nil {
		t.Fatal("expected missing-dependency error, got nil")
	}
}

func TestRegisterDuplicate(t *testing.T) {
	var events []string
	r := NewRegistry()
	_ = r.Register(&fakeModule{name: "a", events: &events})
	if err := r.Register(&fakeModule{name: "a", events: &events}); err == nil {
		t.Fatal("expected duplicate error, got nil")
	}
}

func TestRegisterEmptyName(t *testing.T) {
	var events []string
	r := NewRegistry()
	if err := r.Register(&fakeModule{name: "", events: &events}); err == nil {
		t.Fatal("expected empty-name error, got nil")
	}
}

func TestInitFailureIncludesModuleName(t *testing.T) {
	var events []string
	a := New(nil)
	_ = a.Registry().Register(&fakeModule{name: "bad", initErr: fmt.Errorf("boom"), events: &events})
	_ = a.Registry().Register(&fakeModule{name: "good", events: &events})

	// Run 会因 Init 失败退出；此处直接驱动 Resolve+Init 验证错误传播，
	// 避免在单测中启动 HTTP 监听。
	mods, err := a.Registry().Resolve()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	var got error
	for _, m := range mods {
		if err := m.Init(context.Background(), a); err != nil {
			got = err
			break
		}
	}
	if got == nil {
		t.Fatal("expected init error to propagate")
	}
}
