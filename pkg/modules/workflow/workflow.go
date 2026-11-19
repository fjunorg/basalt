// Package workflow 是 L1 流程与审批模块（M2 业务核心）。
//
// v0.2：流程定义（多级串行步骤）+ 按定义发起 + 转办 + 完成回调。
// v0.3：审批人表达式（发起时解析为快照）+ 流转通知。
// v0.4：条件网关（结构化条件 + 发起时变量）与并行网关（分支子链并行）。
// 步骤内会签/或签，步骤间串行推进；业务单据（财务等）经 OnFinish
// 订阅审批结果实现联动。
package workflow

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
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
	ModeSequential  = "sequential"  // 依次：approvers 按序逐人审批，最后一人通过本步完成
)

// 步骤类型（v0.4 网关语义）。
//   - approval：审批步骤（默认，Type 为空按此处理，历史数据兼容）
//   - condition：条件网关，按 Vars 求值跳转（不能作为当前待办步）
//   - parallel：并行网关，各分支子链并行推进，全部分支完成后继续
const (
	StepTypeApproval  = "approval"
	StepTypeCondition = "condition"
	StepTypeParallel  = "parallel"
)

var (
	ErrNotFound     = errors.New("workflow: instance not found")
	ErrNotApprover  = errors.New("workflow: not an approver of this instance")
	ErrNotTurn      = errors.New("workflow: not this sequential step's turn")
	ErrAlreadyDone  = errors.New("workflow: instance already finished")
	ErrNotCreator   = errors.New("workflow: only creator can cancel")
	ErrBadMode      = errors.New("workflow: mode must be countersign, or-sign or sequential")
	ErrNoApprovers  = errors.New("workflow: at least one approver required")
	ErrNoSteps      = errors.New("workflow: at least one step required")
	ErrDefNotFound  = errors.New("workflow: definition not found")
	ErrDefKeyUsed   = errors.New("workflow: definition key already taken")
	ErrDefDisabled  = errors.New("workflow: definition disabled")
	ErrBadDelegate  = errors.New("workflow: delegate target invalid")
	ErrBadCC        = errors.New("workflow: cc user_ids invalid")
	ErrBadAddSign   = errors.New("workflow: addsign invalid (approvers 1-5, position before|after)")
	ErrBadRejectTo  = errors.New("workflow: reject-to target must be a passed step")
	ErrNoDelegation = errors.New("workflow: no active delegation for this actor")
	// v0.4 网关语义
	ErrBadStepType = errors.New("workflow: step type must be approval, condition or parallel")
	ErrBadGateway  = errors.New("workflow: invalid gateway configuration")
	ErrNoBranch    = errors.New("workflow: no condition rule matched")
	ErrBadBranch   = errors.New("workflow: invalid parallel branch")
	ErrNotSupported = errors.New("workflow: operation not supported on a gateway step")
	ErrGatewayLoop  = errors.New("workflow: gateway loop detected")
)

// 条件运算符（结构化条件，全部 AND 组合）。
var condOps = map[string]bool{
	"eq": true, "ne": true, "gt": true, "ge": true,
	"lt": true, "le": true, "in": true, "contains": true,
}

// 步骤上限（防加签失控）。
const MaxSteps = 20

// Delegation 是一次委托代办授权（本步本轮内有效，被委托人使用一次即消耗）。
type Delegation struct {
	StepIndex int    `json:"step_index"`
	Branch    int    `json:"branch,omitempty"` // 并行分支下标（非并行步为 0）
	Round     int    `json:"round"`
	From      string `json:"from"`
	To        string `json:"to"`
	Active    bool   `json:"active"`
}

// Cond 是一条结构化条件（字段 / 运算符 / 值）。
type Cond struct {
	Field string `json:"field"`
	Op    string `json:"op"` // eq ne gt ge lt le in contains
	Value string `json:"value"`
}

// Rule 是条件网关的一条路由规则：Cond 全部满足时跳到 To。
// Cond 为空表示默认规则（无条件兜底）。
type Rule struct {
	To   int    `json:"to"` // 目标步骤下标；-1 表示流程结束
	Cond []Cond `json:"cond,omitempty"`
}

// BranchState 是并行网关中一条分支的运行时状态（实例快照）。
type BranchState struct {
	Branch    int  `json:"branch"`
	StepIndex int  `json:"step_index"` // 分支内当前子步下标
	Done      bool `json:"done"`
}

// Step 是流程的一个步骤。
//   - 审批步骤：Approvers / ApproverExpr / Mode（步骤内多人，步骤间按序）
//   - 条件网关：Type=condition + Rules（发起时按 Vars 求值，一次定型）
//   - 并行网关：Type=parallel + Branches（各分支子链并行，全部完成后继续）
//
// ApproverExpr 是审批人表达式（v0.3），发起实例时解析为 Approvers
// 快照。语法见 approver.go。为空时直接用 Approvers（向后兼容）。
type Step struct {
	Name         string   `json:"name"`
	Approvers    []string `json:"approvers"`
	ApproverExpr string   `json:"approver_expr,omitempty"`
	Mode         string   `json:"mode"`
	// Type 为空等价于 approval（历史数据兼容）。
	Type     string     `json:"type,omitempty"`
	Rules    []Rule     `json:"rules,omitempty"`
	Branches [][]Step   `json:"branches,omitempty"`
}

// Instance 是一次流程实例。Steps 为展开后的串行步骤；
// Approvers/Mode 为当前步骤的兼容视图字段。
// Notify 是通知配置快照（创建时从 Definition 拷贝，运行中不受定义变更影响）。
type Instance struct {
	ID           string       `json:"id" gorm:"primaryKey;size:40"`
	BusinessKey  string       `json:"business_key" gorm:"size:64;index"`
	Summary      string       `json:"summary" gorm:"size:255"`
	DefinitionID string       `json:"definition_id,omitempty" gorm:"size:40;index"`
	Steps        []Step       `json:"steps" gorm:"serializer:json"`
	StepIndex    int          `json:"step_index"`
	Round        int          `json:"round"` // 审批轮次（reject-to 递增；动作按轮次判定已审）
	CreatorID    string       `json:"creator_id" gorm:"size:40;index"`
	Status       string       `json:"status" gorm:"size:16;index"`
	CreatedAt    time.Time    `json:"created_at"`
	FinishedAt   *time.Time   `json:"finished_at,omitempty"`
	CCUserIDs    []string     `json:"cc_user_ids,omitempty" gorm:"serializer:json"`
	Delegations  []Delegation `json:"delegations,omitempty" gorm:"serializer:json"`
	Notify       NotifySpec   `json:"notify,omitempty" gorm:"serializer:json"`
	// Vars 是发起时提交的业务变量快照（条件网关求值依据）。
	Vars map[string]string `json:"vars,omitempty" gorm:"serializer:json"`
	// Branches 是并行网关的分支运行时状态（非并行步为空）。
	Branches []BranchState `json:"branches,omitempty" gorm:"serializer:json"`
	// 兼容视图：当前步审批人与模式（单步场景即全部审批人；并行步为各分支待办审批人并集）。
	Approvers []string `json:"approvers" gorm:"serializer:json"`
	Mode      string   `json:"mode" gorm:"size:16"`
}

// CurrentStep 返回当前步骤（可能是网关步骤）。
func (in *Instance) CurrentStep() *Step {
	if in.StepIndex >= 0 && in.StepIndex < len(in.Steps) {
		return &in.Steps[in.StepIndex]
	}
	return nil
}

// BranchStep 返回并行网关某分支的当前子步；分支已完成或非法时返回 nil。
func (in *Instance) BranchStep(bs *BranchState) *Step {
	if bs == nil || bs.Done || bs.Branch < 0 {
		return nil
	}
	st := in.CurrentStep()
	if st == nil || st.Type != StepTypeParallel || bs.Branch >= len(st.Branches) {
		return nil
	}
	br := st.Branches[bs.Branch]
	if bs.StepIndex < 0 || bs.StepIndex >= len(br) {
		return nil
	}
	return &br[bs.StepIndex]
}

// TodoFor 判定用户在该实例上是否有待办，并返回待办所在分支
// （-1 无待办；非并行步为 0；并行步为分支下标）。
func (in *Instance) TodoFor(userID string, actions []Action) (bool, int) {
	st := in.CurrentStep()
	if st == nil {
		return false, -1
	}
	if st.Type != StepTypeParallel {
		if contains(st.Approvers, userID) && !acted(actions, userID, in.StepIndex, in.Round, 0) {
			return true, 0
		}
		return false, -1
	}
	for i := range in.Branches {
		bs := &in.Branches[i]
		sub := in.BranchStep(bs)
		if sub == nil {
			continue
		}
		if contains(sub.Approvers, userID) &&
			!acted(actions, userID, in.StepIndex, in.Round, bs.Branch) {
			return true, bs.Branch
		}
	}
	return false, -1
}

// PendingApprovers 返回当前仍未审批的人（并行网关为各分支当前子步
// 未审人并集；依次审批只返回当前轮到的人）。
func (in *Instance) PendingApprovers(actions []Action) []string {
	st := in.CurrentStep()
	if st == nil {
		return nil
	}
	var out []string
	add := func(ids []string, branch int) {
		for _, uid := range ids {
			if !acted(actions, uid, in.StepIndex, in.Round, branch) && !contains(out, uid) {
				out = append(out, uid)
			}
		}
	}
	if st.Type != StepTypeParallel {
		if st.Mode == ModeSequential {
			if turn := sequentialTurn(st, actions, in.StepIndex, in.Round, 0); turn != "" {
				return []string{turn}
			}
			return nil
		}
		add(st.Approvers, 0)
		return out
	}
	for i := range in.Branches {
		bs := &in.Branches[i]
		sub := in.BranchStep(bs)
		if sub == nil {
			continue
		}
		add(sub.Approvers, bs.Branch)
	}
	return out
}

// RefreshView 同步兼容视图字段到当前步。
func (in *Instance) RefreshView() {
	s := in.CurrentStep()
	if s == nil {
		in.Approvers, in.Mode = nil, ""
		return
	}
	if s.Type == StepTypeParallel {
		var ids []string
		for i := range in.Branches {
			if sub := in.BranchStep(&in.Branches[i]); sub != nil {
				for _, uid := range sub.Approvers {
					if !contains(ids, uid) {
						ids = append(ids, uid)
					}
				}
			}
		}
		in.Approvers, in.Mode = ids, ""
		return
	}
	in.Approvers, in.Mode = s.Approvers, s.Mode
}

// NormalizeSteps 校验并规范化步骤（默认会签；网关按类型校验）。
// 有 ApproverExpr 的审批步骤允许 Approvers 为空（发起时解析）。
func NormalizeSteps(steps []Step) ([]Step, error) {
	if len(steps) == 0 {
		return nil, ErrNoSteps
	}
	out := make([]Step, len(steps))
	for i := range steps {
		s := steps[i]
		if s.Type == "" {
			s.Type = StepTypeApproval
		}
		switch s.Type {
		case StepTypeApproval:
			if len(s.Approvers) == 0 && s.ApproverExpr == "" {
				return nil, fmt.Errorf("%w: step %d has no approvers", ErrNoApprovers, i+1)
			}
			if s.Mode == "" {
				s.Mode = ModeCountersign
			}
			if s.Mode != ModeCountersign && s.Mode != ModeOrSign && s.Mode != ModeSequential {
				return nil, ErrBadMode
			}
			if s.Name == "" {
				s.Name = fmt.Sprintf("步骤%d", i+1)
			}
			s.Rules, s.Branches = nil, nil
		case StepTypeCondition:
			if err := normalizeRules(s.Rules, i, len(steps)); err != nil {
				return nil, err
			}
			s.Approvers, s.ApproverExpr, s.Mode, s.Branches = nil, "", "", nil
			if s.Name == "" {
				s.Name = "条件分支"
			}
		case StepTypeParallel:
			if len(s.Branches) == 0 {
				return nil, fmt.Errorf("%w: parallel step %d needs at least one branch", ErrBadGateway, i+1)
			}
			branches := make([][]Step, len(s.Branches))
			for bi := range s.Branches {
				br := s.Branches[bi]
				if len(br) == 0 {
					return nil, fmt.Errorf("%w: parallel step %d branch %d is empty", ErrBadGateway, i+1, bi+1)
				}
				sub := make([]Step, len(br))
				for si := range br {
					bs := br[si]
					if bs.Type != "" && bs.Type != StepTypeApproval {
						return nil, fmt.Errorf("%w: branch steps must be approval steps", ErrBadGateway)
					}
					bs.Type = StepTypeApproval
					if len(bs.Approvers) == 0 && bs.ApproverExpr == "" {
						return nil, fmt.Errorf("%w: step %d branch %d step %d has no approvers",
							ErrNoApprovers, i+1, bi+1, si+1)
					}
					if bs.Mode == "" {
						bs.Mode = ModeCountersign
					}
					if bs.Mode != ModeCountersign && bs.Mode != ModeOrSign && bs.Mode != ModeSequential {
						return nil, ErrBadMode
					}
					if bs.Name == "" {
						bs.Name = fmt.Sprintf("分支%d-%d", bi+1, si+1)
					}
					bs.Rules, bs.Branches = nil, nil
					sub[si] = bs
				}
				branches[bi] = sub
			}
			s.Branches = branches
			s.Approvers, s.ApproverExpr, s.Mode, s.Rules = nil, "", "", nil
			if s.Name == "" {
				s.Name = "并行网关"
			}
		default:
			return nil, ErrBadStepType
		}
		out[i] = s
	}
	return out, nil
}

// normalizeRules 校验条件网关规则：目标前缀（-1 或大于当前步），
// 必须含一条无条件默认规则，运算符合法。
func normalizeRules(rules []Rule, stepIdx, total int) error {
	if len(rules) == 0 {
		return fmt.Errorf("%w: condition step %d needs at least one rule", ErrBadGateway, stepIdx+1)
	}
	hasDefault := false
	for _, r := range rules {
		if r.To < -1 || r.To >= total {
			return fmt.Errorf("%w: condition step %d target %d out of range", ErrBadGateway, stepIdx+1, r.To)
		}
		if r.To != -1 && r.To <= stepIdx {
			return fmt.Errorf("%w: condition step %d target must be a later step", ErrBadGateway, stepIdx+1)
		}
		if len(r.Cond) == 0 {
			hasDefault = true
			continue
		}
		for _, c := range r.Cond {
			if c.Field == "" || !condOps[c.Op] {
				return fmt.Errorf("%w: condition step %d has invalid condition (%s %s)",
					ErrBadGateway, stepIdx+1, c.Field, c.Op)
			}
		}
	}
	if !hasDefault {
		return fmt.Errorf("%w: condition step %d needs a default rule", ErrBadGateway, stepIdx+1)
	}
	return nil
}

// advance 从当前 StepIndex 起自动推进：条件网关求值跳转、并行网关
// 初始化/收敛、越界则置为通过。审批步骤或待审并行步停在此处。
func advance(in *Instance) error {
	for guard := 0; guard <= len(in.Steps)+1; guard++ {
		if in.StepIndex >= len(in.Steps) {
			in.Status, in.FinishedAt = StatusApproved, now()
			in.Branches = nil
			in.RefreshView()
			return nil
		}
		st := in.CurrentStep()
		switch st.Type {
		case StepTypeCondition:
			to, ok := evalRules(st.Rules, in.Vars)
			if !ok {
				return fmt.Errorf("%w: step %d", ErrNoBranch, in.StepIndex+1)
			}
			in.Branches = nil
			in.StepIndex = to // -1 → 下一轮循环判为结束
			if to < 0 {
				in.StepIndex = len(in.Steps)
			}
		case StepTypeParallel:
			if len(in.Branches) == 0 {
				in.Branches = make([]BranchState, len(st.Branches))
				for i := range st.Branches {
					in.Branches[i] = BranchState{Branch: i, Done: len(st.Branches[i]) == 0}
				}
			}
			allDone := true
			for i := range in.Branches {
				bs := &in.Branches[i]
				if bs.Done {
					continue
				}
				if bs.StepIndex >= len(st.Branches[bs.Branch]) {
					bs.Done = true
					continue
				}
				allDone = false
			}
			if allDone {
				in.StepIndex++
				in.Branches = nil
				continue
			}
			in.RefreshView()
			return nil
		default:
			in.RefreshView()
			return nil
		}
	}
	return ErrGatewayLoop
}

// evalRules 按顺序求值，首条命中的规则生效（默认规则无条件命中）。
func evalRules(rules []Rule, vars map[string]string) (int, bool) {
	for _, r := range rules {
		if matchAll(r.Cond, vars) {
			return r.To, true
		}
	}
	return 0, false
}

// matchAll 全部条件 AND。
func matchAll(conds []Cond, vars map[string]string) bool {
	for _, c := range conds {
		if !matchOne(c, vars[c.Field]) {
			return false
		}
	}
	return true
}

// matchOne 单条件求值。数值运算符在两侧均可解析为数字时按数值比较，
// 否则退化为字符串比较；in 为逗号分隔集合，contains 为子串包含。
func matchOne(c Cond, actual string) bool {
	switch c.Op {
	case "eq":
		return actual == c.Value
	case "ne":
		return actual != c.Value
	case "in":
		for _, v := range strings.Split(c.Value, ",") {
			if strings.TrimSpace(v) == actual {
				return true
			}
		}
		return false
	case "contains":
		return strings.Contains(actual, c.Value)
	case "gt", "ge", "lt", "le":
		av, aerr := strconv.ParseFloat(strings.TrimSpace(actual), 64)
		bv, berr := strconv.ParseFloat(strings.TrimSpace(c.Value), 64)
		if aerr != nil || berr != nil {
			return false
		}
		switch c.Op {
		case "gt":
			return av > bv
		case "ge":
			return av >= bv
		case "lt":
			return av < bv
		default:
			return av <= bv
		}
	}
	return false
}

// Action 是一次审批动作（不可变历史）。StepIndex 标记动作所属步骤；
// Round 标记审批轮次（reject-to 回退后旧轮失效、新轮重审）；非空
// DelegateFrom 表示被委托人代审（名义审批人）。
// Seq 是持久化层的追加顺序主键（内存实现以切片顺序保证），对 API
// 透明（json:"-"）。
type Action struct {
	Seq          int64     `json:"-" gorm:"primaryKey;autoIncrement"`
	InstanceID   string    `json:"instance_id" gorm:"size:40;index"`
	ActorID      string    `json:"actor_id" gorm:"size:40"`
	Act          string    `json:"act" gorm:"size:16"` // approve | reject | cancel | delegate | cc | delegate-act | addsign | reject-to
	Comment      string    `json:"comment,omitempty" gorm:"size:255"`
	StepIndex    int       `json:"step_index"`
	Branch       int       `json:"branch,omitempty"` // 并行分支下标（非并行步为 0）
	Round        int       `json:"round"`
	DelegateFrom string    `json:"delegate_from,omitempty" gorm:"size:40"`
	Time         time.Time `json:"time"`
}

// acted 本轮已审判定（reject-to 后旧轮动作不失效新轮）。
// branch 为并行网关分支下标；非并行步为 0。
func acted(actions []Action, userID string, stepIndex, round, branch int) bool {
	for _, a := range actions {
		if a.ActorID == userID && a.StepIndex == stepIndex && a.Round == round &&
			a.Branch == branch &&
			(a.Act == "approve" || a.Act == "reject") {
			return true
		}
	}
	return false
}

// activeDelegationFor 查询 actor 在本步本轮的有效委托（actor 为被委托人）。
func activeDelegationFor(in *Instance, actorID string, branch int) *Delegation {
	for i := range in.Delegations {
		d := &in.Delegations[i]
		if d.Active && d.To == actorID && d.StepIndex == in.StepIndex &&
			d.Round == in.Round && d.Branch == branch {
			return d
		}
	}
	return nil
}

// sequentialTurn 依次审批当前轮到的人（approvers 中第一个本轮未审者）。
func sequentialTurn(step *Step, actions []Action, stepIndex, round, branch int) string {
	for _, uid := range step.Approvers {
		if !acted(actions, uid, stepIndex, round, branch) {
			return uid
		}
	}
	return ""
}

func allApproved(approvers []string, actions []Action, stepIndex, round, branch int, approving string) bool {
	for _, uid := range approvers {
		if uid == approving {
			continue
		}
		if !acted(actions, uid, stepIndex, round, branch) {
			return false
		}
	}
	return true
}

// Definition 是流程定义（模板）：预编排的审批步骤。
// Notify 是流转通知开关（v0.3），零值全部关闭向后兼容。
// Scope 是适用范围（v0.3）：业务类型 + 部门限定。
type Definition struct {
	ID       string     `json:"id" gorm:"primaryKey;size:40"`
	Key      string     `json:"key" gorm:"uniqueIndex;size:64"`
	Name     string     `json:"name" gorm:"size:128"`
	Category string     `json:"category" gorm:"size:32"` // 对接 dict 流程分类
	Steps    []Step     `json:"steps" gorm:"serializer:json"`
	Status   string     `json:"status" gorm:"size:16"` // active | disabled
	Notify   NotifySpec `json:"notify,omitempty" gorm:"serializer:json"`
}

// Store 抽象实例持久化；DefStore 抽象定义持久化。
type Store interface {
	Create(ctx context.Context, in Instance) error
	Get(ctx context.Context, id string) (Instance, []Action, error)
	Act(ctx context.Context, instanceID, actorID, act, comment string) (Instance, error)
	// ActBranch 指定并行分支执行审批动作（branch<0 表示不指定；
	// 非并行步按 0 处理，并行步必须显式给出）。
	ActBranch(ctx context.Context, instanceID, actorID, act, comment string, branch int) (Instance, error)
	Delegate(ctx context.Context, instanceID, fromUserID, toUserID string) (Instance, error)
	// 第一批六项操作扩展（纯函数在包级共享，实现只做读写映射）
	CC(ctx context.Context, instanceID, actorID string, userIDs []string) (Instance, error)
	DelegateAct(ctx context.Context, instanceID, fromUserID, toUserID string) (Instance, error)
	AddSign(ctx context.Context, instanceID, actorID, position string, approvers []string, mode string) (Instance, error)
	RejectTo(ctx context.Context, instanceID, actorID string, targetStep int, comment string) (Instance, error)
	ListCC(ctx context.Context, userID string) ([]Instance, error)
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
	// 发起即定型：按 Vars 求值条件网关、初始化并行分支；
	// 若从首步即可直达终态（如条件网关直接结束），此处完成。
	return advance(in)
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
func (s *MemStore) Act(ctx context.Context, instanceID, actorID, act, comment string) (Instance, error) {
	return s.ActBranch(ctx, instanceID, actorID, act, comment, -1)
}

// ActBranch 在指定并行分支上执行动作；branch<0 表示主流程步骤。
func (s *MemStore) ActBranch(_ context.Context, instanceID, actorID, act, comment string, branch int) (Instance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.byID[instanceID]
	if !ok {
		return Instance{}, ErrNotFound
	}
	in, action, err := ApplyActionBranch(r.inst, r.actions, actorID, act, comment, branch)
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
// 主流程步骤调用；并行分支上的动作走 ApplyActionBranch。
func ApplyAction(in Instance, actions []Action, actorID, act, comment string) (Instance, Action, error) {
	return ApplyActionBranch(in, actions, actorID, act, comment, -1)
}

// ApplyActionBranch 在指定并行分支执行动作（branch<0 表示主流程步骤）：
// 并行网关中 approve 推进该分支子链，全部分支完成后继续后续步骤；
// reject 仍为整单驳回。
func ApplyActionBranch(in Instance, actions []Action, actorID, act, comment string, branch int) (Instance, Action, error) {
	delegateFrom := ""
	branchAt := 0
	stepAt := in.StepIndex // 动作发生时的步骤（推进前索引——与
	// acted/sequentialTurn 判定空间一致；历史缺陷修复：旧实现记录
	// 推进后索引，多步下"审完 N 推进记 N+1"与"在 N+1 已审"冲突）
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
		top := in.CurrentStep()
		if top == nil {
			return Instance{}, Action{}, ErrAlreadyDone
		}
		// 定位实际待审步骤：并行网关取分支子步，否则取主流程步骤。
		step := top
		effBranch := 0
		parallel := top.Type == StepTypeParallel
		if parallel {
			// 未显式指定分支时按操作者推断（同一人负责多个分支时
			// 取首个未完成分支，重复调用可依次处理）。
			if branch < 0 {
				for i := range in.Branches {
					sub := in.BranchStep(&in.Branches[i])
					if sub == nil {
						continue
					}
					if contains(sub.Approvers, actorID) ||
						activeDelegationFor(&in, actorID, i) != nil {
						branch = i
						break
					}
				}
				if branch < 0 {
					return Instance{}, Action{}, ErrNotApprover
				}
			}
			if branch >= len(in.Branches) {
				return Instance{}, Action{}, ErrBadBranch
			}
			bs := &in.Branches[branch]
			step = in.BranchStep(bs)
			if step == nil {
				return Instance{}, Action{}, ErrBadBranch
			}
			effBranch = branch
			branchAt = branch
		}
		isApprover := contains(step.Approvers, actorID)
		// 委托代审：非审批人但在有效被委托名单内 → 允许（消耗委托）。
		var dl *Delegation
		if !isApprover {
			dl = activeDelegationFor(&in, actorID, effBranch)
			if dl == nil {
				return Instance{}, Action{}, ErrNotApprover
			}
			delegateFrom = dl.From
		}
		if acted(actions, actorID, in.StepIndex, in.Round, effBranch) {
			return Instance{}, Action{}, ErrAlreadyDone
		}
		// 依次审批：非（被委托）审批人不可越位——轮到者前置校验。
		if step.Mode == ModeSequential {
			turn := sequentialTurn(step, actions, in.StepIndex, in.Round, effBranch)
			principal := actorID
			if delegateFrom != "" {
				principal = delegateFrom
			}
			if turn == "" || turn != principal {
				return Instance{}, Action{}, ErrNotTurn
			}
		}
		if act == "reject" {
			in.Status, in.FinishedAt = StatusRejected, now()
		} else if stepAdvances(step, actions, &in, actorID, delegateFrom, effBranch) {
			if parallel {
				bs := &in.Branches[effBranch]
				bs.StepIndex++
				allDone := true
				for i := range in.Branches {
					cur := &in.Branches[i]
					if cur.StepIndex >= len(top.Branches[cur.Branch]) {
						cur.Done = true
					}
					if !cur.Done {
						allDone = false
					}
				}
				if allDone {
					in.StepIndex++
					in.Branches = nil
					if err := advance(&in); err != nil {
						return Instance{}, Action{}, err
					}
				}
			} else {
				in.StepIndex++
				if err := advance(&in); err != nil {
					return Instance{}, Action{}, err
				}
			}
		}
		// 消耗委托（代审使用即失效）
		if dl != nil {
			dl.Active = false
		}
	default:
		return Instance{}, Action{}, fmt.Errorf("workflow: unknown act %q", act)
	}
	in.RefreshView()
	at := in.StepIndex
	if act == "approve" || act == "reject" {
		at = stepAt
	}
	return in, Action{
		ActorID: actorID, Act: act, Comment: comment,
		StepIndex: at, Branch: branchAt, Round: in.Round, DelegateFrom: delegateFrom,
		Time: time.Now().UTC(),
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
	if step == nil || step.Type == StepTypeParallel || !contains(step.Approvers, fromUserID) {
		if step != nil && step.Type == StepTypeParallel {
			return Instance{}, Action{}, ErrNotSupported
		}
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
		if todo, _ := r.inst.TodoFor(userID, r.actions); todo {
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

// （acted/allApproved 等辅助已上移至 Action 定义处，带 Round 轮次语义。）

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

// ---------- 第一批六项审批操作：状态机纯函数扩展 ----------

// canOperate 校验操作者是当前步审批人或创建人（cc/addsign/reject-to
// 共用的身份门），pending 限定。
func canOperate(in *Instance, actorID string) (bool, error) {
	if in.Status != StatusPending {
		return false, ErrAlreadyDone
	}
	step := in.CurrentStep()
	if step == nil {
		return false, ErrAlreadyDone
	}
	// 并行网关的审批人在各分支子步上，使用合并视图判定。
	approvers := step.Approvers
	if step.Type == StepTypeParallel {
		approvers = in.Approvers
	}
	if contains(approvers, actorID) || in.CreatorID == actorID {
		return true, nil
	}
	return false, ErrNotApprover
}

// ApplyCC 抄送：追加被抄送人（去重幂等）。操作者须为当前步审批人
// 或创建人（知会语义——非审批负担，被抄送人只读可见）。
func ApplyCC(in Instance, actorID string, userIDs []string) (Instance, Action, error) {
	ok, err := canOperate(&in, actorID)
	if !ok {
		return Instance{}, Action{}, err
	}
	added := 0
	for _, uid := range userIDs {
		if uid == "" || contains(in.CCUserIDs, uid) {
			continue
		}
		in.CCUserIDs = append(in.CCUserIDs, uid)
		added++
	}
	if added == 0 && len(userIDs) > 0 {
		// 全部为重复/空——幂等返回当前态（不产生动作历史噪音）
		return in, Action{InstanceID: in.ID, ActorID: actorID, Act: "cc",
			Comment: "no-op (all duplicated)", StepIndex: in.StepIndex, Round: in.Round,
			Time: time.Now().UTC()}, nil
	}
	return in, Action{
		InstanceID: in.ID, ActorID: actorID, Act: "cc",
		Comment:   fmt.Sprintf("%d user(s) cc'd", added),
		StepIndex: in.StepIndex, Round: in.Round, Time: time.Now().UTC(),
	}, nil
}

// ApplyDelegateAct 委派：审批人 from 委托 to 代审本步本轮一次
// （与转办 Delegate 的区别：审批权不转移，A 名义保留；to 使用一次
// 即消耗，A 自己仍可审）。同步骤同轮 from 可多次委派不同对象。
func ApplyDelegateAct(in Instance, fromUserID, toUserID string) (Instance, Action, error) {
	if toUserID == "" || toUserID == fromUserID {
		return Instance{}, Action{}, ErrBadDelegate
	}
	ok, err := canOperate(&in, fromUserID)
	if !ok || !contains(in.CurrentStep().Approvers, fromUserID) {
		// 委派仅限当前步审批人（创建人无审批权可委）
		if err == nil {
			err = ErrNotApprover
		}
		return Instance{}, Action{}, err
	}
	if in.CurrentStep().Type == StepTypeParallel {
		// 并行分支持以下动作：approve / reject；委派暂不跨分支定位
		return Instance{}, Action{}, ErrNotSupported
	}
	in.Delegations = append(in.Delegations, Delegation{
		StepIndex: in.StepIndex, Round: in.Round,
		From: fromUserID, To: toUserID, Active: true,
	})
	return in, Action{
		InstanceID: in.ID, ActorID: fromUserID, Act: "delegate-act",
		Comment:   "delegate to " + toUserID,
		StepIndex: in.StepIndex, Round: in.Round, Time: time.Now().UTC(),
	}, nil
}

// ApplyAddSign 加签：当前步审批人在本步前/后插入新签核步。
// before：新步先审（原步骤后移，轮到新步）；after：本步完成后
// 先走新步再进原下一步。步骤上限 MaxSteps 防失控。
func ApplyAddSign(in Instance, actorID, position string, approvers []string, mode string) (Instance, Action, error) {
	if len(approvers) == 0 || len(approvers) > 5 {
		return Instance{}, Action{}, ErrBadAddSign
	}
	if position != "before" && position != "after" {
		return Instance{}, Action{}, ErrBadAddSign
	}
	if mode == "" {
		mode = ModeOrSign
	}
	if mode != ModeOrSign && mode != ModeCountersign {
		return Instance{}, Action{}, ErrBadMode
	}
	ok, err := canOperate(&in, actorID)
	if !ok || !contains(in.CurrentStep().Approvers, actorID) {
		// 加签仅限当前步审批人
		if err == nil {
			err = ErrNotApprover
		}
		return Instance{}, Action{}, err
	}
	if in.CurrentStep().Type == StepTypeParallel {
		// 并行网关内不支持加签（会改变分支与主流程下标映射）
		return Instance{}, Action{}, ErrNotSupported
	}
	if len(in.Steps)+1 > MaxSteps {
		return Instance{}, Action{}, ErrBadAddSign
	}
	newStep := Step{Name: "加签", Approvers: approvers, Mode: mode}
	insertAt := in.StepIndex
	if position == "after" {
		insertAt = in.StepIndex + 1
	}
	in.Steps = append(in.Steps[:insertAt], append([]Step{newStep}, in.Steps[insertAt:]...)...)
	// before：新步恰插在当前 StepIndex 位（原步骤自然后移一位），
	// StepIndex 不动即指向新签核步；新步完成后 StepIndex++ 回原步骤。
	in.RefreshView()
	return in, Action{
		InstanceID: in.ID, ActorID: actorID, Act: "addsign",
		Comment:   fmt.Sprintf("%s: %v", position, approvers),
		StepIndex: in.StepIndex, Round: in.Round, Time: time.Now().UTC(),
	}, nil
}

// ApplyRejectTo 驳回至任意已过节点：StepIndex 回退、Round+1——
// 目标步及之后的已审动作按轮次失效（历史完整保留），从目标步重走。
func ApplyRejectTo(in Instance, actorID string, targetStep int, comment string) (Instance, Action, error) {
	ok, err := canOperate(&in, actorID)
	if !ok || !contains(in.CurrentStep().Approvers, actorID) {
		if err == nil {
			err = ErrNotApprover
		}
		return Instance{}, Action{}, err
	}
	if in.CurrentStep().Type == StepTypeParallel {
		return Instance{}, Action{}, ErrNotSupported
	}
	if targetStep < 0 || targetStep >= in.StepIndex {
		return Instance{}, Action{}, ErrBadRejectTo
	}
	from := in.StepIndex
	in.StepIndex = targetStep
	in.Round++
	// 本步本轮的未消耗委托随之失效（指向回退前的步骤轮次）
	for i := range in.Delegations {
		if in.Delegations[i].StepIndex >= targetStep {
			in.Delegations[i].Active = false
		}
	}
	in.RefreshView()
	return in, Action{
		InstanceID: in.ID, ActorID: actorID, Act: "reject-to",
		Comment:   comment + fmt.Sprintf(" (step %d → %d, round %d)", from, targetStep, in.Round),
		StepIndex: targetStep, Round: in.Round, Time: time.Now().UTC(),
	}, nil
}

// ---------- MemStore 六项操作实现（纯函数共享） ----------

// memApply 通用读-纯函数-写回（锁内）。
func (s *MemStore) memApply(instanceID string, fn func(r *record) (Instance, Action, error)) (Instance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.byID[instanceID]
	if !ok {
		return Instance{}, ErrNotFound
	}
	in, action, err := fn(r)
	if err != nil {
		return Instance{}, err
	}
	action.InstanceID = instanceID
	r.inst, r.actions = in, append(r.actions, action)
	return in, nil
}

func (s *MemStore) CC(_ context.Context, instanceID, actorID string, userIDs []string) (Instance, error) {
	return s.memApply(instanceID, func(r *record) (Instance, Action, error) {
		return ApplyCC(r.inst, actorID, userIDs)
	})
}

func (s *MemStore) DelegateAct(_ context.Context, instanceID, fromUserID, toUserID string) (Instance, error) {
	return s.memApply(instanceID, func(r *record) (Instance, Action, error) {
		return ApplyDelegateAct(r.inst, fromUserID, toUserID)
	})
}

func (s *MemStore) AddSign(_ context.Context, instanceID, actorID, position string, approvers []string, mode string) (Instance, error) {
	return s.memApply(instanceID, func(r *record) (Instance, Action, error) {
		return ApplyAddSign(r.inst, actorID, position, approvers, mode)
	})
}

func (s *MemStore) RejectTo(_ context.Context, instanceID, actorID string, targetStep int, comment string) (Instance, error) {
	return s.memApply(instanceID, func(r *record) (Instance, Action, error) {
		return ApplyRejectTo(r.inst, actorID, targetStep, comment)
	})
}

// ListCC 被抄送列表（pending 与终态均可见——知会历史）。
func (s *MemStore) ListCC(_ context.Context, userID string) ([]Instance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Instance
	for _, r := range s.byID {
		if contains(r.inst.CCUserIDs, userID) {
			out = append(out, r.inst)
		}
	}
	sortByCreatedDesc(out)
	return out, nil
}

// principalOf 代审按名义审批人计（无委托即本人）。
func principalOf(actorID, delegateFrom string) string {
	if delegateFrom != "" {
		return delegateFrom
	}
	return actorID
}

// stepAdvances 本步是否随本次 approve 完成：
//   - or-sign：任一通过即完成
//   - sequential：轮到的 principal 是序列最后一人（代审按名义人）
//   - countersign：全员通过（代审按名义人计票）
func stepAdvances(step *Step, actions []Action, in *Instance, actorID, delegateFrom string, branch int) bool {
	principal := principalOf(actorID, delegateFrom)
	switch step.Mode {
	case ModeOrSign:
		return true
	case ModeSequential:
		return principal == step.Approvers[len(step.Approvers)-1]
	default: // countersign
		return allApproved(step.Approvers, actions, in.StepIndex, in.Round, branch, principal)
	}
}
