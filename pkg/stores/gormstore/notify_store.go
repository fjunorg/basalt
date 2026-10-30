package gormstore

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/basalt-go/basalt/pkg/modules/notify"
)

// NotifyStore 实现 notify.Store（GORM，任意方言）。表名经实体
// TableName() 显式声明（notify_* 前缀，防跨模块同名冲突）。
type NotifyStore struct{ db *gorm.DB }

// 编译期接口对齐检查。
var _ notify.Store = (*NotifyStore)(nil)

func (s *NotifyStore) CreateTemplate(_ context.Context, t notify.Template) error {
	var cnt int64
	if err := s.db.Model(&notify.Template{}).Where("code = ?", t.Code).Count(&cnt).Error; err != nil {
		return err
	}
	if cnt > 0 {
		return notify.ErrDupCode
	}
	return s.db.Create(&t).Error
}

func (s *NotifyStore) UpdateTemplate(_ context.Context, t notify.Template) error {
	res := s.db.Model(&notify.Template{}).Where("id = ?", t.ID).
		Select("Code", "Channel", "Name", "Subject", "Content", "Status").
		Updates(t)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return notify.ErrNotFound
	}
	return nil
}

func (s *NotifyStore) DeleteTemplate(_ context.Context, id string) error {
	res := s.db.Delete(&notify.Template{}, "id = ?", id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return notify.ErrNotFound
	}
	return nil
}

func (s *NotifyStore) GetTemplateByCode(_ context.Context, code string) (notify.Template, error) {
	var t notify.Template
	err := s.db.First(&t, "code = ?", code).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return notify.Template{}, notify.ErrNotFound
	}
	return t, err
}

func (s *NotifyStore) ListTemplates(_ context.Context) ([]notify.Template, error) {
	var out []notify.Template
	err := s.db.Order("code").Find(&out).Error
	return out, err
}

func (s *NotifyStore) CreateSMTP(_ context.Context, c notify.SMTPConfig) error {
	var cnt int64
	if err := s.db.Model(&notify.SMTPConfig{}).Where("name = ?", c.Name).Count(&cnt).Error; err != nil {
		return err
	}
	if cnt > 0 {
		return notify.ErrDupCode
	}
	return s.db.Create(&c).Error
}

func (s *NotifyStore) DeleteSMTP(_ context.Context, id string) error {
	res := s.db.Delete(&notify.SMTPConfig{}, "id = ?", id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return notify.ErrNotFound
	}
	return nil
}

func (s *NotifyStore) ListSMTPs(_ context.Context) ([]notify.SMTPConfig, error) {
	var out []notify.SMTPConfig
	err := s.db.Order("name").Find(&out).Error
	return out, err
}

func (s *NotifyStore) CreateSMSChannel(_ context.Context, c notify.SMSChannel) error {
	var cnt int64
	if err := s.db.Model(&notify.SMSChannel{}).Where("name = ?", c.Name).Count(&cnt).Error; err != nil {
		return err
	}
	if cnt > 0 {
		return notify.ErrDupCode
	}
	return s.db.Create(&c).Error
}

func (s *NotifyStore) DeleteSMSChannel(_ context.Context, id string) error {
	res := s.db.Delete(&notify.SMSChannel{}, "id = ?", id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return notify.ErrNotFound
	}
	return nil
}

func (s *NotifyStore) ListSMSChannels(_ context.Context) ([]notify.SMSChannel, error) {
	var out []notify.SMSChannel
	err := s.db.Order("name").Find(&out).Error
	return out, err
}

func (s *NotifyStore) AppendLog(_ context.Context, l notify.SendLog) error {
	return s.db.Create(&l).Error
}

func (s *NotifyStore) ListLogs(_ context.Context, channel string, offset, limit int) ([]notify.SendLog, int64, error) {
	q := s.db.Model(&notify.SendLog{})
	if channel != "" {
		q = q.Where("channel = ?", channel)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if limit <= 0 {
		limit = 50
	}
	var out []notify.SendLog
	err := q.Order("seq DESC").Offset(offset).Limit(limit).Find(&out).Error
	return out, total, err
}

func (s *NotifyStore) GetLog(_ context.Context, id string) (notify.SendLog, error) {
	var l notify.SendLog
	err := s.db.First(&l, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return notify.SendLog{}, notify.ErrNotFound
	}
	return l, err
}

func (s *NotifyStore) CreateInbox(_ context.Context, m notify.InboxMsg) error {
	return s.db.Create(&m).Error
}

func (s *NotifyStore) ListInbox(_ context.Context, userID string, unreadOnly bool, offset, limit int) ([]notify.InboxMsg, int64, error) {
	q := s.db.Model(&notify.InboxMsg{}).Where("user_id = ?", userID)
	if unreadOnly {
		q = q.Where("read = ?", false)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if limit <= 0 {
		limit = 50
	}
	var out []notify.InboxMsg
	err := q.Order("created_at DESC").Offset(offset).Limit(limit).Find(&out).Error
	return out, total, err
}

func (s *NotifyStore) MarkRead(_ context.Context, id, userID string) error {
	now := time.Now().UTC()
	res := s.db.Model(&notify.InboxMsg{}).
		Where("id = ? AND user_id = ? AND read = ?", id, userID, false).
		Updates(map[string]any{"read": true, "read_at": now})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		// 幂等：已读重复标记视为成功，不存在才报错
		var cnt int64
		if err := s.db.Model(&notify.InboxMsg{}).Where("id = ? AND user_id = ?", id, userID).Count(&cnt).Error; err == nil && cnt > 0 {
			return nil
		}
		return notify.ErrNotFound
	}
	return nil
}

func (s *NotifyStore) UnreadCount(_ context.Context, userID string) (int64, error) {
	var n int64
	err := s.db.Model(&notify.InboxMsg{}).
		Where("user_id = ? AND read = ?", userID, false).Count(&n).Error
	return n, err
}
