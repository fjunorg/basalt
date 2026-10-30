package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/basalt-go/basalt/pkg/ai/quota"
	"github.com/basalt-go/basalt/pkg/kernel/app"
	"github.com/basalt-go/basalt/pkg/kernel/web"
	"github.com/basalt-go/basalt/pkg/modules/audit"
	"github.com/basalt-go/basalt/pkg/modules/identity"
)

const Version = "0.2.0"

// 平台级用户（无租户）的计费主体标识。
const PlatformSubject = "platform"

// 权限点。
const (
	PermQuotaManage = "ai:quota:manage"
)

// DefaultQuotaLimit 是未显式配置租户的默认 Token 限额。
// 私有化 G 端默认限量（而非放行），需要放开时显式设为 -1。
const DefaultQuotaLimit = 1_000_000

type Module struct {
	reg      *Registry
	quota    *quota.Manager
	facade   *identity.Facade
	recorder audit.Recorder
}

func New() *Module {
	return &Module{reg: NewRegistry(), quota: quota.NewManager(DefaultQuotaLimit)}
}

// NewWithQuotaStore 注入持久化配额存储（gormstore.QuotaStore），
// 配额账本跨重启延续、按月度账期计量。
func NewWithQuotaStore(store quota.Store) *Module {
	return &Module{reg: NewRegistry(), quota: quota.NewManagerWithStore(store, DefaultQuotaLimit)}
}

// RegisterProvider 注册 OpenAI 兼容提供方（首个为默认）。
// 真实部署经环境变量装配（见 cmd/basalt/serve.go），测试注入 httptest 端点。
func (m *Module) RegisterProvider(name, baseURL, apiKey string) *Module {
	m.reg.Register(name, baseURL, apiKey)
	return m
}

func (*Module) Name() string    { return "ai-gateway" }
func (*Module) Version() string { return Version }

func (*Module) Dependencies() []string {
	// tenant：配额以租户为主体；audit：调用审计入防篡改链。
	return []string{"identity", "tenant", "audit"}
}

func (m *Module) Init(_ context.Context, a *app.App) error {
	idm, ok := a.Registry().MustGet("identity").(*identity.Module)
	if !ok {
		return errModuleUnavailable("identity")
	}
	adm, ok := a.Registry().MustGet("audit").(*audit.Module)
	if !ok {
		return errModuleUnavailable("audit")
	}
	m.facade = idm.Facade()
	m.recorder = adm.Recorder()

	auth := m.facade.Auth
	req := m.facade.Require
	g := a.Router().Group("/api/v1/ai")

	g.Handle(http.MethodPost, "/chat/completions", auth(m.chat))
	g.Handle(http.MethodGet, "/providers", auth(m.listProviders))
	g.Handle(http.MethodPut, "/quota/{tenant}", auth(req(PermQuotaManage, m.setQuota)))
	g.Handle(http.MethodGet, "/quota/{tenant}", auth(m.getQuota))
	return nil
}

func (*Module) Start(context.Context) error { return nil }
func (*Module) Stop(context.Context) error  { return nil }

// chatReq 是对话端点入参（OpenAI 兼容子集 + stream 开关）。
type chatReq struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Stream   bool      `json:"stream"`
}

// chat 是 OpenAI 兼容对话端点：认证 → 租户配额预检 → 转发提供方 →
// 真实用量记账 → 审计入链。
func (m *Module) chat(w http.ResponseWriter, r *http.Request) {
	u, err := m.facade.User(r)
	if err != nil {
		web.Err(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	subject := u.TenantID
	if subject == "" {
		subject = PlatformSubject
	}

	var req chatReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Model == "" || len(req.Messages) == 0 {
		web.Err(w, http.StatusBadRequest, "model and messages are required")
		return
	}

	// 预检：累计用量已到顶即拒绝。请求前无法得知真实消耗，
	// 请求完成后按模型返回的 total_tokens 记账，超额在下次调用拒绝。
	if !m.quota.Allow(subject, 1) {
		m.record(r.Context(), subject, u.ID, req.Model, 0, "quota.denied")
		web.WriteJSON(w, http.StatusTooManyRequests, http.StatusTooManyRequests,
			"token quota exceeded for tenant", nil)
		return
	}

	c, model, err := m.reg.resolve(req.Model)
	if err != nil {
		web.Err(w, http.StatusServiceUnavailable, "no AI provider registered")
		return
	}
	if req.Stream {
		m.chatStream(w, r, c, model, req, subject, u.ID)
		return
	}
	resp, err := c.chat(r.Context(), model, req.Messages)
	if err != nil {
		m.record(r.Context(), subject, u.ID, req.Model, 0, "upstream.error")
		web.Err(w, http.StatusBadGateway, "upstream provider error")
		return
	}

	m.quota.Consume(subject, resp.Usage.TotalTokens)
	m.record(r.Context(), subject, u.ID, req.Model, resp.Usage.TotalTokens, "chat.ok")

	// 保留 OpenAI 兼容响应结构，附加租户用量视图。
	web.OK(w, map[string]any{
		"object":  "chat.completion",
		"choices": resp.Choices,
		"usage": map[string]int64{
			"prompt_tokens":     resp.Usage.PromptTokens,
			"completion_tokens": resp.Usage.CompletionTokens,
			"total_tokens":      resp.Usage.TotalTokens,
		},
	})
}

// chatStream 流式转发：逐行透传上游 SSE 并即时下发（Flush），
// 解析末尾 usage 块完成配额记账与审计。请求已注入
// stream_options.include_usage（见 client.doChat）。
func (m *Module) chatStream(w http.ResponseWriter, r *http.Request, c *client, model string, req chatReq, subject, actor string) {
	rc, err := c.chatStream(r.Context(), model, req.Messages)
	if err != nil {
		m.record(r.Context(), subject, actor, req.Model, 0, "upstream.error")
		web.Err(w, http.StatusBadGateway, "upstream provider error")
		return
	}
	defer rc.Close()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher, canFlush := w.(http.Flusher)

	var total int64
	sc := bufio.NewScanner(rc)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if _, err := w.Write(append(line, '\n')); err != nil {
			break // 客户端断开：停止转发（上游连接随 defer 关闭）
		}
		if canFlush {
			flusher.Flush()
		}
		if data, ok := strings.CutPrefix(string(line), "data: "); ok && data != "[DONE]" {
			var chunk struct {
				Usage *struct {
					TotalTokens int64 `json:"total_tokens"`
				} `json:"usage"`
			}
			if json.Unmarshal([]byte(data), &chunk) == nil && chunk.Usage != nil {
				total = chunk.Usage.TotalTokens
			}
		}
	}
	m.quota.Consume(subject, total)
	m.record(r.Context(), subject, actor, req.Model, total, "chat.stream.ok")
}

func (m *Module) listProviders(w http.ResponseWriter, _ *http.Request) {
	web.OK(w, map[string]any{"providers": m.reg.Names(), "default": m.reg.fallback})
}

type setQuotaReq struct {
	Limit int64 `json:"limit"` // <0 = 不限量
}

func (m *Module) setQuota(w http.ResponseWriter, r *http.Request) {
	var req setQuotaReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		web.Err(w, http.StatusBadRequest, "invalid json body")
		return
	}
	tenant := r.PathValue("tenant")
	m.quota.SetLimit(tenant, req.Limit)
	m.record(r.Context(), tenant, "", "", 0, "quota.updated")
	web.OK(w, map[string]any{"tenant": tenant, "limit": req.Limit})
}

func (m *Module) getQuota(w http.ResponseWriter, r *http.Request) {
	tenant := r.PathValue("tenant")
	used, limit := m.quota.Usage(tenant)
	web.OK(w, map[string]any{
		"tenant": tenant, "used": used, "limit": limit,
		"remaining": remaining(used, limit),
	})
}

func remaining(used, limit int64) any {
	if limit < 0 {
		return "unlimited"
	}
	return limit - used
}

// record 把 AI 调用写入防篡改审计链。
func (m *Module) record(ctx context.Context, subject, actor, model string, tokens int64, event string) {
	if m.recorder == nil {
		return
	}
	detail, _ := json.Marshal(map[string]any{
		"subject": subject, "model": model, "tokens": tokens,
	})
	_ = m.recorder.Record(ctx, audit.Event{
		Module: audit.ModAIGw, Event: event, ActorID: actor,
		Resource: model, Detail: detail, Time: time.Now().UTC(),
	})
}

type errModuleUnavailable string

func (e errModuleUnavailable) Error() string {
	return "gateway: module " + strconv.Quote(string(e)) + " not available"
}

// Describe 实现模块自描述（chat 为登录级能力，不设权限点）。
func (*Module) Describe() app.ModuleInfo {
	return app.ModuleInfo{
		Name:        "ai-gateway",
		Version:     Version,
		Permissions: []string{PermQuotaManage},
	}
}
