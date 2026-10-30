package gormstore

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"github.com/basalt-go/basalt/pkg/modules/notice"
)

// NoticeStore 实现 notice.Store（GORM，任意方言）。
type NoticeStore struct{ db *gorm.DB }

// 编译期接口对齐检查。
var _ notice.Store = (*NoticeStore)(nil)

func (s *NoticeStore) Create(_ context.Context, n notice.Notice) error {
	return s.db.Create(&n).Error
}

func (s *NoticeStore) Update(_ context.Context, n notice.Notice) error {
	res := s.db.Model(&notice.Notice{}).Where("id = ?", n.ID).
		Select("Title", "Content", "Type", "Status", "CreatorID", "CreatedAt", "PublishedAt").
		Updates(n)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return notice.ErrNotFound
	}
	return nil
}

func (s *NoticeStore) Delete(_ context.Context, id string) error {
	res := s.db.Delete(&notice.Notice{}, "id = ?", id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return notice.ErrNotFound
	}
	return nil
}

func (s *NoticeStore) Get(_ context.Context, id string) (notice.Notice, error) {
	var n notice.Notice
	err := s.db.First(&n, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return notice.Notice{}, notice.ErrNotFound
	}
	return n, err
}

// List 按创建时间倒序；publishedOnly 时仅已发布（与 MemStore 语义一致）。
func (s *NoticeStore) List(_ context.Context, publishedOnly bool) ([]notice.Notice, error) {
	q := s.db.Order("created_at DESC")
	if publishedOnly {
		q = q.Where("status = ?", notice.StatusPublished)
	}
	var out []notice.Notice
	err := q.Find(&out).Error
	return list(out), err
}
