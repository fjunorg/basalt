package finance_test

import (
	"context"
	"testing"
	"time"

	"github.com/basalt-go/basalt/pkg/modules/finance"
	"github.com/basalt-go/basalt/pkg/stores/gormstore"
)

// TestFinanceStoreRestartRecovery finance store 重启恢复（文件库）：
// 报销单（含明细/发票关联 serializer 字段）、发票、预算（UPSERT 后的
// Used 累计）、付款单全部写入 → 重建 store 实例 → 逐项读回验证。
// 与 finance_test.go 的 HTTP 集成路径、gormstore 包内内存语义测试互补。
func TestFinanceStoreRestartRecovery(t *testing.T) {
	dsn := t.TempDir() + "/finance.db"
	ctx := context.Background()
	now := time.Now().UTC()

	// 第一次运行：写入全部四类数据
	s1, err := gormstore.Open(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	fs1 := s1.FinanceStore()

	exp := finance.Expense{
		ID: "exp-1", ApplicantID: "u1", DeptID: "dept-a", Amount: 15000,
		Reason: "差旅报销", Status: finance.ExpApproved,
		Items:      []finance.ExpenseItem{{Name: "机票", Amount: 10000}, {Name: "住宿", Amount: 5000}},
		InvoiceIDs: []string{"inv-1"},
		CreatedAt:  now,
	}
	if err := fs1.CreateExpense(ctx, exp); err != nil {
		t.Fatalf("create expense: %v", err)
	}
	if err := fs1.CreateInvoice(ctx, finance.Invoice{
		ID: "inv-1", Code: "INV-RST-001", Title: "某某科技有限公司", Amount: 10000,
		Date: now, CreatedAt: now,
	}); err != nil {
		t.Fatalf("create invoice: %v", err)
	}
	if err := fs1.SetBudget(ctx, finance.Budget{DeptID: "dept-a", Period: "2026-10", Limit: 100000}); err != nil {
		t.Fatalf("set budget: %v", err)
	}
	if err := fs1.AddBudgetUsed(ctx, "dept-a", "2026-10", 15000); err != nil {
		t.Fatalf("add budget used: %v", err)
	}
	if err := fs1.CreatePayment(ctx, finance.Payment{
		ID: "pay-1", ExpenseID: "exp-1", Payee: "张三", Amount: 15000,
		Status: "paid", CreatedAt: now,
	}); err != nil {
		t.Fatalf("create payment: %v", err)
	}
	if sqlDB, err := s1.DB.DB(); err == nil {
		_ = sqlDB.Close()
	}

	// 重启：同一 DSN 重建 store 实例，逐项读回
	s2, err := gormstore.Open(dsn)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	fs2 := s2.FinanceStore()

	gotExp, err := fs2.GetExpense(ctx, "exp-1")
	if err != nil {
		t.Fatalf("get expense after restart: %v", err)
	}
	if gotExp.Status != finance.ExpApproved || gotExp.Amount != 15000 {
		t.Fatalf("expense scalar after restart: %+v", gotExp)
	}
	if len(gotExp.Items) != 2 || gotExp.Items[0].Name != "机票" || gotExp.Items[1].Amount != 5000 {
		t.Fatalf("items serializer after restart: %+v", gotExp.Items)
	}
	if len(gotExp.InvoiceIDs) != 1 || gotExp.InvoiceIDs[0] != "inv-1" {
		t.Fatalf("invoice refs after restart: %+v", gotExp.InvoiceIDs)
	}

	mine, err := fs2.ListExpenses(ctx, "u1")
	if err != nil || len(mine) != 1 || mine[0].ID != "exp-1" {
		t.Fatalf("list by applicant after restart: %+v %v", mine, err)
	}

	invoices, err := fs2.ListInvoices(ctx)
	if err != nil || len(invoices) != 1 || invoices[0].Code != "INV-RST-001" {
		t.Fatalf("invoices after restart: %+v %v", invoices, err)
	}

	b, err := fs2.GetBudget(ctx, "dept-a", "2026-10")
	if err != nil || b.Limit != 100000 || b.Used != 15000 {
		t.Fatalf("budget after restart = %+v %v（限额与占用应延续）", b, err)
	}

	pays, err := fs2.ListPayments(ctx)
	if err != nil || len(pays) != 1 || pays[0].ExpenseID != "exp-1" || pays[0].Status != "paid" {
		t.Fatalf("payments after restart: %+v %v", pays, err)
	}

	// 重启后续接业务语义：预算占用在恢复值基础上继续累计
	if err := fs2.AddBudgetUsed(ctx, "dept-a", "2026-10", 5000); err != nil {
		t.Fatalf("resume add used: %v", err)
	}
	b, _ = fs2.GetBudget(ctx, "dept-a", "2026-10")
	if b.Used != 20000 {
		t.Fatalf("used after resume = %d, want 20000", b.Used)
	}
}
