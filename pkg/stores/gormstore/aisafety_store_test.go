package gormstore_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/basalt-go/basalt/pkg/modules/aisafety"
	"github.com/basalt-go/basalt/pkg/stores/gormstore"
)

// TestAISafetyStoreSemantics 词库 CRUD/唯一词冲突、策略 UPSERT 与
// 回落读取、开关单行读写。
func TestAISafetyStoreSemantics(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	as := s.AISafetyStore()

	t1 := aisafety.Term{ID: "w1", Word: "机密数据", Category: "涉密", Enabled: true, CreatedAt: time.Now().UTC()}
	if err := as.CreateTerm(ctx, t1); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := as.CreateTerm(ctx, aisafety.Term{ID: "w2", Word: "机密数据"}); !errors.Is(err, aisafety.ErrDupWord) {
		t.Fatalf("dup: %v", err)
	}
	t1.Replacement = "「内部」"
	if err := as.UpdateTerm(ctx, t1); err != nil {
		t.Fatalf("update: %v", err)
	}
	// 改词撞已有词 / 不存在优先报 NotFound（与 MemStore 语义一致）
	if err := as.UpdateTerm(ctx, aisafety.Term{ID: "w9", Word: "机密数据"}); !errors.Is(err, aisafety.ErrNotFound) {
		t.Fatalf("update missing should 404: %v", err)
	}
	terms, _ := as.ListTerms(ctx)
	if len(terms) != 1 || terms[0].Replacement != "「内部」" {
		t.Fatalf("terms = %+v", terms)
	}

	// 策略 UPSERT 与回落
	if err := as.SetPolicy(ctx, aisafety.Policy{TenantID: "", Action: "mark", UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("set default: %v", err)
	}
	if err := as.SetPolicy(ctx, aisafety.Policy{TenantID: "", Action: "replace", UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("upsert default: %v", err)
	}
	p, err := as.GetPolicy(ctx, "")
	if err != nil || p.Action != "replace" {
		t.Fatalf("default policy = %+v %v", p, err)
	}
	if _, err := as.GetPolicy(ctx, "tenant-x"); !errors.Is(err, aisafety.ErrNotFound) {
		t.Fatalf("missing policy: %v", err)
	}

	// 开关
	st, err := as.GetSettings(ctx)
	if err != nil || !st.Enabled {
		t.Fatalf("initial settings = %+v %v（默认开启）", st, err)
	}
	if err := as.SaveSettings(ctx, aisafety.Settings{Enabled: false, Updated: time.Now().UTC()}); err != nil {
		t.Fatalf("save settings: %v", err)
	}
	st, _ = as.GetSettings(ctx)
	if st.Enabled {
		t.Fatalf("after save = %+v", st)
	}
	if err := as.DeleteTerm(ctx, "w1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := as.DeleteTerm(ctx, "w1"); !errors.Is(err, aisafety.ErrNotFound) {
		t.Fatalf("delete missing: %v", err)
	}
}

// TestAISafetyRestartRecovery 文件库重启恢复：词库/策略/开关全部
// 延续，引擎重载后审核语义不变。
func TestAISafetyRestartRecovery(t *testing.T) {
	dsn := t.TempDir() + "/aisafety.db"
	ctx := context.Background()

	s1, err := gormstore.Open(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_ = s1.AISafetyStore().CreateTerm(ctx, aisafety.Term{ID: "w1", Word: "违禁词", Enabled: true, CreatedAt: time.Now().UTC()})
	_ = s1.AISafetyStore().SetPolicy(ctx, aisafety.Policy{TenantID: "", Action: "replace", UpdatedAt: time.Now().UTC()})
	_ = s1.AISafetyStore().SaveSettings(ctx, aisafety.Settings{Enabled: true, Updated: time.Now().UTC()})
	if sqlDB, err := s1.DB.DB(); err == nil {
		_ = sqlDB.Close()
	}

	s2, err := gormstore.Open(dsn)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	as := s2.AISafetyStore()
	terms, _ := as.ListTerms(ctx)
	if len(terms) != 1 || terms[0].Word != "违禁词" {
		t.Fatalf("terms after restart = %+v", terms)
	}
	p, err := as.GetPolicy(ctx, "")
	if err != nil || p.Action != "replace" {
		t.Fatalf("policy after restart = %+v %v", p, err)
	}
	st, _ := as.GetSettings(ctx)
	if !st.Enabled {
		t.Fatal("settings after restart disabled")
	}

	// 引擎重载后语义闭环：replace 生效
	e := aisafety.NewEngine()
	if err := e.Reload(ctx, as); err != nil {
		t.Fatalf("reload: %v", err)
	}
	filtered, viol := e.CheckText("", "含违禁词的文本")
	if viol == nil || viol.Action != "replace" || filtered == "含违禁词的文本" {
		t.Fatalf("after restart check = %q %+v", filtered, viol)
	}
}
