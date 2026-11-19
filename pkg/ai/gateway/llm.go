package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// 本文件是 gateway 的 Go 层调用入口（供 agent 等模块内编排复用，
// 不经 HTTP 自环）：HTTP 端点（module.go）保持 OpenAI 兼容面不变，
// 本入口在同一统一层（配额 → 输入审核 → 提供方 → 输出审核 →
// 记账 → 审计）上执行，并扩展工具声明/工具调用解析。
//
// 收敛在新文件而不动 gateway.go/module.go：AI 系模块并行迭代期
// 原文件零改动，合并冲突面最小。

// ErrSafetyRejected 内容安全策略拒绝（输入侧拦截或输出侧拒绝下发）。
// 调用方（agent 引擎）应终止运行并落 safety_rejected 状态。
var ErrSafetyRejected = errors.New("gateway: content rejected by safety policy")

// ---------- 工具协议 ----------

// ToolDef 是工具声明（OpenAI function calling 的 function 对象）。
type ToolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters,omitempty"` // JSON Schema
}

// openAITool 是 OpenAI 兼容请求里的工具条目形态。
type openAITool struct {
	Type     string  `json:"type"` // 固定 function
	Function ToolDef `json:"function"`
}

// ToolCall 是模型发起的一次工具调用（arguments 保留原始 JSON 字符串，
// 由工具实现自行解析——schema 校验责任在工具侧）。
type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ---------- 调用入口 ----------

// ChatMessage 是 Go 层入口的完整消息形态：比 HTTP 面的 Message 多
// tool_calls（assistant 工具调用轮回传）与 tool_call_id/name（工具
// 结果轮关联）——工具循环协议字段，HTTP 直连面不暴露。
type ChatMessage struct {
	Role       string     `json:"role"` // user | assistant | tool | system
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"` // 工具名提示
}

// ChatOptions 是 ChatComplete 的可选参数。
type ChatOptions struct {
	Temperature float64   // 0 = 不传（走端点默认）
	Tools       []ToolDef // 工具声明；空 = 纯对话
}

// ChatResult 是一次非流式调用的结果。
type ChatResult struct {
	Content   string     `json:"content"` // assistant 文本（无工具调用时即最终回答）
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	Usage     struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
		TotalTokens      int64 `json:"total_tokens"`
	} `json:"usage"`
}

// ChatComplete 经统一层执行一次（可选带工具声明的）非流式对话。
// subject 为计费主体（租户 ID 或 PlatformSubject），actor 为触发用户。
// 错误语义：ErrSafetyRejected（策略拒绝）/ ErrNoProvider /
// ErrBadUpstream（上游错误，调用方可重试或落 failed）。
func (m *Module) ChatComplete(ctx context.Context, subject, actor, model string, msgs []ChatMessage, opts ChatOptions) (ChatResult, error) {
	if !m.quota.Allow(ctx, subject, 1) {
		m.record(ctx, subject, actor, model, 0, "quota.denied")
		return ChatResult{}, ErrQuotaExceeded
	}

	// 输入侧审核（与 HTTP 链路同策略；审核面含工具参数——同为
	// 模型产出内容，不可信）。
	if m.safetyEnabled() {
		plain := make([]Message, len(msgs))
		for i, cm := range msgs {
			plain[i] = Message{Role: cm.Role, Content: cm.Content}
			for _, tc := range cm.ToolCalls {
				plain[i].Content += " " + tc.Name + " " + tc.Arguments
			}
		}
		ok, filtered, viol := m.insp.CheckInput(ctx, subject, plain)
		if viol != nil {
			m.recordSafety(ctx, subject, actor, model, "input", viol)
		}
		if !ok {
			return ChatResult{}, ErrSafetyRejected
		}
		if viol != nil && viol.Action == SafetyReplace {
			// 替换动作回写文本面（结构字段不动）。
			for i := range msgs {
				if i < len(filtered) {
					msgs[i].Content = filtered[i].Content
				}
			}
		}
	}

	c, model, err := m.reg.resolve(model)
	if err != nil {
		return ChatResult{}, err
	}
	resp, err := c.chatTools(ctx, model, msgs, opts)
	if err != nil {
		m.record(ctx, subject, actor, model, 0, "upstream.error")
		return ChatResult{}, err
	}

	// 输出侧审核：文本与工具调用参数一并送检（工具参数同样不可信）。
	if m.safetyEnabled() {
		text := resp.Content
		for _, tc := range resp.ToolCalls {
			text += " " + tc.Name + " " + tc.Arguments
		}
		filtered, viol := m.insp.CheckOutput(ctx, subject, text)
		if viol != nil {
			m.recordSafety(ctx, subject, actor, model, "output", viol)
		}
		if viol.Rejected() {
			return ChatResult{}, ErrSafetyRejected
		}
		resp.Content = filtered
	}

	m.quota.Consume(ctx, subject, resp.Usage.TotalTokens)
	m.record(ctx, subject, actor, model, resp.Usage.TotalTokens, "chat.ok")
	return resp, nil
}

// ErrQuotaExceeded 计费主体 token 配额耗尽（HTTP 链路 429 的 Go 侧形态）。
var ErrQuotaExceeded = errors.New("gateway: token quota exceeded")

// ---------- client 扩展（带工具的调用） ----------

// toolsChatResp 是带工具解析的响应结构（独立于 gateway.go 的
// chatCompletionsResp，互不牵连）。
type toolsChatResp struct {
	Choices []struct {
		Message struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
		TotalTokens      int64 `json:"total_tokens"`
	} `json:"usage"`
}

// chatTools 发起带工具声明与温度的一次非流式调用。
func (c *client) chatTools(ctx context.Context, model string, msgs []ChatMessage, opts ChatOptions) (ChatResult, error) {
	payload := map[string]any{"model": model, "messages": msgs}
	if opts.Temperature > 0 {
		payload["temperature"] = opts.Temperature
	}
	if len(opts.Tools) > 0 {
		tools := make([]openAITool, len(opts.Tools))
		for i, t := range opts.Tools {
			tools[i] = openAITool{Type: "function", Function: t}
		}
		payload["tools"] = tools
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return ChatResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return ChatResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return ChatResult{}, fmt.Errorf("%w: %s: %v", ErrBadUpstream, c.name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return ChatResult{}, fmt.Errorf("%w: %s: status %d: %s", ErrBadUpstream, c.name, resp.StatusCode, string(b))
	}
	var out toolsChatResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return ChatResult{}, fmt.Errorf("%w: %s: decode: %v", ErrBadUpstream, c.name, err)
	}
	var res ChatResult
	if len(out.Choices) > 0 {
		res.Content = out.Choices[0].Message.Content
		for _, tc := range out.Choices[0].Message.ToolCalls {
			res.ToolCalls = append(res.ToolCalls, ToolCall{
				ID: tc.ID, Name: tc.Function.Name, Arguments: tc.Function.Arguments,
			})
		}
	}
	res.Usage.PromptTokens = out.Usage.PromptTokens
	res.Usage.CompletionTokens = out.Usage.CompletionTokens
	res.Usage.TotalTokens = out.Usage.TotalTokens
	return res, nil
}
