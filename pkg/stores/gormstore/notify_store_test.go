package gormstore_test

import (
	"context"
	"testing"
	"time"

	"github.com/basalt-go/basalt/pkg/modules/notify"
	"github.com/basalt-go/basalt/pkg/stores/gormstore"
)

// TestNotifyStoreSemantics GORM 语义：模板唯一 code、日志倒序分页、
// 收件箱未读过滤与跨用户隔离。
func TestNotifyStoreSemantics(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	ns := s.NotifyStore()

	tpl := notify.Template{ID: "t1", Code: "welcome", Channel: notify.ChannelInbox,
		Subject: "欢迎", Content: "你好 {{name}}", Status: "active", CreatedAt: time.Now().UTC()}
	if err := ns.CreateTemplate(ctx, tpl); err != nil {
		t.Fatalf("create tpl: %v", err)
	}
	if err := ns.CreateTemplate(ctx, notify.Template{ID: "t2", Code: "welcome", Channel: notify.ChannelInbox, Content: "x"}); err == nil {
		t.Fatal("dup code should fail")
	}
	got, err := ns.GetTemplateByCode(ctx, "welcome")
	if err != nil || got.Content != "你好 {{name}}" {
		t.Fatalf("by code: %+v %v", got, err)
	}

	// 日志：倒序（最新在前）+ 渠道过滤 + 分页
	for i := 0; i < 5; i++ {
		ch := notify.ChannelEmail
		if i%2 == 0 {
			ch = notify.ChannelSMS
		}
		_ = ns.AppendLog(ctx, notify.SendLog{ID: string(rune('a' + i)), Channel: ch,
			Receiver: "r", TemplateCode: "c", Status: "sent",
			CreatedAt: time.Now().UTC().Add(time.Duration(i) * time.Minute)})
	}
	logs, total, _ := ns.ListLogs(ctx, "", 0, 10)
	if total != 5 || logs[0].ID != "e" || logs[4].ID != "a" {
		t.Fatalf("logs desc = %v total=%d", logs, total)
	}
	smsLogs, smsTotal, _ := ns.ListLogs(ctx, notify.ChannelSMS, 1, 1)
	if smsTotal != 3 || len(smsLogs) != 1 || smsLogs[0].ID != "c" {
		t.Fatalf("sms filter+page = %v total=%d", smsLogs, smsTotal)
	}

	// 收件箱
	_ = ns.CreateInbox(ctx, notify.InboxMsg{ID: "m1", UserID: "u1", Title: "a", CreatedAt: time.Now().UTC()})
	_ = ns.CreateInbox(ctx, notify.InboxMsg{ID: "m2", UserID: "u1", Title: "b", CreatedAt: time.Now().UTC()})
	_ = ns.CreateInbox(ctx, notify.InboxMsg{ID: "m3", UserID: "u2", Title: "c", CreatedAt: time.Now().UTC()})
	n, _ := ns.UnreadCount(ctx, "u1")
	if n != 2 {
		t.Fatalf("u1 unread = %d", n)
	}
	if err := ns.MarkRead(ctx, "m1", "u2"); err == nil {
		t.Fatal("cross-user markRead should fail")
	}
	if err := ns.MarkRead(ctx, "m1", "u1"); err != nil {
		t.Fatalf("markRead: %v", err)
	}
	n, _ = ns.UnreadCount(ctx, "u1")
	if n != 1 {
		t.Fatalf("after read = %d", n)
	}
	msgs, total, _ := ns.ListInbox(ctx, "u1", true, 0, 10)
	if total != 1 || msgs[0].ID != "m2" {
		t.Fatalf("unread only = %v total=%d", msgs, total)
	}
}

// TestNotifyRestartRecovery 文件库重启恢复：模板/日志/收件箱延续。
func TestNotifyRestartRecovery(t *testing.T) {
	dsn := t.TempDir() + "/notify.db"
	ctx := context.Background()

	s1, err := gormstore.Open(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ns1 := s1.NotifyStore()
	_ = ns1.CreateTemplate(ctx, notify.Template{ID: "t1", Code: "welcome", Channel: notify.ChannelInbox,
		Subject: "欢迎", Content: "hi {{name}}", Status: "active", CreatedAt: time.Now().UTC()})
	_ = ns1.AppendLog(ctx, notify.SendLog{ID: "L1", Channel: notify.ChannelSMS, Receiver: "138",
		TemplateCode: "welcome", Status: "sent", CreatedAt: time.Now().UTC()})
	_ = ns1.CreateInbox(ctx, notify.InboxMsg{ID: "m1", UserID: "u1", Title: "欢迎", Content: "hi", CreatedAt: time.Now().UTC()})
	if sqlDB, err := s1.DB.DB(); err == nil {
		_ = sqlDB.Close()
	}

	s2, err := gormstore.Open(dsn)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	ns2 := s2.NotifyStore()
	if _, err := ns2.GetTemplateByCode(ctx, "welcome"); err != nil {
		t.Fatalf("template not restored: %v", err)
	}
	logs, total, _ := ns2.ListLogs(ctx, notify.ChannelSMS, 0, 10)
	if total != 1 || logs[0].ID != "L1" {
		t.Fatalf("logs after restart: %v total=%d", logs, total)
	}
	n, _ := ns2.UnreadCount(ctx, "u1")
	if n != 1 {
		t.Fatalf("inbox after restart unread = %d", n)
	}
	if err := ns2.MarkRead(ctx, "m1", "u1"); err != nil {
		t.Fatalf("markRead after restart: %v", err)
	}
}
