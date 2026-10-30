package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/basalt-go/basalt/pkg/ai/gateway"
	"github.com/basalt-go/basalt/pkg/kernel/web"
)

// SSE 事件类型（契约 §4，与 agent-api-contract.md 一一对应）。
const (
	EvRunStarted     = "run.started"
	EvToolCall       = "tool.call"
	EvToolResult     = "tool.result"
	EvLLMDelta       = "llm.delta"
	EvRunCompleted   = "run.completed"
	EvRunFailed      = "run.failed"
	EvSafetyRejected = "safety.rejected"
)

// llm.delta 单段下发上限（rune 计，避免超长单事件）。
const deltaChunkRunes = 256

// tool.result 下发截断（契约：result ≤2000 字符，全文走运行详情）。
const toolResultEmitLimit = 2000

// summaryLimit 工具结果摘要长度。
const summaryLimit = 48

// runReq 运行端点入参。
type runReq struct {
	Input  string `json:"input"`
	Stream bool   `json:"stream"`
}

// startRun 发起运行：同步（stream=false）或 SSE（stream=true）。
// 工具循环见 engine.execute；错误语义见契约 §6。
func (m *Module) startRun(w http.ResponseWriter, r *http.Request) {
	def, err := m.store.GetDef(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			web.Err(w, http.StatusNotFound, "agent not found")
			return
		}
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	if def.Status != DefStatusActive {
		web.Err(w, http.StatusConflict, ErrDisabled.Error())
		return
	}
	u, err := m.facade.User(r)
	if err != nil {
		web.Err(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var req runReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		web.Err(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if strings.TrimSpace(req.Input) == "" {
		web.Err(w, http.StatusBadRequest, ErrInputEmpty.Error())
		return
	}

	// 引擎依赖预检（握手期以 JSON 错误返回，契约 §6）。
	if m.gw == nil {
		web.Err(w, http.StatusServiceUnavailable, ErrNoGateway.Error())
		return
	}
	if err := m.precheckTools(&def); err != nil {
		web.Err(w, http.StatusServiceUnavailable, err.Error())
		return
	}

	now := time.Now().UTC()
	run := Run{
		ID: newID(), AgentID: def.ID, UserID: u.ID,
		Status: RunStatusRunning, Input: req.Input, StartedAt: now,
	}
	if err := m.store.CreateRun(r.Context(), run); err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	_ = m.store.AppendMsg(r.Context(), RunMessage{
		RunID: run.ID, Role: "user", Content: req.Input, CreatedAt: now,
	})

	subject := u.TenantID
	if subject == "" {
		subject = gateway.PlatformSubject
	}

	if req.Stream {
		m.runSSE(w, r, def, run, subject, u.ID)
		return
	}

	// 同步模式：完整执行后一次性返回。
	run, _ = m.execute(r.Context(), def, run, subject, u.ID, func(Event) {})
	msgs, _ := m.store.ListMsgs(r.Context(), run.ID)
	if run.Status == RunStatusSafetyRejected {
		web.WriteJSON(w, http.StatusUnprocessableEntity, http.StatusUnprocessableEntity,
			"content rejected by safety policy (safety."+run.Error+")", nil)
		return
	}
	if run.Status == RunStatusFailed {
		switch {
		case strings.Contains(run.Error, "quota"):
			web.WriteJSON(w, http.StatusTooManyRequests, http.StatusTooManyRequests, run.Error, nil)
		case strings.Contains(run.Error, "no AI provider"):
			web.WriteJSON(w, http.StatusServiceUnavailable, http.StatusServiceUnavailable, run.Error, nil)
		default:
			web.Err(w, http.StatusBadGateway, run.Error)
		}
		return
	}
	web.Created(w, map[string]any{"run": run, "messages": msgs})
}

// precheckTools 工具依赖就绪预检（如 rag embedding 未配置 → 503）。
func (m *Module) precheckTools(def *Definition) error {
	if m.tools == nil {
		if len(def.Tools) == 0 {
			return nil
		}
		return ErrNoTools
	}
	for _, name := range def.Tools {
		t, ok := m.tools.Get(name)
		if !ok {
			return fmt.Errorf("agent: unknown tool %q", name)
		}
		if err := t.Ready(); err != nil {
			return err
		}
	}
	return nil
}

// ---------- SSE ----------

// Event 是一次运行事件（引擎与传输解耦：同步模式丢弃，SSE 模式下发）。
type Event struct {
	Type string
	Data any
}

// runSSE 以 SSE 执行并下发运行事件。握手期错误已在 startRun 以 JSON
// 返回；进入流后错误一律走 run.failed / safety.rejected 事件。
func (m *Module) runSSE(w http.ResponseWriter, r *http.Request, def Definition, run Run, subject, actor string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher, canFlush := w.(http.Flusher)

	emit := func(ev Event) {
		data, err := json.Marshal(ev.Data)
		if err != nil {
			return
		}
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, string(data))
		if canFlush {
			flusher.Flush()
		}
	}

	emit(Event{EvRunStarted, map[string]string{
		"run_id": run.ID, "agent_id": def.ID, "model": def.Model,
	}})
	_, _ = m.execute(r.Context(), def, run, subject, actor, emit)
}

// ---------- 引擎（工具调用循环） ----------

// execute 是编排核心：system+user → LLM（带工具声明）→ 若发起工具
// 调用则逐个执行并回填 → 再 LLM → …直到产出纯文本回答或达轮数上限。
// 终态（completed/failed/safety_rejected）落库；emit 下发过程事件。
// ai-safety：审核在 gateway.ChatComplete 内部（输入侧拦截/输出侧
// 文本与工具参数送检），ErrSafetyRejected 映射 safety_rejected 终态。
func (m *Module) execute(ctx context.Context, def Definition, run Run, subject, actor string, emit func(Event)) (Run, error) {
	// fail 落 failed 终态并统一推送 run.failed 事件（契约 §4.1：
	// 流内失败终态必须下发——同步模式 emit 为 no-op，SSE 模式
	// 在流关闭前让客户端拿到失败原因，而非静默断流）。
	fail := func(status, msg string) (Run, error) {
		if status == RunStatusFailed {
			emit(Event{EvRunFailed, map[string]any{
				"run_id": run.ID, "status": RunStatusFailed, "error": msg,
			}})
		}
		now := time.Now().UTC()
		run.Status, run.Error, run.FinishedAt = status, msg, &now
		_ = m.store.UpdateRun(ctx, run)
		return run, errors.New(msg)
	}

	// 组装初始消息：system（定义的提示词）+ user 输入。
	sys := def.SystemPrompt
	if sys == "" {
		sys = "You are a helpful assistant. Use tools when needed."
	}
	msgs := []gateway.ChatMessage{
		{Role: "system", Content: sys},
		{Role: "user", Content: run.Input},
	}

	// 工具声明（定义绑定的全部工具）。
	var toolDefs []gateway.ToolDef
	if len(def.Tools) > 0 {
		td, err := m.tools.Defs(def.Tools)
		if err != nil {
			return fail(RunStatusFailed, err.Error())
		}
		toolDefs = td
	}
	opts := gateway.ChatOptions{Temperature: def.Temperature, Tools: toolDefs}

	for round := 1; round <= def.MaxToolRounds; round++ {
		res, err := m.gw.ChatComplete(ctx, subject, actor, def.Model, msgs, opts)
		if err != nil {
			switch {
			case errors.Is(err, gateway.ErrSafetyRejected):
				side := "output"
				if round == 1 {
					side = "input"
				}
				run.TotalTokens = 0
				emit(Event{EvSafetyRejected, map[string]string{
					"run_id": run.ID, "status": RunStatusSafetyRejected,
					"side": side, "message": "content rejected by safety policy",
				}})
				now := time.Now().UTC()
				run.Status, run.Error, run.FinishedAt = RunStatusSafetyRejected, "safety."+side, &now
				_ = m.store.UpdateRun(ctx, run)
				return run, err
			case errors.Is(err, gateway.ErrQuotaExceeded):
				return fail(RunStatusFailed, "token quota exceeded for tenant")
			case errors.Is(err, gateway.ErrNoProvider):
				return fail(RunStatusFailed, "no AI provider registered")
			default:
				return fail(RunStatusFailed, "upstream provider error: "+err.Error())
			}
		}
		run.Rounds = round
		run.TotalTokens += res.Usage.TotalTokens

		// 持久化 assistant 消息（含工具调用）。
		var tcRefs []ToolCallRef
		for _, tc := range res.ToolCalls {
			tcRefs = append(tcRefs, ToolCallRef{CallID: tc.ID, Name: tc.Name, Arguments: tc.Arguments})
		}
		_ = m.store.AppendMsg(ctx, RunMessage{
			RunID: run.ID, Role: "assistant", Content: res.Content,
			ToolCalls: tcRefs, CreatedAt: time.Now().UTC(),
		})
		// 回填 assistant 消息进循环上下文（tool_calls 原样回传）。
		msgs = append(msgs, gateway.ChatMessage{Role: "assistant", Content: res.Content, ToolCalls: res.ToolCalls})

		if len(res.ToolCalls) == 0 {
			// 终答：分块下发 + completed 终态。
			for _, seg := range chunkText(res.Content, deltaChunkRunes) {
				emit(Event{EvLLMDelta, map[string]string{"text": seg}})
			}
			now := time.Now().UTC()
			run.Status, run.Output, run.FinishedAt = RunStatusCompleted, res.Content, &now
			run.Error = ""
			_ = m.store.UpdateRun(ctx, run)
			emit(Event{EvRunCompleted, map[string]any{
				"run_id": run.ID, "status": RunStatusCompleted,
				"rounds": run.Rounds, "total_tokens": run.TotalTokens, "output": run.Output,
			}})
			m.record(ctx, actor, "run.completed", run.ID, fmt.Sprintf("rounds=%d tokens=%d", run.Rounds, run.TotalTokens))
			return run, nil
		}

		// 工具调用轮：逐个执行并回填。
		for _, tc := range res.ToolCalls {
			emit(Event{EvToolCall, map[string]string{
				"call_id": tc.ID, "name": tc.Name, "arguments": tc.Arguments,
			}})
			result, execErr := m.execTool(ctx, tc)
			ok := execErr == nil
			text := result
			if !ok {
				text = "tool error: " + execErr.Error()
			}
			_ = m.store.AppendMsg(ctx, RunMessage{
				RunID: run.ID, Role: "tool", Content: text,
				ToolCallID: tc.ID, ToolName: tc.Name, CreatedAt: time.Now().UTC(),
			})
			msgs = append(msgs, gateway.ChatMessage{
				Role: "tool", Content: text, ToolCallID: tc.ID, Name: tc.Name,
			})
			emit(Event{EvToolResult, map[string]any{
				"call_id": tc.ID, "name": tc.Name, "ok": ok,
				"summary": summarize(result), "result": truncateRunes(text, toolResultEmitLimit),
			}})
		}
	}
	// 达轮数上限仍未产出终答。
	return fail(RunStatusFailed, fmt.Sprintf("max tool rounds (%d) exceeded without final answer", def.MaxToolRounds))
}

// execTool 执行一次工具调用；未注册工具返回错误文本（回喂模型）。
func (m *Module) execTool(ctx context.Context, tc gateway.ToolCall) (string, error) {
	t, ok := m.tools.Get(tc.Name)
	if !ok {
		return "", fmt.Errorf("unknown tool %q", tc.Name)
	}
	return t.Execute(ctx, tc.Arguments)
}

// chunkText 按段落切分，超长段再按 rune 上限细切（SSE delta 分段）。
func chunkText(text string, limit int) []string {
	if text == "" {
		return nil
	}
	var out []string
	for _, para := range strings.Split(text, "\n\n") {
		if para == "" {
			continue
		}
		r := []rune(para)
		if len(r) <= limit {
			out = append(out, para)
			continue
		}
		for i := 0; i < len(r); i += limit {
			j := i + limit
			if j > len(r) {
				j = len(r)
			}
			out = append(out, string(r[i:j]))
		}
	}
	if len(out) == 0 {
		out = []string{text}
	}
	return out
}

// truncateRunes 按 rune 截断（不切坏 UTF-8）。
func truncateRunes(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit])
}

// summarize 生成工具结果摘要（契约 tool.result.summary）。
func summarize(result string) string {
	r := []rune(strings.ReplaceAll(strings.TrimSpace(result), "\n", " "))
	if len(r) <= summaryLimit {
		return string(r)
	}
	return string(r[:summaryLimit]) + "…"
}
