package gormstore

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/basalt-go/basalt/pkg/modules/oplog"
)

// OpLogStore 操作/API 日志持久化（BASALT_OPLOG_PERSIST=sqlite 时启用；
// 默认内存滚动。保留策略交运维按库清理，模块不自动删行）。
type OpLogStore struct{ db *gorm.DB }

func (s *OpLogStore) Append(entries []oplog.Entry) error {
	if len(entries) == 0 {
		return nil
	}
	return s.db.Create(&entries).Error
}

func (s *OpLogStore) Query(f oplog.QueryFilter) (oplog.PageResult, error) {
	if f.Page <= 0 {
		f.Page = 1
	}
	if f.PageSize <= 0 {
		f.PageSize = 20
	}
	if f.PageSize > 100 {
		f.PageSize = 100
	}
	q := s.db.Model(&oplog.Entry{})
	if f.Module != "" {
		q = q.Where("module = ?", f.Module)
	}
	if f.UserID != "" {
		q = q.Where("user_id = ?", f.UserID)
	}
	if !f.Since.IsZero() {
		q = q.Where("time >= ?", f.Since)
	}
	if !f.Until.IsZero() {
		q = q.Where("time <= ?", f.Until)
	}
	switch f.StatusClass {
	case "2xx":
		q = q.Where("status >= 200 AND status < 300")
	case "4xx":
		q = q.Where("status >= 400 AND status < 500")
	case "5xx":
		q = q.Where("status >= 500")
	case "other":
		q = q.Where("(status < 200 OR (status >= 300 AND status < 400))")
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return oplog.PageResult{}, err
	}
	var rows []oplog.Entry
	err := q.Order("time DESC").Order("id DESC").
		Limit(f.PageSize).Offset((f.Page - 1) * f.PageSize).
		Find(&rows).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return oplog.PageResult{}, err
	}
	return oplog.PageResult{Rows: rows, Total: int(total), Page: f.Page, PageSize: f.PageSize}, nil
}

// CleanupBefore 按时间清理（运维定时任务可调；幂等）。
func (s *OpLogStore) CleanupBefore(_ context.Context, t time.Time) (int64, error) {
	res := s.db.Where("time < ?", t).Delete(&oplog.Entry{})
	return res.RowsAffected, res.Error
}
