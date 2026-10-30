// Package agent 是 L2 轻量 Agent 编排模块：Agent 定义 → 运行 →
// 工具调用循环 → SSE 运行事件。
//
// 设计边界（信创约束：纯 Go 单二进制、依赖树最小化）：
//   - 不引入 Eino 等编排框架，自研轻量循环（系统提示 → LLM →
//     工具调用 → 结果回填 → 再 LLM → 终答），ToolRegistry 接口缝
//     预留与 Eino 生态及后续工具的对接位。
//   - LLM 调用一律经 ai-gateway 统一层（Go 入口 ChatComplete：
//     配额/内容安全审核/记账/审计全链路），不直连 provider。
//   - ai-safety 集成方案：复用 gateway 集成——审核发生在
//     ChatComplete 内部（输入侧拦截 → ErrSafetyRejected；输出侧
//     文本与工具参数送检），agent 引擎捕获后落 safety_rejected
//     终态并经 SSE safety.rejected 事件下发。
package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/basalt-go/basalt/pkg/ai/gateway"
	"github.com/basalt-go/basalt/pkg/kernel/app"
	"github.com/basalt-go/basalt/pkg/kernel/web"
	"github.com/basalt-go/basalt/pkg/modules/audit"
	"github.com/basalt-go/basalt/pkg/modules/identity"
)

const Version = "0.1.0"

// 权限点。
const (
	PermManage = "agent:manage"
	PermRun    = "agent:run"
	PermRead   = "agent:read"
)

// 运行状态。
const (
	RunStatusRunning        = "running"
	RunStatusCompleted      = "completed"
	RunStatusFailed         = "failed"
	RunStatusSafetyRejected = "safety_rejected"
)

// 定义状态。
const (
	DefStatusActive   = "active"
	DefStatusDisabled = "disabled"
)

var (
	ErrNotFound     = errors.New("agent: not found")
	ErrNameRequired = errors.New("agent: name and model are required")
	ErrDisabled     = errors.New("agent: definition disabled")
	ErrNoGateway    = errors.New("agent: LLM gateway not configured")
	ErrBadTools     = errors.New("agent: invalid tool binding")
	ErrBadTemp      = errors.New("agent: temperature must be within [0,2]")
	ErrBadRounds    = errors.New("agent: max_tool_rounds must be within [1,10]")
	ErrInputEmpty   = errors.New("agent: input required")
)

// 工具循环默认/上限。
const (
	DefaultMaxToolRounds = 5
	MaxToolRounds        = 10
	DefaultTemperature   = 0.7
)

// ---------- 实体 ----------

// Definition 是 Agent 定义（模板）。
type Definition struct {
	ID            string    `json:"id" gorm:"primaryKey;size:40"`
	Name          string    `json:"name" gorm:"size:128"`
	Description   string    `json:"description,omitempty" gorm:"size:255"`
	SystemPrompt  string    `json:"system_prompt,omitempty" gorm:"size:2048"`
	Model         string    `json:"model" gorm:"size:128"`
	Tools         []string  `json:"tools" gorm:"serializer:json"`
	Temperature   float64   `json:"temperature"`
	MaxToolRounds int       `json:"max_tool_rounds"`
	OwnerID       string    `json:"owner_id" gorm:"size:40;index"`
	Status        string    `json:"status" gorm:"size:16"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// TableName 显式表名：GORM 复数规则会把 Definition 映射为
// definitions，与 workflow.Definition 撞表（AutoMigrate 相互覆盖，
// 教训：跨模块实体必须显式表名防默认复数碰撞）。
func (Definition) TableName() string { return "agent_definitions" }

// Run 是一次运行记录。
type Run struct {
	ID          string     `json:"id" gorm:"primaryKey;size:40"`
	AgentID     string     `json:"agent_id" gorm:"size:40;index"`
	UserID      string     `json:"user_id" gorm:"size:40;index"`
	Status      string     `json:"status" gorm:"size:16;index"`
	Input       string     `json:"input" gorm:"type:text"`
	Output      string     `json:"output,omitempty" gorm:"type:text"`
	Rounds      int        `json:"rounds"`
	TotalTokens int64      `json:"total_tokens"`
	Error       string     `json:"error,omitempty" gorm:"size:1024"`
	StartedAt   time.Time  `json:"started_at"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
}

// TableName 显式表名（防跨模块撞表）。
func (Run) TableName() string { return "agent_runs" }

// ToolCallRef 是持久化的 assistant 工具调用（JSON 面与契约一致）。
type ToolCallRef struct {
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// RunMessage 是运行内的一条消息（不可变历史，Seq 为追加序）。
type RunMessage struct {
	Seq        int64         `json:"seq" gorm:"primaryKey;autoIncrement"`
	RunID      string        `json:"run_id" gorm:"size:40;index"`
	Role       string        `json:"role" gorm:"size:16"` // user | assistant | tool
	Content    string        `json:"content" gorm:"type:text"`
	ToolCalls  []ToolCallRef `json:"tool_calls,omitempty" gorm:"serializer:json"`
	ToolCallID string        `json:"tool_call_id,omitempty" gorm:"size:64"`
	ToolName   string        `json:"tool_name,omitempty" gorm:"size:64"`
	CreatedAt  time.Time     `json:"created_at"`
}

// TableName 显式表名（防跨模块撞表）。
func (RunMessage) TableName() string { return "agent_run_messages" }

// ---------- 持久化抽象 ----------

// Store 抽象定义/运行/消息持久化（GORM 落盘或内存实现）。
type Store interface {
	CreateDef(ctx context.Context, d Definition) error
	GetDef(ctx context.Context, id string) (Definition, error)
	ListDefs(ctx context.Context) ([]Definition, error)
	UpdateDef(ctx context.Context, d Definition) error
	DeleteDef(ctx context.Context, id string) error // 级联删运行与消息

	CreateRun(ctx context.Context, r Run) error
	GetRun(ctx context.Context, id string) (Run, error)
	UpdateRun(ctx context.Context, r Run) error
	ListRuns(ctx context.Context, agentID string) ([]Run, error)

	AppendMsg(ctx context.Context, msg RunMessage) error
	ListMsgs(ctx context.Context, runID string) ([]RunMessage, error)
}

// ---------- 内存实现（测试/演示零依赖路径） ----------

type MemStore struct {
	mu     sync.RWMutex
	defs   map[string]Definition
	runs   map[string]Run
	msgs   map[string][]RunMessage // runID -> ordered
	runSeq int64
}

func NewMemStore() *MemStore {
	return &MemStore{defs: map[string]Definition{}, runs: map[string]Run{}, msgs: map[string][]RunMessage{}}
}

func (s *MemStore) CreateDef(_ context.Context, d Definition) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.defs[d.ID] = d
	return nil
}

func (s *MemStore) GetDef(_ context.Context, id string) (Definition, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.defs[id]
	if !ok {
		return Definition{}, ErrNotFound
	}
	return d, nil
}

func (s *MemStore) ListDefs(_ context.Context) ([]Definition, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Definition, 0, len(s.defs))
	for _, d := range s.defs {
		out = append(out, d)
	}
	return out, nil
}

func (s *MemStore) UpdateDef(_ context.Context, d Definition) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.defs[d.ID]; !ok {
		return ErrNotFound
	}
	s.defs[d.ID] = d
	return nil
}

func (s *MemStore) DeleteDef(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.defs[id]; !ok {
		return ErrNotFound
	}
	delete(s.defs, id)
	for rid, r := range s.runs {
		if r.AgentID == id {
			delete(s.runs, rid)
			delete(s.msgs, rid)
		}
	}
	return nil
}

func (s *MemStore) CreateRun(_ context.Context, r Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs[r.ID] = r
	return nil
}

func (s *MemStore) GetRun(_ context.Context, id string) (Run, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.runs[id]
	if !ok {
		return Run{}, ErrNotFound
	}
	return r, nil
}

func (s *MemStore) UpdateRun(_ context.Context, r Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.runs[r.ID]; !ok {
		return ErrNotFound
	}
	s.runs[r.ID] = r
	return nil
}

func (s *MemStore) ListRuns(_ context.Context, agentID string) ([]Run, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Run
	for _, r := range s.runs {
		if r.AgentID == agentID {
			out = append(out, r)
		}
	}
	// 倒序：最新在前
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

func (s *MemStore) AppendMsg(_ context.Context, msg RunMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runSeq++
	msg.Seq = s.runSeq
	s.msgs[msg.RunID] = append(s.msgs[msg.RunID], msg)
	return nil
}

func (s *MemStore) ListMsgs(_ context.Context, runID string) ([]RunMessage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]RunMessage, len(s.msgs[runID]))
	copy(out, s.msgs[runID])
	return out, nil
}

// ---------- 模块 ----------

// LLMGateway 是 agent 引擎对统一层的最小依赖面（gateway.Module
// 实现；测试用 stub 实现同签名——LLM 全 mock，零真实调用）。
type LLMGateway interface {
	ChatComplete(ctx context.Context, subject, actor, model string, msgs []gateway.ChatMessage, opts gateway.ChatOptions) (gateway.ChatResult, error)
}

type Module struct {
	store    Store
	tools    *ToolRegistry
	gw       LLMGateway
	facade   *identity.Facade
	recorder audit.Recorder
}

// New 纯内存装配（gw/tools 可后置注入，未注入时运行端点明确报 503）。
func New() *Module { return &Module{store: NewMemStore(), tools: NewToolRegistry()} }

// NewWithStore 注入持久化实现（GORM 落盘、重启恢复）。
func NewWithStore(store Store) *Module { return &Module{store: store, tools: NewToolRegistry()} }

// SetGateway 注入 LLM 统一层入口（serve 装配期）。
func (m *Module) SetGateway(gw LLMGateway) { m.gw = gw }

// SetTools 注入工具注册表（serve 装配期）。
func (m *Module) SetTools(t *ToolRegistry) { m.tools = t }

func (*Module) Name() string           { return "agent" }
func (*Module) Version() string        { return Version }
func (*Module) Dependencies() []string { return []string{"identity", "audit"} }

func (*Module) Describe() app.ModuleInfo {
	return app.ModuleInfo{Name: "agent", Version: Version, Permissions: []string{
		PermManage, PermRun, PermRead,
	}}
}

func (m *Module) Init(_ context.Context, a *app.App) error {
	idm, ok := a.Registry().MustGet("identity").(*identity.Module)
	if !ok {
		return errors.New("agent: identity module not available")
	}
	m.facade = idm.Facade()
	if adm, ok := a.Registry().MustGet("audit").(*audit.Module); ok {
		m.recorder = adm.Recorder()
	}

	auth := m.facade.Auth
	req := m.facade.Require
	g := a.Router().Group("/api/v1/agent")

	// 定义 CRUD
	g.Handle(http.MethodPost, "/agents", auth(req(PermManage, m.createDef)))
	g.Handle(http.MethodGet, "/agents", auth(req(PermRead, m.listDefs)))
	g.Handle(http.MethodGet, "/agents/{id}", auth(req(PermRead, m.getDef)))
	g.Handle(http.MethodPatch, "/agents/{id}", auth(req(PermManage, m.updateDef)))
	g.Handle(http.MethodDelete, "/agents/{id}", auth(req(PermManage, m.deleteDef)))
	// 运行
	g.Handle(http.MethodPost, "/agents/{id}/runs", auth(req(PermRun, m.startRun)))
	g.Handle(http.MethodGet, "/agents/{id}/runs", auth(req(PermRead, m.listRuns)))
	g.Handle(http.MethodGet, "/runs/{id}", auth(req(PermRead, m.getRun)))
	// 工具注册表
	g.Handle(http.MethodGet, "/tools", auth(req(PermRead, m.listTools)))
	return nil
}

func (*Module) Start(context.Context) error { return nil }
func (*Module) Stop(context.Context) error  { return nil }

// Store 供装配层与集成测试读取。
func (m *Module) Store() Store { return m.store }

// ---------- 定义 handler ----------

// normalizeDef 填默认值并校验（创建/更新共用）。
func (m *Module) normalizeDef(d *Definition) error {
	d.Name = strings.TrimSpace(d.Name)
	d.Model = strings.TrimSpace(d.Model)
	if d.Name == "" || d.Model == "" {
		return ErrNameRequired
	}
	if d.Temperature == 0 {
		d.Temperature = DefaultTemperature
	}
	if d.Temperature < 0 || d.Temperature > 2 {
		return ErrBadTemp
	}
	if d.MaxToolRounds == 0 {
		d.MaxToolRounds = DefaultMaxToolRounds
	}
	if d.MaxToolRounds < 1 || d.MaxToolRounds > MaxToolRounds {
		return ErrBadRounds
	}
	if d.Status == "" {
		d.Status = DefStatusActive
	}
	if d.Status != DefStatusActive && d.Status != DefStatusDisabled {
		return errors.New("agent: status must be active or disabled")
	}
	// 工具绑定校验：全部须已注册（防拼写错误绑定到运行期才爆）。
	if len(d.Tools) > 0 {
		if m.tools == nil {
			return ErrNoTools
		}
		if _, err := m.tools.Defs(d.Tools); err != nil {
			return fmt_badTools(err)
		}
	}
	return nil
}

func fmt_badTools(err error) error {
	return errors.Join(ErrBadTools, err)
}

func (m *Module) createDef(w http.ResponseWriter, r *http.Request) {
	var d Definition
	if err := json.NewDecoder(r.Body).Decode(&d); err != nil {
		web.Err(w, http.StatusBadRequest, "invalid json body")
		return
	}
	u, err := m.facade.User(r)
	if err != nil {
		web.Err(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	d.ID = ""
	if err := m.normalizeDef(&d); err != nil {
		web.Err(w, http.StatusBadRequest, err.Error())
		return
	}
	now := time.Now().UTC()
	d.ID = newID()
	d.OwnerID = u.ID
	d.CreatedAt, d.UpdatedAt = now, now
	if err := m.store.CreateDef(r.Context(), d); err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	m.record(r.Context(), u.ID, "def.created", d.ID, d.Name)
	web.Created(w, d)
}

func (m *Module) listDefs(w http.ResponseWriter, r *http.Request) {
	defs, err := m.store.ListDefs(r.Context())
	if err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.OK(w, map[string]any{"count": len(defs), "items": defs})
}

func (m *Module) getDef(w http.ResponseWriter, r *http.Request) {
	d, err := m.store.GetDef(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			web.Err(w, http.StatusNotFound, "agent not found")
			return
		}
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	runs, _ := m.store.ListRuns(r.Context(), d.ID)
	web.OK(w, map[string]any{"agent": d, "run_count": len(runs)})
}

func (m *Module) updateDef(w http.ResponseWriter, r *http.Request) {
	d, err := m.store.GetDef(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			web.Err(w, http.StatusNotFound, "agent not found")
			return
		}
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	var req struct {
		Name          *string  `json:"name"`
		Description   *string  `json:"description"`
		SystemPrompt  *string  `json:"system_prompt"`
		Model         *string  `json:"model"`
		Tools         []string `json:"tools"`
		Temperature   *float64 `json:"temperature"`
		MaxToolRounds *int     `json:"max_tool_rounds"`
		Status        *string  `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		web.Err(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if req.Name != nil {
		d.Name = *req.Name
	}
	if req.Description != nil {
		d.Description = *req.Description
	}
	if req.SystemPrompt != nil {
		d.SystemPrompt = *req.SystemPrompt
	}
	if req.Model != nil {
		d.Model = *req.Model
	}
	if req.Tools != nil {
		d.Tools = req.Tools
	}
	if req.Temperature != nil {
		d.Temperature = *req.Temperature
	}
	if req.MaxToolRounds != nil {
		d.MaxToolRounds = *req.MaxToolRounds
	}
	if req.Status != nil {
		d.Status = *req.Status
	}
	if err := m.normalizeDef(&d); err != nil {
		web.Err(w, http.StatusBadRequest, err.Error())
		return
	}
	d.UpdatedAt = time.Now().UTC()
	if err := m.store.UpdateDef(r.Context(), d); err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.OK(w, d)
}

func (m *Module) deleteDef(w http.ResponseWriter, r *http.Request) {
	u, err := m.facade.User(r)
	if err != nil {
		web.Err(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if err := m.store.DeleteDef(r.Context(), r.PathValue("id")); err != nil {
		if errors.Is(err, ErrNotFound) {
			web.Err(w, http.StatusNotFound, "agent not found")
			return
		}
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	m.record(r.Context(), u.ID, "def.deleted", r.PathValue("id"), "")
	web.OK(w, nil)
}

// ---------- 运行历史 handler ----------

func (m *Module) listRuns(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	if _, err := m.store.GetDef(r.Context(), agentID); err != nil {
		if errors.Is(err, ErrNotFound) {
			web.Err(w, http.StatusNotFound, "agent not found")
			return
		}
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	runs, err := m.store.ListRuns(r.Context(), agentID)
	if err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.OK(w, map[string]any{"count": len(runs), "items": runs})
}

func (m *Module) getRun(w http.ResponseWriter, r *http.Request) {
	run, err := m.store.GetRun(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			web.Err(w, http.StatusNotFound, "run not found")
			return
		}
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	msgs, err := m.store.ListMsgs(r.Context(), run.ID)
	if err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.OK(w, map[string]any{"run": run, "messages": msgs})
}

func (m *Module) listTools(w http.ResponseWriter, _ *http.Request) {
	if m.tools == nil {
		web.OK(w, map[string]any{"tools": []any{}})
		return
	}
	web.OK(w, map[string]any{"tools": m.tools.List()})
}

// ---------- 工具 ----------

// modAgent 审计模块标识（自治维护，避免共享文件冲突）。
const modAgent = "agent"

func (m *Module) record(ctx context.Context, actor, event, resource, detail string) {
	if m.recorder == nil {
		return
	}
	detailJSON, _ := json.Marshal(map[string]string{"resource": resource, "detail": detail})
	_ = m.recorder.Record(ctx, audit.Event{
		Module: modAgent, Event: event, ActorID: actor,
		Resource: resource, Detail: detailJSON, Time: time.Now().UTC(),
	})
}

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
