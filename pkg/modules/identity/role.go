package identity

import (
	"context"
	"errors"
	"sync"
)

// 权限点常量：格式 "<模块>:<资源>:<动作>"。路由挂载见 handler.go；
// 业务模块未来通过各自常量扩展，中间件支持 "*" 与 "identity:*" 式通配。
const (
	PermUserCreate = "identity:user:create"
	PermUserList   = "identity:user:list"
	PermUserGet    = "identity:user:get"
	PermUserStatus = "identity:user:status"
	PermRoleManage = "identity:role:manage"
)

// RoleAdmin 是内置超级角色代码，Init 时创建并持有 "*" 全量权限。
// G 端审计要求可追溯：该角色分配走与普通角色相同的 AssignRoles 通道，
// 不设隐藏后门。
const RoleAdmin = "admin"

var (
	ErrRoleCodeTaken = errors.New("identity: role code already taken")
	ErrRoleNotFound  = errors.New("identity: role not found")
	ErrRoleInUse     = errors.New("identity: role still assigned to users")
)

// Role 是 RBAC 角色。Permissions 为权限点列表，支持 "*"、前缀通配
// （如 "identity:user:*"）与精确匹配。
type Role struct {
	ID          string   `json:"id" gorm:"primaryKey;size:40"`
	Code        string   `json:"code" gorm:"uniqueIndex;size:64"`
	Name        string   `json:"name" gorm:"size:128"`
	Permissions []string `json:"permissions" gorm:"serializer:json"`
}

// RoleStore 抽象角色持久化，语义与 UserStore 对齐（内存实现先行，
// GORM + 国产数据库方言随后）。
type RoleStore interface {
	Create(ctx context.Context, r Role) error
	Get(ctx context.Context, id string) (Role, error)
	GetByCode(ctx context.Context, code string) (Role, error)
	List(ctx context.Context) ([]Role, error)
	Update(ctx context.Context, r Role) error
	Delete(ctx context.Context, id string) error
}

type MemRoleStore struct {
	mu     sync.RWMutex
	byID   map[string]Role
	byCode map[string]string
}

func NewMemRoleStore() *MemRoleStore {
	return &MemRoleStore{byID: map[string]Role{}, byCode: map[string]string{}}
}

func (s *MemRoleStore) Create(_ context.Context, r Role) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byCode[r.Code]; ok {
		return ErrRoleCodeTaken
	}
	s.byID[r.ID] = r
	s.byCode[r.Code] = r.ID
	return nil
}

func (s *MemRoleStore) Get(_ context.Context, id string) (Role, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.byID[id]
	if !ok {
		return Role{}, ErrRoleNotFound
	}
	return r, nil
}

func (s *MemRoleStore) GetByCode(_ context.Context, code string) (Role, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.byCode[code]
	if !ok {
		return Role{}, ErrRoleNotFound
	}
	return s.byID[id], nil
}

func (s *MemRoleStore) List(_ context.Context) ([]Role, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Role, 0, len(s.byID))
	for _, r := range s.byID {
		out = append(out, r)
	}
	return out, nil
}

func (s *MemRoleStore) Update(_ context.Context, r Role) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.byID[r.ID]
	if !ok {
		return ErrRoleNotFound
	}
	delete(s.byCode, old.Code)
	s.byID[r.ID] = r
	s.byCode[r.Code] = r.ID
	return nil
}

func (s *MemRoleStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.byID[id]
	if !ok {
		return ErrRoleNotFound
	}
	delete(s.byID, id)
	delete(s.byCode, r.Code)
	return nil
}

// HasPermission 判断权限点列表是否覆盖 want。
// 支持 "*"（全量）与前缀通配（"identity:*"、"identity:user:*"）。
func HasPermission(perms []string, want string) bool {
	for _, p := range perms {
		if p == "*" || p == want {
			return true
		}
		if len(p) > 0 && p[len(p)-1] == '*' {
			prefix := p[:len(p)-1]
			if len(want) >= len(prefix) && want[:len(prefix)] == prefix {
				return true
			}
		}
	}
	return false
}
