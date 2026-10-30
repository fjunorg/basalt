package gormstore

import (
	"context"
	"encoding/json"
	"errors"

	"gorm.io/gorm"

	"github.com/basalt-go/basalt/pkg/modules/identity"
	"github.com/basalt-go/basalt/pkg/modules/tenant"
)

// ---------- RoleStore ----------

type RoleStore struct{ db *gorm.DB }

func (s *RoleStore) Create(_ context.Context, r identity.Role) error {
	var cnt int64
	if err := s.db.Model(&identity.Role{}).Where("code = ?", r.Code).Count(&cnt).Error; err != nil {
		return err
	}
	if cnt > 0 {
		return identity.ErrRoleCodeTaken
	}
	return s.db.Create(&r).Error
}

func (s *RoleStore) Get(_ context.Context, id string) (identity.Role, error) {
	var r identity.Role
	err := s.db.First(&r, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return identity.Role{}, identity.ErrRoleNotFound
	}
	return r, err
}

func (s *RoleStore) GetByCode(_ context.Context, code string) (identity.Role, error) {
	var r identity.Role
	err := s.db.First(&r, "code = ?", code).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return identity.Role{}, identity.ErrRoleNotFound
	}
	return r, err
}

func (s *RoleStore) List(_ context.Context) ([]identity.Role, error) {
	var out []identity.Role
	err := s.db.Order("code").Find(&out).Error
	return out, err
}

func (s *RoleStore) Update(_ context.Context, r identity.Role) error {
	// permissions 列手动序列化（map 更新不走 serializer，见 UserStore.AssignRoles）。
	b, err := json.Marshal(r.Permissions)
	if err != nil {
		return err
	}
	res := s.db.Model(&identity.Role{}).Where("id = ?", r.ID).
		Updates(map[string]any{"code": r.Code, "name": r.Name, "permissions": string(b)})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return identity.ErrRoleNotFound
	}
	return nil
}

func (s *RoleStore) Delete(_ context.Context, id string) error {
	res := s.db.Delete(&identity.Role{}, "id = ?", id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return identity.ErrRoleNotFound
	}
	return nil
}

// ---------- TenantStore ----------

type TenantStore struct{ db *gorm.DB }

func (s *TenantStore) Create(_ context.Context, t tenant.Tenant) error {
	var cnt int64
	if err := s.db.Model(&tenant.Tenant{}).Where("code = ?", t.Code).Count(&cnt).Error; err != nil {
		return err
	}
	if cnt > 0 {
		return tenant.ErrCodeTaken
	}
	return s.db.Create(&t).Error
}

func (s *TenantStore) Get(_ context.Context, id string) (tenant.Tenant, error) {
	var t tenant.Tenant
	err := s.db.First(&t, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return tenant.Tenant{}, tenant.ErrNotFound
	}
	return t, err
}

func (s *TenantStore) List(_ context.Context) ([]tenant.Tenant, error) {
	var out []tenant.Tenant
	err := s.db.Order("code").Find(&out).Error
	return out, err
}

func (s *TenantStore) Update(_ context.Context, t tenant.Tenant) error {
	res := s.db.Model(&tenant.Tenant{}).Where("id = ?", t.ID).
		Updates(map[string]any{"code": t.Code, "name": t.Name, "status": t.Status, "isolation_mode": t.IsolationMode})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return tenant.ErrNotFound
	}
	return nil
}
