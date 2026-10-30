package gormstore

import (
	"context"

	"gorm.io/gorm"

	"github.com/basalt-go/basalt/pkg/modules/org"
)

// OrgStore 实现 org.Store（GORM，任意方言）。
type OrgStore struct{ db *gorm.DB }

// 编译期接口对齐检查。
var _ org.Store = (*OrgStore)(nil)

func (s *OrgStore) CreateDept(_ context.Context, d org.Dept) error {
	return s.db.Create(&d).Error
}

func (s *OrgStore) UpdateDept(_ context.Context, d org.Dept) error {
	res := s.db.Model(&org.Dept{}).Where("id = ?", d.ID).
		Select("ParentID", "Name", "Sort", "Status").
		Updates(d)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return org.ErrNotFound
	}
	return nil
}

func (s *OrgStore) DeleteDept(_ context.Context, id string) error {
	var cnt int64
	if err := s.db.Model(&org.Dept{}).Where("parent_id = ?", id).Count(&cnt).Error; err != nil {
		return err
	}
	if cnt > 0 {
		return org.ErrHasChildren
	}
	res := s.db.Delete(&org.Dept{}, "id = ?", id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return org.ErrNotFound
	}
	return nil
}

func (s *OrgStore) ListDepts(_ context.Context) ([]org.Dept, error) {
	var out []org.Dept
	err := s.db.Order("sort").Find(&out).Error
	return list(out), err
}

func (s *OrgStore) CreatePost(_ context.Context, p org.Post) error {
	var cnt int64
	if err := s.db.Model(&org.Post{}).Where("code = ?", p.Code).Count(&cnt).Error; err != nil {
		return err
	}
	if cnt > 0 {
		return org.ErrDupCode
	}
	return s.db.Create(&p).Error
}

func (s *OrgStore) UpdatePost(_ context.Context, p org.Post) error {
	var cnt int64
	if err := s.db.Model(&org.Post{}).Where("code = ? AND id <> ?", p.Code, p.ID).Count(&cnt).Error; err != nil {
		return err
	}
	if cnt > 0 {
		return org.ErrDupCode
	}
	res := s.db.Model(&org.Post{}).Where("id = ?", p.ID).
		Select("Code", "Name", "Sort", "Status").
		Updates(p)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return org.ErrNotFound
	}
	return nil
}

func (s *OrgStore) DeletePost(_ context.Context, id string) error {
	res := s.db.Delete(&org.Post{}, "id = ?", id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return org.ErrNotFound
	}
	return nil
}

func (s *OrgStore) ListPosts(_ context.Context) ([]org.Post, error) {
	var out []org.Post
	err := s.db.Order("sort").Find(&out).Error
	return list(out), err
}
