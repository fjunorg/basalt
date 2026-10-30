// Package sysconfig 是 L1 参数配置模块（主流框架标配）：
// 系统运行参数的动态 KV（区别于 env 启动配置），登录级只读。
package sysconfig

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/basalt-go/basalt/pkg/kernel/app"
	"github.com/basalt-go/basalt/pkg/kernel/web"
	"github.com/basalt-go/basalt/pkg/modules/identity"
)

const Version = "0.1.0"

const (
	PermList   = "config:list"
	PermManage = "config:manage"
)

var (
	ErrNotFound = errors.New("config: not found")
	ErrKeyUsed  = errors.New("config: key already taken")
)

// Item 是一条系统参数。
type Item struct {
	ID        string    `json:"id" gorm:"primaryKey;size:40"`
	Key       string    `json:"key" gorm:"uniqueIndex;size:128"`
	Value     string    `json:"value" gorm:"size:1024"`
	Name      string    `json:"name" gorm:"size:128"`
	Remark    string    `json:"remark,omitempty" gorm:"size:255"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Store 抽象参数持久化。
type Store interface {
	Create(ctx context.Context, it Item) error
	Update(ctx context.Context, it Item) error
	Delete(ctx context.Context, id string) error
	List(ctx context.Context) ([]Item, error)
	GetByKey(ctx context.Context, key string) (Item, error)
}

type MemStore struct {
	mu   sync.RWMutex
	byID map[string]Item
}

func NewMemStore() *MemStore { return &MemStore{byID: map[string]Item{}} }

func (s *MemStore) Create(_ context.Context, it Item) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.byID {
		if e.Key == it.Key {
			return ErrKeyUsed
		}
	}
	s.byID[it.ID] = it
	return nil
}

func (s *MemStore) Update(_ context.Context, it Item) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, e := range s.byID {
		if e.Key == it.Key && id != it.ID {
			return ErrKeyUsed
		}
	}
	if _, ok := s.byID[it.ID]; !ok {
		return ErrNotFound
	}
	s.byID[it.ID] = it
	return nil
}

func (s *MemStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byID[id]; !ok {
		return ErrNotFound
	}
	delete(s.byID, id)
	return nil
}

func (s *MemStore) List(_ context.Context) ([]Item, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Item, 0, len(s.byID))
	for _, it := range s.byID {
		out = append(out, it)
	}
	return out, nil
}

func (s *MemStore) GetByKey(_ context.Context, key string) (Item, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, it := range s.byID {
		if it.Key == key {
			return it, nil
		}
	}
	return Item{}, ErrNotFound
}

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

type Module struct {
	store  Store
	facade *identity.Facade
}

func New() *Module                 { return &Module{store: NewMemStore()} }
func NewWithStore(s Store) *Module { return &Module{store: s} }

func (*Module) Name() string           { return "sysconfig" }
func (*Module) Version() string        { return Version }
func (*Module) Dependencies() []string { return []string{"identity"} }

func (*Module) Describe() app.ModuleInfo {
	return app.ModuleInfo{Name: "sysconfig", Version: Version, Permissions: []string{PermList, PermManage}}
}

func (m *Module) Init(_ context.Context, a *app.App) error {
	idm, ok := a.Registry().MustGet("identity").(*identity.Module)
	if !ok {
		return errors.New("sysconfig: identity module not available")
	}
	m.facade = idm.Facade()

	auth := m.facade.Auth
	req := m.facade.Require
	g := a.Router().Group("/api/v1/configs")
	g.Handle(http.MethodGet, "", auth(req(PermList, m.list)))
	g.Handle(http.MethodPost, "", auth(req(PermManage, m.create)))
	g.Handle(http.MethodPatch, "/{id}", auth(req(PermManage, m.update)))
	g.Handle(http.MethodDelete, "/{id}", auth(req(PermManage, m.delete)))
	// 登录级按 key 只读（业务代码取参数）
	g.Handle(http.MethodGet, "/by-key/{key}", auth(m.byKey))
	return nil
}

func (*Module) Start(context.Context) error { return nil }
func (*Module) Stop(context.Context) error  { return nil }

// Store 供业务模块读取参数。
func (m *Module) Store() Store { return m.store }

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		web.Err(w, http.StatusBadRequest, "invalid json body")
		return false
	}
	return true
}

func (m *Module) list(w http.ResponseWriter, r *http.Request) {
	items, _ := m.store.List(r.Context())
	web.OK(w, items)
}

func (m *Module) create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key    string `json:"key"`
		Value  string `json:"value"`
		Name   string `json:"name"`
		Remark string `json:"remark"`
	}
	if !decode(w, r, &req) || req.Key == "" {
		web.Err(w, http.StatusBadRequest, "key is required")
		return
	}
	it := Item{ID: newID(), Key: req.Key, Value: req.Value, Name: req.Name,
		Remark: req.Remark, UpdatedAt: time.Now().UTC()}
	if err := m.store.Create(r.Context(), it); err != nil {
		if err == ErrKeyUsed {
			web.Err(w, http.StatusConflict, "key already taken")
			return
		}
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.Created(w, it)
}

func (m *Module) update(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Value string `json:"value"`
		Name  string `json:"name"`
	}
	if !decode(w, r, &req) {
		return
	}
	items, _ := m.store.List(r.Context())
	for _, it := range items {
		if it.ID == r.PathValue("id") {
			it.Value = req.Value
			if req.Name != "" {
				it.Name = req.Name
			}
			it.UpdatedAt = time.Now().UTC()
			if err := m.store.Update(r.Context(), it); err != nil {
				web.Err(w, http.StatusInternalServerError, "internal error")
				return
			}
			web.OK(w, it)
			return
		}
	}
	web.Err(w, http.StatusNotFound, "config not found")
}

func (m *Module) delete(w http.ResponseWriter, r *http.Request) {
	if err := m.store.Delete(r.Context(), r.PathValue("id")); err != nil {
		if err == ErrNotFound {
			web.Err(w, http.StatusNotFound, "config not found")
			return
		}
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.OK(w, nil)
}

func (m *Module) byKey(w http.ResponseWriter, r *http.Request) {
	it, err := m.store.GetByKey(r.Context(), r.PathValue("key"))
	if err != nil {
		web.Err(w, http.StatusNotFound, "config key not found")
		return
	}
	web.OK(w, it)
}
