package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
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
	insp     Inspector // 可选：内容安全审核（ai-safety 注入；nil = 零开销直通）
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

// SetInspector 注入内容安全审核器（ai-safety 模块）。nil 或关闭时
// 调用链零开销直通。
func (m *Module) SetInspector(i Inspector) *Module {
	m.insp = i
	return m
}

// safetyEnabled 审核是否生效（nil 安全）。
func (m *Module) safetyEnabled() bool { return m.insp != nil && m.insp.Enabled() }

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

	// 输入侧内容安全审核：命中即按租户策略拦截/替换/标记。
	if m.safetyEnabled() {
		ok, filtered, viol := m.insp.CheckInput(r.Context(), subject, req.Messages)
		if viol != nil {
			m.recordSafety(r.Context(), subject, u.ID, req.Model, "input", viol)
		}
		if !ok {
			web.WriteJSON(w, http.StatusUnprocessableEntity, http.StatusUnprocessableEntity,
				"content rejected by safety policy (safety.input_rejected)", nil)
			return
		}
		req.Messages = filtered
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

	// 输出侧内容安全审核（非流式：完整文本一次检查）。
	if m.safetyEnabled() {
		for i := range resp.Choices {
			filtered, viol := m.insp.CheckOutput(r.Context(), subject, resp.Choices[i].Message.Content)
			if viol != nil {
				m.recordSafety(r.Context(), subject, u.ID, req.Model, "output", viol)
			}
			if viol.Rejected() {
				web.WriteJSON(w, http.StatusUnprocessableEntity, http.StatusUnprocessableEntity,
					"content rejected by safety policy (safety.output_rejected)", nil)
				return
			}
			resp.Choices[i].Message.Content = filtered
		}
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
// 审核开启时，delta content 经 StreamChecker 分块检查与改写
//（残余缓冲在 [DONE] 前冲洗），输出侧命中 reject 即断流。
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
	flush := func() {
		if canFlush {
			flusher.Flush()
		}
	}

	var checker StreamChecker
	if m.safetyEnabled() {
		checker = m.insp.NewStreamChecker(subject)
	}

	var total int64
	sc := bufio.NewScanner(rc)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if checker != nil {
			out, violation, done := m.rewriteStreamLine(w, string(line), checker)
			if violation != nil {
				m.recordSafety(r.Context(), subject, actor, req.Model, "output.stream", violation)
			}
			if done { // 输出侧 reject：error 事件已下发，断流
				return
			}
			line = []byte(out)
		}
		if _, err := w.Write(append(line, '\n')); err != nil {
			break // 客户端断开：停止转发（上游连接随 defer 关闭）
		}
		flush()
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

// rewriteStreamLine 对一行上游 SSE 应用输出侧审核：
//   - delta content 分块送入 checker，安全前缀（已替换）回填后重新
//     序列化该行；usage 行与 [DONE] 原样保留
//   - [DONE] 之前先下发 checker.Finish() 的残余缓冲（跨 chunk 词
//     边界的最后一段）
//   - violation.Rejected() 时下发 OpenAI 风格 error 事件并指示断流
//
// 返回 重写后的行、命中信息、是否应断流。
func (m *Module) rewriteStreamLine(w http.ResponseWriter, line string, checker StreamChecker) (string, *SafetyViolation, bool) {
	data, ok := strings.CutPrefix(line, "data: ")
	if !ok {
		return line, nil, false // 注释行/空行：透传
	}
	if data == "[DONE]" {
		// 残余缓冲以补充 delta 事件形式冲洗，再放行 [DONE]
		rest, viol := checker.Finish()
		if rest != "" {
			_, _ = io.WriteString(w, sseDelta(rest)+"\n")
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
		return line, viol, false
	}
	var obj map[string]any
	if json.Unmarshal([]byte(data), &obj) != nil {
		return line, nil, false // 无法解析：透传（usage 等非内容块）
	}
	content := deltaContent(obj)
	if content == "" {
		return line, nil, false
	}
	filtered, viol := checker.Chunk(content)
	if viol.Rejected() {
		_, _ = io.WriteString(w, `data: {"error":{"code":"safety.output_rejected","message":"content rejected by safety policy"}}`+"\n\n")
		if f, canFlush := w.(http.Flusher); canFlush {
			f.Flush()
		}
		return "", viol, true
	}
	if setDeltaContent(obj, filtered) {
		b, err := json.Marshal(obj)
		if err != nil {
			return line, nil, false
		}
		return "data: " + string(b), viol, false
	}
	return line, viol, false
}

// sseDelta 构造一个补充 delta 事件（Finish 残余下发用）。
func sseDelta(content string) string {
	b, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"delta": map[string]string{"content": content}}},
	})
	return "data: " + string(b)
}

// deltaContent 从解析后的 SSE 块提取首个 delta.content。
func deltaContent(obj map[string]any) string {
	cs, _ := obj["choices"].([]any)
	if len(cs) == 0 {
		return ""
	}
	c0, _ := cs[0].(map[string]any)
	if c0 == nil {
		return ""
	}
	d, _ := c0["delta"].(map[string]any)
	if d == nil {
		return ""
	}
	s, _ := d["content"].(string)
	return s
}

// setDeltaContent 回填首个 delta.content；无 delta 结构时返回 false。
func setDeltaContent(obj map[string]any, content string) bool {
	cs, _ := obj["choices"].([]any)
	if len(cs) == 0 {
		return false
	}
	c0, _ := cs[0].(map[string]any)
	if c0 == nil {
		return false
	}
	d, _ := c0["delta"].(map[string]any)
	if d == nil {
		return false
	}
	d["content"] = content
	return true
}

// recordSafety 把内容安全命中写入防篡改审计链。
func (m *Module) recordSafety(ctx context.Context, subject, actor, model, side string, viol *SafetyViolation) {
	if m.recorder == nil || viol == nil {
		return
	}
	detail, _ := json.Marshal(map[string]any{
		"subject": subject, "model": model, "side": side,
		"action": viol.Action, "words": viol.Words,
	})
	_ = m.recorder.Record(ctx, audit.Event{
		Module: audit.ModAIGw, Event: "safety." + side + "." + viol.Action, ActorID: actor,
		Resource: model, Detail: detail, Time: time.Now().UTC(),
	})
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
