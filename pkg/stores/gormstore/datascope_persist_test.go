package gormstore

import (
	"context"
	"testing"

	"github.com/basalt-go/basalt/pkg/kernel/dataperm"
	"github.com/basalt-go/basalt/pkg/modules/identity"
)

// TestRoleDataScopePersistRestart data_scope/dept_ids 随角色持久化，
// 重启（重开库）后读取一致——数据权限档位跨重启稳定。
func TestRoleDataScopePersistRestart(t *testing.T) {
	dsn := t.TempDir() + "/dpscope.db"

	s1, err := Open(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	role := identity.Role{
		ID: "role-dp-1", Code: "dp-custom", Name: "自定义范围角色",
		Permissions: []string{"document:read"},
		DataScope:   dataperm.ScopeCustom, DeptIDs: []string{"dept-x", "dept-y"},
	}
	if err := s1.RoleStore().Create(context.Background(), role); err != nil {
		t.Fatalf("create: %v", err)
	}
	// 更新档位：custom → dept_and_below（dept_ids 应随 Update 全量覆盖）
	role.DataScope = dataperm.ScopeDeptAndBelow
	role.DeptIDs = nil
	if err := s1.RoleStore().Update(context.Background(), role); err != nil {
		t.Fatalf("update: %v", err)
	}
	if sqlDB, err := s1.DB.DB(); err == nil {
		_ = sqlDB.Close()
	}

	s2, err := Open(dsn)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, err := s2.RoleStore().Get(context.Background(), role.ID)
	if err != nil {
		t.Fatalf("get after restart: %v", err)
	}
	if got.DataScope != dataperm.ScopeDeptAndBelow || got.DeptIDs != nil {
		t.Fatalf("after restart = %+v（档位与部门集应与重启前一致）", got)
	}
	// custom 档字段往返（创建路径的另一种档位再验一遍持久化）
	role2 := identity.Role{
		ID: "role-dp-2", Code: "dp-c2", Name: "n2", DataScope: dataperm.ScopeCustom,
		DeptIDs: []string{"a", "b"}, Permissions: []string{"x"},
	}
	if err := s2.RoleStore().Create(context.Background(), role2); err != nil {
		t.Fatalf("create2: %v", err)
	}
	got2, _ := s2.RoleStore().Get(context.Background(), role2.ID)
	if got2.DataScope != dataperm.ScopeCustom || len(got2.DeptIDs) != 2 || got2.DeptIDs[0] != "a" {
		t.Fatalf("custom roundtrip = %+v", got2)
	}
}

// TestTenantAndDataScopeOrthogonal 租户行级隔离 × 数据权限软范围的
// 叠加语义（交集）：租户边界先裁剪（ListByTenant 只返回本租户行），
// 数据权限在结果集上再裁剪（VisibleRow）。两机制正交组合。
func TestTenantAndDataScopeOrthogonal(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx := context.Background()
	// 租户 A 两个用户（不同部门），租户 B 一个用户
	for _, u := range []identity.User{
		{ID: "u-a1", Username: "a1", DeptID: "dept-rd", TenantID: "t-a"},
		{ID: "u-a2", Username: "a2", DeptID: "dept-mkt", TenantID: "t-a"},
		{ID: "u-b1", Username: "b1", DeptID: "dept-rd", TenantID: "t-b"},
	} {
		if err := st.UserStore().Create(ctx, u); err != nil {
			t.Fatalf("create user: %v", err)
		}
	}
	// 租户硬边界：租户 A 视角只见本租户用户（跨租户 dept-rd 的 b1 不可见）
	tenantRows, _ := st.UserStore().ListByTenant(ctx, "t-a")
	if len(tenantRows) != 2 {
		t.Fatalf("tenant rows = %d, want 2", len(tenantRows))
	}
	// 数据权限软范围：租户 A 结果集上再按部门裁剪（dept 档=研发部）
	sc := dataperm.Scope{DeptIDs: []string{"dept-rd"}}
	var visible int
	for _, u := range tenantRows {
		if sc.VisibleRow(u.DeptID, u.ID) {
			visible++
		}
	}
	if visible != 1 {
		t.Fatalf("tenant ∩ dept scope = %d, want 1（a1；a2 市场部不可见）", visible)
	}
	// all 档不改变租户边界：租户内全可见，跨租户仍不可见
	all := dataperm.Scope{All: true}
	n := 0
	for _, u := range tenantRows {
		if all.VisibleRow(u.DeptID, u.ID) {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("tenant ∩ all = %d, want 2", n)
	}
}
