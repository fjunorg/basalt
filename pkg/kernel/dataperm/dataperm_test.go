package dataperm

import (
	"context"
	"testing"
)

// TestScopeVisibleRow 行可见性判定矩阵。
func TestScopeVisibleRow(t *testing.T) {
	all := Scope{All: true}
	deptA := Scope{DeptIDs: []string{"dept-a"}}
	selfU1 := Scope{Self: true, SelfUserID: "u1"}

	cases := []struct {
		name        string
		sc          Scope
		dept, owner string
		want        bool
	}{
		{"all 任意可见", all, "any", "any", true},
		{"本部门在集可见", deptA, "dept-a", "u9", true},
		{"部门不在集不可见", deptA, "dept-b", "u9", false},
		{"部门为空不可见", deptA, "", "u9", false},
		{"本人可见", selfU1, "", "u1", true},
		{"他人不可见", selfU1, "", "u2", false},
		{"部门+本人叠加", Scope{DeptIDs: []string{"d1"}, Self: true, SelfUserID: "u1"}, "d1", "u2", true},
		{"部门+本人叠加-跨部门本人", Scope{DeptIDs: []string{"d1"}, Self: true, SelfUserID: "u1"}, "d9", "u1", true},
		{"部门+本人叠加-跨部门他人", Scope{DeptIDs: []string{"d1"}, Self: true, SelfUserID: "u1"}, "d9", "u2", false},
		{"空 scope 行为（未注入约束语义由调用方处理）", Scope{}, "d1", "u1", false},
	}
	for _, tc := range cases {
		if got := tc.sc.VisibleRow(tc.dept, tc.owner); got != tc.want {
			t.Errorf("%s: VisibleRow(%q,%q) = %v, want %v", tc.name, tc.dept, tc.owner, got, tc.want)
		}
	}
}

// TestScopeVisibleDept 部门可见性。
func TestScopeVisibleDept(t *testing.T) {
	sc := Scope{DeptIDs: []string{"a", "b"}}
	if !sc.VisibleDept("a") || !sc.VisibleDept("b") || sc.VisibleDept("c") {
		t.Fatal("dept visibility wrong")
	}
	if !(Scope{All: true}).VisibleDept("anything") {
		t.Fatal("all should see any dept")
	}
}

// TestValidScope 档位校验。
func TestValidScope(t *testing.T) {
	for _, ok := range []string{"all", "dept", "dept_and_below", "self", "custom"} {
		if !ValidScope(ok) {
			t.Fatalf("%q should be valid", ok)
		}
	}
	for _, bad := range []string{"", "ALL", "dept-and-below", "any"} {
		if ValidScope(bad) {
			t.Fatalf("%q should be invalid", bad)
		}
	}
}

// TestContextRoundtrip 上下文注入往返。
func TestContextRoundtrip(t *testing.T) {
	ctx := context.Background()
	if _, ok := FromContext(ctx); ok {
		t.Fatal("empty ctx should miss")
	}
	sc := Scope{Self: true, SelfUserID: "u1"}
	ctx = WithScope(ctx, sc)
	got, ok := FromContext(ctx)
	if !ok || !got.Self || got.SelfUserID != "u1" {
		t.Fatalf("roundtrip = %+v ok=%v", got, ok)
	}
}

// TestScopeIsEmpty 空约束判定。
func TestScopeIsEmpty(t *testing.T) {
	if !(Scope{}).IsEmpty() {
		t.Fatal("zero scope should be empty")
	}
	if (Scope{All: true}).IsEmpty() || (Scope{Self: true}).IsEmpty() ||
		(Scope{DeptIDs: []string{"a"}}).IsEmpty() {
		t.Fatal("constrained scopes not empty")
	}
}
