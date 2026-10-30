// Package dict 是 L1 数据字典模块（主流框架标配）：
// 字典类型 + 字典数据，供表单下拉与枚举统一维护。
package dict

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
	PermTypeList   = "dict:type:list"
	PermTypeManage = "dict:type:manage"
	PermDataManage = "dict:data:manage"
)

var (
	ErrNotFound     = errors.New("dict: not found")
	ErrTypeCodeUsed = errors.New("dict: type code already taken")
	ErrTypeHasData  = errors.New("dict: type still has dict data")
)

// Type 是字典类型（如 gender、notice_type）。
type Type struct {
	ID        string    `json:"id" gorm:"primaryKey;size:40"`
	Code      string    `json:"code" gorm:"uniqueIndex;size:64"`
	Name      string    `json:"name" gorm:"size:128"`
	Status    string    `json:"status" gorm:"size:16"`
	Remark    string    `json:"remark,omitempty" gorm:"size:255"`
	CreatedAt time.Time `json:"created_at"`
}

// Data 是字典数据项。
type Data struct {
	ID        string    `json:"id" gorm:"primaryKey;size:40"`
	TypeCode  string    `json:"type_code" gorm:"size:64;index"`
	Label     string    `json:"label" gorm:"size:128"`
	Value     string    `json:"value" gorm:"size:128"`
	Sort      int       `json:"sort"`
	Status    string    `json:"status" gorm:"size:16"`
	CreatedAt time.Time `json:"created_at"`
}

// Store 抽象字典持久化。
type Store interface {
	CreateType(ctx context.Context, t Type) error
	UpdateType(ctx context.Context, t Type) error
	DeleteType(ctx context.Context, id string) error
	ListTypes(ctx context.Context) ([]Type, error)
	CreateData(ctx context.Context, d Data) error
	UpdateData(ctx context.Context, d Data) error
	DeleteData(ctx context.Context, id string) error
	ListDataByType(ctx context.Context, typeCode string) ([]Data, error)
}

type MemStore struct {
	mu    sync.RWMutex
	types map[string]Type
	datas map[string]Data
}

func NewMemStore() *MemStore {
	return &MemStore{types: map[string]Type{}, datas: map[string]Data{}}
}

func (s *MemStore) CreateType(_ context.Context, t Type) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.types {
		if e.Code == t.Code {
			return ErrTypeCodeUsed
		}
	}
	s.types[t.ID] = t
	return nil
}

func (s *MemStore) UpdateType(_ context.Context, t Type) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, e := range s.types {
		if e.Code == t.Code && id != t.ID {
			return ErrTypeCodeUsed
		}
	}
	if _, ok := s.types[t.ID]; !ok {
		return ErrNotFound
	}
	s.types[t.ID] = t
	return nil
}

func (s *MemStore) DeleteType(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.types[id]
	if !ok {
		return ErrNotFound
	}
	for _, d := range s.datas {
		if d.TypeCode == t.Code {
			return ErrTypeHasData
		}
	}
	delete(s.types, id)
	return nil
}

func (s *MemStore) ListTypes(_ context.Context) ([]Type, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Type, 0, len(s.types))
	for _, t := range s.types {
		out = append(out, t)
	}
	return out, nil
}

func (s *MemStore) CreateData(_ context.Context, d Data) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.datas[d.ID] = d
	return nil
}

func (s *MemStore) UpdateData(_ context.Context, d Data) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.datas[d.ID]; !ok {
		return ErrNotFound
	}
	s.datas[d.ID] = d
	return nil
}

func (s *MemStore) DeleteData(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.datas[id]; !ok {
		return ErrNotFound
	}
	delete(s.datas, id)
	return nil
}

// ListDataByType 返回该类型全部数据项（含停用；激活过滤属展示层）。
func (s *MemStore) ListDataByType(_ context.Context, typeCode string) ([]Data, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Data
	for _, d := range s.datas {
		if d.TypeCode == typeCode {
			out = append(out, d)
		}
	}
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

func (*Module) Name() string           { return "dict" }
func (*Module) Version() string        { return Version }
func (*Module) Dependencies() []string { return []string{"identity"} }

func (*Module) Describe() app.ModuleInfo {
	return app.ModuleInfo{Name: "dict", Version: Version, Permissions: []string{
		PermTypeList, PermTypeManage, PermDataManage,
	}}
}

func (m *Module) Init(_ context.Context, a *app.App) error {
	idm, ok := a.Registry().MustGet("identity").(*identity.Module)
	if !ok {
		return errors.New("dict: identity module not available")
	}
	m.facade = idm.Facade()

	auth := m.facade.Auth
	req := m.facade.Require
	g := a.Router().Group("/api/v1/dict")

	g.Handle(http.MethodGet, "/types", auth(req(PermTypeList, m.listTypes)))
	g.Handle(http.MethodPost, "/types", auth(req(PermTypeManage, m.createType)))
	g.Handle(http.MethodPatch, "/types/{id}", auth(req(PermTypeManage, m.updateType)))
	g.Handle(http.MethodDelete, "/types/{id}", auth(req(PermTypeManage, m.deleteType)))

	g.Handle(http.MethodGet, "/datas", auth(req(PermTypeList, m.listDatas)))
	g.Handle(http.MethodPost, "/datas", auth(req(PermDataManage, m.createData)))
	g.Handle(http.MethodPatch, "/datas/{id}", auth(req(PermDataManage, m.updateData)))
	g.Handle(http.MethodDelete, "/datas/{id}", auth(req(PermDataManage, m.deleteData)))
	// 表单下拉数据源：登录级，只读激活项
	g.Handle(http.MethodGet, "/datas/by-type/{code}", auth(m.datasByType))
	return nil
}

func (*Module) Start(context.Context) error { return nil }
func (*Module) Stop(context.Context) error  { return nil }

// Store 供其他模块复用（如 notice 类型枚举）。
func (m *Module) Store() Store { return m.store }

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		web.Err(w, http.StatusBadRequest, "invalid json body")
		return false
	}
	return true
}

func (m *Module) listTypes(w http.ResponseWriter, r *http.Request) {
	ts, _ := m.store.ListTypes(r.Context())
	web.OK(w, ts)
}

func (m *Module) createType(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code   string `json:"code"`
		Name   string `json:"name"`
		Remark string `json:"remark"`
	}
	if !decode(w, r, &req) || req.Code == "" || req.Name == "" {
		web.Err(w, http.StatusBadRequest, "code and name are required")
		return
	}
	t := Type{ID: newID(), Code: req.Code, Name: req.Name, Status: "active",
		Remark: req.Remark, CreatedAt: time.Now().UTC()}
	if err := m.store.CreateType(r.Context(), t); err != nil {
		if err == ErrTypeCodeUsed {
			web.Err(w, http.StatusConflict, "type code already taken")
			return
		}
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.Created(w, t)
}

func (m *Module) updateType(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name   string `json:"name"`
		Status string `json:"status"`
		Remark string `json:"remark"`
	}
	if !decode(w, r, &req) {
		return
	}
	ts, _ := m.store.ListTypes(r.Context())
	for _, t := range ts {
		if t.ID == r.PathValue("id") {
			if req.Name != "" {
				t.Name = req.Name
			}
			if req.Status != "" {
				t.Status = req.Status
			}
			t.Remark = req.Remark
			if err := m.store.UpdateType(r.Context(), t); err != nil {
				web.Err(w, http.StatusInternalServerError, "internal error")
				return
			}
			web.OK(w, t)
			return
		}
	}
	web.Err(w, http.StatusNotFound, "type not found")
}

func (m *Module) deleteType(w http.ResponseWriter, r *http.Request) {
	if err := m.store.DeleteType(r.Context(), r.PathValue("id")); err != nil {
		switch err {
		case ErrNotFound:
			web.Err(w, http.StatusNotFound, "type not found")
		case ErrTypeHasData:
			web.Err(w, http.StatusConflict, "type still has data entries")
		default:
			web.Err(w, http.StatusInternalServerError, "internal error")
		}
		return
	}
	web.OK(w, nil)
}

func (m *Module) listDatas(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("type")
	datas, _ := m.store.ListDataByType(r.Context(), code)
	web.OK(w, datas)
}

func (m *Module) datasByType(w http.ResponseWriter, r *http.Request) {
	datas, _ := m.store.ListDataByType(r.Context(), r.PathValue("code"))
	active := make([]Data, 0, len(datas))
	for _, d := range datas {
		if d.Status == "active" {
			active = append(active, d)
		}
	}
	web.OK(w, active)
}

func (m *Module) createData(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TypeCode string `json:"type_code"`
		Label    string `json:"label"`
		Value    string `json:"value"`
		Sort     int    `json:"sort"`
	}
	if !decode(w, r, &req) || req.TypeCode == "" || req.Label == "" || req.Value == "" {
		web.Err(w, http.StatusBadRequest, "type_code, label and value are required")
		return
	}
	d := Data{ID: newID(), TypeCode: req.TypeCode, Label: req.Label, Value: req.Value,
		Sort: req.Sort, Status: "active", CreatedAt: time.Now().UTC()}
	if err := m.store.CreateData(r.Context(), d); err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.Created(w, d)
}

func (m *Module) updateData(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Label  string `json:"label"`
		Value  string `json:"value"`
		Sort   int    `json:"sort"`
		Status string `json:"status"`
	}
	if !decode(w, r, &req) {
		return
	}
	all, err := m.allDatas(r)
	if err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	for _, d := range all {
		if d.ID == r.PathValue("id") {
			if req.Label != "" {
				d.Label = req.Label
			}
			if req.Value != "" {
				d.Value = req.Value
			}
			d.Sort = req.Sort
			if req.Status != "" {
				d.Status = req.Status
			}
			if err := m.store.UpdateData(r.Context(), d); err != nil {
				web.Err(w, http.StatusInternalServerError, "internal error")
				return
			}
			web.OK(w, d)
			return
		}
	}
	web.Err(w, http.StatusNotFound, "data not found")
}

// allDatas 取全部数据项（跨类型）。
func (m *Module) allDatas(r *http.Request) ([]Data, error) {
	ts, err := m.store.ListTypes(r.Context())
	if err != nil {
		return nil, err
	}
	var out []Data
	for _, t := range ts {
		ds, err := m.store.ListDataByType(r.Context(), t.Code)
		if err != nil {
			return nil, err
		}
		out = append(out, ds...)
	}
	return out, nil
}

func (m *Module) deleteData(w http.ResponseWriter, r *http.Request) {
	if err := m.store.DeleteData(r.Context(), r.PathValue("id")); err != nil {
		if err == ErrNotFound {
			web.Err(w, http.StatusNotFound, "data not found")
			return
		}
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.OK(w, nil)
}
