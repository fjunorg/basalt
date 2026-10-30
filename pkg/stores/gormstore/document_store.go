package gormstore

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"github.com/basalt-go/basalt/pkg/modules/document"
)

// DocumentStore 实现 document.Store（GORM，任意方言）。
type DocumentStore struct{ db *gorm.DB }

// 编译期接口对齐检查。
var _ document.Store = (*DocumentStore)(nil)

func (s *DocumentStore) Create(_ context.Context, d document.Doc) error {
	return s.db.Create(&d).Error
}

func (s *DocumentStore) Update(_ context.Context, d document.Doc) error {
	res := s.db.Model(&document.Doc{}).Where("id = ?", d.ID).
		Select("DocNum", "Type", "Title", "Security", "Urgency", "Correspond",
			"Content", "Status", "CreatorID", "WorkflowInstID").
		Updates(d)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return document.ErrNotFound
	}
	return nil
}

func (s *DocumentStore) Get(_ context.Context, id string) (document.Doc, error) {
	var d document.Doc
	err := s.db.First(&d, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return document.Doc{}, document.ErrNotFound
	}
	return d, err
}

// List 按创建时间倒序；creatorID 为空表示全量（权限判定在 handler）。
func (s *DocumentStore) List(_ context.Context, creatorID string) ([]document.Doc, error) {
	q := s.db.Order("created_at DESC")
	if creatorID != "" {
		q = q.Where("creator_id = ?", creatorID)
	}
	var out []document.Doc
	err := q.Find(&out).Error
	return list(out), err
}
