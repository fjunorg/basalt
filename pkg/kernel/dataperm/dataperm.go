// Package dataperm 是数据权限（角色软范围）引擎：五档数据范围解析、
// 请求上下文注入与行可见性判定。
//
// 与租户隔离的语义分层（两者叠加，不混淆）：
//   - 租户隔离 = 硬边界：dedicated-db 路由/行级过滤，物理隔离全部
//     业务数据，任何角色不可逾越（docs/tenant-isolation.md）。
//   - 数据权限 = 角色软范围：同一租户（或平台）内，按角色声明裁剪
//     可见行集。租户先裁剪（store 路由/行过滤已生效），数据权限
//     在此之上再做部门/本人维度的软裁剪——交集语义。
//
// 接入范式（声明式，业务代码近零改动）：
//  1. handler 挂载 Inject 中间件（需要 identity facade 与部门树 resolver）；
//  2. store 的查询方法读 dataperm.FromContext(ctx) 并应用过滤
//     （gormstore 与 MemStore 同语义）；
//  3. r.Context() 天然贯通 handler → store，无需业务代码传参。
package dataperm

import (
	"context"
)

// 数据范围五档（角色声明值）。
const (
	ScopeAll          = "all"            // 全部数据
	ScopeDept         = "dept"           // 本部门
	ScopeDeptAndBelow = "dept_and_below" // 本部门及以下（子树）
	ScopeSelf         = "self"           // 仅本人
	ScopeCustom       = "custom"         // 自定义部门集
	DefaultDataScope  = ScopeAll         // 未声明默认全量（向后兼容既有角色）
)

// ValidScope 校验档位合法。
func ValidScope(s string) bool {
	switch s {
	case ScopeAll, ScopeDept, ScopeDeptAndBelow, ScopeSelf, ScopeCustom:
		return true
	}
	return false
}

// Scope 是解析后的有效数据范围（多角色并集，子树已展开）。
//   - All 为真时不裁剪（角色含 all 档即最宽，并集语义取宽）
//   - DeptIDs 为可见部门集（含本部门/子树/自定义的并集）
//   - Self 为真时本人创建的行额外可见（跨部门自有数据）
//
// 未注入 Scope 的请求（未挂载 Inject 的端点）视为无数据权限约束，
// 与既有行为一致——接入是逐端点声明式的，不是全局强制。
type Scope struct {
	All        bool     `json:"all"`
	DeptIDs    []string `json:"dept_ids,omitempty"`
	Self       bool     `json:"self"`
	SelfUserID string   `json:"self_user_id,omitempty"`
}

// IsEmpty 无任何约束（等价全量）。
func (s Scope) IsEmpty() bool { return !s.All && !s.Self && len(s.DeptIDs) == 0 }

// VisibleDept 部门是否可见。
func (s Scope) VisibleDept(deptID string) bool {
	if s.All {
		return true
	}
	for _, d := range s.DeptIDs {
		if d == deptID {
			return true
		}
	}
	return false
}

// VisibleRow 行是否可见：deptID 为行归属部门（可空），ownerID 为行
// 所有者（创建人/申请人，可空）。
func (s Scope) VisibleRow(deptID, ownerID string) bool {
	if s.All {
		return true
	}
	if s.Self && ownerID != "" && ownerID == s.SelfUserID {
		return true
	}
	if deptID != "" && s.VisibleDept(deptID) {
		return true
	}
	// 无部门归属且非本人：不可见（数据治理默认拒绝）
	return false
}

// ---------- 上下文注入 ----------

type ctxKey int

const scopeKey ctxKey = iota

// WithScope 注入有效范围。
func WithScope(ctx context.Context, s Scope) context.Context {
	return context.WithValue(ctx, scopeKey, s)
}

// FromContext 取出有效范围；第二个返回值 false = 未注入（该端点
// 未声明数据权限，保持既有行为）。
func FromContext(ctx context.Context) (Scope, bool) {
	s, ok := ctx.Value(scopeKey).(Scope)
	return s, ok
}

// 解析器（Resolver/Inject）位于 identity 包（datascope.go）：它需要
// Facade（用户与角色）与部门树，方向为 identity → dataperm 单向
// 依赖，本包保持零业务依赖（Scope 纯模型 + 上下文 + 行可见性）。
