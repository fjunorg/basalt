// contract 合同导出（pkg/kernel/xlsx 轻量实现）。
package contract

import (
	"net/http"

	"github.com/basalt-go/basalt/pkg/kernel/web"
	"github.com/basalt-go/basalt/pkg/kernel/xlsx"
)

// exportContracts GET /api/v1/contracts/export：合同列表 → xlsx
//（金额元两位小数；复用 PermRead 权限）。
func (m *Module) exportContracts(w http.ResponseWriter, r *http.Request) {
	scope := ""
	if r.URL.Query().Get("scope") != "all" {
		if u, err := m.facade.User(r); err == nil {
			scope = u.ID
		}
	}
	list, err := m.store.List(r.Context(), scope)
	if err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	headers := []string{"合同号", "标题", "对方单位", "金额(元)", "签订日", "到期日", "状态"}
	rows := make([][]string, 0, len(list))
	for _, c := range list {
		rows = append(rows, []string{
			c.ContractNum, c.Title, c.Party,
			yuan(c.Amount),
			c.SignDate.Format("2006-01-02"), c.ExpireDate.Format("2006-01-02"),
			c.Status,
		})
	}
	data, err := xlsx.Build(headers, rows)
	if err != nil {
		web.Err(w, http.StatusInternalServerError, "export failed")
		return
	}
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", `attachment; filename="contracts.xlsx"`)
	_, _ = w.Write(data)
}

// yuan 分 → 元字符串（两位小数）。
func yuan(fen int64) string {
	neg := ""
	if fen < 0 {
		neg, fen = "-", -fen
	}
	q, r := fen/100, fen%100
	s := ""
	if q == 0 {
		s = "0"
	}
	for q > 0 {
		s = string(rune('0'+q%10)) + s
		q /= 10
	}
	cent := string(rune('0' + r/10)) + string(rune('0'+r%10))
	return neg + s + "." + cent
}
