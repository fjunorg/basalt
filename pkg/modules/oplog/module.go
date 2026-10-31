package oplog

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/basalt-go/basalt/pkg/kernel/app"
	"github.com/basalt-go/basalt/pkg/kernel/web"
	"github.com/basalt-go/basalt/pkg/modules/identity"
)

// Module 承载 oplog 查询与指标端点（采集中间件经 App.UseHandler
// 由装配层注入，见 serve.go）。
type Module struct {
	store  Store
	coll   *Collector
	facade *identity.Facade
}

// New 构造模块与采集器（内存滚动存储；parseUser 经 identity issuer 验签）。
func New(mode string) *Module {
	store := NewMemStore(10000)
	return &Module{store: store, coll: NewCollector(mode, store, nil)}
}

// NewWithStore 注入持久化存储（store 为 nil 时回退内存滚动）。
func NewWithStore(mode string, store Store) *Module {
	if store == nil {
		store = NewMemStore(10000)
	}
	return &Module{store: store, coll: NewCollector(mode, store, nil)}
}

// SetUserParser 注入 JWT 验签解析（identity Init 后装配层调用：
// oplog 构造时 identity 尚未 Init，故延迟注入）。
func (m *Module) SetUserParser(fn func(*http.Request) string) {
	m.coll.parseUser = fn
}

// Middleware 暴露采集中间件（装配层 UseHandler 用）。
func (m *Module) Middleware(next http.Handler) http.Handler {
	return m.coll.Middleware(next)
}

// Close 停止采集器（优雅退出刷出缓冲）。
func (m *Module) Close() { m.coll.Close() }

func (*Module) Name() string           { return "oplog" }
func (*Module) Version() string        { return Version }
func (*Module) Dependencies() []string { return []string{"identity"} }

func (*Module) Describe() app.ModuleInfo {
	return app.ModuleInfo{Name: "oplog", Version: Version, Permissions: []string{PermRead}}
}

func (m *Module) Init(_ context.Context, a *app.App) error {
	idm, ok := a.Registry().MustGet("identity").(*identity.Module)
	if !ok {
		return errors.New("oplog: identity module not available")
	}
	m.facade = idm.Facade()
	// JWT 验签还原请求者（identity 已 Init，issuer 就绪）。
	m.coll.parseUser = m.facade.ParseUserID

	g := a.Router().Group("/api/v1/oplog")
	auth := m.facade.Auth
	req := m.facade.Require
	g.Handle(http.MethodGet, "/logs", auth(req(PermRead, m.query)))
	g.Handle(http.MethodGet, "/metrics", auth(req(PermRead, m.metricsView)))
	return nil
}

func (*Module) Start(context.Context) error { return nil }

// Stop 优雅关闭采集器：排空通道缓冲后落库（不丢尾批）。
func (m *Module) Stop(context.Context) error {
	m.coll.Close()
	return nil
}

// query 分页查询：module/user_id/since/until/status/page/page_size。
func (m *Module) query(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := QueryFilter{
		Module:      q.Get("module"),
		UserID:      q.Get("user_id"),
		StatusClass: normalizeClass(q.Get("status")),
	}
	if v := q.Get("since"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.Since = t
		} else {
			web.Err(w, http.StatusBadRequest, "since must be RFC3339")
			return
		}
	}
	if v := q.Get("until"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.Until = t
		} else {
			web.Err(w, http.StatusBadRequest, "until must be RFC3339")
			return
		}
	}
	if v := q.Get("page"); v != "" {
		n, _ := strconv.Atoi(v)
		f.Page = n
	}
	if v := q.Get("page_size"); v != "" {
		n, _ := strconv.Atoi(v)
		f.PageSize = n
	}
	res, err := m.store.Query(f)
	if err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.OK(w, res)
}

func (m *Module) metricsView(w http.ResponseWriter, _ *http.Request) {
	web.OK(w, m.coll.Snapshot())
}

func normalizeClass(s string) string {
	switch strings.ToLower(s) {
	case "2xx", "4xx", "5xx", "other":
		return strings.ToLower(s)
	}
	return ""
}

var _ = json.Marshal
