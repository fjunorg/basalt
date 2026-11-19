// 审批人动态解析（v0.3）：把流程定义里的审批人表达式展开为具体
// 用户 ID 列表。表达式在发起实例时一次性解析（快照化），运行中
// 不受组织/角色变更影响。
//
// 表达式语法（Step.ApproverExpr，多个用英文逗号分隔）：
//
//	user:<id>            指定用户（等价于在 Approvers 里直接写 ID）
//	role:<code>          某角色下全部启用用户
//	dept-head            发起人所在部门的负责人
//	dept-head:<n>        发起人部门向上第 n 级负责人（1=本部门）
//	role-in-dept:<code>  发起人部门内拥有某角色的启用用户
//	<id>                 无前缀裸 ID 向后兼容（等价 user:<id>）
package workflow

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrBadApproverExpr 表达式语法错误或解析结果为空。
var ErrBadApproverExpr = errors.New("workflow: bad approver expression")

// ApproverResolver 是表达式解析所需的领域查询能力，由 serve 装配层
// 注入（实现包装 identity + org 模块），workflow 包不直接 import 它们。
type ApproverResolver interface {
	// UsersByRole 返回拥有指定角色 code 的全部启用用户 ID。
	UsersByRole(ctx context.Context, roleCode string) ([]string, error)
	// DeptHead 返回 deptID 向上第 level 级部门的负责人用户 ID
	// （level=1 本部门，level=2 父部门）。部门或负责人不存在返回空串。
	DeptHead(ctx context.Context, deptID string, level int) (string, error)
	// UsersByRoleInDept 返回指定部门内拥有指定角色 code 的启用用户 ID。
	UsersByRoleInDept(ctx context.Context, deptID, roleCode string) ([]string, error)
	// UserDept 返回用户所属部门 ID（无部门返回空串）。
	UserDept(ctx context.Context, userID string) (string, error)
}

// ResolveApprovers 把逗号分隔的表达式列表展开为去重后的用户 ID 列表。
// expr 为空返回 nil（调用方回退到 Step.Approvers，向后兼容）。
// creatorID 是实例发起人（dept-head / role-in-dept 以发起人为锚点）。
func ResolveApprovers(ctx context.Context, expr, creatorID string, resolver ApproverResolver) ([]string, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return nil, nil
	}
	if resolver == nil {
		return nil, fmt.Errorf("%w: resolver not configured", ErrBadApproverExpr)
	}
	var out []string
	seen := map[string]bool{}
	add := func(ids ...string) {
		for _, id := range ids {
			if id != "" && !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	for _, token := range strings.Split(expr, ",") {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		ids, err := resolveOne(ctx, token, creatorID, resolver)
		if err != nil {
			return nil, err
		}
		add(ids...)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: %q resolved to empty", ErrBadApproverExpr, expr)
	}
	return out, nil
}

// resolveOne 解析单个表达式 token。
func resolveOne(ctx context.Context, token, creatorID string, r ApproverResolver) ([]string, error) {
	// dept-head 无参数形式（无冒号，优先于裸 ID 判定）。
	if token == "dept-head" {
		return resolveDeptHead(ctx, creatorID, 1, r)
	}
	// 裸 ID 向后兼容（无冒号前缀且非已知表达式）。
	if !strings.Contains(token, ":") {
		return []string{token}, nil
	}
	scheme, arg, _ := strings.Cut(token, ":")
	switch scheme {
	case "user":
		if arg == "" {
			return nil, fmt.Errorf("%w: user: requires id", ErrBadApproverExpr)
		}
		return []string{arg}, nil
	case "role":
		if arg == "" {
			return nil, fmt.Errorf("%w: role: requires code", ErrBadApproverExpr)
		}
		return r.UsersByRole(ctx, arg)
	case "dept-head":
		level := 1
		if arg != "" {
			n, err := strconv.Atoi(arg)
			if err != nil || n < 1 {
				return nil, fmt.Errorf("%w: dept-head level must be positive integer", ErrBadApproverExpr)
			}
			level = n
		}
		return resolveDeptHead(ctx, creatorID, level, r)
	case "role-in-dept":
		if arg == "" {
			return nil, fmt.Errorf("%w: role-in-dept: requires code", ErrBadApproverExpr)
		}
		deptID, err := r.UserDept(ctx, creatorID)
		if err != nil {
			return nil, err
		}
		if deptID == "" {
			return nil, fmt.Errorf("%w: creator %s has no department", ErrBadApproverExpr, creatorID)
		}
		return r.UsersByRoleInDept(ctx, deptID, arg)
	default:
		return nil, fmt.Errorf("%w: unknown scheme %q", ErrBadApproverExpr, scheme)
	}
}

// resolveDeptHead 解析部门负责人（dept-head 表达式的公共逻辑）。
func resolveDeptHead(ctx context.Context, creatorID string, level int, r ApproverResolver) ([]string, error) {
	deptID, err := r.UserDept(ctx, creatorID)
	if err != nil {
		return nil, err
	}
	if deptID == "" {
		return nil, fmt.Errorf("%w: creator %s has no department", ErrBadApproverExpr, creatorID)
	}
	head, err := r.DeptHead(ctx, deptID, level)
	if err != nil {
		return nil, err
	}
	if head == "" {
		return nil, fmt.Errorf("%w: dept-head level %d resolved to empty", ErrBadApproverExpr, level)
	}
	return []string{head}, nil
}
