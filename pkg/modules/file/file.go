// Package file 是 L1 文件管理模块：本地上传/下载/列表。
// S3 兼容对象存储（MinIO/国产 OSS）驱动经 Storage 接口接入（规划）。
package file

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/basalt-go/basalt/pkg/kernel/app"
	"github.com/basalt-go/basalt/pkg/kernel/web"
	"github.com/basalt-go/basalt/pkg/modules/identity"
)

const Version = "0.1.0"

const (
	PermList   = "file:list"
	PermUpload = "file:upload"
	PermDelete = "file:delete"
)

var ErrNotFound = errors.New("file: not found")

// Meta 是文件元信息。
type Meta struct {
	ID          string    `json:"id" gorm:"primaryKey;size:40"`
	Name        string    `json:"name" gorm:"size:255"`
	Size        int64     `json:"size"`
	ContentType string    `json:"content_type" gorm:"size:128"`
	StoragePath string    `json:"-" gorm:"size:512"`
	UploaderID  string    `json:"uploader_id" gorm:"size:40"`
	CreatedAt   time.Time `json:"created_at"`
}

// Store 抽象元信息持久化；Storage 抽象字节存放（本地磁盘先行）。
type Store interface {
	Create(ctx context.Context, m Meta) error
	Delete(ctx context.Context, id string) error
	Get(ctx context.Context, id string) (Meta, error)
	List(ctx context.Context) ([]Meta, error)
}

type Storage interface {
	Save(ctx context.Context, rel string, r io.Reader) (int64, error)
	Open(ctx context.Context, rel string) (io.ReadCloser, error)
	Remove(ctx context.Context, rel string) error
}

type MemStore struct {
	mu sync.RWMutex
	m  map[string]Meta
}

func NewMemStore() *MemStore { return &MemStore{m: map[string]Meta{}} }

func (s *MemStore) Create(_ context.Context, m Meta) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[m.ID] = m
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

func (s *MemStore) Get(_ context.Context, id string) (Meta, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m, ok := s.m[id]
	if !ok {
		return Meta{}, ErrNotFound
	}
	return m, nil
}

func (s *MemStore) List(_ context.Context) ([]Meta, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Meta, 0, len(s.m))
	for _, m := range s.m {
		out = append(out, m)
	}
	return out, nil
}

// LocalStorage 本地磁盘存储（BASALT_UPLOAD_DIR，默认 ./uploads）。
// 目录名按 日期/ID-原文件名 组织，避免重名覆盖。
type LocalStorage struct{ root string }

func NewLocalStorage(root string) (*LocalStorage, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	return &LocalStorage{root: root}, nil
}

func (l *LocalStorage) Save(_ context.Context, rel string, r io.Reader) (int64, error) {
	full := filepath.Join(l.root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return 0, err
	}
	f, err := os.Create(full)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return io.Copy(f, r)
}

func (l *LocalStorage) Open(_ context.Context, rel string) (io.ReadCloser, error) {
	return os.Open(filepath.Join(l.root, rel))
}

func (l *LocalStorage) Remove(_ context.Context, rel string) error {
	return os.Remove(filepath.Join(l.root, rel))
}

// MaxUploadSize 上传大小上限（G 端合规默认 32MB）。
const MaxUploadSize = 32 << 20

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// safeName 过滤路径穿越与控制字符。
func safeName(name string) string {
	name = filepath.Base(name)
	var b strings.Builder
	for _, r := range name {
		if r >= 32 && r != 127 && r != '/' && r != '\\' && r != ':' && r != '*' && r != '?' && r != '"' && r != '<' && r != '>' && r != '|' {
			b.WriteRune(r)
		}
	}
	out := b.String()
	if out == "" || out == "." || out == ".." {
		out = "unnamed"
	}
	return out
}

type Module struct {
	store   Store
	storage Storage
	facade  *identity.Facade
}

func New(storage Storage) *Module { return &Module{store: NewMemStore(), storage: storage} }

func NewWithStore(store Store, storage Storage) *Module {
	return &Module{store: store, storage: storage}
}

func (*Module) Name() string           { return "file" }
func (*Module) Version() string        { return Version }
func (*Module) Dependencies() []string { return []string{"identity"} }

func (*Module) Describe() app.ModuleInfo {
	return app.ModuleInfo{Name: "file", Version: Version, Permissions: []string{
		PermList, PermUpload, PermDelete,
	}}
}

func (m *Module) Init(_ context.Context, a *app.App) error {
	idm, ok := a.Registry().MustGet("identity").(*identity.Module)
	if !ok {
		return errors.New("file: identity module not available")
	}
	m.facade = idm.Facade()

	auth := m.facade.Auth
	req := m.facade.Require
	g := a.Router().Group("/api/v1/files")
	g.Handle(http.MethodGet, "", auth(req(PermList, m.list)))
	g.Handle(http.MethodPost, "", auth(req(PermUpload, m.upload)))
	g.Handle(http.MethodDelete, "/{id}", auth(req(PermDelete, m.deleteFile)))
	// 下载：登录级（文件 ACL 随版本迭代）
	g.Handle(http.MethodGet, "/{id}/download", auth(m.download))
	return nil
}

func (*Module) Start(context.Context) error { return nil }
func (*Module) Stop(context.Context) error  { return nil }

func (m *Module) Store() Store { return m.store }

func (m *Module) list(w http.ResponseWriter, r *http.Request) {
	ms, _ := m.store.List(r.Context())
	web.OK(w, ms)
}

func (m *Module) upload(w http.ResponseWriter, r *http.Request) {
	u, err := m.facade.User(r)
	if err != nil {
		web.Err(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxUploadSize)
	f, header, err := r.FormFile("file")
	if err != nil {
		web.Err(w, http.StatusBadRequest, "multipart field 'file' required (max 32MB)")
		return
	}
	defer f.Close()

	id := newID()
	name := safeName(header.Filename)
	rel := filepath.Join(time.Now().UTC().Format("20060102"), id+"-"+name)
	size, err := m.storage.Save(r.Context(), rel, f)
	if err != nil {
		web.Err(w, http.StatusInternalServerError, "save failed")
		return
	}
	meta := Meta{
		ID: id, Name: name, Size: size,
		ContentType: header.Header.Get("Content-Type"),
		StoragePath: rel, UploaderID: u.ID, CreatedAt: time.Now().UTC(),
	}
	if err := m.store.Create(r.Context(), meta); err != nil {
		_ = m.storage.Remove(r.Context(), rel)
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.Created(w, meta)
}

func (m *Module) download(w http.ResponseWriter, r *http.Request) {
	meta, err := m.store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		web.Err(w, http.StatusNotFound, "file not found")
		return
	}
	rc, err := m.storage.Open(r.Context(), meta.StoragePath)
	if err != nil {
		web.Err(w, http.StatusNotFound, "file content missing")
		return
	}
	defer rc.Close()
	// 上传上限 32MB：整体读入后经 bytes.Reader 提供Seek（对象存储
	// 流式驱动接入后改 Range 透传）。
	data, err := io.ReadAll(io.LimitReader(rc, MaxUploadSize+1))
	if err != nil {
		web.Err(w, http.StatusInternalServerError, "read failed")
		return
	}
	ct := meta.ContentType
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	// attachment 防止内联执行（上传内容不可信的安全基线）
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="%s"`, strings.ReplaceAll(meta.Name, `"`, "")))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, meta.Name, meta.CreatedAt, bytes.NewReader(data))
}

func (m *Module) deleteFile(w http.ResponseWriter, r *http.Request) {
	meta, err := m.store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		web.Err(w, http.StatusNotFound, "file not found")
		return
	}
	if err := m.store.Delete(r.Context(), meta.ID); err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	_ = m.storage.Remove(r.Context(), meta.StoragePath)
	web.OK(w, nil)
}
