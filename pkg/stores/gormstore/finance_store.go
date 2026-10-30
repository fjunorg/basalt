package gormstore

import (
	"context"
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/basalt-go/basalt/pkg/modules/finance"
)

// FinanceStore 实现 finance.Store（GORM，任意方言）。
//
// Expense 的 Items/InvoiceIDs 经 serializer:json 落库（struct 写入路径），
// UpdateExpense 用 Select+Updates(struct) 保持该路径；Budget 为复合主键
// （dept_id, period），SetBudget 走 UPSERT，AddBudgetUsed 走 SQL 原子累加
// （对齐 quotaStore.AddUsage，避免读-改-写竞争）。
type FinanceStore struct{ db *gorm.DB }

// 编译期接口对齐检查。
var _ finance.Store = (*FinanceStore)(nil)

func (s *FinanceStore) CreateExpense(_ context.Context, e finance.Expense) error {
	return s.db.Create(&e).Error
}

func (s *FinanceStore) UpdateExpense(_ context.Context, e finance.Expense) error {
	res := s.db.Model(&finance.Expense{}).Where("id = ?", e.ID).
		Select("BusinessKey", "ApplicantID", "DeptID", "Amount", "Reason",
			"Items", "InvoiceIDs", "Status", "WorkflowInstID").
		Updates(e)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return finance.ErrNotFound
	}
	return nil
}

func (s *FinanceStore) GetExpense(_ context.Context, id string) (finance.Expense, error) {
	var e finance.Expense
	err := s.db.First(&e, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return finance.Expense{}, finance.ErrNotFound
	}
	return e, err
}

// ListExpenses 按创建时间倒序；applicantID 为空表示全量（权限判定在 handler）。
func (s *FinanceStore) ListExpenses(_ context.Context, applicantID string) ([]finance.Expense, error) {
	q := s.db.Order("created_at DESC")
	if applicantID != "" {
		q = q.Where("applicant_id = ?", applicantID)
	}
	var out []finance.Expense
	err := q.Find(&out).Error
	return out, err
}

func (s *FinanceStore) CreateInvoice(_ context.Context, iv finance.Invoice) error {
	var cnt int64
	if err := s.db.Model(&finance.Invoice{}).Where("code = ?", iv.Code).Count(&cnt).Error; err != nil {
		return err
	}
	if cnt > 0 {
		return finance.ErrDupInvoice
	}
	return s.db.Create(&iv).Error
}

func (s *FinanceStore) ListInvoices(_ context.Context) ([]finance.Invoice, error) {
	var out []finance.Invoice
	err := s.db.Order("created_at").Find(&out).Error
	return out, err
}

func (s *FinanceStore) SetBudget(_ context.Context, b finance.Budget) error {
	return s.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "dept_id"}, {Name: "period"}},
		DoUpdates: clause.AssignmentColumns([]string{
			// Updated 字段无显式 column tag，GORM 列名为 updated
			"limit", "used", "updated",
		}),
	}).Create(&b).Error
}

func (s *FinanceStore) GetBudget(_ context.Context, deptID, period string) (finance.Budget, error) {
	var b finance.Budget
	err := s.db.First(&b, "dept_id = ? AND period = ?", deptID, period).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return finance.Budget{}, finance.ErrNotFound
	}
	return b, err
}

// AddBudgetUsed 原子累加已用额度；预算行不存在时报 ErrNotFound
// （与 MemStore 语义一致：占用仅发生在已设置预算的部门）。
func (s *FinanceStore) AddBudgetUsed(_ context.Context, deptID, period string, delta int64) error {
	res := s.db.Model(&finance.Budget{}).
		Where("dept_id = ? AND period = ?", deptID, period).
		Updates(map[string]any{
			"used":   gorm.Expr("used + ?", delta),
			"updated": gorm.Expr("CURRENT_TIMESTAMP"),
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return finance.ErrNotFound
	}
	return nil
}

func (s *FinanceStore) ListBudgets(_ context.Context) ([]finance.Budget, error) {
	var out []finance.Budget
	err := s.db.Order("dept_id, period").Find(&out).Error
	return out, err
}

func (s *FinanceStore) CreatePayment(_ context.Context, p finance.Payment) error {
	return s.db.Create(&p).Error
}

func (s *FinanceStore) UpdatePayment(_ context.Context, p finance.Payment) error {
	res := s.db.Model(&finance.Payment{}).Where("id = ?", p.ID).
		Select("ExpenseID", "Payee", "Amount", "Status").
		Updates(p)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return finance.ErrNotFound
	}
	return nil
}

func (s *FinanceStore) ListPayments(_ context.Context) ([]finance.Payment, error) {
	var out []finance.Payment
	err := s.db.Order("created_at").Find(&out).Error
	return out, err
}
