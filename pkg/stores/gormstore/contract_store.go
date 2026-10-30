package gormstore

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/basalt-go/basalt/pkg/modules/contract"
)

// ContractStore 实现 contract.Store（GORM，任意方言）。
type ContractStore struct{ db *gorm.DB }

// 编译期接口对齐检查。
var _ contract.Store = (*ContractStore)(nil)

func (s *ContractStore) Create(_ context.Context, c contract.Contract) error {
	return s.db.Create(&c).Error
}

func (s *ContractStore) Update(_ context.Context, c contract.Contract) error {
	res := s.db.Model(&contract.Contract{}).Where("id = ?", c.ID).
		Select("ContractNum", "Title", "Party", "Amount", "SignDate",
			"ExpireDate", "Status", "CreatorID", "WorkflowInstID").
		Updates(c)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return contract.ErrNotFound
	}
	return nil
}

func (s *ContractStore) Get(_ context.Context, id string) (contract.Contract, error) {
	var c contract.Contract
	err := s.db.First(&c, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return contract.Contract{}, contract.ErrNotFound
	}
	return c, err
}

// List 按创建时间倒序；creatorID 为空表示全量（权限判定在 handler）。
func (s *ContractStore) List(_ context.Context, creatorID string) ([]contract.Contract, error) {
	q := s.db.Order("created_at DESC")
	if creatorID != "" {
		q = q.Where("creator_id = ?", creatorID)
	}
	var out []contract.Contract
	err := q.Find(&out).Error
	return out, err
}

// ListExpiring 返回处于生效态、到期时间落在 before 之前的合同。
// expire_date > 零值下界排除未填写到期日的合同（对齐 MemStore 的
// !ExpireDate.IsZero() 条件；RFC3339 文本列的字典序与时间序一致）。
func (s *ContractStore) ListExpiring(_ context.Context, before time.Time) ([]contract.Contract, error) {
	var out []contract.Contract
	err := s.db.Where("status = ? AND expire_date > ? AND expire_date < ?",
		contract.StActive, time.Time{}, before).
		Order("expire_date").Find(&out).Error
	return out, err
}
