package gormstore

import (
	"context"
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/basalt-go/basalt/pkg/modules/aisafety"
)

// AISafetyStore 实现 aisafety.Store（GORM，任意方言）：
// 词库（唯一 word）、策略（租户主键 UPSERT）、全局开关（固定单行）。
type AISafetyStore struct{ db *gorm.DB }

// 编译期接口对齐检查。
var _ aisafety.Store = (*AISafetyStore)(nil)

func (s *AISafetyStore) CreateTerm(_ context.Context, t aisafety.Term) error {
	var cnt int64
	if err := s.db.Model(&aisafety.Term{}).Where("word = ?", t.Word).Count(&cnt).Error; err != nil {
		return err
	}
	if cnt > 0 {
		return aisafety.ErrDupWord
	}
	return s.db.Create(&t).Error
}

func (s *AISafetyStore) UpdateTerm(_ context.Context, t aisafety.Term) error {
	// 先确认存在性（与 MemStore 语义一致：不存在 → ErrNotFound 优先）
	var cur aisafety.Term
	if err := s.db.First(&cur, "id = ?", t.ID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return aisafety.ErrNotFound
		}
		return err
	}
	if t.Word != cur.Word {
		var cnt int64
		if err := s.db.Model(&aisafety.Term{}).Where("word = ?", t.Word).Count(&cnt).Error; err != nil {
			return err
		}
		if cnt > 0 {
			return aisafety.ErrDupWord
		}
	}
	res := s.db.Model(&aisafety.Term{}).Where("id = ?", t.ID).
		Select("Word", "Category", "Replacement", "Enabled").
		Updates(t)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return aisafety.ErrNotFound
	}
	return nil
}

func (s *AISafetyStore) DeleteTerm(_ context.Context, id string) error {
	res := s.db.Delete(&aisafety.Term{}, "id = ?", id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return aisafety.ErrNotFound
	}
	return nil
}

func (s *AISafetyStore) ListTerms(_ context.Context) ([]aisafety.Term, error) {
	var out []aisafety.Term
	err := s.db.Order("word").Find(&out).Error
	return list(out), err
}

func (s *AISafetyStore) GetPolicy(_ context.Context, tenantID string) (aisafety.Policy, error) {
	var p aisafety.Policy
	err := s.db.First(&p, "tenant_id = ?", tenantID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return aisafety.Policy{}, aisafety.ErrNotFound
	}
	return p, err
}

func (s *AISafetyStore) SetPolicy(_ context.Context, p aisafety.Policy) error {
	return s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "tenant_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"action", "updated_at"}),
	}).Create(&p).Error
}

func (s *AISafetyStore) ListPolicies(_ context.Context) ([]aisafety.Policy, error) {
	var out []aisafety.Policy
	err := s.db.Order("tenant_id").Find(&out).Error
	return list(out), err
}

func (s *AISafetyStore) GetSettings(_ context.Context) (aisafety.Settings, error) {
	var st aisafety.Settings
	// 固定单行：不存在时返回默认开启（与 MemStore 初始态一致）
	err := s.db.First(&st, "id = ?", aisafety.SettingsID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return aisafety.Settings{ID: aisafety.SettingsID, Enabled: true}, nil
	}
	return st, err
}

func (s *AISafetyStore) SaveSettings(_ context.Context, st aisafety.Settings) error {
	st.ID = aisafety.SettingsID
	return s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		DoUpdates: clause.AssignmentColumns([]string{"enabled", "updated"}), // Updated 字段列名为 updated（无显式 tag）
	}).Create(&st).Error
}
