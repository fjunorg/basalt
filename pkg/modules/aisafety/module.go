package aisafety

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/basalt-go/basalt/pkg/ai/gateway"
	"github.com/basalt-go/basalt/pkg/kernel/app"
	"github.com/basalt-go/basalt/pkg/kernel/web"
	"github.com/basalt-go/basalt/pkg/modules/audit"
	"github.com/basalt-go/basalt/pkg/modules/identity"
)

type Module struct {
	store    Store
	engine   *Engine
	facade   *identity.Facade
	recorder audit.Recorder
}

func New() *Module { return NewWithStore(NewMemStore()) }

// NewWithStore 注入持久化实现（GORM 落盘、重启恢复）；
// New() 的纯内存路径保留（测试/演示场景可退回）。
func NewWithStore(s Store) *Module {
	return &Module{store: s, engine: NewEngine()}
}

// Inspector 返回网关审核器（经 gateway.SetInspector 注入）。
func (m *Module) Inspector() gateway.Inspector { return m.engine }

func (*Module) Name() string           { return "ai-safety" }
func (*Module) Version() string        { return Version }
func (*Module) Dependencies() []string { return []string{"identity", "audit"} }

func (*Module) Describe() app.ModuleInfo {
	return app.ModuleInfo{Name: "ai-safety", Version: Version,
		Permissions: []string{PermList, PermManage}}
}

func (m *Module) Init(ctx context.Context, a *app.App) error {
	idm, ok := a.Registry().MustGet("identity").(*identity.Module)
	if !ok {
		return errors.New("ai-safety: identity module not available")
	}
	adm, ok := a.Registry().MustGet("audit").(*audit.Module)
	if !ok {
		return errors.New("ai-safety: audit module not available")
	}
	m.facade = idm.Facade()
	m.recorder = adm.Recorder()

	// 启动即装载词库/策略快照（服务热路径零 DB 查询）
	if err := m.engine.Reload(ctx, m.store); err != nil {
		return err
	}

	auth := m.facade.Auth
	req := m.facade.Require
	g := a.Router().Group("/api/v1/ai-safety")

	g.Handle(http.MethodGet, "/terms", auth(req(PermList, m.listTerms)))
	g.Handle(http.MethodPost, "/terms", auth(req(PermManage, m.createTerm)))
	g.Handle(http.MethodPatch, "/terms/{id}", auth(req(PermManage, m.updateTerm)))
	g.Handle(http.MethodDelete, "/terms/{id}", auth(req(PermManage, m.deleteTerm)))
	g.Handle(http.MethodPost, "/terms/import", auth(req(PermManage, m.importTerms)))
	g.Handle(http.MethodGet, "/terms/export", auth(req(PermList, m.exportTerms)))

	g.Handle(http.MethodGet, "/policies", auth(req(PermList, m.listPolicies)))
	g.Handle(http.MethodPut, "/policies/{tenant}", auth(req(PermManage, m.setPolicy)))

	g.Handle(http.MethodGet, "/settings", auth(req(PermList, m.getSettings)))
	g.Handle(http.MethodPut, "/settings", auth(req(PermManage, m.putSettings)))

	// 审核试算（运维验证词库与策略效果）
	g.Handle(http.MethodPost, "/check", auth(req(PermManage, m.check)))
	return nil
}

// Start 确保默认策略存在（首次部署开箱即用：平台默认 reject）。
func (m *Module) Start(ctx context.Context) error {
	if _, err := m.store.GetPolicy(ctx, ""); err != nil {
		if err := m.store.SetPolicy(ctx, Policy{TenantID: "", Action: DefaultAction, UpdatedAt: time.Now().UTC()}); err != nil {
			return err
		}
	}
	return m.engine.Reload(ctx, m.store)
}

func (*Module) Stop(context.Context) error { return nil }

// reload 变更后刷新引擎快照（沿用现有请求 ctx）。
func (m *Module) reload(ctx context.Context) {
	_ = m.engine.Reload(ctx, m.store)
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		web.Err(w, http.StatusBadRequest, "invalid json body")
		return false
	}
	return true
}

// ---------- 词库 ----------

func (m *Module) listTerms(w http.ResponseWriter, r *http.Request) {
	terms, err := m.store.ListTerms(r.Context())
	if err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.OK(w, terms)
}

func (m *Module) createTerm(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Word        string `json:"word"`
		Category    string `json:"category"`
		Replacement string `json:"replacement"`
		Enabled     *bool  `json:"enabled"`
	}
	if !decode(w, r, &req) || strings.TrimSpace(req.Word) == "" {
		web.Err(w, http.StatusBadRequest, "word is required")
		return
	}
	t := Term{ID: newID(), Word: strings.TrimSpace(req.Word), Category: req.Category,
		Replacement: req.Replacement, Enabled: req.Enabled == nil || *req.Enabled,
		CreatedAt: time.Now().UTC()}
	if err := m.store.CreateTerm(r.Context(), t); err != nil {
		if errors.Is(err, ErrDupWord) {
			web.Err(w, http.StatusConflict, "term word already exists")
			return
		}
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	m.reload(r.Context())
	web.Created(w, t)
}

func (m *Module) updateTerm(w http.ResponseWriter, r *http.Request) {
	terms, _ := m.store.ListTerms(r.Context())
	var cur Term
	found := false
	for _, t := range terms {
		if t.ID == r.PathValue("id") {
			cur, found = t, true
			break
		}
	}
	if !found {
		web.Err(w, http.StatusNotFound, "term not found")
		return
	}
	var req struct {
		Word        *string `json:"word"`
		Category    *string `json:"category"`
		Replacement *string `json:"replacement"`
		Enabled     *bool   `json:"enabled"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Word != nil && strings.TrimSpace(*req.Word) != "" {
		cur.Word = strings.TrimSpace(*req.Word)
	}
	if req.Category != nil {
		cur.Category = *req.Category
	}
	if req.Replacement != nil {
		cur.Replacement = *req.Replacement
	}
	if req.Enabled != nil {
		cur.Enabled = *req.Enabled
	}
	if err := m.store.UpdateTerm(r.Context(), cur); err != nil {
		switch {
		case errors.Is(err, ErrDupWord):
			web.Err(w, http.StatusConflict, "term word already exists")
		case errors.Is(err, ErrNotFound):
			web.Err(w, http.StatusNotFound, "term not found")
		default:
			web.Err(w, http.StatusInternalServerError, "internal error")
		}
		return
	}
	m.reload(r.Context())
	web.OK(w, cur)
}

func (m *Module) deleteTerm(w http.ResponseWriter, r *http.Request) {
	if err := m.store.DeleteTerm(r.Context(), r.PathValue("id")); err != nil {
		if errors.Is(err, ErrNotFound) {
			web.Err(w, http.StatusNotFound, "term not found")
			return
		}
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	m.reload(r.Context())
	web.OK(w, nil)
}

// importTerms 批量导入：JSON 数组、JSON 字符串或纯文本
//（每行一词，# 注释行忽略）。重复词幂等跳过。
func (m *Module) importTerms(w http.ResponseWriter, r *http.Request) {
	body, err := readAll(r)
	if err != nil {
		web.Err(w, http.StatusBadRequest, "invalid body")
		return
	}
	var terms []Term
	trimmed := strings.TrimSpace(body)
	if strings.HasPrefix(trimmed, "[") {
		var reqs []struct {
			Word        string `json:"word"`
			Category    string `json:"category"`
			Replacement string `json:"replacement"`
		}
		if err := json.Unmarshal([]byte(trimmed), &reqs); err != nil {
			web.Err(w, http.StatusBadRequest, "invalid json array")
			return
		}
		for _, q := range reqs {
			terms = append(terms, Term{Word: strings.TrimSpace(q.Word),
				Category: q.Category, Replacement: q.Replacement, Enabled: true})
		}
	} else {
		// 兼容 JSON 字符串 body（Content-Type: application/json 时
		// 客户端把纯文本包成 JSON 字符串发送）
		var s string
		if strings.HasPrefix(trimmed, `"`) && json.Unmarshal([]byte(trimmed), &s) == nil {
			trimmed = s
		}
		for _, line := range strings.Split(trimmed, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			terms = append(terms, Term{Word: line, Enabled: true})
		}
	}
	var imported, skipped int
	for _, t := range terms {
		if t.Word == "" {
			skipped++
			continue
		}
		t.ID, t.CreatedAt = newID(), time.Now().UTC()
		if err := m.store.CreateTerm(r.Context(), t); err != nil {
			skipped++ // 重复词跳过（幂等导入）
			continue
		}
		imported++
	}
	m.reload(r.Context())
	m.audit(r, "terms.imported", "", "imported", imported, "skipped", skipped)
	web.OK(w, map[string]int{"imported": imported, "skipped": skipped})
}

func (m *Module) exportTerms(w http.ResponseWriter, r *http.Request) {
	terms, err := m.store.ListTerms(r.Context())
	if err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := make([]map[string]any, 0, len(terms))
	for _, t := range terms {
		out = append(out, map[string]any{
			"word": t.Word, "category": t.Category,
			"replacement": t.Replacement, "enabled": t.Enabled,
		})
	}
	web.OK(w, out)
}

// ---------- 策略与开关 ----------

func (m *Module) listPolicies(w http.ResponseWriter, r *http.Request) {
	policies, err := m.store.ListPolicies(r.Context())
	if err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.OK(w, policies)
}

// setPolicy 设置租户策略；{tenant} 为 "default" 时表示平台默认策略。
func (m *Module) setPolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action string `json:"action"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !validAction(req.Action) {
		web.Err(w, http.StatusBadRequest, "action must be reject, replace or mark")
		return
	}
	tenant := r.PathValue("tenant")
	if tenant == "default" {
		tenant = ""
	}
	p := Policy{TenantID: tenant, Action: req.Action, UpdatedAt: time.Now().UTC()}
	if err := m.store.SetPolicy(r.Context(), p); err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	m.reload(r.Context())
	m.audit(r, "policy.updated", tenant, "action", req.Action)
	web.OK(w, p)
}

func (m *Module) getSettings(w http.ResponseWriter, r *http.Request) {
	st, err := m.store.GetSettings(r.Context())
	if err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.OK(w, st)
}

func (m *Module) putSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if !decode(w, r, &req) {
		return
	}
	st := Settings{ID: SettingsID, Enabled: req.Enabled, Updated: time.Now().UTC()}
	if err := m.store.SaveSettings(r.Context(), st); err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	m.reload(r.Context())
	m.audit(r, "settings.updated", "", "enabled", req.Enabled)
	web.OK(w, st)
}

// check 审核试算：给定文本与租户，返回判定与处理后文本。
// 跟随全局开关：关闭时直通（与 gateway 调用链行为一致）。
func (m *Module) check(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text   string `json:"text"`
		Tenant string `json:"tenant"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !m.engine.Enabled() {
		web.OK(w, map[string]any{"action": "pass", "filtered": req.Text})
		return
	}
	filtered, viol := m.engine.CheckText(req.Tenant, req.Text)
	resp := map[string]any{"filtered": filtered}
	if viol != nil {
		resp["action"] = viol.Action
		resp["words"] = viol.Words
	} else {
		resp["action"] = "pass"
	}
	web.OK(w, resp)
}

func (m *Module) audit(r *http.Request, event, resource string, kv ...any) {
	if m.recorder == nil {
		return
	}
	detail, _ := json.Marshal(map[string]any{"kv": kv})
	_ = m.recorder.Record(r.Context(), audit.Event{
		Module: audit.ModAISafety, Event: event, ActorID: m.actorID(r),
		Resource: resource, Detail: detail, Time: time.Now().UTC(),
	})
}

func (m *Module) actorID(r *http.Request) string {
	if m.facade == nil {
		return ""
	}
	if u, err := m.facade.User(r); err == nil {
		return u.ID
	}
	return ""
}

func readAll(r *http.Request) (string, error) {
	b, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	return string(b), err
}
