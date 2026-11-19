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

func (s *NotifyStore) CreateTemplate(ctx context.Context, t notify.Template) error {
	var cnt int64
	if err := s.db.WithContext(ctx).Model(&notify.Template{}).Where("code = ?", t.Code).Count(&cnt).Error; err != nil {
		return err
	}
	if cnt > 0 {
		return notify.ErrDupCode
	}
	return s.db.WithContext(ctx).Create(&t).Error
}

func (s *NotifyStore) UpdateTemplate(ctx context.Context, t notify.Template) error {
	res := s.db.WithContext(ctx).Model(&notify.Template{}).Where("id = ?", t.ID).
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

func (s *NotifyStore) DeleteTemplate(ctx context.Context, id string) error {
	res := s.db.WithContext(ctx).Delete(&notify.Template{}, "id = ?", id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return notify.ErrNotFound
	}
	return nil
}

func (s *NotifyStore) GetTemplateByCode(ctx context.Context, code string) (notify.Template, error) {
	var t notify.Template
	err := s.db.WithContext(ctx).First(&t, "code = ?", code).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return notify.Template{}, notify.ErrNotFound
	}
	return t, err
}

func (s *NotifyStore) ListTemplates(ctx context.Context) ([]notify.Template, error) {
	var out []notify.Template
	err := s.db.WithContext(ctx).Order("code").Find(&out).Error
	return list(out), err
}

func (s *NotifyStore) CreateSMTP(ctx context.Context, c notify.SMTPConfig) error {
	var cnt int64
	if err := s.db.WithContext(ctx).Model(&notify.SMTPConfig{}).Where("name = ?", c.Name).Count(&cnt).Error; err != nil {
		return err
	}
	if cnt > 0 {
		return notify.ErrDupCode
	}
	return s.db.WithContext(ctx).Create(&c).Error
}

func (s *NotifyStore) DeleteSMTP(ctx context.Context, id string) error {
	res := s.db.WithContext(ctx).Delete(&notify.SMTPConfig{}, "id = ?", id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return notify.ErrNotFound
	}
	return nil
}

func (s *NotifyStore) ListSMTPs(ctx context.Context) ([]notify.SMTPConfig, error) {
	var out []notify.SMTPConfig
	err := s.db.WithContext(ctx).Order("name").Find(&out).Error
	return list(out), err
}

func (s *NotifyStore) CreateSMSChannel(ctx context.Context, c notify.SMSChannel) error {
	var cnt int64
	if err := s.db.WithContext(ctx).Model(&notify.SMSChannel{}).Where("name = ?", c.Name).Count(&cnt).Error; err != nil {
		return err
	}
	if cnt > 0 {
		return notify.ErrDupCode
	}
	return s.db.WithContext(ctx).Create(&c).Error
}

func (s *NotifyStore) DeleteSMSChannel(ctx context.Context, id string) error {
	res := s.db.WithContext(ctx).Delete(&notify.SMSChannel{}, "id = ?", id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return notify.ErrNotFound
	}
	return nil
}

func (s *NotifyStore) ListSMSChannels(ctx context.Context) ([]notify.SMSChannel, error) {
	var out []notify.SMSChannel
	err := s.db.WithContext(ctx).Order("name").Find(&out).Error
	return list(out), err
}

func (s *NotifyStore) AppendLog(ctx context.Context, l notify.SendLog) error {
	return s.db.WithContext(ctx).Create(&l).Error
}

func (s *NotifyStore) ListLogs(ctx context.Context, channel string, offset, limit int) ([]notify.SendLog, int64, error) {
	q := s.db.WithContext(ctx).Model(&notify.SendLog{})
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

func (s *NotifyStore) GetLog(ctx context.Context, id string) (notify.SendLog, error) {
	var l notify.SendLog
	err := s.db.WithContext(ctx).First(&l, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return notify.SendLog{}, notify.ErrNotFound
	}
	return l, err
}

func (s *NotifyStore) CreateInbox(ctx context.Context, m notify.InboxMsg) error {
	return s.db.WithContext(ctx).Create(&m).Error
}

func (s *NotifyStore) ListInbox(ctx context.Context, userID string, unreadOnly bool, offset, limit int) ([]notify.InboxMsg, int64, error) {
	q := s.db.WithContext(ctx).Model(&notify.InboxMsg{}).Where("user_id = ?", userID)
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

func (s *NotifyStore) MarkRead(ctx context.Context, id, userID string) error {
	now := time.Now().UTC()
	res := s.db.WithContext(ctx).Model(&notify.InboxMsg{}).
		Where("id = ? AND user_id = ? AND read = ?", id, userID, false).
		Updates(map[string]any{"read": true, "read_at": now})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		// 幂等：已读重复标记视为成功，不存在才报错
		var cnt int64
		if err := s.db.WithContext(ctx).Model(&notify.InboxMsg{}).Where("id = ? AND user_id = ?", id, userID).Count(&cnt).Error; err == nil && cnt > 0 {
			return nil
		}
		return notify.ErrNotFound
	}
	return nil
}

func (s *NotifyStore) UnreadCount(ctx context.Context, userID string) (int64, error) {
	var n int64
	err := s.db.WithContext(ctx).Model(&notify.InboxMsg{}).
		Where("user_id = ? AND read = ?", userID, false).Count(&n).Error
	return n, err
}
