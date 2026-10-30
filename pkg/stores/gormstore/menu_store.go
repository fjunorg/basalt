package gormstore

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/basalt-go/basalt/pkg/modules/menu"
)

// MenuStore 实现 menu.Store（GORM，任意方言；表 menus/role_menus
// 经实体 TableName 显式声明）。
type MenuStore struct{ db *gorm.DB }

// 编译期接口对齐检查。
var _ menu.Store = (*MenuStore)(nil)

func (s *MenuStore) Create(_ context.Context, m menu.Menu) error {
	return s.db.Create(&m).Error
}

func (s *MenuStore) Update(_ context.Context, m menu.Menu) error {
	res := s.db.Model(&menu.Menu{}).Where("id = ?", m.ID).
		Select("ParentID", "Name", "Path", "Icon", "Perm", "Sort", "Enabled").
		Updates(m)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return menu.ErrNotFound
	}
	return nil
}

func (s *MenuStore) Delete(_ context.Context, id string) error {
	// 有子节点/有角色绑定由调用方先查（语义错误值），此处仅物理删除
	res := s.db.Delete(&menu.Menu{}, "id = ?", id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return menu.ErrNotFound
	}
	return nil
}

func (s *MenuStore) Get(_ context.Context, id string) (menu.Menu, error) {
	var m menu.Menu
	err := s.db.First(&m, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return menu.Menu{}, menu.ErrNotFound
	}
	return m, err
}

func (s *MenuStore) List(_ context.Context) ([]menu.Menu, error) {
	var out []menu.Menu
	err := s.db.Order("sort, id").Find(&out).Error
	return list(out), err
}

func (s *MenuStore) CountByPath(_ context.Context, path, excludeID string) (int64, error) {
	q := s.db.Model(&menu.Menu{}).Where("path = ?", path)
	if excludeID != "" {
		q = q.Where("id <> ?", excludeID)
	}
	var n int64
	err := q.Count(&n).Error
	return n, err
}

func (s *MenuStore) SetRoleMenus(_ context.Context, roleID string, menuIDs []string) error {
	return s.db.Save(&menu.RoleMenu{RoleID: roleID, MenuIDs: menuIDs, Updated: time.Now().UTC()}).Error
}

func (s *MenuStore) GetRoleMenus(_ context.Context, roleID string) ([]string, error) {
	var rm menu.RoleMenu
	err := s.db.First(&rm, "role_id = ?", roleID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil // 未绑定 = 空集（区别于错误）
	}
	if err != nil {
		return nil, err
	}
	return rm.MenuIDs, nil
}

func (s *MenuStore) BoundRoleCount(_ context.Context, menuID string) (int64, error) {
	var rms []menu.RoleMenu
	if err := s.db.Find(&rms).Error; err != nil {
		return 0, err
	}
	var n int64
	for _, rm := range rms {
		for _, id := range rm.MenuIDs {
			if id == menuID {
				n++
				break
			}
		}
	}
	return n, nil
}
