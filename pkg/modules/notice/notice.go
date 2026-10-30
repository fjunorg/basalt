// Package notice 是 L1 通知公告模块：草稿/发布/全员已读视图。
// 站内私信与多渠道推送（邮件/短信/webhook）属 notify 模块规划。
package notice

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/basalt-go/basalt/pkg/kernel/app"
	"github.com/basalt-go/basalt/pkg/kernel/web"
	"github.com/basalt-go/basalt/pkg/modules/identity"
)

const Version = "0.1.0"

const (
	PermList    = "notice:list"
	PermManage  = "notice:manage"
	PermPublish = "notice:publish"
)

const (
	StatusDraft     = "draft"
	StatusPublished = "published"
	StatusRevoked   = "revoked"
)

var ErrNotFound = errors.New("notice: not found")

// Notice 是一条公告。
type Notice struct {
	ID          string     `json:"id" gorm:"primaryKey;size:40"`
	Title       string     `json:"title" gorm:"size:255"`
	Content     string     `json:"content" gorm:"type:text"`
	Type        string     `json:"type" gorm:"size:32"` // notice | announcement
	Status      string     `json:"status" gorm:"size:16;index"`
	CreatorID   string     `json:"creator_id" gorm:"size:40"`
	CreatedAt   time.Time  `json:"created_at"`
	PublishedAt *time.Time `json:"published_at,omitempty"`
}

// Store 抽象公告持久化。
type Store interface {
	Create(ctx context.Context, n Notice) error
	Update(ctx context.Context, n Notice) error
	Delete(ctx context.Context, id string) error
	Get(ctx context.Context, id string) (Notice, error)
	List(ctx context.Context, publishedOnly bool) ([]Notice, error)
}

type MemStore struct {
	mu sync.RWMutex
	m  map[string]Notice
}

func NewMemStore() *MemStore { return &MemStore{m: map[string]Notice{}} }

func (s *MemStore) Create(_ context.Context, n Notice) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[n.ID] = n
	return nil
}

func (s *MemStore) Update(_ context.Context, n Notice) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.m[n.ID]; !ok {
		return ErrNotFound
	}
	s.m[n.ID] = n
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

func (s *MemStore) Get(_ context.Context, id string) (Notice, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n, ok := s.m[id]
	if !ok {
		return Notice{}, ErrNotFound
	}
	return n, nil
}

func (s *MemStore) List(_ context.Context, publishedOnly bool) ([]Notice, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Notice
	for _, n := range s.m {
		if !publishedOnly || n.Status == StatusPublished {
			out = append(out, n)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
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

func (*Module) Name() string           { return "notice" }
func (*Module) Version() string        { return Version }
func (*Module) Dependencies() []string { return []string{"identity"} }

func (*Module) Describe() app.ModuleInfo {
	return app.ModuleInfo{Name: "notice", Version: Version, Permissions: []string{
		PermList, PermManage, PermPublish,
	}}
}

func (m *Module) Init(_ context.Context, a *app.App) error {
	idm, ok := a.Registry().MustGet("identity").(*identity.Module)
	if !ok {
		return errors.New("notice: identity module not available")
	}
	m.facade = idm.Facade()

	auth := m.facade.Auth
	req := m.facade.Require
	g := a.Router().Group("/api/v1/notices")
	g.Handle(http.MethodGet, "", auth(req(PermList, m.list)))
	g.Handle(http.MethodPost, "", auth(req(PermManage, m.create)))
	g.Handle(http.MethodPatch, "/{id}", auth(req(PermManage, m.update)))
	g.Handle(http.MethodDelete, "/{id}", auth(req(PermManage, m.delete)))
	g.Handle(http.MethodPost, "/{id}/publish", auth(req(PermPublish, m.publish)))
	g.Handle(http.MethodPost, "/{id}/revoke", auth(req(PermPublish, m.revoke)))
	// 全员已发布公告（登录级）
	g.Handle(http.MethodGet, "/published", auth(m.published))
	return nil
}

func (*Module) Start(context.Context) error { return nil }
func (*Module) Stop(context.Context) error  { return nil }

func (m *Module) Store() Store { return m.store }

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		web.Err(w, http.StatusBadRequest, "invalid json body")
		return false
	}
	return true
}

func (m *Module) list(w http.ResponseWriter, r *http.Request) {
	ns, _ := m.store.List(r.Context(), false)
	web.OK(w, ns)
}

func (m *Module) published(w http.ResponseWriter, r *http.Request) {
	ns, _ := m.store.List(r.Context(), true)
	web.OK(w, ns)
}

func (m *Module) create(w http.ResponseWriter, r *http.Request) {
	u, err := m.facade.User(r)
	if err != nil {
		web.Err(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var req struct {
		Title   string `json:"title"`
		Content string `json:"content"`
		Type    string `json:"type"`
	}
	if !decode(w, r, &req) || req.Title == "" {
		web.Err(w, http.StatusBadRequest, "title is required")
		return
	}
	if req.Type == "" {
		req.Type = "notice"
	}
	n := Notice{ID: newID(), Title: req.Title, Content: req.Content, Type: req.Type,
		Status: StatusDraft, CreatorID: u.ID, CreatedAt: time.Now().UTC()}
	if err := m.store.Create(r.Context(), n); err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.Created(w, n)
}

func (m *Module) update(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title   string `json:"title"`
		Content string `json:"content"`
	}
	if !decode(w, r, &req) {
		return
	}
	n, err := m.store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		web.Err(w, http.StatusNotFound, "notice not found")
		return
	}
	if n.Status != StatusDraft {
		web.Err(w, http.StatusConflict, "only draft notices can be edited")
		return
	}
	if req.Title != "" {
		n.Title = req.Title
	}
	n.Content = req.Content
	if err := m.store.Update(r.Context(), n); err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.OK(w, n)
}

func (m *Module) publish(w http.ResponseWriter, r *http.Request) {
	n, err := m.store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		web.Err(w, http.StatusNotFound, "notice not found")
		return
	}
	if n.Status != StatusDraft {
		web.Err(w, http.StatusConflict, "notice already published or revoked")
		return
	}
	now := time.Now().UTC()
	n.Status, n.PublishedAt = StatusPublished, &now
	if err := m.store.Update(r.Context(), n); err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.OK(w, n)
}

func (m *Module) revoke(w http.ResponseWriter, r *http.Request) {
	n, err := m.store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		web.Err(w, http.StatusNotFound, "notice not found")
		return
	}
	if n.Status != StatusPublished {
		web.Err(w, http.StatusConflict, "only published notices can be revoked")
		return
	}
	n.Status = StatusRevoked
	if err := m.store.Update(r.Context(), n); err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.OK(w, n)
}

func (m *Module) delete(w http.ResponseWriter, r *http.Request) {
	if err := m.store.Delete(r.Context(), r.PathValue("id")); err != nil {
		if err == ErrNotFound {
			web.Err(w, http.StatusNotFound, "notice not found")
			return
		}
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.OK(w, nil)
}
