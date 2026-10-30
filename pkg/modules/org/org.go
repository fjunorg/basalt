// Package org 是 L1 组织架构模块（部门树 + 岗位）。
//
// 对标主流框架标配（RuoYi 系/芋道 org 模块）：部门树 CRUD、岗位 CRUD，
// 用户经 identity 的 DeptID/PostID 关联。数据权限（部门级数据范围）
// 建立在部门树之上，随数据权限 DSL 迭代落地。
package org

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/basalt-go/basalt/pkg/kernel/app"
	"github.com/basalt-go/basalt/pkg/kernel/web"
	"github.com/basalt-go/basalt/pkg/modules/identity"
)

const Version = "0.1.0"

// 权限点。
const (
	PermDeptList   = "org:dept:list"
	PermDeptManage = "org:dept:manage"
	PermPostList   = "org:post:list"
	PermPostManage = "org:post:manage"
)

var (
	ErrNotFound    = errors.New("org: not found")
	ErrHasChildren = errors.New("org: department has child departments")
	ErrDupCode     = errors.New("org: post code already taken")
)

// Dept 是部门（树形，ParentID 为空表示根）。
type Dept struct {
	ID        string    `json:"id" gorm:"primaryKey;size:40"`
	ParentID  string    `json:"parent_id" gorm:"size:40;index"`
	Name      string    `json:"name" gorm:"size:128"`
	Sort      int       `json:"sort"`
	Status    string    `json:"status" gorm:"size:16"`
	CreatedAt time.Time `json:"created_at"`
}

// DeptNode 是带子树的部门视图。
type DeptNode struct {
	Dept
	Children []*DeptNode `json:"children,omitempty"`
}

// Post 是岗位。
type Post struct {
	ID        string    `json:"id" gorm:"primaryKey;size:40"`
	Code      string    `json:"code" gorm:"uniqueIndex;size:64"`
	Name      string    `json:"name" gorm:"size:128"`
	Sort      int       `json:"sort"`
	Status    string    `json:"status" gorm:"size:16"`
	CreatedAt time.Time `json:"created_at"`
}

// Store 抽象组织持久化（内存实现先行，GORM 见 stores/gormstore）。
type Store interface {
	CreateDept(ctx context.Context, d Dept) error
	UpdateDept(ctx context.Context, d Dept) error
	DeleteDept(ctx context.Context, id string) error
	ListDepts(ctx context.Context) ([]Dept, error)
	CreatePost(ctx context.Context, p Post) error
	UpdatePost(ctx context.Context, p Post) error
	DeletePost(ctx context.Context, id string) error
	ListPosts(ctx context.Context) ([]Post, error)
}

type MemStore struct {
	mu    sync.RWMutex
	depts map[string]Dept
	posts map[string]Post
}

func NewMemStore() *MemStore {
	return &MemStore{depts: map[string]Dept{}, posts: map[string]Post{}}
}

func (s *MemStore) CreateDept(_ context.Context, d Dept) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.depts[d.ID] = d
	return nil
}

func (s *MemStore) UpdateDept(_ context.Context, d Dept) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.depts[d.ID]; !ok {
		return ErrNotFound
	}
	s.depts[d.ID] = d
	return nil
}

func (s *MemStore) DeleteDept(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.depts {
		if d.ParentID == id {
			return ErrHasChildren
		}
	}
	if _, ok := s.depts[id]; !ok {
		return ErrNotFound
	}
	delete(s.depts, id)
	return nil
}

func (s *MemStore) ListDepts(_ context.Context) ([]Dept, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Dept, 0, len(s.depts))
	for _, d := range s.depts {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Sort < out[j].Sort })
	return out, nil
}

func (s *MemStore) CreatePost(_ context.Context, p Post) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.posts {
		if e.Code == p.Code {
			return ErrDupCode
		}
	}
	s.posts[p.ID] = p
	return nil
}

func (s *MemStore) UpdatePost(_ context.Context, p Post) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, e := range s.posts {
		if e.Code == p.Code && id != p.ID {
			return ErrDupCode
		}
	}
	if _, ok := s.posts[p.ID]; !ok {
		return ErrNotFound
	}
	s.posts[p.ID] = p
	return nil
}

func (s *MemStore) DeletePost(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.posts[id]; !ok {
		return ErrNotFound
	}
	delete(s.posts, id)
	return nil
}

func (s *MemStore) ListPosts(_ context.Context) ([]Post, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Post, 0, len(s.posts))
	for _, p := range s.posts {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Sort < out[j].Sort })
	return out, nil
}

// BuildTree 由扁平列表构建部门树。
func BuildTree(depts []Dept) []*DeptNode {
	nodes := map[string]*DeptNode{}
	var roots []*DeptNode
	for i := range depts {
		nodes[depts[i].ID] = &DeptNode{Dept: depts[i]}
	}
	for _, d := range depts {
		n := nodes[d.ID]
		if p, ok := nodes[d.ParentID]; ok && d.ParentID != d.ID {
			p.Children = append(p.Children, n)
		} else {
			roots = append(roots, n)
		}
	}
	return roots
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

func (*Module) Name() string           { return "org" }
func (*Module) Version() string        { return Version }
func (*Module) Dependencies() []string { return []string{"identity"} }

func (*Module) Describe() app.ModuleInfo {
	return app.ModuleInfo{Name: "org", Version: Version, Permissions: []string{
		PermDeptList, PermDeptManage, PermPostList, PermPostManage,
	}}
}

func (m *Module) Init(_ context.Context, a *app.App) error {
	idm, ok := a.Registry().MustGet("identity").(*identity.Module)
	if !ok {
		return fmt.Errorf("org: identity module not available")
	}
	m.facade = idm.Facade()

	auth := m.facade.Auth
	req := m.facade.Require
	g := a.Router().Group("/api/v1/org")

	g.Handle(http.MethodGet, "/depts/tree", auth(req(PermDeptList, m.deptTree)))
	g.Handle(http.MethodPost, "/depts", auth(req(PermDeptManage, m.createDept)))
	g.Handle(http.MethodPatch, "/depts/{id}", auth(req(PermDeptManage, m.updateDept)))
	g.Handle(http.MethodDelete, "/depts/{id}", auth(req(PermDeptManage, m.deleteDept)))

	g.Handle(http.MethodGet, "/posts", auth(req(PermPostList, m.listPosts)))
	g.Handle(http.MethodPost, "/posts", auth(req(PermPostManage, m.createPost)))
	g.Handle(http.MethodPatch, "/posts/{id}", auth(req(PermPostManage, m.updatePost)))
	g.Handle(http.MethodDelete, "/posts/{id}", auth(req(PermPostManage, m.deletePost)))
	return nil
}

func (*Module) Start(context.Context) error { return nil }
func (*Module) Stop(context.Context) error  { return nil }

// Store 供其他模块（数据权限）取用。
func (m *Module) Store() Store { return m.store }

func (m *Module) deptTree(w http.ResponseWriter, r *http.Request) {
	depts, err := m.store.ListDepts(r.Context())
	if err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.OK(w, BuildTree(depts))
}

func (m *Module) createDept(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ParentID string `json:"parent_id"`
		Name     string `json:"name"`
		Sort     int    `json:"sort"`
	}
	if !decode(w, r, &req) || req.Name == "" {
		web.Err(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.ParentID != "" {
		if _, err := m.store.ListDepts(r.Context()); err != nil {
			web.Err(w, http.StatusInternalServerError, "internal error")
			return
		}
	}
	d := Dept{ID: newID(), ParentID: req.ParentID, Name: req.Name,
		Sort: req.Sort, Status: "active", CreatedAt: time.Now().UTC()}
	if err := m.store.CreateDept(r.Context(), d); err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.Created(w, d)
}

func (m *Module) updateDept(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name   string `json:"name"`
		Sort   int    `json:"sort"`
		Status string `json:"status"`
	}
	if !decode(w, r, &req) {
		return
	}
	depts, _ := m.store.ListDepts(r.Context())
	for _, d := range depts {
		if d.ID == r.PathValue("id") {
			if req.Name != "" {
				d.Name = req.Name
			}
			d.Sort = req.Sort
			if req.Status != "" {
				d.Status = req.Status
			}
			if err := m.store.UpdateDept(r.Context(), d); err != nil {
				web.Err(w, http.StatusInternalServerError, "internal error")
				return
			}
			web.OK(w, d)
			return
		}
	}
	web.Err(w, http.StatusNotFound, "department not found")
}

func (m *Module) deleteDept(w http.ResponseWriter, r *http.Request) {
	if err := m.store.DeleteDept(r.Context(), r.PathValue("id")); err != nil {
		switch err {
		case ErrHasChildren:
			web.Err(w, http.StatusConflict, "department has child departments")
		case ErrNotFound:
			web.Err(w, http.StatusNotFound, "department not found")
		default:
			web.Err(w, http.StatusInternalServerError, "internal error")
		}
		return
	}
	web.OK(w, nil)
}

func (m *Module) listPosts(w http.ResponseWriter, r *http.Request) {
	posts, err := m.store.ListPosts(r.Context())
	if err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.OK(w, posts)
}

func (m *Module) createPost(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
		Name string `json:"name"`
		Sort int    `json:"sort"`
	}
	if !decode(w, r, &req) || req.Code == "" || req.Name == "" {
		return
	}
	p := Post{ID: newID(), Code: req.Code, Name: req.Name,
		Sort: req.Sort, Status: "active", CreatedAt: time.Now().UTC()}
	if err := m.store.CreatePost(r.Context(), p); err != nil {
		if err == ErrDupCode {
			web.Err(w, http.StatusConflict, "post code already taken")
			return
		}
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.Created(w, p)
}

func (m *Module) updatePost(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name   string `json:"name"`
		Sort   int    `json:"sort"`
		Status string `json:"status"`
	}
	if !decode(w, r, &req) {
		return
	}
	posts, _ := m.store.ListPosts(r.Context())
	for _, p := range posts {
		if p.ID == r.PathValue("id") {
			if req.Name != "" {
				p.Name = req.Name
			}
			p.Sort = req.Sort
			if req.Status != "" {
				p.Status = req.Status
			}
			if err := m.store.UpdatePost(r.Context(), p); err != nil {
				if err == ErrDupCode {
					web.Err(w, http.StatusConflict, "post code already taken")
					return
				}
				web.Err(w, http.StatusInternalServerError, "internal error")
				return
			}
			web.OK(w, p)
			return
		}
	}
	web.Err(w, http.StatusNotFound, "post not found")
}

func (m *Module) deletePost(w http.ResponseWriter, r *http.Request) {
	if err := m.store.DeletePost(r.Context(), r.PathValue("id")); err != nil {
		if err == ErrNotFound {
			web.Err(w, http.StatusNotFound, "post not found")
			return
		}
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.OK(w, nil)
}

// decode 统一 JSON 解码与错误响应；返回 false 表示已写响应。
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		web.Err(w, http.StatusBadRequest, "invalid json body")
		return false
	}
	return true
}
