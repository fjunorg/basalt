package gormstore

import (
	"context"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// quotaLimit / quotaUsage 是配额持久化的两张表：
// limit 跨账期延续（每租户一行），usage 按账期隔离（租户×账期一行）。
type quotaLimit struct {
	TenantID string `gorm:"primaryKey;size:64"`
	LimitTok int64  `gorm:"column:limit_tokens"`
}

type quotaUsage struct {
	TenantID string `gorm:"primaryKey;size:64"`
	Period   string `gorm:"primaryKey;size:7"` // YYYY-MM
	Used     int64  `gorm:"column:used_tokens"`
}

// QuotaStore 实现 quota.Store（GORM，任意方言）。
type QuotaStore struct{ db *gorm.DB }

func NewQuotaStore(db *gorm.DB) (*QuotaStore, error) {
	if err := db.AutoMigrate(&quotaLimit{}, &quotaUsage{}); err != nil {
		return nil, err
	}
	return &QuotaStore{db: db}, nil
}

func (s *QuotaStore) GetLimit(ctx context.Context, tenantID string) (int64, bool) {
	var row quotaLimit
	err := s.db.WithContext(ctx).First(&row, "tenant_id = ?", tenantID).Error
	if err != nil {
		return 0, false
	}
	return row.LimitTok, true
}

func (s *QuotaStore) SetLimit(ctx context.Context, tenantID string, limit int64) error {
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "tenant_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"limit_tokens"}),
	}).Create(&quotaLimit{TenantID: tenantID, LimitTok: limit}).Error
}

func (s *QuotaStore) GetUsage(ctx context.Context, tenantID, period string) (int64, bool) {
	var row quotaUsage
	err := s.db.WithContext(ctx).First(&row, "tenant_id = ? AND period = ?", tenantID, period).Error
	if err != nil {
		return 0, false
	}
	return row.Used, true
}

// AddUsage 以原子 UPSERT 累加，避免读-改-写竞争。
func (s *QuotaStore) AddUsage(ctx context.Context, tenantID, period string, delta int64) error {
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "tenant_id"}, {Name: "period"}},
		DoUpdates: clause.Assignments(map[string]any{"used_tokens": gorm.Expr("used_tokens + ?", delta)}),
	}).Create(&quotaUsage{TenantID: tenantID, Period: period, Used: delta}).Error
}
