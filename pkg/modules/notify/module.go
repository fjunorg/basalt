package notify

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/basalt-go/basalt/pkg/kernel/app"
	"github.com/basalt-go/basalt/pkg/kernel/web"
	"github.com/basalt-go/basalt/pkg/modules/audit"
	"github.com/basalt-go/basalt/pkg/modules/identity"
)

type Module struct {
	store    Store
	facade   *identity.Facade
	recorder audit.Recorder
}

func New() *Module { return &Module{store: NewMemStore()} }

// NewWithStore 注入持久化实现（GORM 落盘、重启恢复）；
// New() 纯内存路径保留（测试/演示可退回）。
func NewWithStore(s Store) *Module { return &Module{store: s} }

func (*Module) Name() string           { return "notify" }
func (*Module) Version() string        { return Version }
func (*Module) Dependencies() []string { return []string{"identity", "audit"} }

func (*Module) Describe() app.ModuleInfo {
	return app.ModuleInfo{Name: "notify", Version: Version,
		Permissions: []string{PermManage, PermList}}
}

func (m *Module) Init(_ context.Context, a *app.App) error {
	idm, ok := a.Registry().MustGet("identity").(*identity.Module)
	if !ok {
		return errors.New("notify: identity module not available")
	}
	adm, ok := a.Registry().MustGet("audit").(*audit.Module)
	if !ok {
		return errors.New("notify: audit module not available")
	}
	m.facade = idm.Facade()
	m.recorder = adm.Recorder()

	auth := m.facade.Auth
	req := m.facade.Require
	g := a.Router().Group("/api/v1/notify")

	g.Handle(http.MethodPost, "/templates", auth(req(PermManage, m.createTemplate)))
	g.Handle(http.MethodGet, "/templates", auth(req(PermList, m.listTemplates)))
	g.Handle(http.MethodPatch, "/templates/{id}", auth(req(PermManage, m.updateTemplate)))
	g.Handle(http.MethodDelete, "/templates/{id}", auth(req(PermManage, m.deleteTemplate)))

	g.Handle(http.MethodPost, "/smtp-accounts", auth(req(PermManage, m.createSMTP)))
	g.Handle(http.MethodGet, "/smtp-accounts", auth(req(PermList, m.listSMTPs)))
	g.Handle(http.MethodDelete, "/smtp-accounts/{id}", auth(req(PermManage, m.deleteSMTP)))

	g.Handle(http.MethodPost, "/sms-channels", auth(req(PermManage, m.createSMSChannel)))
	g.Handle(http.MethodGet, "/sms-channels", auth(req(PermList, m.listSMSChannels)))
	g.Handle(http.MethodDelete, "/sms-channels/{id}", auth(req(PermManage, m.deleteSMSChannel)))

	g.Handle(http.MethodPost, "/send", auth(req(PermManage, m.send)))
	g.Handle(http.MethodPost, "/resend/{id}", auth(req(PermManage, m.resend)))
	g.Handle(http.MethodGet, "/logs", auth(req(PermList, m.logs)))

	// 站内信收件箱：登录级，仅自己的消息
	g.Handle(http.MethodGet, "/inbox", auth(m.inbox))
	g.Handle(http.MethodGet, "/inbox/unread-count", auth(m.unreadCount))
	g.Handle(http.MethodPost, "/inbox/{id}/read", auth(m.markRead))
	return nil
}

func (*Module) Start(context.Context) error { return nil }
func (*Module) Stop(context.Context) error  { return nil }

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		web.Err(w, http.StatusBadRequest, "invalid json body")
		return false
	}
	return true
}

// ---------- 模板 ----------

func (m *Module) createTemplate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code    string `json:"code"`
		Channel string `json:"channel"`
		Name    string `json:"name"`
		Subject string `json:"subject"`
		Content string `json:"content"`
	}
	if !decode(w, r, &req) || req.Code == "" || req.Content == "" {
		web.Err(w, http.StatusBadRequest, "code and content are required")
		return
	}
	if req.Channel != ChannelSMS && req.Channel != ChannelEmail && req.Channel != ChannelInbox {
		web.Err(w, http.StatusBadRequest, "channel must be sms, email or inbox")
		return
	}
	t := Template{ID: newID(), Code: req.Code, Channel: req.Channel, Name: req.Name,
		Subject: req.Subject, Content: req.Content, Status: "active", CreatedAt: time.Now().UTC()}
	if err := m.store.CreateTemplate(r.Context(), t); err != nil {
		if errors.Is(err, ErrDupCode) {
			web.Err(w, http.StatusConflict, "code already taken")
			return
		}
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.Created(w, t)
}

func (m *Module) listTemplates(w http.ResponseWriter, r *http.Request) {
	ts, err := m.store.ListTemplates(r.Context())
	if err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.OK(w, ts)
}

func (m *Module) updateTemplate(w http.ResponseWriter, r *http.Request) {
	ts, _ := m.store.ListTemplates(r.Context())
	var cur Template
	found := false
	for _, t := range ts {
		if t.ID == r.PathValue("id") {
			cur, found = t, true
			break
		}
	}
	if !found {
		web.Err(w, http.StatusNotFound, "template not found")
		return
	}
	var req struct {
		Name    *string `json:"name"`
		Subject *string `json:"subject"`
		Content *string `json:"content"`
		Status  *string `json:"status"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Name != nil {
		cur.Name = *req.Name
	}
	if req.Subject != nil {
		cur.Subject = *req.Subject
	}
	if req.Content != nil {
		cur.Content = *req.Content
	}
	if req.Status != nil {
		if *req.Status != "active" && *req.Status != "disabled" {
			web.Err(w, http.StatusBadRequest, "status must be active or disabled")
			return
		}
		cur.Status = *req.Status
	}
	if err := m.store.UpdateTemplate(r.Context(), cur); err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.OK(w, cur)
}

func (m *Module) deleteTemplate(w http.ResponseWriter, r *http.Request) {
	if err := m.store.DeleteTemplate(r.Context(), r.PathValue("id")); err != nil {
		if errors.Is(err, ErrNotFound) {
			web.Err(w, http.StatusNotFound, "template not found")
			return
		}
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.OK(w, nil)
}

// ---------- SMTP / 短信渠道 ----------

func (m *Module) createSMTP(w http.ResponseWriter, r *http.Request) {
	var req SMTPConfig
	if !decode(w, r, &req) || req.Name == "" || req.Host == "" || req.Port <= 0 || req.From == "" {
		web.Err(w, http.StatusBadRequest, "name, host, port and from are required")
		return
	}
	req.ID, req.CreatedAt = newID(), time.Now().UTC()
	if err := m.store.CreateSMTP(r.Context(), req); err != nil {
		if errors.Is(err, ErrDupCode) {
			web.Err(w, http.StatusConflict, "name already taken")
			return
		}
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.Created(w, req)
}

func (m *Module) listSMTPs(w http.ResponseWriter, r *http.Request) {
	cs, err := m.store.ListSMTPs(r.Context())
	if err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.OK(w, cs)
}

func (m *Module) deleteSMTP(w http.ResponseWriter, r *http.Request) {
	if err := m.store.DeleteSMTP(r.Context(), r.PathValue("id")); err != nil {
		if errors.Is(err, ErrNotFound) {
			web.Err(w, http.StatusNotFound, "smtp account not found")
			return
		}
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.OK(w, nil)
}

func (m *Module) createSMSChannel(w http.ResponseWriter, r *http.Request) {
	var req SMSChannel
	if !decode(w, r, &req) || req.Name == "" || req.Sign == "" {
		web.Err(w, http.StatusBadRequest, "name and sign are required")
		return
	}
	if req.Provider != "aliyun" && req.Provider != "tencent" {
		web.Err(w, http.StatusBadRequest, "provider must be aliyun or tencent")
		return
	}
	req.ID, req.CreatedAt = newID(), time.Now().UTC()
	if err := m.store.CreateSMSChannel(r.Context(), req); err != nil {
		if errors.Is(err, ErrDupCode) {
			web.Err(w, http.StatusConflict, "name already taken")
			return
		}
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.Created(w, req)
}

func (m *Module) listSMSChannels(w http.ResponseWriter, r *http.Request) {
	cs, err := m.store.ListSMSChannels(r.Context())
	if err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.OK(w, cs)
}

func (m *Module) deleteSMSChannel(w http.ResponseWriter, r *http.Request) {
	if err := m.store.DeleteSMSChannel(r.Context(), r.PathValue("id")); err != nil {
		if errors.Is(err, ErrNotFound) {
			web.Err(w, http.StatusNotFound, "sms channel not found")
			return
		}
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.OK(w, nil)
}

// ---------- 发送编排 ----------

// Send 渲染模板并经渠道适配器发送；结果（含失败原因）记日志与
// 审计链。失败不自动重试（明确失败语义，见包注释），可经 resend
// 手动重发。
func (m *Module) Send(ctx context.Context, channel, receiver, templateCode string, vars map[string]string, actor string) (SendLog, error) {
	log := SendLog{ID: newID(), Channel: channel, Receiver: receiver,
		TemplateCode: templateCode, CreatedAt: time.Now().UTC()}
	fail := func(err error) (SendLog, error) {
		log.Status, log.Err = "failed", err.Error()
		_ = m.store.AppendLog(ctx, log)
		m.audit(ctx, actor, "notify.send."+channel+".failed", log.ID, "err", log.Err)
		return log, err
	}

	tpl, err := m.store.GetTemplateByCode(ctx, templateCode)
	if err != nil {
		return fail(err)
	}
	if tpl.Channel != channel {
		return fail(errors.New("notify: template channel mismatch"))
	}
	if tpl.Status != "active" {
		return fail(ErrTplDisabled)
	}
	content := Render(tpl.Content, vars)
	subject := Render(tpl.Subject, vars)
	log.Subject, log.Content = subject, content

	var sendErr error
	switch channel {
	case ChannelInbox:
		msg := InboxMsg{ID: newID(), UserID: receiver, Title: subject,
			Content: content, CreatedAt: time.Now().UTC()}
		sendErr = m.store.CreateInbox(ctx, msg)
	case ChannelEmail:
		n := &SMTPNotifier{Cfg: m.defaultSMTP()}
		if n.Cfg.ID == "" {
			sendErr = ErrNoSMTP
		} else {
			sendErr = n.Send(ctx, receiver, subject, content)
		}
	case ChannelSMS:
		chanCfg, err := m.defaultSMSChannel(ctx)
		if err != nil {
			sendErr = err
			break
		}
		var n Notifier
		switch chanCfg.Provider {
		case "aliyun":
			n = NewAliyunSMSNotifier(chanCfg)
		case "tencent":
			n = &TencentSMSNotifier{Cfg: chanCfg}
		default:
			sendErr = ErrBadProvider
		}
		if sendErr == nil && n != nil {
			// 短信参数语义见 AliyunSMSNotifier.Send 注释
			sendErr = n.Send(ctx, receiver, tpl.Code, content)
		}
	default:
		sendErr = ErrBadChannel
	}
	if sendErr != nil {
		return fail(sendErr)
	}
	log.Status = "sent"
	_ = m.store.AppendLog(ctx, log)
	m.audit(ctx, actor, "notify.send."+channel+".sent", log.ID, "receiver", receiver)
	return log, nil
}

// defaultSMTP 取列表首个账号（多账号择优随迭代演进）。
func (m *Module) defaultSMTP() SMTPConfig {
	cs, _ := m.store.ListSMTPs(context.Background())
	if len(cs) == 0 {
		return SMTPConfig{}
	}
	return cs[0]
}

// defaultSMSChannel 取列表首个渠道。
func (m *Module) defaultSMSChannel(ctx context.Context) (SMSChannel, error) {
	cs, _ := m.store.ListSMSChannels(ctx)
	if len(cs) == 0 {
		return SMSChannel{}, ErrNoSMSChan
	}
	return cs[0], nil
}

func (m *Module) send(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Channel      string            `json:"channel"`
		Receiver     string            `json:"receiver"`
		TemplateCode string            `json:"template_code"`
		Vars         map[string]string `json:"vars"`
	}
	if !decode(w, r, &req) || req.Channel == "" || req.Receiver == "" || req.TemplateCode == "" {
		web.Err(w, http.StatusBadRequest, "channel, receiver and template_code are required")
		return
	}
	u, uerr := m.facade.User(r)
	actor := ""
	if uerr == nil {
		actor = u.ID
	}
	log, err := m.Send(r.Context(), req.Channel, req.Receiver, req.TemplateCode, req.Vars, actor)
	if err != nil {
		// 业务性失败（模板/渠道/凭据问题）已记日志——返回 422 携带日志 ID 供排障
		web.WriteJSON(w, http.StatusUnprocessableEntity, http.StatusUnprocessableEntity,
			"send failed: "+err.Error()+" (log "+log.ID+")", log)
		return
	}
	web.OK(w, log)
}

// resend 手动重发一条失败日志（明确失败语义的补面）。
func (m *Module) resend(w http.ResponseWriter, r *http.Request) {
	old, err := m.store.GetLog(r.Context(), r.PathValue("id"))
	if err != nil {
		web.Err(w, http.StatusNotFound, "log not found")
		return
	}
	u, uerr := m.facade.User(r)
	actor := ""
	if uerr == nil {
		actor = u.ID
	}
	log, err := m.resendRaw(r.Context(), old, actor)
	if err != nil {
		web.WriteJSON(w, http.StatusUnprocessableEntity, http.StatusUnprocessableEntity,
			"resend failed: "+err.Error(), log)
		return
	}
	web.OK(w, log)
}

// resendRaw 按原日志的渲染结果重发（同渠道同接收者）。
func (m *Module) resendRaw(ctx context.Context, old SendLog, actor string) (SendLog, error) {
	log := SendLog{ID: newID(), Channel: old.Channel, Receiver: old.Receiver,
		TemplateCode: old.TemplateCode, Subject: old.Subject, Content: old.Content,
		CreatedAt: time.Now().UTC()}
	fail := func(err error) (SendLog, error) {
		log.Status, log.Err = "failed", err.Error()
		_ = m.store.AppendLog(ctx, log)
		m.audit(ctx, actor, "notify.resend."+old.Channel+".failed", log.ID, "err", log.Err)
		return log, err
	}
	var sendErr error
	switch old.Channel {
	case ChannelInbox:
		msg := InboxMsg{ID: newID(), UserID: old.Receiver, Title: old.Subject,
			Content: old.Content, CreatedAt: time.Now().UTC()}
		sendErr = m.store.CreateInbox(ctx, msg)
	case ChannelEmail:
		n := &SMTPNotifier{Cfg: m.defaultSMTP()}
		if n.Cfg.ID == "" {
			sendErr = ErrNoSMTP
		} else {
			sendErr = n.Send(ctx, old.Receiver, old.Subject, old.Content)
		}
	case ChannelSMS:
		chanCfg, err := m.defaultSMSChannel(ctx)
		if err != nil {
			sendErr = err
			break
		}
		// 短信重发：TemplateCode 从原日志恢复
		var n Notifier = NewAliyunSMSNotifier(chanCfg)
		if chanCfg.Provider == "tencent" {
			n = &TencentSMSNotifier{Cfg: chanCfg}
		}
		sendErr = n.Send(ctx, old.Receiver, old.TemplateCode, old.Content)
	}
	if sendErr != nil {
		return fail(sendErr)
	}
	log.Status = "sent"
	_ = m.store.AppendLog(ctx, log)
	m.audit(ctx, actor, "notify.resend."+old.Channel+".sent", log.ID, "receiver", old.Receiver)
	return log, nil
}

func (m *Module) logs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	offset, _ := strconv.Atoi(q.Get("offset"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	logs, total, err := m.store.ListLogs(r.Context(), q.Get("channel"), offset, limit)
	if err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.OK(w, map[string]any{"items": logs, "total": total, "offset": offset, "limit": limit})
}

// ---------- 站内信收件箱 ----------

func (m *Module) inbox(w http.ResponseWriter, r *http.Request) {
	u, err := m.facade.User(r)
	if err != nil {
		web.Err(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	q := r.URL.Query()
	offset, _ := strconv.Atoi(q.Get("offset"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	msgs, total, err := m.store.ListInbox(r.Context(), u.ID, q.Get("unread") == "1", offset, limit)
	if err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.OK(w, map[string]any{"items": msgs, "total": total})
}

func (m *Module) unreadCount(w http.ResponseWriter, r *http.Request) {
	u, err := m.facade.User(r)
	if err != nil {
		web.Err(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	n, _ := m.store.UnreadCount(r.Context(), u.ID)
	web.OK(w, map[string]int64{"unread": n})
}

func (m *Module) markRead(w http.ResponseWriter, r *http.Request) {
	u, err := m.facade.User(r)
	if err != nil {
		web.Err(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if err := m.store.MarkRead(r.Context(), r.PathValue("id"), u.ID); err != nil {
		web.Err(w, http.StatusNotFound, "message not found")
		return
	}
	web.OK(w, nil)
}

func (m *Module) audit(ctx context.Context, actor, event, resource string, kv ...any) {
	if m.recorder == nil {
		return
	}
	detail, _ := json.Marshal(map[string]any{"kv": kv})
	_ = m.recorder.Record(ctx, audit.Event{
		Module: "notify", Event: event, ActorID: actor,
		Resource: resource, Detail: detail, Time: time.Now().UTC(),
	})
}
