package gormstore

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"github.com/basalt-go/basalt/pkg/modules/dict"
)

// DictStore 实现 dict.Store（GORM，任意方言）。
// 查询体在包级 dictXxx 函数中单一来源，RoutedDictStore 复用。
type DictStore struct{ db *gorm.DB }

// 编译期接口对齐检查。
var _ dict.Store = (*DictStore)(nil)

func (s *DictStore) CreateType(_ context.Context, t dict.Type) error { return dictCreateType(s.db, t) }
func (s *DictStore) UpdateType(_ context.Context, t dict.Type) error { return dictUpdateType(s.db, t) }
func (s *DictStore) DeleteType(_ context.Context, id string) error   { return dictDeleteType(s.db, id) }
func (s *DictStore) ListTypes(_ context.Context) ([]dict.Type, error) {
	return dictListTypes(s.db)
}
func (s *DictStore) CreateData(_ context.Context, d dict.Data) error { return dictCreateData(s.db, d) }
func (s *DictStore) UpdateData(_ context.Context, d dict.Data) error { return dictUpdateData(s.db, d) }
func (s *DictStore) DeleteData(_ context.Context, id string) error   { return dictDeleteData(s.db, id) }
func (s *DictStore) ListDataByType(_ context.Context, typeCode string) ([]dict.Data, error) {
	return dictListDataByType(s.db, typeCode)
}

// ---------- 查询体（单一来源，固定库与租户路由实现共用） ----------

func dictCreateType(db *gorm.DB, t dict.Type) error {
	var cnt int64
	if err := db.Model(&dict.Type{}).Where("code = ?", t.Code).Count(&cnt).Error; err != nil {
		return err
	}
	if cnt > 0 {
		return dict.ErrTypeCodeUsed
	}
	return db.Create(&t).Error
}

func dictUpdateType(db *gorm.DB, t dict.Type) error {
	var cnt int64
	if err := db.Model(&dict.Type{}).Where("code = ? AND id <> ?", t.Code, t.ID).Count(&cnt).Error; err != nil {
		return err
	}
	if cnt > 0 {
		return dict.ErrTypeCodeUsed
	}
	res := db.Model(&dict.Type{}).Where("id = ?", t.ID).
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

func dictDeleteType(db *gorm.DB, id string) error {
	var t dict.Type
	if err := db.First(&t, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return dict.ErrNotFound
		}
		return err
	}
	var cnt int64
	if err := db.Model(&dict.Data{}).Where("type_code = ?", t.Code).Count(&cnt).Error; err != nil {
		return err
	}
	if cnt > 0 {
		return dict.ErrTypeHasData
	}
	return db.Delete(&dict.Type{}, "id = ?", id).Error
}

func dictListTypes(db *gorm.DB) ([]dict.Type, error) {
	var out []dict.Type
	err := db.Order("code").Find(&out).Error
	return list(out), err
}

func dictCreateData(db *gorm.DB, d dict.Data) error {
	return db.Create(&d).Error
}

func dictUpdateData(db *gorm.DB, d dict.Data) error {
	res := db.Model(&dict.Data{}).Where("id = ?", d.ID).
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

func dictDeleteData(db *gorm.DB, id string) error {
	res := db.Delete(&dict.Data{}, "id = ?", id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return dict.ErrNotFound
	}
	return nil
}

// dictListDataByType 返回该类型全部数据项（含停用；激活过滤属展示层）。
func dictListDataByType(db *gorm.DB, typeCode string) ([]dict.Data, error) {
	var out []dict.Data
	err := db.Where("type_code = ?", typeCode).Order("sort").Find(&out).Error
	return list(out), err
}
