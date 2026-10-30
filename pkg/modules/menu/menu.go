// Package menu 是 L1 动态菜单模块（竞品 Gap P0#4，ui-contract 兑现）：
// 菜单 CRUD + 角色绑定 + 按当前用户权限过滤的菜单树下发。
//
// 选型：独立模块（identity 提供角色权限读取缝），避免 identity
// 继续膨胀；角色绑定 API 复用 identity:role:manage 权限点。
// 前端回退契约见 deliverables/menu-api-contract.md：/me 返回空
// menus 时前端回退静态 meta.perms 映射。
package menu

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sort"
	"sync"
	"time"
)

const Version = "0.1.0"

// 权限点。
const (
	PermList   = "system:menu:list"
	PermManage = "system:menu:manage"
)

var (
	ErrNotFound    = errors.New("menu: not found")
	ErrDupPath     = errors.New("menu: path already taken")
	ErrHasChildren = errors.New("menu: has child menus")
	ErrRoleBound   = errors.New("menu: bound to roles")
	ErrBadParent   = errors.New("menu: parent invalid or would cycle")
	ErrNoParent    = errors.New("menu: parent not found")
)

// Menu 是一个菜单节点（path 与前端路由强约定）。
type Menu struct {
	ID        string    `json:"id" gorm:"primaryKey;size:48"`
	ParentID  string    `json:"parent_id" gorm:"size:48;index"`
	Name      string    `json:"name" gorm:"size:128"`
	Path      string    `json:"path" gorm:"size:255;index"`
	Icon      string    `json:"icon,omitempty" gorm:"size:64"`
	Perm      string    `json:"perm,omitempty" gorm:"size:64"`
	Sort      int       `json:"sort"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
}

// TableName 显式表名（跨模块同名防冲突）。
func (Menu) TableName() string { return "menus" }

// RoleMenu 是角色-菜单绑定（全量替换语义）。
type RoleMenu struct {
	RoleID  string    `json:"role_id" gorm:"primaryKey;size:40"`
	MenuIDs []string  `json:"menu_ids" gorm:"serializer:json"`
	Updated time.Time `json:"updated_at"`
}

// TableName 显式表名。
func (RoleMenu) TableName() string { return "role_menus" }

// MenuNode 是带子树的菜单视图（/me 下发用）。
type MenuNode struct {
	Menu
	Children []*MenuNode `json:"children"`
}

// Store 抽象菜单持久化（内存实现先行，GORM 见 stores/gormstore）。
type Store interface {
	Create(ctx context.Context, m Menu) error
	Update(ctx context.Context, m Menu) error
	Delete(ctx context.Context, id string) error
	Get(ctx context.Context, id string) (Menu, error)
	List(ctx context.Context) ([]Menu, error)
	CountByPath(ctx context.Context, path, excludeID string) (int64, error)

	SetRoleMenus(ctx context.Context, roleID string, menuIDs []string) error
	GetRoleMenus(ctx context.Context, roleID string) ([]string, error)
	BoundRoleCount(ctx context.Context, menuID string) (int64, error)
}

// ---------- 内存实现 ----------

type MemStore struct {
	mu    sync.RWMutex
	menus map[string]Menu
	roles map[string][]string // roleID → menuIDs
}

func NewMemStore() *MemStore {
	return &MemStore{menus: map[string]Menu{}, roles: map[string][]string{}}
}

func (s *MemStore) Create(_ context.Context, m Menu) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.menus {
		if e.Path == m.Path {
			return ErrDupPath
		}
	}
	s.menus[m.ID] = m
	return nil
}

func (s *MemStore) Update(_ context.Context, m Menu) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.menus[m.ID]; !ok {
		return ErrNotFound
	}
	for id, e := range s.menus {
		if e.Path == m.Path && id != m.ID {
			return ErrDupPath
		}
	}
	s.menus[m.ID] = m
	return nil
}

func (s *MemStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.menus[id]; !ok {
		return ErrNotFound
	}
	for _, e := range s.menus {
		if e.ParentID == id {
			return ErrHasChildren
		}
	}
	for r, ids := range s.roles {
		for _, mid := range ids {
			if mid == id {
				_ = r
				return ErrRoleBound
			}
		}
	}
	delete(s.menus, id)
	return nil
}

func (s *MemStore) Get(_ context.Context, id string) (Menu, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m, ok := s.menus[id]
	if !ok {
		return Menu{}, ErrNotFound
	}
	return m, nil
}

func (s *MemStore) List(_ context.Context) ([]Menu, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Menu, 0, len(s.menus))
	for _, m := range s.menus {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Sort != out[j].Sort {
			return out[i].Sort < out[j].Sort
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (s *MemStore) CountByPath(_ context.Context, path, excludeID string) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var n int64
	for id, e := range s.menus {
		if e.Path == path && id != excludeID {
			n++
		}
	}
	return n, nil
}

func (s *MemStore) SetRoleMenus(_ context.Context, roleID string, menuIDs []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.roles[roleID] = append([]string(nil), menuIDs...)
	return nil
}

func (s *MemStore) GetRoleMenus(_ context.Context, roleID string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string(nil), s.roles[roleID]...), nil
}

func (s *MemStore) BoundRoleCount(_ context.Context, menuID string) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var n int64
	for _, ids := range s.roles {
		for _, mid := range ids {
			if mid == menuID {
				n++
				break
			}
		}
	}
	return n, nil
}

// BuildTree 由扁平列表构建菜单树（父节点不存在时视为根）。
func BuildTree(menus []Menu) []*MenuNode {
	nodes := map[string]*MenuNode{}
	var roots []*MenuNode
	for i := range menus {
		nodes[menus[i].ID] = &MenuNode{Menu: menus[i]}
	}
	for _, m := range menus {
		n := nodes[m.ID]
		if p, ok := nodes[m.ParentID]; ok && m.ParentID != m.ID {
			p.Children = append(p.Children, n)
		} else {
			roots = append(roots, n)
		}
	}
	var sortNodes func(ns []*MenuNode)
	sortNodes = func(ns []*MenuNode) {
		sort.Slice(ns, func(i, j int) bool {
			if ns[i].Sort != ns[j].Sort {
				return ns[i].Sort < ns[j].Sort
			}
			return ns[i].ID < ns[j].ID
		})
		for _, c := range ns {
			sortNodes(c.Children)
		}
	}
	sortNodes(roots)
	return roots
}

// VisibleFor 按用户权限点过滤菜单（契约 §2.1 规则）：
// perm 关联且无权限 → 剪除子树；纯目录无可见子 → 剪除；enabled=false 全剪。
func VisibleFor(menus []Menu, hasPerm func(perm string) bool) []*MenuNode {
	byID := map[string]*MenuNode{}
	var flat []*MenuNode
	for i := range menus {
		m := menus[i]
		if !m.Enabled {
			continue
		}
		if m.Perm != "" && !hasPerm(m.Perm) {
			continue
		}
		n := &MenuNode{Menu: m}
		byID[m.ID] = n
		flat = append(flat, n)
	}
	var roots []*MenuNode
	for _, n := range flat {
		if p, ok := byID[n.ParentID]; ok && n.ParentID != n.ID {
			p.Children = append(p.Children, n)
		} else {
			roots = append(roots, n)
		}
	}
	// 原始树中拥有子节点的集合（区分"目录"与"无 perm 叶子"）
	hasChildrenOrig := map[string]bool{}
	for _, m := range menus {
		if m.ParentID != "" {
			hasChildrenOrig[m.ParentID] = true
		}
	}
	// 剪除失去全部可见子的纯目录；无 perm 的叶子（登录即可见页）保留
	return prune(roots, hasChildrenOrig)
}

func prune(nodes []*MenuNode, hasChildrenOrig map[string]bool) []*MenuNode {
	out := nodes[:0]
	for _, n := range nodes {
		n.Children = prune(n.Children, hasChildrenOrig)
		// 目录（原有子）的全部子被权限过滤 → 不下发空目录；
		// 无 perm 的叶子（概览/审批中心等登录可见页）保留
		if n.Perm == "" && hasChildrenOrig[n.ID] && len(n.Children) == 0 {
			continue
		}
		out = append(out, n)
	}
	return out
}

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
