package audit

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/basalt-go/basalt/pkg/kernel/app"
	"github.com/basalt-go/basalt/pkg/kernel/crypto"
	"github.com/basalt-go/basalt/pkg/kernel/web"
	"github.com/basalt-go/basalt/pkg/modules/identity"
)

var errIdentityUnavailable = errors.New("audit: identity module not available")

const Version = "0.2.0"

// 权限点。
const (
	PermRead = "audit:read"
)

// Module 是审计模块本体。依赖 identity 以复用认证与权限校验。
// 存储可插拔：默认内存链；持久化经 NewWithStore 注入 GORM 实现。
type Module struct {
	store Store
	algo  string
}

func New() *Module { return &Module{} }

// NewWithStore 注入持久化链实现（gormstore.AuditChain）。
func NewWithStore(s Store) *Module { return &Module{store: s} }

func (*Module) Name() string           { return "audit" }
func (*Module) Version() string        { return Version }
func (*Module) Dependencies() []string { return []string{"identity"} }

func (m *Module) Init(_ context.Context, a *app.App) error {
	idm, ok := a.Registry().MustGet("identity").(*identity.Module)
	if !ok {
		return errIdentityUnavailable
	}
	// 哈希算法跟随内核配置：BASALT_CRYPTO_ALGO=sm3 时审计链即为国密链。
	provider := crypto.NewProvider(a.Config().CryptoAlgo)
	m.algo = provider.Algorithm()
	if m.store == nil {
		m.store = NewChain(hasherAdapter{p: provider})
	}

	// 把审计链接入 identity：登录/授权等关键事件自动入链。
	idm.Facade().SetAuditSink(sinkAdapter{store: m.store, algo: m.algo})

	auth := idm.Facade().Auth
	req := idm.Facade().Require
	g := a.Router().Group("/api/v1/audit")
	g.Handle(http.MethodGet, "/events", auth(req(PermRead, m.listEvents)))
	g.Handle(http.MethodGet, "/verify", auth(req(PermRead, m.verify)))
	return nil
}

func (*Module) Start(context.Context) error { return nil }
func (*Module) Stop(context.Context) error  { return nil }

// Count 返回审计链事件总数（供监控与内置任务）。
func (m *Module) Count(ctx context.Context) int {
	events, err := m.store.List(ctx, "", 0)
	if err != nil {
		return 0
	}
	return len(events)
}

// Recorder 供依赖模块（workflow/gateway/tenant）写入业务审计事件。
func (m *Module) Recorder() Recorder { return storeRecorder{s: m.store} }

// storeRecorder 把 Store 适配为 Recorder（Append/Record 命名对齐）。
type storeRecorder struct{ s Store }

func (r storeRecorder) Record(ctx context.Context, e Event) error { return r.s.Append(ctx, e) }

// Algorithm 返回当前链哈希算法标识（审计报告需要披露）。
func (m *Module) Algorithm() string { return m.algo }

func (m *Module) listEvents(w http.ResponseWriter, r *http.Request) {
	module := r.URL.Query().Get("module")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	events, err := m.store.List(r.Context(), module, limit)
	if err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.OK(w, map[string]any{
		"algorithm": m.algo,
		"count":     len(events),
		"events":    events,
	})
}

type verifyResp struct {
	OK          bool `json:"ok"`
	BreakAt     int  `json:"break_at,omitempty"`
	TotalEvents int  `json:"total_events"`
}

func (m *Module) verify(w http.ResponseWriter, r *http.Request) {
	brk, err := m.store.Verify(r.Context())
	if err != nil {
		// 链断：返回明细而非 500——这是调用方要检出的正常业务状态。
		events, _ := m.store.List(r.Context(), "", 0)
		web.OK(w, verifyResp{OK: false, BreakAt: brk, TotalEvents: len(events)})
		return
	}
	events, _ := m.store.List(r.Context(), "", 0)
	web.OK(w, verifyResp{OK: true, TotalEvents: len(events)})
}

// sinkAdapter 实现 identity.AuditSink：identity 事件写入审计链。
type sinkAdapter struct {
	store Store
	algo  string
}

func (s sinkAdapter) Sink(module, event, actorID, remote string, kv ...any) {
	detail, err := json.Marshal(kvToMap(kv))
	if err != nil {
		detail = nil
	}
	_ = s.store.Append(context.Background(), Event{
		Module: module, Event: event, ActorID: actorID, Remote: remote, Detail: detail,
	})
}

func kvToMap(kv []any) map[string]any {
	m := make(map[string]any, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		if k, ok := kv[i].(string); ok {
			m[k] = kv[i+1]
		}
	}
	return m
}

// Describe 实现模块自描述。
func (*Module) Describe() app.ModuleInfo {
	return app.ModuleInfo{Name: "audit", Version: Version, Permissions: []string{PermRead}}
}
