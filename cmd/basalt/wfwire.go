// workflow v0.3 装配：审批人表达式解析器 + 流转通知发送端。
// 延迟解析模块引用（Init 顺序无关），Register 后 Run 前注入。
package main

import (
	"context"
	"fmt"

	"github.com/basalt-go/basalt/pkg/kernel/app"
	"github.com/basalt-go/basalt/pkg/modules/identity"
	"github.com/basalt-go/basalt/pkg/modules/notify"
	"github.com/basalt-go/basalt/pkg/modules/org"
	"github.com/basalt-go/basalt/pkg/modules/workflow"
)

// wfResolver 实现 workflow.ApproverResolver（包装 identity + org）。
type wfResolver struct{ app *app.App }

func (r *wfResolver) identityMod() (*identity.Module, error) {
	m, ok := r.app.Registry().MustGet("identity").(*identity.Module)
	if !ok {
		return nil, fmt.Errorf("identity module not available")
	}
	return m, nil
}

func (r *wfResolver) orgMod() (*org.Module, error) {
	m, ok := r.app.Registry().MustGet("org").(*org.Module)
	if !ok {
		return nil, fmt.Errorf("org module not available")
	}
	return m, nil
}

func (r *wfResolver) UsersByRole(ctx context.Context, roleCode string) ([]string, error) {
	idm, err := r.identityMod()
	if err != nil {
		return nil, err
	}
	// 按 role code 找到角色 ID
	roles := idm.Facade().Roles()
	allRoles, err := roles.List(ctx)
	if err != nil {
		return nil, err
	}
	var roleID string
	for _, role := range allRoles {
		if role.Code == roleCode {
			roleID = role.ID
			break
		}
	}
	if roleID == "" {
		return nil, nil // 角色不存在 → 空列表
	}
	// 找到拥有该角色的启用用户
	users, err := idm.Facade().Users().List(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, u := range users {
		if u.Status != identity.StatusActive {
			continue
		}
		for _, rid := range u.RoleIDs {
			if rid == roleID {
				out = append(out, u.ID)
				break
			}
		}
	}
	return out, nil
}

func (r *wfResolver) DeptHead(ctx context.Context, deptID string, level int) (string, error) {
	orgM, err := r.orgMod()
	if err != nil {
		return "", err
	}
	depts, err := orgM.Store().ListDepts(ctx)
	if err != nil {
		return "", err
	}
	byID := map[string]org.Dept{}
	for _, d := range depts {
		byID[d.ID] = d
	}
	// 向上爬 level 级
	current := deptID
	for i := 0; i < level; i++ {
		d, ok := byID[current]
		if !ok {
			return "", nil
		}
		if i == level-1 {
			return d.LeaderID, nil
		}
		if d.ParentID == "" {
			return "", nil // 到顶了
		}
		current = d.ParentID
	}
	return "", nil
}

func (r *wfResolver) UsersByRoleInDept(ctx context.Context, deptID, roleCode string) ([]string, error) {
	idm, err := r.identityMod()
	if err != nil {
		return nil, err
	}
	roles := idm.Facade().Roles()
	allRoles, err := roles.List(ctx)
	if err != nil {
		return nil, err
	}
	var roleID string
	for _, role := range allRoles {
		if role.Code == roleCode {
			roleID = role.ID
			break
		}
	}
	if roleID == "" {
		return nil, nil
	}
	users, err := idm.Facade().Users().List(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, u := range users {
		if u.Status != identity.StatusActive || u.DeptID != deptID {
			continue
		}
		for _, rid := range u.RoleIDs {
			if rid == roleID {
				out = append(out, u.ID)
				break
			}
		}
	}
	return out, nil
}

func (r *wfResolver) UserDept(ctx context.Context, userID string) (string, error) {
	idm, err := r.identityMod()
	if err != nil {
		return "", err
	}
	u, err := idm.Facade().Users().Get(ctx, userID)
	if err != nil {
		return "", err
	}
	return u.DeptID, nil
}

// wfNotifier 实现 workflow.Notifier（包装 notify 模块站内信）。
type wfNotifier struct{ app *app.App }

func (n *wfNotifier) Notify(ctx context.Context, ev workflow.NotifyEvent) error {
	m, ok := n.app.Registry().MustGet("notify").(*notify.Module)
	if !ok {
		return nil // notify 模块不可用 → 静默跳过
	}
	title, content := renderNotifyText(ev)
	for _, uid := range ev.Recipients {
		_ = m.SendInbox(ctx, uid, title, content) // 单条失败不阻断其余
	}
	return nil
}

// renderNotifyText 把通知事件渲染为站内信标题+正文。
func renderNotifyText(ev workflow.NotifyEvent) (title, content string) {
	switch ev.Type {
	case workflow.NotifyStepEnter:
		title = fmt.Sprintf("【待办】%s — 待您审批", ev.Summary)
		content = fmt.Sprintf("流程「%s」已进入第 %d 步「%s」，请及时处理。\n业务键：%s",
			ev.Summary, ev.StepIndex+1, ev.StepName, ev.BizKey)
	case workflow.NotifyApproved:
		title = fmt.Sprintf("【已通过】%s", ev.Summary)
		content = fmt.Sprintf("您发起的流程「%s」已审批通过。\n业务键：%s", ev.Summary, ev.BizKey)
	case workflow.NotifyRejected:
		title = fmt.Sprintf("【已驳回】%s", ev.Summary)
		content = fmt.Sprintf("您发起的流程「%s」被驳回。\n业务键：%s\n驳回原因：%s",
			ev.Summary, ev.BizKey, ev.Comment)
	case workflow.NotifyCanceled:
		title = fmt.Sprintf("【已撤回】%s", ev.Summary)
		content = fmt.Sprintf("流程「%s」已被发起人撤回，您的待办已消除。\n业务键：%s", ev.Summary, ev.BizKey)
	case workflow.NotifyCC:
		title = fmt.Sprintf("【抄送】%s", ev.Summary)
		content = fmt.Sprintf("流程「%s」抄送给您知悉。\n业务键：%s", ev.Summary, ev.BizKey)
	default:
		title = ev.Summary
		content = ev.BizKey
	}
	return
}
