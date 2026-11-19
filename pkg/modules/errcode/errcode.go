// Package errcode 是错误码管理模块（竞品 Gap P0#8）：错误码注册表
// CRUD + Console API + 现有模块错误码抽样种子。
//
// 定位：注册表是「平台错误码字典」（模块/码/描述），供 Console 错误码
// 管理页查询与前端错误翻译对照；不改变各模块运行时错误返回路径
// （web.Err 语义保持）。
package errcode

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/basalt-go/basalt/pkg/kernel/app"
	"github.com/basalt-go/basalt/pkg/kernel/crypto/randutil"
	"github.com/basalt-go/basalt/pkg/kernel/web"
	"github.com/basalt-go/basalt/pkg/modules/identity"
)

const Version = "0.1.0"

// 权限点。
const (
	PermList   = "errcode:list"
	PermManage = "errcode:manage"
)

var (
	ErrNotFound = errors.New("errcode: entry not found")
	ErrDup      = errors.New("errcode: module+code already registered")
)

// Entry 是一条错误码登记（模块内唯一键 = Module+Code）。
type Entry struct {
	ID         string    `json:"id" gorm:"primaryKey;size:40"`
	Module     string    `json:"module" gorm:"size:32;uniqueIndex:idx_mod_code"`
	Code       string    `json:"code" gorm:"size:32;uniqueIndex:idx_mod_code"`
	Message    string    `json:"message" gorm:"size:255"`
	HTTPStatus int       `json:"http_status"`
	Detail     string    `json:"detail,omitempty" gorm:"size:512"`
	CreatedAt  time.Time `json:"created_at"`
}

// TableName 显式表名。
func (Entry) TableName() string { return "error_codes" }

// Store 抽象注册表持久化（内存先行，GORM 见 stores/gormstore）。
type Store interface {
	Create(ctx context.Context, e Entry) error
	Get(ctx context.Context, id string) (Entry, error)
	List(ctx context.Context, module string) ([]Entry, error)
	Update(ctx context.Context, e Entry) error
	Delete(ctx context.Context, id string) error
}

// ---------- 内存实现 ----------

type MemStore struct {
	mu sync.RWMutex
	m  map[string]Entry
}

func NewMemStore() *MemStore { return &MemStore{m: map[string]Entry{}} }

func (s *MemStore) Create(_ context.Context, e Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.m {
		if x.Module == e.Module && x.Code == e.Code {
			return ErrDup
		}
	}
	s.m[e.ID] = e
	return nil
}

func (s *MemStore) Get(_ context.Context, id string) (Entry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.m[id]
	if !ok {
		return Entry{}, ErrNotFound
	}
	return e, nil
}

func (s *MemStore) List(_ context.Context, module string) ([]Entry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Entry, 0, len(s.m))
	for _, e := range s.m {
		if module == "" || e.Module == module {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Module != out[j].Module {
			return out[i].Module < out[j].Module
		}
		return out[i].Code < out[j].Code
	})
	return out, nil
}

func (s *MemStore) Update(_ context.Context, e Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.m[e.ID]; !ok {
		return ErrNotFound
	}
	s.m[e.ID] = e
	return nil
}

func (s *MemStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.m[id]; !ok {
		return ErrNotFound
	}
	delete(s.m, id)
	return nil
}

// ---------- 模块 ----------

type Module struct {
	store  Store
	facade *identity.Facade
}

func New() *Module { return &Module{store: NewMemStore()} }

// NewWithStore 注入持久化实现。
func NewWithStore(s Store) *Module { return &Module{store: s} }

func (*Module) Name() string           { return "errcode" }
func (*Module) Version() string        { return Version }
func (*Module) Dependencies() []string { return []string{"identity"} }

func (*Module) Describe() app.ModuleInfo {
	return app.ModuleInfo{Name: "errcode", Version: Version, Permissions: []string{PermList, PermManage}}
}

func (m *Module) Init(_ context.Context, a *app.App) error {
	idm, ok := a.Registry().MustGet("identity").(*identity.Module)
	if !ok {
		return errors.New("errcode: identity module not available")
	}
	m.facade = idm.Facade()

	auth := m.facade.Auth
	req := m.facade.Require
	g := a.Router().Group("/api/v1/errcodes")
	g.Handle(http.MethodGet, "", auth(req(PermList, m.list)))
	g.Handle(http.MethodGet, "/{id}", auth(req(PermList, m.get)))
	g.Handle(http.MethodPost, "", auth(req(PermManage, m.create)))
	g.Handle(http.MethodPatch, "/{id}", auth(req(PermManage, m.update)))
	g.Handle(http.MethodDelete, "/{id}", auth(req(PermManage, m.delete)))
	return nil
}

// Start 幂等播种：注册表为空时灌入现有模块错误码抽样种子（合理抽样，
// 不穷尽——覆盖各模块代表性错误：认证/鉴权/冲突/业务规则四类）。
func (m *Module) Start(ctx context.Context) error {
	existing, err := m.store.List(ctx, "")
	if err != nil || len(existing) > 0 {
		return err
	}
	for _, s := range Seeds {
		e := s
		e.ID = newID()
		e.CreatedAt = time.Now().UTC()
		_ = m.store.Create(ctx, e)
	}
	return nil
}

func (*Module) Stop(context.Context) error { return nil }

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		web.Err(w, http.StatusBadRequest, "invalid json body")
		return false
	}
	return true
}

func (m *Module) list(w http.ResponseWriter, r *http.Request) {
	out, err := m.store.List(r.Context(), r.URL.Query().Get("module"))
	if err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.OK(w, out)
}

func (m *Module) get(w http.ResponseWriter, r *http.Request) {
	e, err := m.store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			web.Err(w, http.StatusNotFound, "error code not found")
			return
		}
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.OK(w, e)
}

func (m *Module) create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Module     string `json:"module"`
		Code       string `json:"code"`
		Message    string `json:"message"`
		HTTPStatus int    `json:"http_status"`
		Detail     string `json:"detail"`
	}
	if !decode(w, r, &req) || req.Module == "" || req.Code == "" || req.Message == "" {
		web.Err(w, http.StatusBadRequest, "module, code and message are required")
		return
	}
	if req.HTTPStatus == 0 {
		req.HTTPStatus = 400
	}
	e := Entry{ID: newID(), Module: req.Module, Code: req.Code, Message: req.Message,
		HTTPStatus: req.HTTPStatus, Detail: req.Detail, CreatedAt: time.Now().UTC()}
	if err := m.store.Create(r.Context(), e); err != nil {
		if errors.Is(err, ErrDup) {
			web.Err(w, http.StatusConflict, "module+code already registered")
			return
		}
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.Created(w, e)
}

func (m *Module) update(w http.ResponseWriter, r *http.Request) {
	e, err := m.store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			web.Err(w, http.StatusNotFound, "error code not found")
			return
		}
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	var req struct {
		Message    *string `json:"message"`
		HTTPStatus *int    `json:"http_status"`
		Detail     *string `json:"detail"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Message != nil && *req.Message != "" {
		e.Message = *req.Message
	}
	if req.HTTPStatus != nil {
		e.HTTPStatus = *req.HTTPStatus
	}
	if req.Detail != nil {
		e.Detail = *req.Detail
	}
	if err := m.store.Update(r.Context(), e); err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.OK(w, e)
}

func (m *Module) delete(w http.ResponseWriter, r *http.Request) {
	if err := m.store.Delete(r.Context(), r.PathValue("id")); err != nil {
		if errors.Is(err, ErrNotFound) {
			web.Err(w, http.StatusNotFound, "error code not found")
			return
		}
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.OK(w, nil)
}

// Seeds 是现有模块错误码盘点抽样（模块→代表性错误；覆盖认证/鉴权/
// 资源不存在/冲突/业务规则五类，每模块 2-4 条，不穷尽）。
var Seeds = []Entry{
	// identity
	{Module: "identity", Code: "UNAUTHORIZED", Message: "未认证或令牌失效", HTTPStatus: 401, Detail: "登录过期、令牌被吊销（踢下线/登出）或未携带 Bearer 令牌"},
	{Module: "identity", Code: "INVALID_CREDENTIALS", Message: "用户名或密码错误", HTTPStatus: 401, Detail: "不区分用户不存在与密码错误，防枚举"},
	{Module: "identity", Code: "ACCOUNT_DISABLED", Message: "账号已停用", HTTPStatus: 403},
	{Module: "identity", Code: "FORBIDDEN", Message: "无权限点访问该资源", HTTPStatus: 403, Detail: "RBAC 权限校验未通过，含套餐裁剪后收敛的权限"},
	{Module: "identity", Code: "USERNAME_TAKEN", Message: "用户名已存在", HTTPStatus: 409},
	{Module: "identity", Code: "USER_NOT_FOUND", Message: "用户不存在", HTTPStatus: 404},
	{Module: "identity", Code: "WEAK_PASSWORD", Message: "密码不满足基线（≥8 位）", HTTPStatus: 400},
	// tenant
	{Module: "tenant", Code: "CODE_TAKEN", Message: "租户编码已存在", HTTPStatus: 409},
	{Module: "tenant", Code: "NOT_FOUND", Message: "租户不存在", HTTPStatus: 404},
	{Module: "tenant", Code: "BAD_ISOLATION", Message: "非法隔离模式", HTTPStatus: 400, Detail: "合法值：shared-schema / shared-db / dedicated-db"},
	{Module: "tenant", Code: "NO_TENANT", Message: "当前用户未归属租户", HTTPStatus: 404},
	// menu
	{Module: "menu", Code: "NOT_FOUND", Message: "菜单不存在", HTTPStatus: 404},
	{Module: "menu", Code: "PATH_TAKEN", Message: "菜单路径已被占用", HTTPStatus: 409},
	{Module: "menu", Code: "HAS_CHILDREN", Message: "存在子菜单，禁止删除", HTTPStatus: 409},
	{Module: "menu", Code: "ROLE_BOUND", Message: "菜单已绑定角色，禁止删除", HTTPStatus: 409},
	// tenantpkg
	{Module: "tenantpkg", Code: "PKG_NOT_FOUND", Message: "套餐不存在", HTTPStatus: 404},
	{Module: "tenantpkg", Code: "PKG_NAME_TAKEN", Message: "套餐名称已存在", HTTPStatus: 409},
	{Module: "tenantpkg", Code: "PKG_IN_USE", Message: "套餐已绑定租户，禁止删除", HTTPStatus: 409},
	// workflow
	{Module: "workflow", Code: "INSTANCE_NOT_FOUND", Message: "流程实例不存在", HTTPStatus: 404},
	{Module: "workflow", Code: "INVALID_TRANSITION", Message: "非法状态迁移", HTTPStatus: 409, Detail: "当前节点不接受的审批动作"},
	// file
	{Module: "file", Code: "TOO_LARGE", Message: "文件超出大小限制", HTTPStatus: 413},
	{Module: "file", Code: "BAD_TYPE", Message: "文件类型不在白名单", HTTPStatus: 400},
	// audit
	{Module: "audit", Code: "CHAIN_BROKEN", Message: "审计链校验断裂", HTTPStatus: 200, Detail: "verify 返回 ok=false 时的业务态（非传输错误）"},
	// sso（identity 子域）
	{Module: "identity", Code: "SSO_INVALID_CLIENT", Message: "OAuth2 客户端认证失败", HTTPStatus: 401, Detail: "client_id/secret 不匹配或未注册"},
	{Module: "identity", Code: "SSO_INVALID_GRANT", Message: "OAuth2 授权无效", HTTPStatus: 400, Detail: "code 过期/已用/redirect 不匹配/PKCE 失败/refresh 重放"},
	{Module: "identity", Code: "SSO_INVALID_SCOPE", Message: "请求 scope 超出注册范围", HTTPStatus: 400},
}

func newID() string { return randutil.Hex16() }
