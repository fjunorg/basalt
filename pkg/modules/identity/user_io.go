// identity 用户导入导出（pkg/kernel/xlsx 轻量实现，零新依赖）。
//
// 权限复用模块管理权限：导出 identity:user:list（只读面），导入
// identity:user:create（写面）。冲突策略：username 已存在跳过并
// 计入错误行（不覆盖既有账号）；错误行报告（行号+原因）不静默丢弃。
package identity

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"io"
	"net/http"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"github.com/basalt-go/basalt/pkg/kernel/web"
	"github.com/basalt-go/basalt/pkg/kernel/xlsx"
)

// userIOHeaders 用户导入导出的列契约（导入按表头列名定位，顺序无关）。
var userIOHeaders = []string{"username", "email", "dept_id", "role_codes", "password"}

// exportUsers GET /api/v1/identity/users/export：全量用户 → xlsx。
// role_codes 列为逗号分隔；不导出口令哈希（安全基线）。
func (h *Handler) exportUsers(w http.ResponseWriter, r *http.Request) {
	users, err := h.f.Users().List(r.Context())
	if err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	// role_codes 列以角色 code 表达（导入按 code 反查——对称契约）；
	// RoleIDs 是内部 ID（生产为不可读 hex），直接导出会破坏往返。
	roles, err := h.f.Roles().List(r.Context())
	if err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	roleCode := make(map[string]string, len(roles))
	for _, ro := range roles {
		roleCode[ro.ID] = ro.Code
	}
	rows := make([][]string, 0, len(users))
	for _, u := range users {
		codes := make([]string, 0, len(u.RoleIDs))
		for _, id := range u.RoleIDs {
			if c, ok := roleCode[id]; ok {
				codes = append(codes, c)
			}
		}
		rows = append(rows, []string{u.Username, u.Email, u.DeptID, strings.Join(codes, ","), ""})
	}
	data, err := xlsx.Build(userIOHeaders, rows)
	if err != nil {
		web.Err(w, http.StatusInternalServerError, "export failed")
		return
	}
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", `attachment; filename="users.xlsx"`)
	_, _ = w.Write(data)
}

// importRowErr 一行导入失败（行号 1-based，含表头）。
type importRowErr struct {
	Row    int    `json:"row"`
	Reason string `json:"reason"`
}

// importUsers POST /api/v1/identity/users/import（body 为 xlsx 文件）。
// 密码列可缺省：空则生成一次性随机口令（导入后管理员重置）。
func (h *Handler) importUsers(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
	if err != nil {
		web.Err(w, http.StatusBadRequest, "invalid body")
		return
	}
	grid, err := xlsx.Read(bytes.NewReader(body))
	if err != nil {
		web.Err(w, http.StatusBadRequest, "invalid xlsx file: "+err.Error())
		return
	}
	if len(grid) < 2 {
		web.Err(w, http.StatusBadRequest, "empty sheet (need header + data rows)")
		return
	}
	col := map[string]int{}
	for i, c := range grid[0] {
		col[strings.ToLower(strings.TrimSpace(c))] = i
	}
	for _, want := range []string{"username", "email", "dept_id", "role_codes"} {
		if _, ok := col[want]; !ok {
			web.Err(w, http.StatusBadRequest, "missing column: "+want)
			return
		}
	}
	get := func(row []string, name string) string {
		if i, ok := col[name]; ok && i < len(row) {
			return strings.TrimSpace(row[i])
		}
		return ""
	}

	var imported, failed int
	var rowErrs []importRowErr
	for i, row := range grid[1:] {
		rowNo := i + 2
		u, reason := h.importOneUser(r, row, get)
		if u == nil {
			failed++
			rowErrs = append(rowErrs, importRowErr{Row: rowNo, Reason: reason})
			continue
		}
		if err := h.f.Users().Create(r.Context(), *u); err != nil {
			failed++
			rowErrs = append(rowErrs, importRowErr{Row: rowNo, Reason: err.Error()})
			continue
		}
		imported++
	}
	if rowErrs == nil {
		rowErrs = []importRowErr{}
	}
	web.OK(w, map[string]any{"imported": imported, "failed": failed, "errors": rowErrs})
}

// importOneUser 校验并构造一行用户（nil + 原因 = 该行失败）。
func (h *Handler) importOneUser(r *http.Request, row []string, get func([]string, string) string) (*User, string) {
	username := get(row, "username")
	if username == "" {
		return nil, "username 为空"
	}
	if _, err := h.f.Users().GetByUsername(r.Context(), username); err == nil {
		return nil, "用户名已存在（跳过，不覆盖）"
	}
	u := User{Username: username, Email: get(row, "name"),
		DeptID: get(row, "dept_id"), Status: StatusActive}
	if codes := get(row, "role_codes"); codes != "" {
		for _, c := range strings.Split(codes, ",") {
			if c = strings.TrimSpace(c); c == "" {
				continue
			}
			role, err := h.f.Roles().GetByCode(r.Context(), c)
			if err != nil {
				return nil, "角色不存在: " + c
			}
			u.RoleIDs = append(u.RoleIDs, role.ID)
		}
	}
	pass := get(row, "password")
	if pass == "" {
		b := make([]byte, 6)
		_, _ = rand.Read(b)
		pass = "Basalt@" + hex.EncodeToString(b)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(pass), bcrypt.DefaultCost)
	if err != nil {
		return nil, "口令哈希失败"
	}
	u.PasswordHash = string(hash)
	return &u, ""
}
