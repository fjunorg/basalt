package gormstore

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"github.com/basalt-go/basalt/pkg/modules/sysconfig"
)

// SysConfigStore 实现 sysconfig.Store（GORM，任意方言）。
type SysConfigStore struct{ db *gorm.DB }

// 编译期接口对齐检查。
var _ sysconfig.Store = (*SysConfigStore)(nil)

func (s *SysConfigStore) Create(_ context.Context, it sysconfig.Item) error {
	var cnt int64
	if err := s.db.Model(&sysconfig.Item{}).Where("key = ?", it.Key).Count(&cnt).Error; err != nil {
		return err
	}
	if cnt > 0 {
		return sysconfig.ErrKeyUsed
	}
	return s.db.Create(&it).Error
}

func (s *SysConfigStore) Update(_ context.Context, it sysconfig.Item) error {
	var cnt int64
	if err := s.db.Model(&sysconfig.Item{}).Where("`key` = ? AND id <> ?", it.Key, it.ID).Count(&cnt).Error; err != nil {
		return err
	}
	if cnt > 0 {
		return sysconfig.ErrKeyUsed
	}
	res := s.db.Model(&sysconfig.Item{}).Where("id = ?", it.ID).
		Select("Key", "Value", "Name", "Remark", "UpdatedAt").
		Updates(it)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return sysconfig.ErrNotFound
	}
	return nil
}

func (s *SysConfigStore) Delete(_ context.Context, id string) error {
	res := s.db.Delete(&sysconfig.Item{}, "id = ?", id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return sysconfig.ErrNotFound
	}
	return nil
}

func (s *SysConfigStore) List(_ context.Context) ([]sysconfig.Item, error) {
	var out []sysconfig.Item
	err := s.db.Order("`key`").Find(&out).Error
	return out, err
}

func (s *SysConfigStore) GetByKey(_ context.Context, key string) (sysconfig.Item, error) {
	var it sysconfig.Item
	err := s.db.First(&it, "`key` = ?", key).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return sysconfig.Item{}, sysconfig.ErrNotFound
	}
	return it, err
}
