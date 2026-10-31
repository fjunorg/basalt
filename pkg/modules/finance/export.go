// finance 报销单导出（pkg/kernel/xlsx 轻量实现）。
package finance

import (
	"net/http"

	"github.com/basalt-go/basalt/pkg/kernel/web"
	"github.com/basalt-go/basalt/pkg/kernel/xlsx"
)

// exportExpenses GET /api/v1/finance/expenses/export：报销单列表 → xlsx
//（金额列以元展示，两位小数；复用 PermExpenseList 权限）。
// 大表说明：当前全量导出；行集超万级时按 offset/limit 分批导出
//（API 参数已预留，随需求启用）。
func (m *Module) exportExpenses(w http.ResponseWriter, r *http.Request) {
	scope := ""
	if r.URL.Query().Get("scope") != "all" {
		if u, err := m.facade.User(r); err == nil {
			scope = u.ID
		}
	}
	list, err := m.store.ListExpenses(r.Context(), scope)
	if err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	headers := []string{"单号", "申请人", "部门", "金额(元)", "事由", "状态", "创建时间"}
	rows := make([][]string, 0, len(list))
	for _, e := range list {
		rows = append(rows, []string{
			e.ID, e.ApplicantID, e.DeptID,
			formatYuan(e.Amount), e.Reason, e.Status,
			e.CreatedAt.Format("2006-01-02 15:04"),
		})
	}
	data, err := xlsx.Build(headers, rows)
	if err != nil {
		web.Err(w, http.StatusInternalServerError, "export failed")
		return
	}
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", `attachment; filename="expenses.xlsx"`)
	_, _ = w.Write(data)
}

// formatYuan 分 → 元字符串（两位小数）。
func formatYuan(fen int64) string {
	neg := ""
	if fen < 0 {
		neg, fen = "-", -fen
	}
	return neg + itoa64(fen/100) + "." + pad2(itoa64(fen % 100))
}

func itoa64(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func pad2(s string) string {
	if len(s) >= 2 {
		return s
	}
	return "0" + s
}
