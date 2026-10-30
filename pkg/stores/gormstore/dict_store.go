package gormstore

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"github.com/basalt-go/basalt/pkg/modules/dict"
)

// DictStore 实现 dict.Store（GORM，任意方言）。
type DictStore struct{ db *gorm.DB }

// 编译期接口对齐检查。
var _ dict.Store = (*DictStore)(nil)

func (s *DictStore) CreateType(_ context.Context, t dict.Type) error {
	var cnt int64
	if err := s.db.Model(&dict.Type{}).Where("code = ?", t.Code).Count(&cnt).Error; err != nil {
		return err
	}
	if cnt > 0 {
		return dict.ErrTypeCodeUsed
	}
	return s.db.Create(&t).Error
}

func (s *DictStore) UpdateType(_ context.Context, t dict.Type) error {
	var cnt int64
	if err := s.db.Model(&dict.Type{}).Where("code = ? AND id <> ?", t.Code, t.ID).Count(&cnt).Error; err != nil {
		return err
	}
	if cnt > 0 {
		return dict.ErrTypeCodeUsed
	}
	res := s.db.Model(&dict.Type{}).Where("id = ?", t.ID).
		Select("Code", "Name", "Status", "Remark").
		Updates(t)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return dict.ErrNotFound
	}
	return nil
}

func (s *DictStore) DeleteType(_ context.Context, id string) error {
	var t dict.Type
	if err := s.db.First(&t, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return dict.ErrNotFound
		}
		return err
	}
	var cnt int64
	if err := s.db.Model(&dict.Data{}).Where("type_code = ?", t.Code).Count(&cnt).Error; err != nil {
		return err
	}
	if cnt > 0 {
		return dict.ErrTypeHasData
	}
	return s.db.Delete(&dict.Type{}, "id = ?", id).Error
}

func (s *DictStore) ListTypes(_ context.Context) ([]dict.Type, error) {
	var out []dict.Type
	err := s.db.Order("code").Find(&out).Error
	return out, err
}

func (s *DictStore) CreateData(_ context.Context, d dict.Data) error {
	return s.db.Create(&d).Error
}

func (s *DictStore) UpdateData(_ context.Context, d dict.Data) error {
	res := s.db.Model(&dict.Data{}).Where("id = ?", d.ID).
		Select("TypeCode", "Label", "Value", "Sort", "Status").
		Updates(d)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return dict.ErrNotFound
	}
	return nil
}

func (s *DictStore) DeleteData(_ context.Context, id string) error {
	res := s.db.Delete(&dict.Data{}, "id = ?", id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return dict.ErrNotFound
	}
	return nil
}

// ListDataByType 返回该类型全部数据项（含停用；激活过滤属展示层）。
func (s *DictStore) ListDataByType(_ context.Context, typeCode string) ([]dict.Data, error) {
	var out []dict.Data
	err := s.db.Where("type_code = ?", typeCode).Order("sort").Find(&out).Error
	return out, err
}
