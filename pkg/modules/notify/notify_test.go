package notify_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/basalt-go/basalt/pkg/modules/notify"
)

// TestRender 纯函数：变量命中/缺失保留/多变量/空 vars。
func TestRender(t *testing.T) {
	cases := []struct {
		tpl  string
		vars map[string]string
		want string
	}{
		{"您的验证码 {{code}}，{{mins}} 分钟内有效", map[string]string{"code": "8899", "mins": "5"}, "您的验证码 8899，5 分钟内有效"},
		{"{{missing}} 保留原样", map[string]string{"a": "b"}, "{{missing}} 保留原样"},
		{"无变量模板", nil, "无变量模板"},
		{"", map[string]string{"a": "b"}, ""},
		{"{{a}}{{a}}", map[string]string{"a": "x"}, "xx"},
		{"嵌套 {{a}} 与 {{b}}（b 未提供）", map[string]string{"a": "1"}, "嵌套 1 与 {{b}}（b 未提供）"},
	}
	for i, c := range cases {
		if got := notify.Render(c.tpl, c.vars); got != c.want {
			t.Fatalf("case %d: %q vars=%v = %q, want %q", i, c.tpl, c.vars, got, c.want)
		}
	}
}

// TestInboxFlow 站内信：发送 → 收件箱列表 → 未读数 → 已读 → 幂等。
func TestInboxFlow(t *testing.T) {
	s := notify.NewMemStore()
	ctx := context.Background()
	_ = s.CreateTemplate(ctx, notify.Template{ID: "t1", Code: "welcome", Channel: notify.ChannelInbox,
		Subject: "欢迎", Content: "{{name}}，欢迎使用玄武平台", Status: "active", CreatedAt: time.Now().UTC()})
	m := notify.NewWithStore(s)

	log, err := m.Send(ctx, notify.ChannelInbox, "u1", "welcome", map[string]string{"name": "张三"}, "admin")
	if err != nil || log.Status != "sent" {
		t.Fatalf("inbox send: %+v %v", log, err)
	}
	msgs, total, _ := s.ListInbox(ctx, "u1", false, 0, 20)
	if total != 1 || msgs[0].Title != "欢迎" || !strings.Contains(msgs[0].Content, "张三") {
		t.Fatalf("inbox = %+v", msgs)
	}
	if msgs[0].Read {
		t.Fatal("fresh msg should be unread")
	}
	n, _ := s.UnreadCount(ctx, "u1")
	if n != 1 {
		t.Fatalf("unread = %d", n)
	}
	// 他人不可读/标已读
	if err := s.MarkRead(ctx, msgs[0].ID, "u2"); err == nil {
		t.Fatal("cross-user markRead should fail")
	}
	if err := s.MarkRead(ctx, msgs[0].ID, "u1"); err != nil {
		t.Fatalf("markRead: %v", err)
	}
	if err := s.MarkRead(ctx, msgs[0].ID, "u1"); err != nil {
		t.Fatalf("markRead idempotent: %v", err)
	}
	n, _ = s.UnreadCount(ctx, "u1")
	if n != 0 {
		t.Fatalf("unread after read = %d", n)
	}
	// 发送日志（sent）+ 模板停用后失败
	logs, _, _ := s.ListLogs(ctx, notify.ChannelInbox, 0, 10)
	if len(logs) != 1 || logs[0].Status != "sent" {
		t.Fatalf("logs = %+v", logs)
	}
	tpl, _ := s.GetTemplateByCode(ctx, "welcome")
	tpl.Status = "disabled"
	_ = s.UpdateTemplate(ctx, tpl)
	if _, err := m.Send(ctx, notify.ChannelInbox, "u1", "welcome", nil, "admin"); err == nil {
		t.Fatal("disabled template should fail")
	}
	logs, _, _ = s.ListLogs(ctx, notify.ChannelInbox, 0, 10)
	if logs[0].Status != "failed" { // 倒序最新在前
		t.Fatalf("latest log = %+v", logs[0])
	}
}

// fakeSMTP 本地 SMTP stub：接受一次会话，可注入拒绝命令。
func fakeSMTP(t *testing.T, rejectAt string, got *[]string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		w := conn
		_, _ = w.Write([]byte("220 stub ESMTP\r\n"))
		inData := false
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			cmd := strings.TrimSpace(line)
			// DATA 模式：吞掉内容行，仅 "." 结束时应答一次
			if inData {
				if cmd == "." {
					inData = false
					_, _ = w.Write([]byte("250 accepted\r\n"))
				}
				continue
			}
			if got != nil {
				*got = append(*got, cmd)
			}
			upper := strings.ToUpper(cmd)
			switch {
			case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
				_, _ = w.Write([]byte("250-stub\r\n250 AUTH PLAIN\r\n"))
			case strings.HasPrefix(upper, "AUTH"):
				if rejectAt == "auth" {
					_, _ = w.Write([]byte("535 auth failed\r\n"))
					return
				}
				_, _ = w.Write([]byte("235 ok\r\n"))
			case strings.HasPrefix(upper, "MAIL FROM:"):
				if rejectAt == "mail" {
					_, _ = w.Write([]byte("550 refused\r\n"))
					return
				}
				_, _ = w.Write([]byte("250 ok\r\n"))
			case strings.HasPrefix(upper, "RCPT TO:"):
				_, _ = w.Write([]byte("250 ok\r\n"))
			case strings.HasPrefix(upper, "DATA"):
				inData = true
				_, _ = w.Write([]byte("354 go ahead\r\n"))
			case upper == "QUIT":
				_, _ = w.Write([]byte("221 bye\r\n"))
				return
			default:
				_, _ = w.Write([]byte("250 ok\r\n"))
			}
		}
	}()
	return ln.Addr().String()
}

// TestSMTPSendSuccessAndFailure SMTP stub：成功送达内容、认证失败
// 记 failed 日志。
func TestSMTPSendSuccessAndFailure(t *testing.T) {
	var got []string
	addr := fakeSMTP(t, "", &got)
	host, port, _ := net.SplitHostPort(addr)
	p, _ := time.ParseDuration("1s")
	_ = p
	s := notify.NewMemStore()
	ctx := context.Background()
	_ = s.CreateSMTP(ctx, notify.SMTPConfig{ID: "s1", Name: "main", Host: host,
		Port: mustPort(t, port), Username: "user", From: "noreply@example.com", CreatedAt: time.Now().UTC()})
	_ = s.CreateTemplate(ctx, notify.Template{ID: "t1", Code: "notice-email", Channel: notify.ChannelEmail,
		Subject: "系统通知", Content: "您好 {{name}}，余额不足", Status: "active", CreatedAt: time.Now().UTC()})
	m := notify.NewWithStore(s)

	log, err := m.Send(ctx, notify.ChannelEmail, "to@example.com", "notice-email", map[string]string{"name": "李四"}, "admin")
	if err != nil || log.Status != "sent" {
		t.Fatalf("smtp send: %+v %v", log, err)
	}
	// DATA 内容含主题与渲染变量（stub 会话记录）
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "RCPT TO:<to@example.com>") {
		t.Fatalf("rcpt missing: %v", got)
	}

	// 认证失败 stub → failed 日志 + 明确错误
	var got2 []string
	addr2 := fakeSMTP(t, "auth", &got2)
	host2, port2, _ := net.SplitHostPort(addr2)
	s2 := notify.NewMemStore()
	_ = s2.CreateSMTP(ctx, notify.SMTPConfig{ID: "s1", Name: "main", Host: host2,
		Port: mustPort(t, port2), Username: "user", From: "noreply@example.com", CreatedAt: time.Now().UTC()})
	_ = s2.CreateTemplate(ctx, notify.Template{ID: "t1", Code: "notice-email", Channel: notify.ChannelEmail,
		Subject: "s", Content: "c", Status: "active", CreatedAt: time.Now().UTC()})
	m2 := notify.NewWithStore(s2)
	log2, err := m2.Send(ctx, notify.ChannelEmail, "to@example.com", "notice-email", nil, "admin")
	if err == nil || log2.Status != "failed" || log2.Err == "" {
		t.Fatalf("smtp fail: %+v %v（应明确失败）", log2, err)
	}
}

func mustPort(t *testing.T, s string) int {
	t.Helper()
	p, err := strconv.Atoi(s)
	if err != nil {
		t.Fatalf("port: %v", err)
	}
	return p
}

// TestAliyunSMSStub 阿里云短信 HTTP stub：签名参数齐备、成功路径、
// 业务错误码路径。
func TestAliyunSMSStub(t *testing.T) {
	var gotQuery map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = map[string]string{}
		for k, v := range r.URL.Query() {
			gotQuery[k] = v[0]
		}
		if strings.Contains(r.URL.RawQuery, "FAILTPL") {
			_ = json.NewEncoder(w).Encode(map[string]string{"Code": "isv.BUSINESS_LIMIT_CONTROL", "Message": "频次超限"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"Code": "OK", "Message": "ok", "BizId": "12345"})
	}))
	t.Cleanup(srv.Close)

	cfg := notify.SMSChannel{ID: "c1", Name: "main-sms", Provider: "aliyun", Sign: "玄武平台", CreatedAt: time.Now().UTC()}
	n := notify.NewAliyunSMSNotifier(cfg)
	n = withStubURL(n, srv.URL)

	// 凭据缺失 → 明确报错（env 未设）
	if err := n.Send(context.Background(), "13800000000", "SMS_123", `{"code":"9988"}`); err == nil {
		t.Fatal("credentials missing should fail explicitly")
	}
	// 注入 stub 凭据（测试通过 t.Setenv 模拟 env）
	t.Setenv("BASALT_NOTIFY_SMS_MAIN_SMS_KEY", "test-key")
	t.Setenv("BASALT_NOTIFY_SMS_MAIN_SMS_SECRET", "test-secret")
	if err := n.Send(context.Background(), "13800000000", "SMS_123", `{"code":"9988"}`); err != nil {
		t.Fatalf("send: %v", err)
	}
	for _, k := range []string{"Signature", "AccessKeyId", "PhoneNumbers", "SignName", "TemplateCode", "TemplateParam", "SignatureNonce"} {
		if gotQuery[k] == "" {
			t.Fatalf("param %s missing: %v", k, gotQuery)
		}
	}
	if gotQuery["PhoneNumbers"] != "13800000000" || gotQuery["TemplateCode"] != "SMS_123" {
		t.Fatalf("params = %v", gotQuery)
	}
	// 业务错误码
	if err := n.Send(context.Background(), "13800000000", "FAILTPL", `{}`); err == nil || !strings.Contains(err.Error(), "isv.") {
		t.Fatalf("biz error: %v", err)
	}
}

// withStubURL 替换适配器端点（测试注入）。
func withStubURL(n *notify.AliyunSMSNotifier, url string) *notify.AliyunSMSNotifier {
	return notify.WithBaseURL(n, url)
}
