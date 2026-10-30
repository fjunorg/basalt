package identity

import (
	"context"
	"net/http"
	"sort"

	"github.com/basalt-go/basalt/pkg/kernel/dataperm"
)

// ---------- 数据权限解析（角色软范围，叠加于租户硬边界之上） ----------

// DeptResolver 抽象部门子树解析（org 模块实现，装配期注入）。
type DeptResolver interface {
	// SubtreeIDs 返回部门及其全部子孙部门 ID（含自身）。
	SubtreeIDs(ctx context.Context, deptID string) []string
}

// DataScopeResolver 把用户角色声明解析为有效数据范围。
type DataScopeResolver struct {
	f     *Facade
	depts DeptResolver
}

// NewDataScopeResolver 构造解析器（depts 可为 nil：无部门视图时
// 子树档降级为本部门）。
func NewDataScopeResolver(f *Facade, depts DeptResolver) *DataScopeResolver {
	return &DataScopeResolver{f: f, depts: depts}
}

// Resolve 计算用户的有效范围：任一角色 all → All；否则部门集 =
// 各角色（本部门/子树/自定义）并集；任一角色 self → Self 叠加。
// 多角色取并集（最宽容义），与权限点聚合语义一致。
func (rs *DataScopeResolver) Resolve(ctx context.Context, u User) (dataperm.Scope, error) {
	sc := dataperm.Scope{SelfUserID: u.ID}
	granted, err := rs.f.RolesOf(ctx, u)
	if err != nil {
		return dataperm.Scope{}, err
	}
	deptSet := map[string]bool{}
	for _, role := range granted {
		switch role.DataScope {
		case "", dataperm.ScopeAll:
			sc.All = true
		case dataperm.ScopeDept:
			if u.DeptID != "" {
				deptSet[u.DeptID] = true
			}
		case dataperm.ScopeDeptAndBelow:
			switch {
			case u.DeptID == "":
				// 无部门用户子树档无贡献
			case rs.depts != nil:
				for _, d := range rs.depts.SubtreeIDs(ctx, u.DeptID) {
					deptSet[d] = true
				}
			default:
				deptSet[u.DeptID] = true
			}
		case dataperm.ScopeSelf:
			sc.Self = true
		case dataperm.ScopeCustom:
			for _, d := range role.DeptIDs {
				deptSet[d] = true
			}
		}
	}
	sc.DeptIDs = sortedDeptIDs(deptSet)
	return sc, nil
}

func sortedDeptIDs(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Inject 是数据权限注入中间件：解析当前用户有效范围并放入请求
// 上下文（store 层经 dataperm.FromContext 消费）。必须置于 Auth
// 之后。解析失败按最严（仅本人）降级——数据权限 fail-closed。
func (rs *DataScopeResolver) Inject(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var sc dataperm.Scope
		u, err := rs.f.User(r)
		if err == nil {
			sc, err = rs.Resolve(r.Context(), u)
		}
		if err != nil {
			sc = dataperm.Scope{Self: true, SelfUserID: u.ID}
		}
		next(w, r.WithContext(dataperm.WithScope(r.Context(), sc)))
	}
}
