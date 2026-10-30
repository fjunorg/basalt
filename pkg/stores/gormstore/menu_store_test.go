package gormstore_test

import (
	"context"
	"testing"
	"time"

	"github.com/basalt-go/basalt/pkg/modules/menu"
	"github.com/basalt-go/basalt/pkg/stores/gormstore"
)

// TestMenuStoreSemantics GORM 语义：CRUD/唯一 path/角色绑定读写。
func TestMenuStoreSemantics(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	ms := s.MenuStore()

	m1 := menu.Menu{ID: "m1", Name: "概览", Path: "/overview", Sort: 1, Enabled: true, CreatedAt: time.Now().UTC()}
	if err := ms.Create(ctx, m1); err != nil {
		t.Fatalf("create: %v", err)
	}
	if n, _ := ms.CountByPath(ctx, "/overview", ""); n != 1 {
		t.Fatalf("count by path = %d", n)
	}
	if n, _ := ms.CountByPath(ctx, "/overview", "m1"); n != 0 {
		t.Fatalf("count exclude self = %d", n)
	}
	m1.Name = "仪表盘"
	if err := ms.Update(ctx, m1); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, _ := ms.Get(ctx, "m1")
	if got.Name != "仪表盘" {
		t.Fatalf("after update: %+v", got)
	}
	if _, err := ms.Get(ctx, "ghost"); err == nil {
		t.Fatal("missing get should fail")
	}

	// 角色绑定
	if err := ms.SetRoleMenus(ctx, "r1", []string{"m1"}); err != nil {
		t.Fatalf("set role menus: %v", err)
	}
	ids, err := ms.GetRoleMenus(ctx, "r1")
	if err != nil || len(ids) != 1 || ids[0] != "m1" {
		t.Fatalf("role menus = %v %v", ids, err)
	}
	if n, _ := ms.BoundRoleCount(ctx, "m1"); n != 1 {
		t.Fatalf("bound count = %d", n)
	}
	// 全量替换
	_ = ms.SetRoleMenus(ctx, "r1", []string{})
	if ids, _ := ms.GetRoleMenus(ctx, "r1"); len(ids) != 0 {
		t.Fatalf("after replace = %v", ids)
	}
	// 未绑定角色 → 空集（nil 区别于错误）
	if ids, err := ms.GetRoleMenus(ctx, "r-none"); err != nil || ids != nil {
		t.Fatalf("unbound = %v %v", ids, err)
	}
	if err := ms.Delete(ctx, "m1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
}

// TestMenuRestartRecovery 重启恢复：菜单与角色绑定延续，幂等播种
// 不覆盖既有数据。
func TestMenuRestartRecovery(t *testing.T) {
	dsn := t.TempDir() + "/menu.db"
	ctx := context.Background()

	s1, err := gormstore.Open(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_ = s1.MenuStore().Create(ctx, menu.Menu{ID: "m1", Name: "自定义", Path: "/custom", Enabled: true, CreatedAt: time.Now().UTC()})
	_ = s1.MenuStore().SetRoleMenus(ctx, "r1", []string{"m1"})
	if sqlDB, err := s1.DB.DB(); err == nil {
		_ = sqlDB.Close()
	}

	s2, err := gormstore.Open(dsn)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	all, _ := s2.MenuStore().List(ctx)
	if len(all) != 1 || all[0].Path != "/custom" {
		t.Fatalf("menus after restart = %+v", all)
	}
	ids, _ := s2.MenuStore().GetRoleMenus(ctx, "r1")
	if len(ids) != 1 {
		t.Fatalf("binding after restart = %v", ids)
	}
	// 模块 Start 幂等播种：已有数据 → 不再播种（list 仍 1）
	m := menuModForStore(s2.MenuStore())
	if err := m.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	all, _ = s2.MenuStore().List(ctx)
	if len(all) != 1 {
		t.Fatalf("seeding must not override existing: %d", len(all))
	}
}

func menuModForStore(s menu.Store) *menu.Module {
	return menu.NewWithStore(s)
}
