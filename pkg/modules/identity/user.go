package identity

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

const (
	StatusActive   = "active"
	StatusDisabled = "disabled"
)

var (
	ErrUsernameTaken = errors.New("identity: username already taken")
	ErrUserNotFound  = errors.New("identity: user not found")
	ErrBadStatus     = errors.New("identity: invalid status")
)

// User 是身份主体。PasswordHash 永不出现在任何 JSON 响应中。
// gorm 标签供 pkg/stores/gormstore 复用同一领域模型（读写分离于存储实现）。
type User struct {
	ID           string    `json:"id" gorm:"primaryKey;size:40"`
	Username     string    `json:"username" gorm:"uniqueIndex;size:64"`
	PasswordHash string    `json:"-" gorm:"size:128"`
	Email        string    `json:"email" gorm:"size:128"`
	Status       string    `json:"status" gorm:"size:16;index"`
	RoleIDs      []string  `json:"role_ids" gorm:"serializer:json"`
	TenantID     string    `json:"tenant_id,omitempty" gorm:"size:40;index"`
	DeptID       string    `json:"dept_id,omitempty" gorm:"size:40;index"`
	PostID       string    `json:"post_id,omitempty" gorm:"size:40"`
	CreatedAt    time.Time `json:"created_at"`
}

// UserStore 抽象用户持久化。M1 后期提供 GORM 实现（含国产数据库方言），
// 内存实现用于开发、测试与 All-in-One 单体形态的默认开箱。
// 所有实现必须并发安全。
type UserStore interface {
	Create(ctx context.Context, u User) error
	Get(ctx context.Context, id string) (User, error)
	GetByUsername(ctx context.Context, username string) (User, error)
	List(ctx context.Context) ([]User, error)
	UpdateStatus(ctx context.Context, id, status string) error
	// AssignRoles 整体替换用户的角色集合（幂等）。
	AssignRoles(ctx context.Context, userID string, roleIDs []string) error
	// ListByTenant 列出指定租户的全部用户（行级隔离的读取路径）。
	// tenantID 为空串表示平台级（未归属租户）用户。
	ListByTenant(ctx context.Context, tenantID string) ([]User, error)
}

type MemUserStore struct {
	mu     sync.RWMutex
	byID   map[string]User
	byName map[string]string // username -> id
}

func NewMemUserStore() *MemUserStore {
	return &MemUserStore{byID: map[string]User{}, byName: map[string]string{}}
}

func (s *MemUserStore) Create(_ context.Context, u User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byName[u.Username]; ok {
		return ErrUsernameTaken
	}
	s.byID[u.ID] = u
	s.byName[u.Username] = u.ID
	return nil
}

func (s *MemUserStore) Get(_ context.Context, id string) (User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.byID[id]
	if !ok {
		return User{}, ErrUserNotFound
	}
	return u, nil
}

func (s *MemUserStore) GetByUsername(_ context.Context, username string) (User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.byName[username]
	if !ok {
		return User{}, ErrUserNotFound
	}
	return s.byID[id], nil
}

func (s *MemUserStore) List(_ context.Context) ([]User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]User, 0, len(s.byID))
	for _, u := range s.byID {
		out = append(out, u)
	}
	return out, nil
}

func (s *MemUserStore) ListByTenant(_ context.Context, tenantID string) ([]User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []User
	for _, u := range s.byID {
		if u.TenantID == tenantID {
			out = append(out, u)
		}
	}
	return out, nil
}

func (s *MemUserStore) UpdateStatus(_ context.Context, id, status string) error {
	if status != StatusActive && status != StatusDisabled {
		return ErrBadStatus
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.byID[id]
	if !ok {
		return ErrUserNotFound
	}
	u.Status = status
	s.byID[id] = u
	return nil
}

func (s *MemUserStore) AssignRoles(_ context.Context, userID string, roleIDs []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.byID[userID]
	if !ok {
		return ErrUserNotFound
	}
	u.RoleIDs = append([]string(nil), roleIDs...)
	s.byID[userID] = u
	return nil
}

// NewUserID 生成用户 ID。避免引入 uuid 依赖（信创最小依赖原则），
// 128 位随机数 hex 编码，与 UUID v4 碰撞概率同级。
func NewUserID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("identity: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b)
}
