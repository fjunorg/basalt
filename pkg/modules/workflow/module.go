package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/basalt-go/basalt/pkg/kernel/app"
	"github.com/basalt-go/basalt/pkg/kernel/web"
	"github.com/basalt-go/basalt/pkg/modules/audit"
	"github.com/basalt-go/basalt/pkg/modules/identity"
)

const Version = "0.2.0"

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
}

func New() *Module {
	return &Module{store: NewMemStore(), defs: NewMemDefStore()}
}

// OnFinish 注册流程终态回调（财务等业务单据联动通道）。
func (m *Module) OnFinish(h func(ctx context.Context, in Instance)) {
	m.onFinish = append(m.onFinish, h)
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
func (m *Module) StartByDefinition(ctx context.Context, defKey, businessKey, summary, creatorID string, stepsOverride []Step) (Instance, error) {
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
	in := Instance{
		ID: NewInstanceID(), BusinessKey: businessKey, Summary: summary,
		DefinitionID: def.ID, Steps: steps, StepIndex: 0,
		CreatorID: creatorID, Status: StatusPending, CreatedAt: time.Now().UTC(),
	}
	if err := m.store.Create(ctx, in); err != nil {
		return Instance{}, err
	}
	m.record(ctx, creatorID, "instance.created", in.ID, in.Status)
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
	g.Handle(http.MethodGet, "/instances/mine", auth(m.myInstances))
	g.Handle(http.MethodGet, "/instances/all", auth(req(PermRead, m.allInstances)))
	g.Handle(http.MethodPost, "/instances/{id}/delegate", auth(m.delegate))
	g.Handle(http.MethodPost, "/instances/by-definition/{key}", auth(m.startByDef)) // 发起自己的流程：登录即可

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
	in := Instance{
		ID: NewInstanceID(), BusinessKey: req.BusinessKey, Summary: req.Summary,
		Mode: req.Mode, Approvers: req.Approvers, CreatorID: u.ID,
		Status: StatusPending, CreatedAt: time.Now().UTC(),
	}
	if err := m.store.Create(r.Context(), in); err != nil {
		if errors.Is(err, ErrBadMode) || errors.Is(err, ErrNoApprovers) {
			web.Err(w, http.StatusBadRequest, err.Error())
			return
		}
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	m.record(r.Context(), u.ID, "instance.created", in.ID, in.BusinessKey)
	web.Created(w, in)
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
func (m *Module) act(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Comment string `json:"comment"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req) // body 可空
		u, err := m.facade.User(r)
		if err != nil {
			web.Err(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		in, err := m.store.Act(r.Context(), r.PathValue("id"), u.ID, kind, req.Comment)
		switch {
		case errors.Is(err, ErrNotFound):
			web.Err(w, http.StatusNotFound, "instance not found")
			return
		case errors.Is(err, ErrNotApprover):
			web.Err(w, http.StatusForbidden, "not an approver of this instance")
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
		web.OK(w, in)
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
		BusinessKey string `json:"business_key"`
		Summary     string `json:"summary"`
		Steps       []Step `json:"steps"` // 可选：覆盖定义步骤（如动态审批人）
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.BusinessKey == "" {
		web.Err(w, http.StatusBadRequest, "business_key is required")
		return
	}
	in, err := m.StartByDefinition(r.Context(), r.PathValue("key"), req.BusinessKey, req.Summary, u.ID, req.Steps)
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
		Name   string `json:"name"`
		Status string `json:"status"`
		Steps  []Step `json:"steps"`
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
		d.Steps = req.Steps
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
