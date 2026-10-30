package gormstore

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/basalt-go/basalt/pkg/modules/errcode"
	"github.com/basalt-go/basalt/pkg/modules/identity"
	"github.com/basalt-go/basalt/pkg/modules/tenantpkg"
)

// ---------- OnlineStore（在线会话注册表 + 用户级踢出时间戳） ----------

type OnlineStore struct{ db *gorm.DB }

func (s *OnlineStore) Register(_ context.Context, ses identity.OnlineSession) error {
	// upsert：同指纹重登记幂等覆盖；新登录同时清除该用户的踢出标记。
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(&ses).Error; err != nil {
			return err
		}
		return tx.Where("user_id = ?", ses.UserID).Delete(&identity.UserKick{}).Error
	})
}

func (s *OnlineStore) Get(_ context.Context, tokenID string) (identity.OnlineSession, error) {
	var ses identity.OnlineSession
	err := s.db.First(&ses, "token_id = ?", tokenID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return identity.OnlineSession{}, identity.ErrSessionNotFound
	}
	return ses, err
}

func (s *OnlineStore) Revoke(_ context.Context, tokenID string, at time.Time) error {
	res := s.db.Model(&identity.OnlineSession{}).
		Where("token_id = ? AND revoked_at IS NULL", tokenID).
		Update("revoked_at", at)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		// 幂等：已吊销视为成功（登出重放无害）。
		var n int64
		if err := s.db.Model(&identity.OnlineSession{}).Where("token_id = ?", tokenID).Count(&n).Error; err == nil && n > 0 {
			return nil
		}
		return identity.ErrSessionNotFound
	}
	return nil
}

func (s *OnlineStore) RevokeByUser(_ context.Context, userID string, at time.Time) (int, error) {
	err := s.db.Transaction(func(tx *gorm.DB) error {
		// 用户级吊销时间戳（upsert：重复踢下线取最新时刻）。
		if err := tx.Save(&identity.UserKick{UserID: userID, KickedAt: at}).Error; err != nil {
			return err
		}
		return tx.Model(&identity.OnlineSession{}).
			Where("user_id = ? AND revoked_at IS NULL", userID).
			Update("revoked_at", at).Error
	})
	if err != nil {
		return 0, err
	}
	var n int64
	if err := s.db.Model(&identity.OnlineSession{}).
		Where("user_id = ? AND revoked_at = ?", userID, at).Count(&n).Error; err != nil {
		return 0, err
	}
	return int(n), nil
}

func (s *OnlineStore) ListOnline(_ context.Context, now time.Time) ([]identity.OnlineSession, error) {
	var out []identity.OnlineSession
	err := s.db.Where("revoked_at IS NULL AND expires_at > ?", now).
		Order("login_at DESC").Find(&out).Error
	return out, err
}

// CheckToken 吊销裁决（AuthMiddleware 每请求调用，两次索引查询）。
func (s *OnlineStore) CheckToken(_ context.Context, userID, tokenID string, iat time.Time) bool {
	var kick identity.UserKick
	err := s.db.First(&kick, "user_id = ?", userID).Error
	if err == nil && iat.Before(kick.KickedAt) {
		return false
	}
	var n int64
	if err := s.db.Model(&identity.OnlineSession{}).
		Where("token_id = ? AND revoked_at IS NOT NULL", tokenID).
		Count(&n).Error; err != nil {
		return true // 查询失败 fail-open（可用性优先，踢出仍留 kicked_at 兜底）
	}
	return n == 0
}

func (s *OnlineStore) Cleanup(_ context.Context, now time.Time) error {
	if err := s.db.Where("expires_at < ?", now).Delete(&identity.OnlineSession{}).Error; err != nil {
		return err
	}
	return nil
}

// ---------- TenantPkgStore（套餐 + 绑定） ----------

type TenantPkgStore struct{ db *gorm.DB }

func (s *TenantPkgStore) CreatePkg(_ context.Context, p tenantpkg.Package) error {
	var cnt int64
	if err := s.db.Model(&tenantpkg.Package{}).Where("name = ?", p.Name).Count(&cnt).Error; err != nil {
		return err
	}
	if cnt > 0 {
		return tenantpkg.ErrPkgNameTaken
	}
	return s.db.Create(&p).Error
}

func (s *TenantPkgStore) GetPkg(_ context.Context, id string) (tenantpkg.Package, error) {
	var p tenantpkg.Package
	err := s.db.First(&p, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return tenantpkg.Package{}, tenantpkg.ErrPkgNotFound
	}
	return p, err
}

func (s *TenantPkgStore) ListPkgs(_ context.Context) ([]tenantpkg.Package, error) {
	var out []tenantpkg.Package
	err := s.db.Order("created_at").Find(&out).Error
	return out, err
}

func (s *TenantPkgStore) UpdatePkg(_ context.Context, p tenantpkg.Package) error {
	menuIDs, err := json.Marshal(p.MenuIDs)
	if err != nil {
		return err
	}
	perms, err := json.Marshal(p.Perms)
	if err != nil {
		return err
	}
	res := s.db.Model(&tenantpkg.Package{}).Where("id = ?", p.ID).Updates(map[string]any{
		"name": p.Name, "status": p.Status,
		"menu_ids": string(menuIDs), "perms": string(perms), "remark": p.Remark,
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return tenantpkg.ErrPkgNotFound
	}
	return nil
}

func (s *TenantPkgStore) DeletePkg(_ context.Context, id string) error {
	res := s.db.Delete(&tenantpkg.Package{}, "id = ?", id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return tenantpkg.ErrPkgNotFound
	}
	return nil
}

func (s *TenantPkgStore) CountBound(_ context.Context, pkgID string) (int64, error) {
	var n int64
	err := s.db.Model(&tenantpkg.TenantPackage{}).Where("package_id = ?", pkgID).Count(&n).Error
	return n, err
}

func (s *TenantPkgStore) Bind(_ context.Context, tenantID, pkgID string, at time.Time) error {
	return s.db.Save(&tenantpkg.TenantPackage{TenantID: tenantID, PackageID: pkgID, UpdatedAt: at}).Error
}

func (s *TenantPkgStore) Unbind(_ context.Context, tenantID string) error {
	res := s.db.Delete(&tenantpkg.TenantPackage{}, "tenant_id = ?", tenantID)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return tenantpkg.ErrBindNotFound
	}
	return nil
}

func (s *TenantPkgStore) BoundPkg(_ context.Context, tenantID string) (tenantpkg.Package, bool, error) {
	var bind tenantpkg.TenantPackage
	err := s.db.First(&bind, "tenant_id = ?", tenantID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return tenantpkg.Package{}, false, nil
	}
	if err != nil {
		return tenantpkg.Package{}, false, err
	}
	var p tenantpkg.Package
	err = s.db.First(&p, "id = ?", bind.PackageID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return tenantpkg.Package{}, false, nil
	}
	return p, err == nil, err
}

// ---------- ErrCodeStore（错误码注册表） ----------

type ErrCodeStore struct{ db *gorm.DB }

func (s *ErrCodeStore) Create(_ context.Context, e errcode.Entry) error {
	var cnt int64
	if err := s.db.Model(&errcode.Entry{}).
		Where("module = ? AND code = ?", e.Module, e.Code).Count(&cnt).Error; err != nil {
		return err
	}
	if cnt > 0 {
		return errcode.ErrDup
	}
	return s.db.Create(&e).Error
}

func (s *ErrCodeStore) Get(_ context.Context, id string) (errcode.Entry, error) {
	var e errcode.Entry
	err := s.db.First(&e, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return errcode.Entry{}, errcode.ErrNotFound
	}
	return e, err
}

func (s *ErrCodeStore) List(_ context.Context, module string) ([]errcode.Entry, error) {
	var out []errcode.Entry
	q := s.db.Order("module").Order("code")
	if module != "" {
		q = q.Where("module = ?", module)
	}
	err := q.Find(&out).Error
	return out, err
}

func (s *ErrCodeStore) Update(_ context.Context, e errcode.Entry) error {
	res := s.db.Model(&errcode.Entry{}).Where("id = ?", e.ID).Updates(map[string]any{
		"message": e.Message, "http_status": e.HTTPStatus, "detail": e.Detail,
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errcode.ErrNotFound
	}
	return nil
}

func (s *ErrCodeStore) Delete(_ context.Context, id string) error {
	res := s.db.Delete(&errcode.Entry{}, "id = ?", id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errcode.ErrNotFound
	}
	return nil
}
