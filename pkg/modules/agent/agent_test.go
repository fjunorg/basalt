package agent_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/basalt-go/basalt/pkg/ai/gateway"
	"github.com/basalt-go/basalt/pkg/kernel/app"
	"github.com/basalt-go/basalt/pkg/modules/agent"
	"github.com/basalt-go/basalt/pkg/modules/audit"
	"github.com/basalt-go/basalt/pkg/modules/identity"
	"github.com/basalt-go/basalt/pkg/modules/rag"
	"github.com/basalt-go/basalt/pkg/stores/gormstore"
)

// ---------- 测试基建 ----------

// newAppMods 通用装配：identity + audit + 业务模块。
func newAppMods(t *testing.T, mods ...app.Module) http.Handler {
	t.Helper()
	a := app.New(nil)
	for _, mod := range append([]app.Module{identity.New(), audit.New()}, mods...) {
		if err := a.Registry().Register(mod); err != nil {
			t.Fatalf("register %s: %v", mod.Name(), err)
		}
	}
	resolved, err := a.Registry().Resolve()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	for _, mod := range resolved {
		if err := mod.Init(context.Background(), a); err != nil {
			t.Fatalf("init %s: %v", mod.Name(), err)
		}
		_ = mod.Start(context.Background())
	}
	return a.Router().Handler()
}

func newApp(t *testing.T, m *agent.Module) http.Handler {
	t.Helper()
	return newAppMods(t, m)
}

func adminToken(t *testing.T, h http.Handler) string {
	t.Helper()
	rec, data := do(t, h, http.MethodPost, "/api/v1/identity/login", "", map[string]string{
		"username": "admin", "password": "Basalt@2026",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("admin login: %d %s", rec.Code, rec.Body.String())
	}
	var d struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(data, &d)
	return d.Token
}

func do(t *testing.T, h http.Handler, method, path, token string, body any) (*httptest.ResponseRecorder, json.RawMessage) {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	return rec, env.Data
}

// ---------- LLM stub（脚本化：按调用次数返回预设结果） ----------

type stubGateway struct {
	calls    int
	script   []gateway.ChatResult // 每次调用弹出一项（越界重复最后一项）
	errAt    int                  // 第 N 次（1 起）返回该错误；0 = 不注入
	err      error
	lastMsgs []gateway.ChatMessage // 捕获最近一次入参（验证工具回填协议）
	lastOpts gateway.ChatOptions   // 捕获最近一次工具声明
	subject  string                // 捕获计费主体
}

func (g *stubGateway) ChatComplete(_ context.Context, subject, _, _ string, msgs []gateway.ChatMessage, opts gateway.ChatOptions) (gateway.ChatResult, error) {
	g.calls++
	g.lastMsgs, g.lastOpts, g.subject = msgs, opts, subject
	if g.errAt == g.calls {
		return gateway.ChatResult{}, g.err
	}
	idx := g.calls - 1
	if idx >= len(g.script) {
		idx = len(g.script) - 1
	}
	return g.script[idx], nil
}

// echoTool 测试工具（无 rag 依赖的工具循环用）。
type echoTool struct{ fail bool }

func (*echoTool) Def() gateway.ToolDef {
	return gateway.ToolDef{
		Name: "echo.tool", Description: "echo back arguments",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{
			"x": map[string]any{"type": "string"},
		}},
	}
}
func (t *echoTool) Execute(_ context.Context, argsJSON string) (string, error) {
	if t.fail {
		return "", errors.New("echo exploded")
	}
	return "echo:" + argsJSON, nil
}
func (*echoTool) Ready() error { return nil }

// stubEmbedder 与 rag 模块测试同款（确定性 rune 桶向量）。
type stubEmbedder struct{ dim int }

func (s stubEmbedder) ModelName() string { return "stub-embed" }
func (s stubEmbedder) Embed(_ context.Context, texts []string) ([][]float64, error) {
	out := make([][]float64, len(texts))
	for i, t := range texts {
		v := make([]float64, s.dim)
		for _, r := range t {
			v[int(r)%s.dim]++
		}
		out[i] = v
	}
	return out, nil
}

// mkModule 组装带 stub gateway 与 echo 工具的模块。
func mkModule(gw agent.LLMGateway) *agent.Module {
	m := agent.New()
	m.SetGateway(gw)
	tools := agent.NewToolRegistry()
	tools.Register(&echoTool{})
	m.SetTools(tools)
	return m
}

// ---------- 定义 CRUD ----------

func TestDefCRUD(t *testing.T) {
	m := mkModule(&stubGateway{})
	h := newApp(t, m)
	tok := adminToken(t, h)

	// 校验分支
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/agent/agents", tok, map[string]any{
		"model": "m",
	}); rec.Code != http.StatusBadRequest {
		t.Fatalf("no name: %d", rec.Code)
	}
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/agent/agents", tok, map[string]any{
		"name": "n", "model": "m", "temperature": 3,
	}); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad temperature: %d", rec.Code)
	}
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/agent/agents", tok, map[string]any{
		"name": "n", "model": "m", "max_tool_rounds": 11,
	}); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad rounds: %d", rec.Code)
	}
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/agent/agents", tok, map[string]any{
		"name": "n", "model": "m", "tools": []string{"ghost.tool"},
	}); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown tool: %d", rec.Code)
	}

	// 创建（默认值填充）
	rec, data := do(t, h, http.MethodPost, "/api/v1/agent/agents", tok, map[string]any{
		"name": "检索助手", "model": "qwen-7b", "tools": []string{"echo.tool"},
		"system_prompt": "你是检索助手",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var def agent.Definition
	_ = json.Unmarshal(data, &def)
	if def.ID == "" || def.Temperature != 0.7 || def.MaxToolRounds != 5 ||
		def.Status != "active" || len(def.Tools) != 1 {
		t.Fatalf("def defaults = %+v", def)
	}

	// 列表 / 详情 / 更新 / 删除 / 404
	rec, data = do(t, h, http.MethodGet, "/api/v1/agent/agents", tok, nil)
	var lst struct {
		Count int `json:"count"`
	}
	_ = json.Unmarshal(data, &lst)
	if lst.Count != 1 {
		t.Fatalf("count = %d", lst.Count)
	}
	rec, data = do(t, h, http.MethodGet, "/api/v1/agent/agents/"+def.ID, tok, nil)
	var detail struct {
		Agent    agent.Definition `json:"agent"`
		RunCount int              `json:"run_count"`
	}
	_ = json.Unmarshal(data, &detail)
	if detail.Agent.Name != "检索助手" || detail.RunCount != 0 {
		t.Fatalf("detail = %+v", detail)
	}
	rec, data = do(t, h, http.MethodPatch, "/api/v1/agent/agents/"+def.ID, tok, map[string]any{
		"status": "disabled", "temperature": 0.2,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("patch: %d", rec.Code)
	}
	_ = json.Unmarshal(data, &def)
	if def.Status != "disabled" || def.Temperature != 0.2 || def.Name != "检索助手" {
		t.Fatalf("patched = %+v", def)
	}
	// disabled → 运行 409
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/agent/agents/"+def.ID+"/runs", tok, map[string]any{
		"input": "hi",
	}); rec.Code != http.StatusConflict {
		t.Fatalf("run disabled: %d, want 409", rec.Code)
	}
	// 404 分支
	if rec, _ := do(t, h, http.MethodGet, "/api/v1/agent/agents/ghost", tok, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("get ghost: %d", rec.Code)
	}
	if rec, _ := do(t, h, http.MethodDelete, "/api/v1/agent/agents/ghost", tok, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("delete ghost: %d", rec.Code)
	}
	if rec, _ := do(t, h, http.MethodDelete, "/api/v1/agent/agents/"+def.ID, tok, nil); rec.Code != http.StatusOK {
		t.Fatalf("delete: %d", rec.Code)
	}
	if rec, _ := do(t, h, http.MethodGet, "/api/v1/agent/agents/"+def.ID, tok, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("get after delete: %d", rec.Code)
	}
}

// ---------- 运行：纯对话（无工具） ----------

func TestRunPureChat(t *testing.T) {
	gw := &stubGateway{script: []gateway.ChatResult{{
		Content: "你好，我是助手。",
	}}}
	gw.script[0].Usage.TotalTokens = 100
	m := mkModule(gw)
	h := newApp(t, m)
	tok := adminToken(t, h)

	_, data := do(t, h, http.MethodPost, "/api/v1/agent/agents", tok, map[string]any{
		"name": "纯对话", "model": "m1",
	})
	var def agent.Definition
	_ = json.Unmarshal(data, &def)

	rec, data := do(t, h, http.MethodPost, "/api/v1/agent/agents/"+def.ID+"/runs", tok, map[string]any{
		"input": "打个招呼",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("run: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Run      agent.Run          `json:"run"`
		Messages []agent.RunMessage `json:"messages"`
	}
	_ = json.Unmarshal(data, &out)
	if out.Run.Status != "completed" || out.Run.Output != "你好，我是助手。" ||
		out.Run.TotalTokens != 100 || out.Run.Rounds != 1 {
		t.Fatalf("run = %+v", out.Run)
	}
	if len(out.Messages) != 2 || out.Messages[0].Role != "user" || out.Messages[1].Role != "assistant" {
		t.Fatalf("messages = %+v", out.Messages)
	}
	// 首轮消息：system + user；无工具声明
	if len(gw.lastMsgs) != 2 || gw.lastMsgs[0].Role != "system" || gw.lastMsgs[1].Content != "打个招呼" {
		t.Fatalf("msgs = %+v", gw.lastMsgs)
	}
	if len(gw.lastOpts.Tools) != 0 {
		t.Fatalf("tools = %+v", gw.lastOpts.Tools)
	}
	// 平台用户计费主体
	if gw.subject != "platform" {
		t.Fatalf("subject = %q", gw.subject)
	}

	// 运行历史与详情
	rec, data = do(t, h, http.MethodGet, "/api/v1/agent/agents/"+def.ID+"/runs", tok, nil)
	var runs struct {
		Count int         `json:"count"`
		Items []agent.Run `json:"items"`
	}
	_ = json.Unmarshal(data, &runs)
	if runs.Count != 1 || runs.Items[0].ID != out.Run.ID {
		t.Fatalf("runs = %+v", runs)
	}
	rec, data = do(t, h, http.MethodGet, "/api/v1/agent/runs/"+out.Run.ID, tok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get run: %d", rec.Code)
	}
	if rec, _ := do(t, h, http.MethodGet, "/api/v1/agent/runs/ghost", tok, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("get ghost run: %d", rec.Code)
	}
	// 空 input → 400
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/agent/agents/"+def.ID+"/runs", tok, map[string]any{"input": " "}); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty input: %d", rec.Code)
	}
}

// ---------- 运行：工具调用循环（核心语义） ----------

func toolCallRound(callID, name, args string) gateway.ChatResult {
	r := gateway.ChatResult{}
	r.ToolCalls = []gateway.ToolCall{{ID: callID, Name: name, Arguments: args}}
	return r
}

func TestRunToolLoop(t *testing.T) {
	gw := &stubGateway{script: []gateway.ChatResult{
		toolCallRound("c1", "echo.tool", `{"x":"hello"}`),
		{Content: "工具说：echo:{\"x\":\"hello\"}"},
	}}
	m := mkModule(gw)
	h := newApp(t, m)
	tok := adminToken(t, h)
	_, data := do(t, h, http.MethodPost, "/api/v1/agent/agents", tok, map[string]any{
		"name": "工具助手", "model": "m1", "tools": []string{"echo.tool"},
	})
	var def agent.Definition
	_ = json.Unmarshal(data, &def)

	rec, data := do(t, h, http.MethodPost, "/api/v1/agent/agents/"+def.ID+"/runs", tok, map[string]any{
		"input": "用一下工具",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("run: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Run      agent.Run          `json:"run"`
		Messages []agent.RunMessage `json:"messages"`
	}
	_ = json.Unmarshal(data, &out)
	if out.Run.Status != "completed" || out.Run.Rounds != 2 {
		t.Fatalf("run = %+v", out.Run)
	}
	// 消息序列：user → assistant(tool_calls) → tool → assistant(终答)
	if len(out.Messages) != 4 {
		t.Fatalf("messages = %+v", out.Messages)
	}
	a1 := out.Messages[1]
	if a1.Role != "assistant" || len(a1.ToolCalls) != 1 || a1.ToolCalls[0].CallID != "c1" ||
		a1.ToolCalls[0].Name != "echo.tool" {
		t.Fatalf("assistant tool msg = %+v", a1)
	}
	tm := out.Messages[2]
	if tm.Role != "tool" || tm.ToolCallID != "c1" || tm.ToolName != "echo.tool" ||
		!strings.Contains(tm.Content, "echo:") {
		t.Fatalf("tool msg = %+v", tm)
	}
	// 第二轮收到的消息：system, user, assistant(tool_calls), tool
	if len(gw.lastMsgs) != 4 {
		t.Fatalf("round2 msgs = %d", len(gw.lastMsgs))
	}
	r2a := gw.lastMsgs[2]
	if r2a.Role != "assistant" || len(r2a.ToolCalls) != 1 || r2a.ToolCalls[0].ID != "c1" {
		t.Fatalf("round2 assistant = %+v", r2a)
	}
	r2t := gw.lastMsgs[3]
	if r2t.Role != "tool" || r2t.ToolCallID != "c1" || r2t.Name != "echo.tool" {
		t.Fatalf("round2 tool = %+v", r2t)
	}
	// 工具声明已随请求传递
	if len(gw.lastOpts.Tools) != 1 || gw.lastOpts.Tools[0].Name != "echo.tool" {
		t.Fatalf("round tools = %+v", gw.lastOpts.Tools)
	}
}

// 工具执行失败：错误文本回喂模型，不终止运行。
func TestRunToolErrorFeedsModel(t *testing.T) {
	gw := &stubGateway{script: []gateway.ChatResult{
		toolCallRound("c1", "echo.tool", `{"x":"boom"}`),
		{Content: "工具坏了但我收到了错误"},
	}}
	m := agent.New()
	m.SetGateway(gw)
	tools := agent.NewToolRegistry()
	tools.Register(&echoTool{fail: true}) // 本次注入失败版工具
	m.SetTools(tools)
	h := newApp(t, m)
	tok := adminToken(t, h)
	_, data := do(t, h, http.MethodPost, "/api/v1/agent/agents", tok, map[string]any{
		"name": "坏工具", "model": "m1", "tools": []string{"echo.tool"},
	})
	var def agent.Definition
	_ = json.Unmarshal(data, &def)
	rec, data := do(t, h, http.MethodPost, "/api/v1/agent/agents/"+def.ID+"/runs", tok, map[string]any{
		"input": "x",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("run: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Run agent.Run `json:"run"`
	}
	_ = json.Unmarshal(data, &out)
	if out.Run.Status != "completed" {
		t.Fatalf("run = %+v（工具失败不应终止）", out.Run)
	}
	// 回喂文本含 tool error
	r2t := gw.lastMsgs[3]
	if !strings.Contains(r2t.Content, "tool error:") {
		t.Fatalf("feed = %q", r2t.Content)
	}
}

// 循环上限：脚本永远返回工具调用 → max rounds failed。
func TestRunMaxRoundsExceeded(t *testing.T) {
	gw := &stubGateway{script: []gateway.ChatResult{
		toolCallRound("c1", "echo.tool", `{"x":"a"}`),
	}}
	m := mkModule(gw)
	h := newApp(t, m)
	tok := adminToken(t, h)
	_, data := do(t, h, http.MethodPost, "/api/v1/agent/agents", tok, map[string]any{
		"name": "死循环", "model": "m1", "tools": []string{"echo.tool"}, "max_tool_rounds": 2,
	})
	var def agent.Definition
	_ = json.Unmarshal(data, &def)
	rec, _ := do(t, h, http.MethodPost, "/api/v1/agent/agents/"+def.ID+"/runs", tok, map[string]any{
		"input": "x",
	})
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("run: %d, want 502", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "max tool rounds") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

// ---------- 异常路径 ----------

func TestRunErrorPaths(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code int
	}{
		{"safety-rejected", gateway.ErrSafetyRejected, http.StatusUnprocessableEntity},
		{"quota", gateway.ErrQuotaExceeded, http.StatusTooManyRequests},
		{"no-provider", gateway.ErrNoProvider, http.StatusServiceUnavailable},
		{"upstream", gateway.ErrBadUpstream, http.StatusBadGateway},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gw := &stubGateway{script: []gateway.ChatResult{{Content: "x"}}}
			gw.errAt, gw.err = 1, tc.err
			m := mkModule(gw)
			h := newApp(t, m)
			tok := adminToken(t, h)
			_, data := do(t, h, http.MethodPost, "/api/v1/agent/agents", tok, map[string]any{
				"name": "异常", "model": "m1",
			})
			var def agent.Definition
			_ = json.Unmarshal(data, &def)
			rec, _ := do(t, h, http.MethodPost, "/api/v1/agent/agents/"+def.ID+"/runs", tok, map[string]any{
				"input": "x",
			})
			if rec.Code != tc.code {
				t.Fatalf("%s: %d, want %d (%s)", tc.name, rec.Code, tc.code, rec.Body.String())
			}
		})
	}
	// gateway 未装配 → 503
	m := agent.New() // 无 gw
	h := newApp(t, m)
	tok := adminToken(t, h)
	_, data := do(t, h, http.MethodPost, "/api/v1/agent/agents", tok, map[string]any{
		"name": "无网关", "model": "m1",
	})
	var def agent.Definition
	_ = json.Unmarshal(data, &def)
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/agent/agents/"+def.ID+"/runs", tok, map[string]any{
		"input": "x",
	}); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no gateway: %d, want 503", rec.Code)
	}
}

// ---------- SSE ----------

// parseSSE 解析 SSE 文本为 (event, data) 序列。
func parseSSE(body string) [][2]string {
	var out [][2]string
	for _, block := range strings.Split(body, "\n\n") {
		var ev, data string
		for _, line := range strings.Split(block, "\n") {
			if v, ok := strings.CutPrefix(line, "event: "); ok {
				ev = v
			}
			if v, ok := strings.CutPrefix(line, "data: "); ok {
				data = v
			}
		}
		if ev != "" {
			out = append(out, [2]string{ev, data})
		}
	}
	return out
}

func TestRunSSE(t *testing.T) {
	gw := &stubGateway{script: []gateway.ChatResult{
		toolCallRound("c1", "echo.tool", `{"x":"s"}`),
		{Content: "第一段\n\n第二段"},
	}}
	m := mkModule(gw)
	h := newApp(t, m)
	tok := adminToken(t, h)
	_, data := do(t, h, http.MethodPost, "/api/v1/agent/agents", tok, map[string]any{
		"name": "流式", "model": "m1", "tools": []string{"echo.tool"},
	})
	var def agent.Definition
	_ = json.Unmarshal(data, &def)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/agents/"+def.ID+"/runs",
		bytes.NewReader([]byte(`{"input":"流式跑一次","stream":true}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("sse: %d %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("content-type = %q", ct)
	}
	events := parseSSE(rec.Body.String())
	var seq []string
	for _, e := range events {
		seq = append(seq, e[0])
	}
	want := []string{"run.started", "tool.call", "tool.result", "llm.delta", "llm.delta", "run.completed"}
	if len(seq) != len(want) {
		t.Fatalf("event seq = %v", seq)
	}
	for i := range want {
		if seq[i] != want[i] {
			t.Fatalf("event seq = %v, want %v", seq, want)
		}
	}
	// 关键字段抽查
	var tc struct {
		CallID string `json:"call_id"`
		Name   string `json:"name"`
	}
	if err := json.Unmarshal([]byte(events[1][1]), &tc); err != nil {
		t.Fatalf("tool.call json: %v", err)
	}
	if tc.CallID != "c1" || tc.Name != "echo.tool" {
		t.Fatalf("tool.call data = %s", events[1][1])
	}
	var done struct {
		Status      string `json:"status"`
		Output      string `json:"output"`
		TotalTokens int64  `json:"total_tokens"`
	}
	_ = json.Unmarshal([]byte(events[len(events)-1][1]), &done)
	if done.Status != "completed" || done.Output != "第一段\n\n第二段" {
		t.Fatalf("completed data = %+v", done)
	}
}

// SSE + 安全拦截：safety.rejected 事件（第 1 轮 = 输入侧）。
func TestRunSSESafetyRejected(t *testing.T) {
	gw := &stubGateway{script: []gateway.ChatResult{{Content: "x"}}}
	gw.errAt, gw.err = 1, gateway.ErrSafetyRejected
	m := mkModule(gw)
	h := newApp(t, m)
	tok := adminToken(t, h)
	_, data := do(t, h, http.MethodPost, "/api/v1/agent/agents", tok, map[string]any{
		"name": "被拦截", "model": "m1",
	})
	var def agent.Definition
	_ = json.Unmarshal(data, &def)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/agents/"+def.ID+"/runs",
		bytes.NewReader([]byte(`{"input":"敏感内容","stream":true}`)))
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	events := parseSSE(rec.Body.String())
	if len(events) != 2 || events[0][0] != "run.started" || events[1][0] != "safety.rejected" {
		t.Fatalf("events = %v", events)
	}
	var sr struct {
		Status string `json:"status"`
		Side   string `json:"side"`
	}
	_ = json.Unmarshal([]byte(events[1][1]), &sr)
	if sr.Status != "safety_rejected" || sr.Side != "input" {
		t.Fatalf("safety.rejected = %+v", sr)
	}
}

// SSE 失败终态：引擎错误路径必须推送 run.failed 后关流（契约 §4.1，
// 前端联调发现的回归：曾只发 run.started 即静默关闭）。
func TestRunSSEFailed(t *testing.T) {
	cases := []struct {
		name    string
		gw      *stubGateway
		rounds  int
		wantErr string
	}{
		{"no-provider", &stubGateway{
			script: []gateway.ChatResult{{Content: "x"}},
			errAt:  1, err: gateway.ErrNoProvider,
		}, 5, "no AI provider registered"},
		{"upstream-error", &stubGateway{
			script: []gateway.ChatResult{{Content: "x"}},
			errAt:  1, err: gateway.ErrBadUpstream,
		}, 5, "upstream provider error"},
		{"max-rounds", &stubGateway{
			script: []gateway.ChatResult{toolCallRound("c1", "echo.tool", `{"x":"a"}`)},
		}, 2, "max tool rounds"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gw := tc.gw
			m := mkModule(gw)
			h := newApp(t, m)
			tok := adminToken(t, h)
			_, data := do(t, h, http.MethodPost, "/api/v1/agent/agents", tok, map[string]any{
				"name": "失败流", "model": "m1", "tools": []string{"echo.tool"},
				"max_tool_rounds": tc.rounds,
			})
			var def agent.Definition
			_ = json.Unmarshal(data, &def)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/agents/"+def.ID+"/runs",
				bytes.NewReader([]byte(`{"input":"x","stream":true}`)))
			req.Header.Set("Authorization", "Bearer "+tok)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("sse: %d", rec.Code)
			}
			events := parseSSE(rec.Body.String())
			// 失败终态必须是流的最后一个事件（首轮即错的场景恰为
			// started+failed 两个；工具循环达上限的场景中间含工具事件）。
			if len(events) < 2 || events[0][0] != "run.started" ||
				events[len(events)-1][0] != "run.failed" {
				t.Fatalf("%s: events = %v（失败终态必须以 run.failed 下发）", tc.name, events)
			}
			var rf struct {
				RunID  string `json:"run_id"`
				Status string `json:"status"`
				Error  string `json:"error"`
			}
			_ = json.Unmarshal([]byte(events[len(events)-1][1]), &rf)
			if rf.Status != "failed" || !strings.Contains(rf.Error, tc.wantErr) {
				t.Fatalf("%s: run.failed = %+v", tc.name, rf)
			}
			// 流内终态与持久化记录一致
			_, runs := do(t, h, http.MethodGet, "/api/v1/agent/agents/"+def.ID+"/runs", tok, nil)
			var lst struct {
				Items []agent.Run `json:"items"`
			}
			_ = json.Unmarshal(runs, &lst)
			if len(lst.Items) != 1 || lst.Items[0].Status != "failed" ||
				!strings.Contains(lst.Items[0].Error, tc.wantErr) {
				t.Fatalf("%s: persisted = %+v", tc.name, lst.Items)
			}
		})
	}
}

// ---------- rag 集成（确定性 stub embedding） ----------

func TestRagToolIntegration(t *testing.T) {
	// 真 rag 模块 + stub embedding：入库→切片→向量化
	ragM := rag.New(nil, stubEmbedder{dim: 64})
	h0 := newAppMods(t, ragM)
	tok0 := adminToken(t, h0)
	_, data := do(t, h0, http.MethodPost, "/api/v1/rag/kbs", tok0, map[string]any{
		"name": "集成库", "strategy": map[string]any{"mode": "separator", "separator": "\n\n"},
	})
	var kb rag.KnowledgeBase
	_ = json.Unmarshal(data, &kb)
	rec, data := do(t, h0, http.MethodPost, "/api/v1/rag/kbs/"+kb.ID+"/documents", tok0, map[string]any{
		"name": "水果", "text": "苹果 香蕉 水果\n\n量子 物理 科学",
	})
	var doc rag.Document
	_ = json.Unmarshal(data, &doc)
	_, _ = do(t, h0, http.MethodPost, "/api/v1/rag/documents/"+doc.ID+"/chunk", tok0, nil)
	if rec, _ = do(t, h0, http.MethodPost, "/api/v1/rag/documents/"+doc.ID+"/embed", tok0, nil); rec.Code != http.StatusOK {
		t.Fatalf("rag embed: %d", rec.Code)
	}

	// agent：第 1 轮调用 rag.search，第 2 轮基于检索结果作答
	args, _ := json.Marshal(map[string]any{"kb_id": kb.ID, "query": "苹果", "top_k": 3})
	gw := &stubGateway{script: []gateway.ChatResult{
		toolCallRound("c1", "rag.search", string(args)),
		{Content: "根据资料：苹果是水果"},
	}}
	m := agent.New()
	m.SetGateway(gw)
	tools := agent.NewToolRegistry()
	tools.Register(&agent.RagSearchTool{RAG: ragM})
	m.SetTools(tools)
	h := newApp(t, m)
	tok := adminToken(t, h)
	_, data = do(t, h, http.MethodPost, "/api/v1/agent/agents", tok, map[string]any{
		"name": "检索 agent", "model": "m1", "tools": []string{"rag.search"},
	})
	var def agent.Definition
	_ = json.Unmarshal(data, &def)
	rec, data = do(t, h, http.MethodPost, "/api/v1/agent/agents/"+def.ID+"/runs", tok, map[string]any{
		"input": "苹果是什么",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("run: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Run agent.Run `json:"run"`
	}
	_ = json.Unmarshal(data, &out)
	if out.Run.Status != "completed" {
		t.Fatalf("run = %+v", out.Run)
	}
	// 回喂模型的工具结果含真实检索命中（含"苹果"的片段，非量子段）
	r2t := gw.lastMsgs[3]
	if !strings.Contains(r2t.Content, "苹果") || strings.Contains(r2t.Content, "量子 物理 科学") {
		t.Fatalf("rag feed = %q", r2t.Content)
	}
}

// rag embedding 未配置 → 工具 Ready 预检 503（契约 §6）。
func TestRagToolNotReady(t *testing.T) {
	ragM := rag.New(nil, nil) // 无 embedding 客户端
	gw := &stubGateway{script: []gateway.ChatResult{{Content: "x"}}}
	m := agent.New()
	m.SetGateway(gw)
	tools := agent.NewToolRegistry()
	tools.Register(&agent.RagSearchTool{RAG: ragM})
	m.SetTools(tools)
	h := newApp(t, m)
	tok := adminToken(t, h)
	rec, _ := do(t, h, http.MethodPost, "/api/v1/agent/agents", tok, map[string]any{
		"name": "未就绪", "model": "m1", "tools": []string{"rag.search"},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create with unready tool should pass (ready 只在运行期预检): %d", rec.Code)
	}
	var defs struct {
		Items []agent.Definition `json:"items"`
	}
	_, data := do(t, h, http.MethodGet, "/api/v1/agent/agents", tok, nil)
	_ = json.Unmarshal(data, &defs)
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/agent/agents/"+defs.Items[0].ID+"/runs", tok, map[string]any{
		"input": "x",
	}); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("run unready: %d, want 503", rec.Code)
	}
}

// ---------- 权限拦截 ----------

func TestPermissionGuard(t *testing.T) {
	m := mkModule(&stubGateway{})
	h := newApp(t, m)
	tok := adminToken(t, h)
	rec, _ := do(t, h, http.MethodPost, "/api/v1/identity/users", tok, map[string]string{
		"username": "plain2", "password": "Plain@2026", "nickname": "普通用户",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create user: %d", rec.Code)
	}
	rec, _ = do(t, h, http.MethodPost, "/api/v1/identity/login", "", map[string]string{
		"username": "plain2", "password": "Plain@2026",
	})
	var d struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &d)
	plain := "Bearer " + d.Token
	_ = plain
	var env struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)

	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/agent/agents"},
		{http.MethodGet, "/api/v1/agent/agents"},
		{http.MethodPost, "/api/v1/agent/agents/x/runs"},
		{http.MethodGet, "/api/v1/agent/tools"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, bytes.NewReader([]byte(`{}`)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+env.Data.Token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s %s: %d, want 403", tc.method, tc.path, rec.Code)
		}
	}
	// 工具列表端点（admin 正常路径）
	if rec, _ := do(t, h, http.MethodGet, "/api/v1/agent/tools", tok, nil); rec.Code != http.StatusOK {
		t.Fatalf("tools: %d", rec.Code)
	}
}

// ---------- sqlite GORM：重启恢复 ----------

func TestAgentPersistenceRestart(t *testing.T) {
	dsn := t.TempDir() + "/agent.db"
	gw := &stubGateway{script: []gateway.ChatResult{
		toolCallRound("c1", "echo.tool", `{"x":"restart"}`),
		{Content: "重启后依然正确"},
	}}

	mkApp := func(store agent.Store) http.Handler {
		m := agent.NewWithStore(store)
		m.SetGateway(gw)
		tools := agent.NewToolRegistry()
		tools.Register(&echoTool{})
		m.SetTools(tools)
		return newApp(t, m)
	}

	s1, err := gormstore.Open(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	h1 := mkApp(s1.AgentStore())
	tok := adminToken(t, h1)
	_, data := do(t, h1, http.MethodPost, "/api/v1/agent/agents", tok, map[string]any{
		"name": "重启助手", "model": "m1", "tools": []string{"echo.tool"},
	})
	var def agent.Definition
	_ = json.Unmarshal(data, &def)
	rec, data := do(t, h1, http.MethodPost, "/api/v1/agent/agents/"+def.ID+"/runs", tok, map[string]any{
		"input": "重启测试",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("run: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Run agent.Run `json:"run"`
	}
	_ = json.Unmarshal(data, &out)
	if sqlDB, err := s1.DB.DB(); err == nil {
		_ = sqlDB.Close()
	}

	// 重启：同一 dsn 重开，定义/运行/消息全量恢复
	s2, err := gormstore.Open(dsn)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	h2 := mkApp(s2.AgentStore())
	tok2 := adminToken(t, h2)

	rec, data = do(t, h2, http.MethodGet, "/api/v1/agent/agents/"+def.ID, tok2, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("def after restart: %d", rec.Code)
	}
	rec, data = do(t, h2, http.MethodGet, "/api/v1/agent/runs/"+out.Run.ID, tok2, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("run after restart: %d", rec.Code)
	}
	var detail struct {
		Run      agent.Run          `json:"run"`
		Messages []agent.RunMessage `json:"messages"`
	}
	_ = json.Unmarshal(data, &detail)
	if detail.Run.Status != "completed" || detail.Run.Output != "重启后依然正确" {
		t.Fatalf("run after restart = %+v", detail.Run)
	}
	if len(detail.Messages) != 4 || detail.Messages[2].Role != "tool" || detail.Messages[2].ToolName != "echo.tool" {
		t.Fatalf("messages after restart = %+v", detail.Messages)
	}
	// 重启后可继续发起新运行（工具循环在新会话可用）
	gw.script = []gateway.ChatResult{{Content: "新会话回答"}}
	rec, _ = do(t, h2, http.MethodPost, "/api/v1/agent/agents/"+def.ID+"/runs", tok2, map[string]any{
		"input": "再来一次",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("run after restart: %d %s", rec.Code, rec.Body.String())
	}
}
