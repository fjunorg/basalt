package gormstore

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"github.com/basalt-go/basalt/pkg/modules/file"
)

// FileStore 实现 file.Store（GORM，任意方言）。
//
// 持久化范围是文件元信息（Meta）；文件本体由 file.Storage
// （LocalStorage，BASALT_UPLOAD_DIR）落盘，二者共同构成完整恢复态：
// 重启后元信息自本表恢复，字节流自磁盘读出。
type FileStore struct{ db *gorm.DB }

// 编译期接口对齐检查。
var _ file.Store = (*FileStore)(nil)

func (s *FileStore) Create(ctx context.Context, m file.Meta) error {
	return s.db.WithContext(ctx).Create(&m).Error
}

func (s *FileStore) Delete(ctx context.Context, id string) error {
	res := s.db.WithContext(ctx).Delete(&file.Meta{}, "id = ?", id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return file.ErrNotFound
	}
	return nil
}

func (s *FileStore) Get(ctx context.Context, id string) (file.Meta, error) {
	var m file.Meta
	err := s.db.WithContext(ctx).First(&m, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return file.Meta{}, file.ErrNotFound
	}
	return m, err
}

// List 按创建时间倒序（上传列表最新在前）。
func (s *FileStore) List(ctx context.Context) ([]file.Meta, error) {
	var out []file.Meta
	err := s.db.WithContext(ctx).Order("created_at DESC").Find(&out).Error
	return list(out), err
}
