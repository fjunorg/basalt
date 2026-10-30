// Package workflow 是 L1 流程与审批模块（M2 业务核心）。
//
// v0.2：流程定义（多级串行步骤）+ 按定义发起 + 转办 + 完成回调。
// 步骤内会签/或签，步骤间串行推进；业务单据（财务等）经 OnFinish
// 订阅审批结果实现联动。完整 BPMN（网关/并行/条件路由）仍出主线，
// Flowable sidecar 集成维持原规划。
package workflow

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"
)

// 实例状态。
const (
	StatusPending  = "pending"
	StatusApproved = "approved"
	StatusRejected = "rejected"
	StatusCanceled = "canceled"
)

// 审批模式。
const (
	ModeCountersign = "countersign" // 会签：本步全员通过才进入下一步
	ModeOrSign      = "or-sign"     // 或签：本步任一动作即定局
)

var (
	ErrNotFound    = errors.New("workflow: instance not found")
	ErrNotApprover = errors.New("workflow: not an approver of this instance")
	ErrAlreadyDone = errors.New("workflow: instance already finished")
	ErrNotCreator  = errors.New("workflow: only creator can cancel")
	ErrBadMode     = errors.New("workflow: mode must be countersign or or-sign")
	ErrNoApprovers = errors.New("workflow: at least one approver required")
	ErrNoSteps     = errors.New("workflow: at least one step required")
	ErrDefNotFound = errors.New("workflow: definition not found")
	ErrDefKeyUsed  = errors.New("workflow: definition key already taken")
	ErrDefDisabled = errors.New("workflow: definition disabled")
	ErrBadDelegate = errors.New("workflow: delegate target invalid")
)

// Step 是流程的一个审批步骤（步骤内多人，步骤间串行）。
type Step struct {
	Name      string   `json:"name"`
	Approvers []string `json:"approvers"`
	Mode      string   `json:"mode"`
}

// Instance 是一次流程实例。Steps 为展开后的串行步骤；
// Approvers/Mode 为当前步骤的兼容视图字段。
type Instance struct {
	ID           string     `json:"id" gorm:"primaryKey;size:40"`
	BusinessKey  string     `json:"business_key" gorm:"size:64;index"`
	Summary      string     `json:"summary" gorm:"size:255"`
	DefinitionID string     `json:"definition_id,omitempty" gorm:"size:40;index"`
	Steps        []Step     `json:"steps" gorm:"serializer:json"`
	StepIndex    int        `json:"step_index"`
	CreatorID    string     `json:"creator_id" gorm:"size:40;index"`
	Status       string     `json:"status" gorm:"size:16;index"`
	CreatedAt    time.Time  `json:"created_at"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
	// 兼容视图：当前步审批人与模式（单步场景即全部审批人）。
	Approvers []string `json:"approvers" gorm:"serializer:json"`
	Mode      string   `json:"mode" gorm:"size:16"`
}

// CurrentStep 返回当前待办步骤。
func (in *Instance) CurrentStep() *Step {
	if in.StepIndex < len(in.Steps) {
		return &in.Steps[in.StepIndex]
	}
	return nil
}

// RefreshView 同步兼容视图字段到当前步。
func (in *Instance) RefreshView() {
	if s := in.CurrentStep(); s != nil {
		in.Approvers, in.Mode = s.Approvers, s.Mode
	} else {
		in.Approvers, in.Mode = nil, ""
	}
}

// normalizeSteps 校验并规范化步骤（默认会签）。
func NormalizeSteps(steps []Step) ([]Step, error) {
	if len(steps) == 0 {
		return nil, ErrNoSteps
	}
	out := make([]Step, len(steps))
	for i, s := range steps {
		if len(s.Approvers) == 0 {
			return nil, fmt.Errorf("%w: step %d has no approvers", ErrNoApprovers, i+1)
		}
		if s.Mode == "" {
			s.Mode = ModeCountersign
		}
		if s.Mode != ModeCountersign && s.Mode != ModeOrSign {
			return nil, ErrBadMode
		}
		if s.Name == "" {
			s.Name = fmt.Sprintf("步骤%d", i+1)
		}
		out[i] = s
	}
	return out, nil
}

// Action 是一次审批动作（不可变历史）。StepIndex 标记动作所属步骤。
// Seq 是持久化层的追加顺序主键（内存实现以切片顺序保证），对 API
// 透明（json:"-"）。
type Action struct {
	Seq        int64     `json:"-" gorm:"primaryKey;autoIncrement"`
	InstanceID string    `json:"instance_id" gorm:"size:40;index"`
	ActorID    string    `json:"actor_id" gorm:"size:40"`
	Act        string    `json:"act" gorm:"size:16"` // approve | reject | cancel | delegate
	Comment    string    `json:"comment,omitempty" gorm:"size:255"`
	StepIndex  int       `json:"step_index"`
	Time       time.Time `json:"time"`
}

// Definition 是流程定义（模板）：预编排的审批步骤。
type Definition struct {
	ID       string `json:"id" gorm:"primaryKey;size:40"`
	Key      string `json:"key" gorm:"uniqueIndex;size:64"`
	Name     string `json:"name" gorm:"size:128"`
	Category string `json:"category" gorm:"size:32"` // 对接 dict 流程分类
	Steps    []Step `json:"steps" gorm:"serializer:json"`
	Status   string `json:"status" gorm:"size:16"` // active | disabled
}

// Store 抽象实例持久化；DefStore 抽象定义持久化。
type Store interface {
	Create(ctx context.Context, in Instance) error
	Get(ctx context.Context, id string) (Instance, []Action, error)
	Act(ctx context.Context, instanceID, actorID, act, comment string) (Instance, error)
	Delegate(ctx context.Context, instanceID, fromUserID, toUserID string) (Instance, error)
	ListTodo(ctx context.Context, userID string) ([]Instance, error)
	ListByCreator(ctx context.Context, userID string) ([]Instance, error)
	ListAll(ctx context.Context) ([]Instance, error)
}

type DefStore interface {
	CreateDef(ctx context.Context, d Definition) error
	UpdateDef(ctx context.Context, d Definition) error
	DeleteDef(ctx context.Context, id string) error
	GetDef(ctx context.Context, id string) (Definition, error)
	GetDefByKey(ctx context.Context, key string) (Definition, error)
	ListDefs(ctx context.Context) ([]Definition, error)
}

// MemStore 内存实现（实例 + 定义）。
type MemStore struct {
	mu   sync.Mutex
	byID map[string]*record
}

type record struct {
	inst    Instance
	actions []Action
}

func NewMemStore() *MemStore { return &MemStore{byID: map[string]*record{}} }

// PrepareInstance 规范化实例（兼容包装 + 步骤校验 + 视图同步），
// 供各持久化实现的 Create 路径复用（语义单一来源）。
func PrepareInstance(in *Instance) error {
	// 兼容：无 Steps 但给了 Approvers → 包装为单步。
	if len(in.Steps) == 0 && len(in.Approvers) > 0 {
		in.Steps = []Step{{Approvers: in.Approvers, Mode: in.Mode}}
	}
	steps, err := NormalizeSteps(in.Steps)
	if err != nil {
		return err
	}
	in.Steps = steps
	in.RefreshView()
	return nil
}

func (s *MemStore) Create(_ context.Context, in Instance) error {
	if err := PrepareInstance(&in); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byID[in.ID] = &record{inst: in}
	return nil
}

func (s *MemStore) Get(_ context.Context, id string) (Instance, []Action, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.byID[id]
	if !ok {
		return Instance{}, nil, ErrNotFound
	}
	return r.inst, append([]Action(nil), r.actions...), nil
}

// Act 执行动作并推进状态机（拒绝/撤回直接终态；通过则按步推进）。
func (s *MemStore) Act(_ context.Context, instanceID, actorID, act, comment string) (Instance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.byID[instanceID]
	if !ok {
		return Instance{}, ErrNotFound
	}
	in, action, err := ApplyAction(r.inst, r.actions, actorID, act, comment)
	if err != nil {
		return Instance{}, err
	}
	action.InstanceID = instanceID
	r.inst, r.actions = in, append(r.actions, action)
	return in, nil
}

// ApplyAction 是审批动作的状态机核心（单一事实来源，内存与 GORM
// 持久化实现共用）：对实例与动作历史的不可变快照执行动作，返回
// 推进后的实例副本与应追加的动作记录；不触碰任何存储。
// 注意 Action.StepIndex 记录动作完成后实例所处步骤（沿用历史语义：
// 触发本步完成的通过动作记新步序号）。
func ApplyAction(in Instance, actions []Action, actorID, act, comment string) (Instance, Action, error) {
	switch act {
	case "cancel":
		if in.CreatorID != actorID {
			return Instance{}, Action{}, ErrNotCreator
		}
		if in.Status != StatusPending {
			return Instance{}, Action{}, ErrAlreadyDone
		}
		in.Status, in.FinishedAt = StatusCanceled, now()
	case "approve", "reject":
		if in.Status != StatusPending {
			return Instance{}, Action{}, ErrAlreadyDone
		}
		step := in.CurrentStep()
		if step == nil || !contains(step.Approvers, actorID) {
			return Instance{}, Action{}, ErrNotApprover
		}
		if acted(actions, actorID, in.StepIndex) {
			return Instance{}, Action{}, ErrAlreadyDone
		}
		if act == "reject" {
			in.Status, in.FinishedAt = StatusRejected, now()
		} else if step.Mode == ModeOrSign || allApproved(step.Approvers, actions, in.StepIndex, actorID) {
			// 本步完成 → 推进下一步或终态
			in.StepIndex++
			if in.StepIndex >= len(in.Steps) {
				in.Status, in.FinishedAt = StatusApproved, now()
			}
		}
	default:
		return Instance{}, Action{}, fmt.Errorf("workflow: unknown act %q", act)
	}
	in.RefreshView()
	return in, Action{
		ActorID: actorID, Act: act, Comment: comment,
		StepIndex: in.StepIndex, Time: time.Now().UTC(),
	}, nil
}

// Delegate 转办：当前步审批人 from 将自己的审批权转让给 to
// （替换当前步审批人名单，历史动作保留可审计）。
func (s *MemStore) Delegate(_ context.Context, instanceID, fromUserID, toUserID string) (Instance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.byID[instanceID]
	if !ok {
		return Instance{}, ErrNotFound
	}
	in, action, err := ApplyDelegate(r.inst, fromUserID, toUserID)
	if err != nil {
		return Instance{}, err
	}
	action.InstanceID = instanceID
	r.inst, r.actions = in, append(r.actions, action)
	return in, nil
}

// ApplyDelegate 是转办的状态机核心（与 ApplyAction 同理，内存与
// GORM 持久化实现共用），不触碰任何存储。
func ApplyDelegate(in Instance, fromUserID, toUserID string) (Instance, Action, error) {
	if in.Status != StatusPending {
		return Instance{}, Action{}, ErrAlreadyDone
	}
	step := in.CurrentStep()
	if step == nil || !contains(step.Approvers, fromUserID) {
		return Instance{}, Action{}, ErrNotApprover
	}
	if toUserID == "" || contains(step.Approvers, toUserID) {
		return Instance{}, Action{}, ErrBadDelegate
	}
	for i, uid := range step.Approvers {
		if uid == fromUserID {
			step.Approvers[i] = toUserID
			break
		}
	}
	in.RefreshView()
	return in, Action{
		ActorID: fromUserID, Act: "delegate",
		Comment: "-> " + toUserID, StepIndex: in.StepIndex, Time: time.Now().UTC(),
	}, nil
}

func (s *MemStore) ListTodo(_ context.Context, userID string) ([]Instance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Instance
	for _, r := range s.byID {
		if r.inst.Status != StatusPending {
			continue
		}
		step := r.inst.CurrentStep()
		if step != nil && contains(step.Approvers, userID) && !acted(r.actions, userID, r.inst.StepIndex) {
			out = append(out, r.inst)
		}
	}
	return out, nil
}

func (s *MemStore) ListByCreator(_ context.Context, userID string) ([]Instance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Instance
	for _, r := range s.byID {
		if r.inst.CreatorID == userID {
			out = append(out, r.inst)
		}
	}
	sortByCreatedDesc(out)
	return out, nil
}

func (s *MemStore) ListAll(_ context.Context) ([]Instance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Instance, 0, len(s.byID))
	for _, r := range s.byID {
		out = append(out, r.inst)
	}
	sortByCreatedDesc(out)
	return out, nil
}

// ---------- 定义的内存实现 ----------

type MemDefStore struct {
	mu    sync.RWMutex
	byID  map[string]Definition
	byKey map[string]string
}

func NewMemDefStore() *MemDefStore {
	return &MemDefStore{byID: map[string]Definition{}, byKey: map[string]string{}}
}

func (s *MemDefStore) CreateDef(_ context.Context, d Definition) error {
	steps, err := NormalizeSteps(d.Steps)
	if err != nil {
		return err
	}
	d.Steps = steps
	if d.Status == "" {
		d.Status = "active"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.byKey[d.Key]; dup {
		return ErrDefKeyUsed
	}
	s.byID[d.ID] = d
	s.byKey[d.Key] = d.ID
	return nil
}

func (s *MemDefStore) UpdateDef(_ context.Context, d Definition) error {
	steps, err := NormalizeSteps(d.Steps)
	if err != nil {
		return err
	}
	d.Steps = steps
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.byID[d.ID]
	if !ok {
		return ErrDefNotFound
	}
	for id, def := range s.byID {
		if def.Key == d.Key && id != d.ID {
			return ErrDefKeyUsed
		}
	}
	delete(s.byKey, old.Key)
	s.byID[d.ID] = d
	s.byKey[d.Key] = d.ID
	return nil
}

func (s *MemDefStore) DeleteDef(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.byID[id]
	if !ok {
		return ErrDefNotFound
	}
	delete(s.byID, id)
	delete(s.byKey, d.Key)
	return nil
}

func (s *MemDefStore) GetDef(_ context.Context, id string) (Definition, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.byID[id]
	if !ok {
		return Definition{}, ErrDefNotFound
	}
	return d, nil
}

func (s *MemDefStore) GetDefByKey(_ context.Context, key string) (Definition, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.byKey[key]
	if !ok {
		return Definition{}, ErrDefNotFound
	}
	return s.byID[id], nil
}

func (s *MemDefStore) ListDefs(_ context.Context) ([]Definition, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Definition, 0, len(s.byID))
	for _, d := range s.byID {
		out = append(out, d)
	}
	return out, nil
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// acted 判断某用户在指定步骤是否已有通过/驳回动作。
func acted(actions []Action, userID string, stepIndex int) bool {
	for _, a := range actions {
		if a.ActorID == userID && a.StepIndex == stepIndex && (a.Act == "approve" || a.Act == "reject") {
			return true
		}
	}
	return false
}

// allApproved 判断本步（含当前用户即将计入的 approve）是否全员通过。
func allApproved(approvers []string, actions []Action, stepIndex int, approving string) bool {
	for _, uid := range approvers {
		if uid == approving {
			continue
		}
		if !acted(actions, uid, stepIndex) {
			return false
		}
	}
	return true
}

func sortByCreatedDesc(list []Instance) {
	sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt.After(list[j].CreatedAt) })
}

func now() *time.Time {
	t := time.Now().UTC()
	return &t
}

func NewInstanceID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("workflow: crypto/rand unavailable: %v", err))
	}
	return hex.EncodeToString(b)
}

// 确保 HTTP 处理器可用的编译期引用。
var _ = http.StatusOK
