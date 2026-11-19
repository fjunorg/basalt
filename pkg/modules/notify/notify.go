// Package notify 是 L1 通知中心模块（竞品 Gap P0#1）：
// 短信 / 邮件 / 站内信三渠道，统一模板变量渲染与发送日志。
//
// 设计要点：
//   - 凭据不落库：SMTP 密码与短信 AccessKey/Secret 经环境变量注入
//     （BASALT_NOTIFY_SMTP_PWD_<账号名大写> /
//     BASALT_NOTIFY_SMS_<渠道名大写>_KEY / _SECRET），沿用 tenant
//     DSN 模式（docs/tenant-isolation.md）
//   - 站内信选独立收件箱（InboxMsg 定向到人）而非复用 notice：
//     notice 是全员公告广播（无收件人模型、无已读态），站内信是
//     点对点消息，语义不同；notice 保持独立演进
//   - 失败语义：不自动重试。发送失败记 SendLog(failed, err)，
//     管理面可见并支持手动重发——自动重试需消息队列与渠道幂等
//     ID（单体阶段引入不成比例）；异步化经 Notifier 接口缝演进
//   - 模板变量 {{key}} 渲染为纯函数；未匹配变量保留原样（排障可见）
package notify

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/basalt-go/basalt/pkg/kernel/crypto/randutil"
)

const Version = "0.1.0"

// 渠道。
const (
	ChannelSMS   = "sms"
	ChannelEmail = "email"
	ChannelInbox = "inbox"
)

// 权限点。
const (
	PermManage = "notify:manage"
	PermList   = "notify:list"
)

var (
	ErrNotFound     = errors.New("notify: not found")
	ErrDupCode      = errors.New("notify: code already taken")
	ErrBadChannel   = errors.New("notify: channel must be sms, email or inbox")
	ErrTplDisabled  = errors.New("notify: template disabled")
	ErrNoSMTP       = errors.New("notify: no smtp account configured")
	ErrNoSMSChan    = errors.New("notify: no sms channel configured")
	ErrBadProvider  = errors.New("notify: unsupported sms provider")
)

// Template 是通知模板（三渠道共用；{{var}} 变量）。
type Template struct {
	ID        string    `json:"id" gorm:"primaryKey;size:40"`
	Code      string    `json:"code" gorm:"uniqueIndex;size:64"`
	Channel   string    `json:"channel" gorm:"size:16;index"`
	Name      string    `json:"name" gorm:"size:128"`
	Subject   string    `json:"subject,omitempty" gorm:"size:255"` // 邮件主题/站内信标题
	Content   string    `json:"content" gorm:"type:text"`
	Status    string    `json:"status" gorm:"size:16"`
	CreatedAt time.Time `json:"created_at"`
}

// 显式表名（跨模块同名实体防冲突，见 agent_definitions 先例）。
func (Template) TableName() string   { return "notify_templates" }
func (SMTPConfig) TableName() string { return "notify_smtp_configs" }
func (SMSChannel) TableName() string { return "notify_sms_channels" }
func (SendLog) TableName() string    { return "notify_logs" }
func (InboxMsg) TableName() string   { return "notify_inbox" }

// SMTPConfig 是 SMTP 账号（密码不落库，经
// BASALT_NOTIFY_SMTP_PWD_<账号名大写> 注入）。
type SMTPConfig struct {
	ID        string    `json:"id" gorm:"primaryKey;size:40"`
	Name      string    `json:"name" gorm:"uniqueIndex;size:64"`
	Host      string    `json:"host" gorm:"size:255"`
	Port      int       `json:"port"`
	Username  string    `json:"username" gorm:"size:128"`
	From      string    `json:"from" gorm:"size:255"`
	StartTLS  bool      `json:"start_tls"`
	CreatedAt time.Time `json:"created_at"`
}

// SMSChannel 是短信渠道配置（Provider 元数据落库；AccessKey/Secret
// 经 BASALT_NOTIFY_SMS_<渠道名大写>_KEY/_SECRET 注入）。
type SMSChannel struct {
	ID        string    `json:"id" gorm:"primaryKey;size:40"`
	Name      string    `json:"name" gorm:"uniqueIndex;size:64"`
	Provider  string    `json:"provider" gorm:"size:32"` // aliyun | tencent
	Sign      string    `json:"sign" gorm:"size:64"`     // 短信签名
	Region    string    `json:"region,omitempty" gorm:"size:32"`
	CreatedAt time.Time `json:"created_at"`
}

// SendLog 是一次发送的不可变记录（sent/failed 双态，支持手动重发）。
type SendLog struct {
	Seq          int64     `json:"-" gorm:"primaryKey;autoIncrement"`
	ID           string    `json:"id" gorm:"size:40;index"`
	Channel      string    `json:"channel" gorm:"size:16;index"`
	Receiver     string    `json:"receiver" gorm:"size:255"`
	TemplateCode string    `json:"template_code" gorm:"size:64"`
	Subject      string    `json:"subject,omitempty" gorm:"size:255"`
	Content      string    `json:"content" gorm:"type:text"` // 渲染后内容（排障）
	Status       string    `json:"status" gorm:"size:16;index"` // sent | failed
	Err          string    `json:"err,omitempty" gorm:"size:512"`
	CreatedAt    time.Time `json:"created_at" gorm:"index"`
}

// InboxMsg 是站内信（定向收件箱，与 notice 公告语义分离）。
type InboxMsg struct {
	ID        string     `json:"id" gorm:"primaryKey;size:40"`
	UserID    string     `json:"user_id" gorm:"size:40;index"`
	Title     string     `json:"title" gorm:"size:255"`
	Content   string     `json:"content" gorm:"type:text"`
	Read      bool       `json:"read" gorm:"index"`
	ReadAt    *time.Time `json:"read_at,omitempty"`
	CreatedAt time.Time  `json:"created_at" gorm:"index"`
}

// Store 抽象通知持久化（内存实现先行，GORM 见 stores/gormstore）。
type Store interface {
	CreateTemplate(ctx context.Context, t Template) error
	UpdateTemplate(ctx context.Context, t Template) error
	DeleteTemplate(ctx context.Context, id string) error
	GetTemplateByCode(ctx context.Context, code string) (Template, error)
	ListTemplates(ctx context.Context) ([]Template, error)

	CreateSMTP(ctx context.Context, c SMTPConfig) error
	DeleteSMTP(ctx context.Context, id string) error
	ListSMTPs(ctx context.Context) ([]SMTPConfig, error)

	CreateSMSChannel(ctx context.Context, c SMSChannel) error
	DeleteSMSChannel(ctx context.Context, id string) error
	ListSMSChannels(ctx context.Context) ([]SMSChannel, error)

	AppendLog(ctx context.Context, l SendLog) error
	ListLogs(ctx context.Context, channel string, offset, limit int) ([]SendLog, int64, error)
	GetLog(ctx context.Context, id string) (SendLog, error)

	CreateInbox(ctx context.Context, m InboxMsg) error
	ListInbox(ctx context.Context, userID string, unreadOnly bool, offset, limit int) ([]InboxMsg, int64, error)
	MarkRead(ctx context.Context, id, userID string) error
	UnreadCount(ctx context.Context, userID string) (int64, error)
}

// ---------- 内存实现 ----------

type MemStore struct {
	mu        sync.RWMutex
	templates map[string]Template
	smtps     map[string]SMTPConfig
	smsChans  map[string]SMSChannel
	logs      []SendLog
	inbox     map[string]InboxMsg
}

func NewMemStore() *MemStore {
	return &MemStore{
		templates: map[string]Template{},
		smtps:     map[string]SMTPConfig{},
		smsChans:  map[string]SMSChannel{},
		inbox:     map[string]InboxMsg{},
	}
}

func (s *MemStore) CreateTemplate(_ context.Context, t Template) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.templates {
		if e.Code == t.Code {
			return ErrDupCode
		}
	}
	s.templates[t.ID] = t
	return nil
}

func (s *MemStore) UpdateTemplate(_ context.Context, t Template) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, e := range s.templates {
		if e.ID == t.ID {
			for id2, e2 := range s.templates {
				if e2.Code == t.Code && id2 != id {
					return ErrDupCode
				}
			}
			s.templates[id] = t
			return nil
		}
	}
	return ErrNotFound
}

func (s *MemStore) DeleteTemplate(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.templates {
		if e.ID == id {
			delete(s.templates, e.Code)
			return nil
		}
	}
	return ErrNotFound
}

func (s *MemStore) GetTemplateByCode(_ context.Context, code string) (Template, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, t := range s.templates {
		if t.Code == code {
			return t, nil
		}
	}
	return Template{}, ErrNotFound
}

func (s *MemStore) ListTemplates(_ context.Context) ([]Template, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Template, 0, len(s.templates))
	for _, t := range s.templates {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out, nil
}

func (s *MemStore) CreateSMTP(_ context.Context, c SMTPConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.smtps {
		if e.Name == c.Name {
			return ErrDupCode
		}
	}
	s.smtps[c.ID] = c
	return nil
}

func (s *MemStore) DeleteSMTP(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.smtps[id]; !ok {
		return ErrNotFound
	}
	delete(s.smtps, id)
	return nil
}

func (s *MemStore) ListSMTPs(_ context.Context) ([]SMTPConfig, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]SMTPConfig, 0, len(s.smtps))
	for _, c := range s.smtps {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *MemStore) CreateSMSChannel(_ context.Context, c SMSChannel) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.smsChans {
		if e.Name == c.Name {
			return ErrDupCode
		}
	}
	s.smsChans[c.ID] = c
	return nil
}

func (s *MemStore) DeleteSMSChannel(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.smsChans[id]; !ok {
		return ErrNotFound
	}
	delete(s.smsChans, id)
	return nil
}

func (s *MemStore) ListSMSChannels(_ context.Context) ([]SMSChannel, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]SMSChannel, 0, len(s.smsChans))
	for _, c := range s.smsChans {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *MemStore) AppendLog(_ context.Context, l SendLog) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logs = append(s.logs, l)
	return nil
}

func (s *MemStore) ListLogs(_ context.Context, channel string, offset, limit int) ([]SendLog, int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var filtered []SendLog
	for i := len(s.logs) - 1; i >= 0; i-- { // 倒序（最新在前）
		if channel == "" || s.logs[i].Channel == channel {
			filtered = append(filtered, s.logs[i])
		}
	}
	total := int64(len(filtered))
	if offset >= len(filtered) {
		return []SendLog{}, total, nil
	}
	end := offset + limit
	if limit <= 0 || end > len(filtered) {
		end = len(filtered)
	}
	return filtered[offset:end], total, nil
}

func (s *MemStore) GetLog(_ context.Context, id string) (SendLog, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, l := range s.logs {
		if l.ID == id {
			return l, nil
		}
	}
	return SendLog{}, ErrNotFound
}

func (s *MemStore) CreateInbox(_ context.Context, m InboxMsg) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inbox[m.ID] = m
	return nil
}

func (s *MemStore) ListInbox(_ context.Context, userID string, unreadOnly bool, offset, limit int) ([]InboxMsg, int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var filtered []InboxMsg
	for _, m := range s.inbox {
		if m.UserID != userID || (unreadOnly && m.Read) {
			continue
		}
		filtered = append(filtered, m)
	}
	sort.Slice(filtered, func(i, j int) bool { return filtered[i].CreatedAt.After(filtered[j].CreatedAt) })
	total := int64(len(filtered))
	if offset >= len(filtered) {
		return []InboxMsg{}, total, nil
	}
	end := offset + limit
	if limit <= 0 || end > len(filtered) {
		end = len(filtered)
	}
	return filtered[offset:end], total, nil
}

func (s *MemStore) MarkRead(_ context.Context, id, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.inbox[id]
	if !ok || m.UserID != userID {
		return ErrNotFound
	}
	if !m.Read {
		now := time.Now().UTC()
		m.Read, m.ReadAt = true, &now
		s.inbox[id] = m
	}
	return nil
}

func (s *MemStore) UnreadCount(_ context.Context, userID string) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var n int64
	for _, m := range s.inbox {
		if m.UserID == userID && !m.Read {
			n++
		}
	}
	return n, nil
}

// Render 渲染模板变量：{{key}} → vars[key]。未匹配变量保留原样
//（排障可见）。纯函数，可直接单测。
func Render(tpl string, vars map[string]string) string {
	if len(vars) == 0 {
		return tpl
	}
	out := tpl
	for k, v := range vars {
		out = strings.ReplaceAll(out, "{{"+k+"}}", v)
	}
	return out
}

func newID() string { return randutil.Hex16() }
