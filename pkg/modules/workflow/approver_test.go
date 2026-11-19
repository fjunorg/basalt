package workflow

import (
	"context"
	"errors"
	"testing"
)

// mockResolver 实现 ApproverResolver 用于单测。
type mockResolver struct {
	roleUsers  map[string][]string // roleCode → userIDs
	deptHeads  map[string]string   // deptID → leaderID
	deptParent map[string]string   // deptID → parentDeptID
	userDept   map[string]string   // userID → deptID
	deptUsers  map[string][]string // deptID → userIDs (with role filter applied externally)
}

func (r *mockResolver) UsersByRole(_ context.Context, code string) ([]string, error) {
	return r.roleUsers[code], nil
}

func (r *mockResolver) DeptHead(_ context.Context, deptID string, level int) (string, error) {
	current := deptID
	for i := 0; i < level; i++ {
		head, ok := r.deptHeads[current]
		if !ok {
			return "", nil
		}
		if i == level-1 {
			return head, nil
		}
		parent, ok := r.deptParent[current]
		if !ok {
			return "", nil
		}
		current = parent
	}
	return "", nil
}

func (r *mockResolver) UsersByRoleInDept(_ context.Context, deptID, code string) ([]string, error) {
	deptUsers := r.deptUsers[deptID]
	roleUsers := r.roleUsers[code]
	roleSet := map[string]bool{}
	for _, u := range roleUsers {
		roleSet[u] = true
	}
	var out []string
	for _, u := range deptUsers {
		if roleSet[u] {
			out = append(out, u)
		}
	}
	return out, nil
}

func (r *mockResolver) UserDept(_ context.Context, userID string) (string, error) {
	return r.userDept[userID], nil
}

func TestResolveApprovers_UserScheme(t *testing.T) {
	r := &mockResolver{}
	ids, err := ResolveApprovers(context.Background(), "user:u1,user:u2", "creator", r)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != "u1" || ids[1] != "u2" {
		t.Fatalf("ids = %v", ids)
	}
}

func TestResolveApprovers_BareID_BackwardCompat(t *testing.T) {
	r := &mockResolver{}
	ids, err := ResolveApprovers(context.Background(), "u1,u2", "creator", r)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 {
		t.Fatalf("ids = %v", ids)
	}
}

func TestResolveApprovers_RoleScheme(t *testing.T) {
	r := &mockResolver{roleUsers: map[string][]string{"manager": {"m1", "m2"}}}
	ids, err := ResolveApprovers(context.Background(), "role:manager", "creator", r)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 {
		t.Fatalf("ids = %v", ids)
	}
}

func TestResolveApprovers_DeptHead(t *testing.T) {
	r := &mockResolver{
		userDept:  map[string]string{"creator": "d1"},
		deptHeads: map[string]string{"d1": "boss1", "d0": "ceo"},
		deptParent: map[string]string{"d1": "d0"},
	}
	// level=1: 本部门负责人
	ids, err := ResolveApprovers(context.Background(), "dept-head", "creator", r)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != "boss1" {
		t.Fatalf("ids = %v", ids)
	}
	// level=2: 父部门负责人
	ids, err = ResolveApprovers(context.Background(), "dept-head:2", "creator", r)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != "ceo" {
		t.Fatalf("ids = %v", ids)
	}
}

func TestResolveApprovers_RoleInDept(t *testing.T) {
	r := &mockResolver{
		userDept:  map[string]string{"creator": "d1"},
		deptUsers: map[string][]string{"d1": {"u1", "u2", "u3"}},
		roleUsers: map[string][]string{"finance": {"u2", "u3", "u4"}},
	}
	ids, err := ResolveApprovers(context.Background(), "role-in-dept:finance", "creator", r)
	if err != nil {
		t.Fatal(err)
	}
	// u2, u3 在 d1 且有 finance 角色；u4 有角色但不在 d1；u1 在 d1 但没角色
	if len(ids) != 2 {
		t.Fatalf("ids = %v", ids)
	}
}

func TestResolveApprovers_Mixed(t *testing.T) {
	r := &mockResolver{
		userDept:  map[string]string{"creator": "d1"},
		deptHeads: map[string]string{"d1": "boss1"},
		roleUsers: map[string][]string{"audit": {"a1"}},
	}
	ids, err := ResolveApprovers(context.Background(), "dept-head,role:audit,user:extra", "creator", r)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 3 {
		t.Fatalf("ids = %v", ids)
	}
}

func TestResolveApprovers_Dedup(t *testing.T) {
	r := &mockResolver{roleUsers: map[string][]string{"r1": {"u1", "u2"}}}
	ids, err := ResolveApprovers(context.Background(), "user:u1,role:r1", "creator", r)
	if err != nil {
		t.Fatal(err)
	}
	// u1 去重
	if len(ids) != 2 {
		t.Fatalf("ids = %v (should dedup)", ids)
	}
}

func TestResolveApprovers_EmptyExpr(t *testing.T) {
	ids, err := ResolveApprovers(context.Background(), "", "creator", nil)
	if err != nil || ids != nil {
		t.Fatalf("empty expr should return nil, nil; got %v, %v", ids, err)
	}
}

func TestResolveApprovers_EmptyResult_Error(t *testing.T) {
	r := &mockResolver{roleUsers: map[string][]string{}}
	_, err := ResolveApprovers(context.Background(), "role:nonexistent", "creator", r)
	if !errors.Is(err, ErrBadApproverExpr) {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveApprovers_CreatorNoDept_Error(t *testing.T) {
	r := &mockResolver{userDept: map[string]string{}}
	_, err := ResolveApprovers(context.Background(), "dept-head", "creator", r)
	if !errors.Is(err, ErrBadApproverExpr) {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveApprovers_UnknownScheme_Error(t *testing.T) {
	r := &mockResolver{}
	_, err := ResolveApprovers(context.Background(), "invalid:foo", "creator", r)
	if !errors.Is(err, ErrBadApproverExpr) {
		t.Fatalf("err = %v", err)
	}
}

func TestNormalizeSteps_AllowsEmptyApproversWithExpr(t *testing.T) {
	steps := []Step{{Name: "审批", ApproverExpr: "dept-head", Mode: ModeCountersign}}
	out, err := NormalizeSteps(steps)
	if err != nil {
		t.Fatalf("NormalizeSteps should allow empty approvers when expr present: %v", err)
	}
	if out[0].ApproverExpr != "dept-head" {
		t.Fatalf("expr lost: %v", out[0])
	}
}

func TestNormalizeSteps_RejectsEmptyApproversWithoutExpr(t *testing.T) {
	steps := []Step{{Name: "审批", Mode: ModeCountersign}}
	_, err := NormalizeSteps(steps)
	if !errors.Is(err, ErrNoApprovers) {
		t.Fatalf("should reject empty approvers without expr: %v", err)
	}
}
