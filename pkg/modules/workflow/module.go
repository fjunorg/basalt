package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/basalt-go/basalt/pkg/kernel/app"
	"github.com/basalt-go/basalt/pkg/kernel/web"
	"github.com/basalt-go/basalt/pkg/modules/audit"
	"github.com/basalt-go/basalt/pkg/modules/identity"
)

const Version = "0.4.0"

// 权限点。
const (
	PermCreate    = "workflow:create"
	PermRead      = "workflow:read"
	PermDefManage = "workflow:def:manage"
)

type Module struct {
	store    Store
	defs     DefStore
	facade   *identity.Facade
	recorder audit.Recorder
	onFinish []func(ctx context.Context, in Instance)
	resolver ApproverResolver // 审批人表达式解析器（serve 装配注入）
	notifier Notifier         // 流转通知发送端（serve 装配注入）
}

func New() *Module {
	return &Module{store: NewMemStore(), defs: NewMemDefStore()}
}

// NewWithStores 注入持久化实现（实例 + 定义，GORM 落盘、重启恢复）；
// New() 的纯内存路径保留（测试/演示场景可退回）。OnFinish 回调由
// 各业务模块在 Init 期注册，进程重启即随装配重建，无需持久化。
func NewWithStores(store Store, defs DefStore) *Module {
	return &Module{store: store, defs: defs}
}

// OnFinish 注册流程终态回调（财务等业务单据联动通道）。
func (m *Module) OnFinish(h func(ctx context.Context, in Instance)) {
	m.onFinish = append(m.onFinish, h)
}

// SetResolver 注入审批人表达式解析器（serve 装配层调用）。
func (m *Module) SetResolver(r ApproverResolver) { m.resolver = r }

// SetNotifier 注入流转通知发送端（serve 装配层调用）。
func (m *Module) SetNotifier(n Notifier) { m.notifier = n }

// resolveSteps 解析步骤里的审批人表达式为具体用户 ID（快照化）。
// 无表达式的步骤保持原样；并行网关递归解析各分支子步。
// resolver 为 nil 时有表达式的步骤报错。
func (m *Module) resolveSteps(ctx context.Context, steps []Step, creatorID string) ([]Step, error) {
	out := make([]Step, len(steps))
	for i, s := range steps {
		out[i] = s
		if s.ApproverExpr != "" {
			ids, err := ResolveApprovers(ctx, s.ApproverExpr, creatorID, m.resolver)
			if err != nil {
				return nil, fmt.Errorf("step %d: %w", i+1, err)
			}
			out[i].Approvers = ids
		}
		if s.Type == StepTypeParallel && len(s.Branches) > 0 {
			branches := make([][]Step, len(s.Branches))
			for bi := range s.Branches {
				sub, err := m.resolveSteps(ctx, s.Branches[bi], creatorID)
				if err != nil {
					return nil, fmt.Errorf("step %d branch %d: %w", i+1, bi+1, err)
				}
				branches[bi] = sub
			}
			out[i].Branches = branches
		}
	}
	return out, nil
}

// notifyEvent 触发一次流转通知（锁外、异常不扩散、配置关闭则跳过）。
func (m *Module) notifyEvent(ctx context.Context, ev NotifyEvent, spec NotifySpec) {
	if m.notifier == nil || !spec.shouldNotify(ev.Type) {
		return
	}
	if len(ev.Recipients) == 0 {
		return
	}
	_ = m.notifier.Notify(ctx, ev) // 通知失败不阻断流程
}

// fireFinish 在实例进入终态时触发回调（锁外、异常不扩散）。
func (m *Module) fireFinish(ctx context.Context, in Instance) {
	if in.Status == StatusPending {
		return
	}
	for _, h := range m.onFinish {
		h(ctx, in)
	}
}

// StartByDefinition 按流程定义发起实例（业务模块联动入口）。
// stepsOverride 为空时使用定义步骤。
// v0.3：表达式解析 + Notify 配置快照 + 首步审批人通知。
// v0.4：vars 为条件网关求值变量（快照到实例）。
func (m *Module) StartByDefinition(ctx context.Context, defKey, businessKey, summary, creatorID string, stepsOverride []Step) (Instance, error) {
	return m.StartByDefinitionWithVars(ctx, defKey, businessKey, summary, creatorID, stepsOverride, nil)
}

// StartByDefinitionWithVars 同 StartByDefinition，额外携带条件网关变量。
func (m *Module) StartByDefinitionWithVars(ctx context.Context, defKey, businessKey, summary, creatorID string, stepsOverride []Step, vars map[string]string) (Instance, error) {
	def, err := m.defs.GetDefByKey(ctx, defKey)
	if err != nil {
		return Instance{}, err
	}
	if def.Status != "active" {
		return Instance{}, ErrDefDisabled
	}
	steps := def.Steps
	if len(stepsOverride) > 0 {
		steps = stepsOverride
	}
	// 表达式解析（快照化：解析结果固化为 Approvers）。
	steps, err = m.resolveSteps(ctx, steps, creatorID)
	if err != nil {
		return Instance{}, err
	}
	in := Instance{
		ID: NewInstanceID(), BusinessKey: businessKey, Summary: summary,
		DefinitionID: def.ID, Steps: steps, StepIndex: 0,
		CreatorID: creatorID, Status: StatusPending, CreatedAt: time.Now().UTC(),
		Notify: def.Notify, // 通知配置快照
		Vars:   vars,
	}
	if err := m.store.Create(ctx, in); err != nil {
		return Instance{}, err
	}
	// 回读规范化后的实例（条件网关跳转/并行分支初始化在 Create 内完成）
	if created, _, err := m.store.Get(ctx, in.ID); err == nil {
		in = created
	}
	m.record(ctx, creatorID, "instance.created", in.ID, in.Status)
	// 通知当前步审批人（条件网关可能已跳转，并行网关为各分支并集）
	if step := in.CurrentStep(); step != nil && len(in.Approvers) > 0 {
		m.notifyEvent(ctx, NotifyEvent{
			Type: NotifyStepEnter, InstanceID: in.ID, Summary: in.Summary,
			BizKey: in.BusinessKey, StepName: step.Name, StepIndex: in.StepIndex,
			Recipients: in.Approvers, CreatorID: creatorID, ActorID: creatorID,
		}, in.Notify)
	}
	return in, nil
}

func (*Module) Name() string           { return "workflow" }
func (*Module) Version() string        { return Version }
func (*Module) Dependencies() []string { return []string{"identity", "audit"} }

func (m *Module) Init(_ context.Context, a *app.App) error {
	idm, ok := a.Registry().MustGet("identity").(*identity.Module)
	if !ok {
		return errors.New("workflow: identity module not available")
	}
	adm, ok := a.Registry().MustGet("audit").(*audit.Module)
	if !ok {
		return errors.New("workflow: audit module not available")
	}
	m.facade = idm.Facade()
	m.recorder = adm.Recorder()

	auth := m.facade.Auth
	req := m.facade.Require
	g := a.Router().Group("/api/v1/wf")

	g.Handle(http.MethodPost, "/instances", auth(req(PermCreate, m.createInstance)))
	g.Handle(http.MethodGet, "/instances/{id}", auth(req(PermRead, m.getInstance)))
	g.Handle(http.MethodPost, "/instances/{id}/approve", auth(m.act("approve")))
	g.Handle(http.MethodPost, "/instances/{id}/reject", auth(m.act("reject")))
	g.Handle(http.MethodPost, "/instances/{id}/cancel", auth(m.cancel))
	g.Handle(http.MethodGet, "/todo", auth(m.myTodo))
	g.Handle(http.MethodGet, "/cc", auth(m.myCC)) // 我的被抄送（只读知会）
	g.Handle(http.MethodGet, "/instances/mine", auth(m.myInstances))
	g.Handle(http.MethodGet, "/instances/all", auth(req(PermRead, m.allInstances)))
	g.Handle(http.MethodPost, "/instances/{id}/delegate", auth(m.delegate))
	g.Handle(http.MethodPost, "/instances/by-definition/{key}", auth(m.startByDef)) // 发起自己的流程：登录即可

	// 第一批六项操作扩展（契约 workflow-api-contract.md v1）
	g.Handle(http.MethodPost, "/instances/{id}/cc", auth(m.ccUser))
	g.Handle(http.MethodPost, "/instances/{id}/delegate-act", auth(m.delegateAct))
	g.Handle(http.MethodPost, "/instances/{id}/addsign", auth(m.addsign))
	g.Handle(http.MethodPost, "/instances/{id}/reject-to", auth(m.rejectTo))

	// 流程定义（模板）
	g.Handle(http.MethodGet, "/definitions", auth(req(PermRead, m.listDefs)))
	g.Handle(http.MethodPost, "/definitions", auth(req(PermDefManage, m.createDef)))
	g.Handle(http.MethodPatch, "/definitions/{id}", auth(req(PermDefManage, m.updateDef)))
	g.Handle(http.MethodDelete, "/definitions/{id}", auth(req(PermDefManage, m.deleteDef)))
	return nil
}

func (*Module) Start(context.Context) error { return nil }
func (*Module) Stop(context.Context) error  { return nil }

type createInstanceReq struct {
	BusinessKey string   `json:"business_key"`
	Summary     string   `json:"summary"`
	Approvers   []string `json:"approvers"`
	Mode        string   `json:"mode"`
	// Steps 多步直发（可选；与 Approvers 单步形态二选一，Steps 优先）。
	// 每步 mode 支持 countersign | or-sign | sequential（契约 §1）；
	// 步骤 type 支持 approval | condition | parallel（v0.4 网关语义）。
	Steps []Step            `json:"steps"`
	Vars  map[string]string `json:"vars"`
}

func (m *Module) createInstance(w http.ResponseWriter, r *http.Request) {
	var req createInstanceReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		web.Err(w, http.StatusBadRequest, "invalid json body")
		return
	}
	u, err := m.facade.User(r)
	if err != nil {
		web.Err(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if req.BusinessKey == "" || req.Summary == "" {
		web.Err(w, http.StatusBadRequest, "business_key and summary are required")
		return
	}
	// 表达式解析
	steps, err := m.resolveSteps(r.Context(), req.Steps, u.ID)
	if err != nil {
		web.Err(w, http.StatusBadRequest, err.Error())
		return
	}
	in := Instance{
		ID: NewInstanceID(), BusinessKey: req.BusinessKey, Summary: req.Summary,
		Mode: req.Mode, Approvers: req.Approvers, CreatorID: u.ID,
		Steps:  steps,
		Status: StatusPending, CreatedAt: time.Now().UTC(),
		Vars: req.Vars,
	}
	if err := m.store.Create(r.Context(), in); err != nil {
		if isBadRequestErr(err) {
			web.Err(w, http.StatusBadRequest, err.Error())
			return
		}
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	// 回读规范化后的实例（条件网关跳转/并行分支初始化在 Create 内完成）
	if created, _, err := m.store.Get(r.Context(), in.ID); err == nil {
		in = created
	}
	m.record(r.Context(), u.ID, "instance.created", in.ID, in.BusinessKey)
	web.Created(w, in)
}

// isBadRequestErr 判定是否为请求参数/流程配置类错误（映射 400）。
func isBadRequestErr(err error) bool {
	for _, e := range []error{
		ErrBadMode, ErrNoApprovers, ErrNoSteps, ErrBadStepType,
		ErrBadGateway, ErrNoBranch, ErrBadBranch,
	} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

func (m *Module) getInstance(w http.ResponseWriter, r *http.Request) {
	in, actions, err := m.store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			web.Err(w, http.StatusNotFound, "instance not found")
			return
		}
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.OK(w, map[string]any{"instance": in, "actions": actions})
}

// act 是审批动作（approve/reject）的通用处理器：仅审批人可操作。
// v0.3：审批后触发流转通知（下一步审批人 / 终态通知发起人+抄送人）。
func (m *Module) act(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Comment string `json:"comment"`
			Branch  *int   `json:"branch"` // 并行网关分支下标（可选）
		}
		_ = json.NewDecoder(r.Body).Decode(&req) // body 可空
		u, err := m.facade.User(r)
		if err != nil {
			web.Err(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		instanceID := r.PathValue("id")
		branch := -1
		if req.Branch != nil {
			branch = *req.Branch
		}
		// 取审批前状态（分支推进/通知对比用）
		prev, prevActions, _ := m.store.Get(r.Context(), instanceID)

		in, err := m.store.ActBranch(r.Context(), instanceID, u.ID, kind, req.Comment, branch)
		switch {
		case errors.Is(err, ErrNotFound):
			web.Err(w, http.StatusNotFound, "instance not found")
			return
		case errors.Is(err, ErrNotApprover):
			web.Err(w, http.StatusForbidden, "not an approver of this instance")
			return
		case errors.Is(err, ErrNotTurn):
			web.Err(w, http.StatusForbidden, ErrNotTurn.Error())
			return
		case errors.Is(err, ErrBadBranch), errors.Is(err, ErrNotSupported):
			web.Err(w, http.StatusBadRequest, err.Error())
			return
		case errors.Is(err, ErrAlreadyDone):
			web.Err(w, http.StatusConflict, "instance already finished or already acted")
			return
		case err != nil:
			web.Err(w, http.StatusInternalServerError, "internal error")
			return
		}
		m.record(r.Context(), u.ID, "instance."+kind, in.ID, in.Status)
		m.fireFinish(r.Context(), in)
		m.notifyAfterAct(r.Context(), in, u.ID, kind, req.Comment, prev, prevActions)
		web.OK(w, in)
	}
}

// notifyAfterAct 审批动作后的通知分发。
func (m *Module) notifyAfterAct(ctx context.Context, in Instance, actorID, kind, comment string, prev Instance, prevActions []Action) {
	base := NotifyEvent{
		InstanceID: in.ID, Summary: in.Summary, BizKey: in.BusinessKey,
		CreatorID: in.CreatorID, ActorID: actorID, Comment: comment,
	}
	switch {
	case in.Status == StatusApproved:
		// 终态通过：通知发起人 + 抄送人
		recipients := append([]string{in.CreatorID}, in.CCUserIDs...)
		base.Type = NotifyApproved
		base.Recipients = recipients
		m.notifyEvent(ctx, base, in.Notify)
	case in.Status == StatusRejected:
		// 终态驳回：通知发起人
		base.Type = NotifyRejected
		base.Recipients = []string{in.CreatorID}
		m.notifyEvent(ctx, base, in.Notify)
	case in.Status == StatusPending:
		// 仅通知"新进入待审"的人：并行分支推进到下一子步、
		// 全部分支完成后进入后续步骤；会签中间态不重复打扰。
		_, actions, err := m.store.Get(ctx, in.ID)
		if err != nil {
			return
		}
		newPending := in.PendingApprovers(actions)
		prevPending := prev.PendingApprovers(prevActions)
		var recipients []string
		for _, uid := range newPending {
			if uid == actorID || contains(prevPending, uid) {
				continue
			}
			recipients = append(recipients, uid)
		}
		if len(recipients) == 0 {
			return
		}
		base.Type = NotifyStepEnter
		base.Recipients = recipients
		base.StepIndex = in.StepIndex
		if step := in.CurrentStep(); step != nil {
			base.StepName = step.Name
		}
		m.notifyEvent(ctx, base, in.Notify)
	}
}

func (m *Module) cancel(w http.ResponseWriter, r *http.Request) {
	u, err := m.facade.User(r)
	if err != nil {
		web.Err(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	in, err := m.store.Act(r.Context(), r.PathValue("id"), u.ID, "cancel", "")
	switch {
	case errors.Is(err, ErrNotFound):
		web.Err(w, http.StatusNotFound, "instance not found")
		return
	case errors.Is(err, ErrNotCreator):
		web.Err(w, http.StatusForbidden, "only the creator can cancel")
		return
	case errors.Is(err, ErrAlreadyDone):
		web.Err(w, http.StatusConflict, "instance already finished")
		return
	case err != nil:
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	m.record(r.Context(), u.ID, "instance.canceled", in.ID, in.Status)
	m.fireFinish(r.Context(), in)
	// 撤回：通知当前步审批人（待办消除）
	m.notifyEvent(r.Context(), NotifyEvent{
		Type: NotifyCanceled, InstanceID: in.ID, Summary: in.Summary,
		BizKey: in.BusinessKey, CreatorID: in.CreatorID, ActorID: u.ID,
		Recipients: in.Approvers,
	}, in.Notify)
	web.OK(w, in)
}

func (m *Module) myTodo(w http.ResponseWriter, r *http.Request) {
	u, err := m.facade.User(r)
	if err != nil {
		web.Err(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	list, err := m.store.ListTodo(r.Context(), u.ID)
	if err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.OK(w, map[string]any{"count": len(list), "items": list})
}

// record 审批动作写入防篡改审计链。
func (m *Module) record(ctx context.Context, actor, event, instanceID, status string) {
	if m.recorder == nil {
		return
	}
	detail, _ := json.Marshal(map[string]string{"instance_id": instanceID, "status": status})
	_ = m.recorder.Record(ctx, audit.Event{
		Module: audit.ModWorkflow, Event: event, ActorID: actor,
		Resource: instanceID, Detail: detail, Time: time.Now().UTC(),
	})
}

// Describe 实现模块自描述。
func (*Module) Describe() app.ModuleInfo {
	return app.ModuleInfo{
		Name:        "workflow",
		Version:     Version,
		Permissions: []string{PermCreate, PermRead, PermDefManage},
	}
}

// myInstances 我发起的实例（全部状态，登录即可）。
func (m *Module) myInstances(w http.ResponseWriter, r *http.Request) {
	u, err := m.facade.User(r)
	if err != nil {
		web.Err(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	list, err := m.store.ListByCreator(r.Context(), u.ID)
	if err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.OK(w, map[string]any{"count": len(list), "items": list})
}

// allInstances 全量实例（workflow:read）。
func (m *Module) allInstances(w http.ResponseWriter, r *http.Request) {
	list, err := m.store.ListAll(r.Context())
	if err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.OK(w, map[string]any{"count": len(list), "items": list})
}

// Store 供业务模块读取实例。
func (m *Module) Store() Store { return m.store }

// Defs 供业务模块读取定义。
func (m *Module) Defs() DefStore { return m.defs }

func (m *Module) delegate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ToUserID string `json:"to_user_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	u, err := m.facade.User(r)
	if err != nil || req.ToUserID == "" {
		web.Err(w, http.StatusBadRequest, "to_user_id required")
		return
	}
	in, err := m.store.Delegate(r.Context(), r.PathValue("id"), u.ID, req.ToUserID)
	switch {
	case errors.Is(err, ErrNotFound):
		web.Err(w, http.StatusNotFound, "instance not found")
		return
	case errors.Is(err, ErrNotApprover):
		web.Err(w, http.StatusForbidden, "not an approver of this instance")
		return
	case errors.Is(err, ErrAlreadyDone):
		web.Err(w, http.StatusConflict, "instance already finished")
		return
	case errors.Is(err, ErrBadDelegate):
		web.Err(w, http.StatusBadRequest, "delegate target invalid")
		return
	case err != nil:
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	m.record(r.Context(), u.ID, "instance.delegated", in.ID, "to "+req.ToUserID)
	web.OK(w, in)
}

func (m *Module) startByDef(w http.ResponseWriter, r *http.Request) {
	u, err := m.facade.User(r)
	if err != nil {
		web.Err(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var req struct {
		BusinessKey string            `json:"business_key"`
		Summary     string            `json:"summary"`
		Steps       []Step            `json:"steps"` // 可选：覆盖定义步骤（如动态审批人）
		Vars        map[string]string `json:"vars"`  // 条件网关求值变量
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.BusinessKey == "" {
		web.Err(w, http.StatusBadRequest, "business_key is required")
		return
	}
	in, err := m.StartByDefinitionWithVars(r.Context(), r.PathValue("key"), req.BusinessKey, req.Summary, u.ID, req.Steps, req.Vars)
	if err != nil {
		switch {
		case errors.Is(err, ErrDefNotFound):
			web.Err(w, http.StatusNotFound, "definition not found")
		case errors.Is(err, ErrDefDisabled):
			web.Err(w, http.StatusConflict, "definition disabled")
		default:
			web.Err(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	web.Created(w, in)
}

func (m *Module) listDefs(w http.ResponseWriter, r *http.Request) {
	defs, err := m.defs.ListDefs(r.Context())
	if err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.OK(w, defs)
}

func (m *Module) createDef(w http.ResponseWriter, r *http.Request) {
	var d Definition
	if err := json.NewDecoder(r.Body).Decode(&d); err != nil {
		web.Err(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if d.Key == "" || d.Name == "" {
		web.Err(w, http.StatusBadRequest, "key and name are required")
		return
	}
	if len(d.Steps) > 0 {
		steps, err := NormalizeSteps(d.Steps)
		if err != nil {
			web.Err(w, http.StatusBadRequest, err.Error())
			return
		}
		d.Steps = steps
	}
	d.ID = NewInstanceID()
	if err := m.defs.CreateDef(r.Context(), d); err != nil {
		if errors.Is(err, ErrDefKeyUsed) {
			web.Err(w, http.StatusConflict, "definition key already taken")
			return
		}
		web.Err(w, http.StatusBadRequest, err.Error())
		return
	}
	web.Created(w, d)
}

func (m *Module) updateDef(w http.ResponseWriter, r *http.Request) {
	d, err := m.defs.GetDef(r.Context(), r.PathValue("id"))
	if err != nil {
		web.Err(w, http.StatusNotFound, "definition not found")
		return
	}
	var req struct {
		Name   string      `json:"name"`
		Status string      `json:"status"`
		Steps  []Step      `json:"steps"`
		Notify *NotifySpec `json:"notify"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		web.Err(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if req.Name != "" {
		d.Name = req.Name
	}
	if req.Status != "" {
		d.Status = req.Status
	}
	if req.Steps != nil {
		steps, err := NormalizeSteps(req.Steps)
		if err != nil {
			web.Err(w, http.StatusBadRequest, err.Error())
			return
		}
		d.Steps = steps
	}
	if req.Notify != nil {
		d.Notify = *req.Notify
	}
	if err := m.defs.UpdateDef(r.Context(), d); err != nil {
		web.Err(w, http.StatusBadRequest, err.Error())
		return
	}
	web.OK(w, d)
}

func (m *Module) deleteDef(w http.ResponseWriter, r *http.Request) {
	if err := m.defs.DeleteDef(r.Context(), r.PathValue("id")); err != nil {
		if errors.Is(err, ErrDefNotFound) {
			web.Err(w, http.StatusNotFound, "definition not found")
			return
		}
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.OK(w, nil)
}

// ---------- 六项操作 handler（契约 workflow-api-contract.md v1） ----------

// ccUser 抄送（当前步审批人或创建人）。
func (m *Module) ccUser(w http.ResponseWriter, r *http.Request) {
	u, err := m.facade.User(r)
	if err != nil {
		web.Err(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var req struct {
		UserIDs []string `json:"user_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.UserIDs) == 0 {
		web.Err(w, http.StatusBadRequest, "user_ids required")
		return
	}
	in, err := m.store.CC(r.Context(), r.PathValue("id"), u.ID, req.UserIDs)
	if err != nil {
		m.wfErr(w, err)
		return
	}
	m.record(r.Context(), u.ID, "instance.cc", in.ID, fmt.Sprintf("%d users", len(req.UserIDs)))
	// 抄送通知
	m.notifyEvent(r.Context(), NotifyEvent{
		Type: NotifyCC, InstanceID: in.ID, Summary: in.Summary,
		BizKey: in.BusinessKey, CreatorID: in.CreatorID, ActorID: u.ID,
		Recipients: req.UserIDs,
	}, in.Notify)
	web.OK(w, in)
}

// delegateAct 委派（一次性代办，与转办 /delegate 语义对照见契约 §4）。
func (m *Module) delegateAct(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ToUserID string `json:"to_user_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	u, err := m.facade.User(r)
	if err != nil || req.ToUserID == "" {
		web.Err(w, http.StatusBadRequest, "to_user_id required")
		return
	}
	in, err := m.store.DelegateAct(r.Context(), r.PathValue("id"), u.ID, req.ToUserID)
	if err != nil {
		m.wfErr(w, err)
		return
	}
	m.record(r.Context(), u.ID, "instance.delegated-act", in.ID, "to "+req.ToUserID)
	web.OK(w, in)
}

// addsign 加签（before/after，当前步审批人）。
func (m *Module) addsign(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Position  string   `json:"position"`
		Approvers []string `json:"approvers"`
		Mode      string   `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		web.Err(w, http.StatusBadRequest, "invalid json body")
		return
	}
	u, err := m.facade.User(r)
	if err != nil {
		web.Err(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	in, err := m.store.AddSign(r.Context(), r.PathValue("id"), u.ID, req.Position, req.Approvers, req.Mode)
	if err != nil {
		m.wfErr(w, err)
		return
	}
	m.record(r.Context(), u.ID, "instance.addsign", in.ID, req.Position)
	web.OK(w, in)
}

// rejectTo 驳回至任意已过节点（当前步审批人）。
func (m *Module) rejectTo(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TargetStep int    `json:"target_step"`
		Comment    string `json:"comment"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		web.Err(w, http.StatusBadRequest, "invalid json body")
		return
	}
	u, err := m.facade.User(r)
	if err != nil {
		web.Err(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	in, err := m.store.RejectTo(r.Context(), r.PathValue("id"), u.ID, req.TargetStep, req.Comment)
	if err != nil {
		m.wfErr(w, err)
		return
	}
	m.record(r.Context(), u.ID, "instance.reject-to", in.ID, fmt.Sprintf("step %d round %d", req.TargetStep, in.Round))
	m.fireFinish(r.Context(), in)
	web.OK(w, in)
}

// myCC 被抄送列表（登录即可）。
func (m *Module) myCC(w http.ResponseWriter, r *http.Request) {
	u, err := m.facade.User(r)
	if err != nil {
		web.Err(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	list, err := m.store.ListCC(r.Context(), u.ID)
	if err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.OK(w, map[string]any{"count": len(list), "items": list})
}

// wfErr 统一错误映射（六操作共用）。
func (m *Module) wfErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		web.Err(w, http.StatusNotFound, "instance not found")
	case errors.Is(err, ErrAlreadyDone):
		web.Err(w, http.StatusConflict, "instance already finished")
	case errors.Is(err, ErrNotApprover), errors.Is(err, ErrNotTurn):
		web.Err(w, http.StatusForbidden, err.Error())
	case errors.Is(err, ErrBadAddSign), errors.Is(err, ErrBadRejectTo),
		errors.Is(err, ErrBadDelegate), errors.Is(err, ErrBadMode), errors.Is(err, ErrNoSteps):
		web.Err(w, http.StatusBadRequest, err.Error())
	default:
		web.Err(w, http.StatusBadRequest, err.Error())
	}
}
