package gormstore

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/basalt-go/basalt/pkg/modules/identity"
)

// ---------- SSOStore（OAuth2/SSO 三实体：Client / AuthCode / RefreshToken） ----------
//
// 原子性要点：
//   - ConsumeAuthCode：单事务内「查 + 条件更新」——UPDATE ... WHERE code=?
//     AND used=false，RowsAffected==0 区分不存在/已用，单次使用由数据库
//     行级保证（并发双花仅一方成功）；
//   - RotateRefresh：同一模式条件更新 revoked=false → true，重放方失败
//     并取得旧记录（含 ChainID）供链吊销。

type SSOStore struct{ db *gorm.DB }

// CreateClient 创建 SSO 应用注册。
func (s *SSOStore) CreateClient(ctx context.Context, c identity.Client) error {
	var cnt int64
	if err := s.db.WithContext(ctx).Model(&identity.Client{}).Where("client_id = ?", c.ClientID).Count(&cnt).Error; err != nil {
		return err
	}
	if cnt > 0 {
		return identity.ErrClientIDTaken
	}
	return s.db.WithContext(ctx).Create(&c).Error
}

func (s *SSOStore) GetClient(ctx context.Context, id string) (identity.Client, error) {
	var c identity.Client
	err := s.db.WithContext(ctx).First(&c, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return identity.Client{}, identity.ErrClientNotFound
	}
	return c, err
}

func (s *SSOStore) GetClientByClientID(ctx context.Context, clientID string) (identity.Client, error) {
	var c identity.Client
	err := s.db.WithContext(ctx).First(&c, "client_id = ?", clientID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return identity.Client{}, identity.ErrClientNotFound
	}
	return c, err
}

func (s *SSOStore) ListClients(ctx context.Context) ([]identity.Client, error) {
	var out []identity.Client
	err := s.db.WithContext(ctx).Order("created_at").Find(&out).Error
	return list(out), err
}

func (s *SSOStore) UpdateClient(ctx context.Context, c identity.Client) error {
	// 切片列手动序列化（map 更新不走 serializer，见 RoleStore.Update 注释）。
	uris, err := json.Marshal(c.RedirectURIs)
	if err != nil {
		return err
	}
	scopes, err := json.Marshal(c.Scopes)
	if err != nil {
		return err
	}
	grants, err := json.Marshal(c.Grants)
	if err != nil {
		return err
	}
	res := s.db.WithContext(ctx).Model(&identity.Client{}).Where("id = ?", c.ID).Updates(map[string]any{
		"name": c.Name, "secret_hash": c.SecretHash,
		"redirect_uris": string(uris), "scopes": string(scopes), "grants": string(grants),
		"is_public": c.IsPublic, "status": c.Status, "tenant_id": c.TenantID,
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return identity.ErrClientNotFound
	}
	return nil
}

func (s *SSOStore) DeleteClient(ctx context.Context, id string) error {
	res := s.db.WithContext(ctx).Delete(&identity.Client{}, "id = ?", id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return identity.ErrClientNotFound
	}
	return nil
}

func (s *SSOStore) SaveAuthCode(ctx context.Context, ac identity.AuthCode) error {
	// struct 创建路径：Scopes 经 serializer:json 自动序列化
	// （与 RoleStore.Create 的 Permissions 同范式）。
	return s.db.WithContext(ctx).Create(&ac).Error
}

// ConsumeAuthCode 原子消费：条件更新 used=false → true，单次使用由
// 数据库行级保证；不存在与已用以哨兵错误区分。
func (s *SSOStore) ConsumeAuthCode(ctx context.Context, code string) (identity.AuthCode, error) {
	var ac identity.AuthCode
	if err := s.db.WithContext(ctx).First(&ac, "code = ?", code).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return identity.AuthCode{}, identity.ErrAuthCodeNotFound
		}
		return identity.AuthCode{}, err
	}
	res := s.db.WithContext(ctx).Model(&identity.AuthCode{}).
		Where("code = ? AND used = ?", code, false).
		Update("used", true)
	if res.Error != nil {
		return identity.AuthCode{}, res.Error
	}
	if res.RowsAffected == 0 {
		return ac, identity.ErrAuthCodeUsed
	}
	ac.Used = true
	return ac, nil
}

func (s *SSOStore) SaveRefresh(ctx context.Context, rt identity.RefreshToken) error {
	return s.db.WithContext(ctx).Create(&rt).Error
}

// RotateRefresh 原子轮换：条件更新 revoked=false → true；已吊销值再
// 使用返回旧记录（ErrRefreshReplayed）供 handler 吊销整条链。
func (s *SSOStore) RotateRefresh(ctx context.Context, tokenHash string) (identity.RefreshToken, error) {
	var rt identity.RefreshToken
	if err := s.db.WithContext(ctx).First(&rt, "token_hash = ?", tokenHash).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return identity.RefreshToken{}, identity.ErrRefreshNotFound
		}
		return identity.RefreshToken{}, err
	}
	res := s.db.WithContext(ctx).Model(&identity.RefreshToken{}).
		Where("token_hash = ? AND revoked = ?", tokenHash, false).
		Update("revoked", true)
	if res.Error != nil {
		return identity.RefreshToken{}, res.Error
	}
	if res.RowsAffected == 0 {
		return rt, identity.ErrRefreshReplayed
	}
	rt.Revoked = true
	return rt, nil
}

// RevokeChain 吊销整条授权链（重放响应）。
func (s *SSOStore) RevokeChain(ctx context.Context, chainID string) error {
	return s.db.WithContext(ctx).Model(&identity.RefreshToken{}).
		Where("chain_id = ? AND revoked = ?", chainID, false).
		Update("revoked", true).Error
}

// CleanupExpired 清理过期授权码与 refresh（运维端点/定时任务可调用，
// 当前由进程重启后的惰性过期校验兜底——过期值不可用，仅占行）。
func (s *SSOStore) CleanupExpired(ctx context.Context, before time.Time) error {
	if err := s.db.WithContext(ctx).Where("expires_at < ?", before).Delete(&identity.AuthCode{}).Error; err != nil {
		return err
	}
	return s.db.WithContext(ctx).Where("expires_at < ?", before).Delete(&identity.RefreshToken{}).Error
}
