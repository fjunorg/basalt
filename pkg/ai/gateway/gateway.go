// Package gateway 是 L2 AI 模型网关（M2 交付）。
//
// 能力（docs/03 P0 空白项）：
//   - OpenAI 兼容协议统一层：一套客户端通吃 Qwen/GLM/DeepSeek/vLLM/Ollama
//     等一切 OpenAI 兼容端点（私有化一等公民）
//   - 租户级 Token 配额与用量核算（pkg/ai/quota），平台级用户归入
//     "platform" 计费主体
//   - AI 调用全链路审计（经 audit.Recorder 进入防篡改链）
//
// model 参数支持 "provider-name/model" 形式显式选择提供方，
// 裸 model 走默认提供方。
package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ---------- OpenAI 兼容协议 ----------

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatCompletionsReq struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
}

type chatCompletionsResp struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
		TotalTokens      int64 `json:"total_tokens"`
	} `json:"usage"`
}

// ---------- 提供方客户端 ----------

var (
	ErrNoProvider  = errors.New("gateway: no provider registered")
	ErrBadUpstream = errors.New("gateway: upstream provider error")
)

// client 是 OpenAI 兼容端点的极薄客户端——刻意不引入任何 SDK，
// 协议面收敛在 chat/completions 一个端点上，最大化兼容面。
type client struct {
	name    string
	baseURL string // 形如 https://api.example.com（不含 /v1）
	apiKey  string
	http    *http.Client
}

func newClient(name, baseURL, apiKey string) *client {
	return &client{name: name, baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey,
		http: &http.Client{Timeout: 60 * time.Second}}
}

// chatKey 请求密钥头部：OpenAI 兼容端点统一 Bearer。
func (c *client) chat(ctx context.Context, model string, msgs []Message) (chatCompletionsResp, error) {
	resp, err := c.doChat(ctx, model, msgs, false)
	if err != nil {
		return chatCompletionsResp{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return chatCompletionsResp{}, fmt.Errorf("%w: %s: status %d: %s", ErrBadUpstream, c.name, resp.StatusCode, string(b))
	}
	var out chatCompletionsResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return chatCompletionsResp{}, fmt.Errorf("%w: %s: decode: %v", ErrBadUpstream, c.name, err)
	}
	return out, nil
}

// chatStream 发起流式对话（SSE）。响应体由调用方关闭；网关注入
// stream_options.include_usage 使上游在末尾返回用量块用于记账。
func (c *client) chatStream(ctx context.Context, model string, msgs []Message) (io.ReadCloser, error) {
	resp, err := c.doChat(ctx, model, msgs, true)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		resp.Body.Close()
		return nil, fmt.Errorf("%w: %s: status %d: %s", ErrBadUpstream, c.name, resp.StatusCode, string(b))
	}
	return resp.Body, nil
}

func (c *client) doChat(ctx context.Context, model string, msgs []Message, stream bool) (*http.Response, error) {
	payload := map[string]any{"model": model, "messages": msgs}
	if stream {
		payload["stream"] = true
		payload["stream_options"] = map[string]bool{"include_usage": true}
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrBadUpstream, c.name, err)
	}
	return resp, nil
}

// Registry 管理已注册提供方。name 唯一。
type Registry struct {
	order    []string
	byName   map[string]*client
	fallback string // 裸 model 使用的默认提供方
}

func NewRegistry() *Registry { return &Registry{byName: map[string]*client{}} }

// Register 注册提供方；第一个注册的自动成为默认。
func (r *Registry) Register(name, baseURL, apiKey string) {
	c := newClient(name, baseURL, apiKey)
	r.byName[name] = c
	r.order = append(r.order, name)
	if r.fallback == "" {
		r.fallback = name
	}
}

// resolve 按 "provider/model" 或裸 model 解析 (client, model)。
func (r *Registry) resolve(model string) (*client, string, error) {
	if name, rest, ok := strings.Cut(model, "/"); ok {
		if c, found := r.byName[name]; found {
			return c, rest, nil
		}
	}
	if c, found := r.byName[r.fallback]; found {
		return c, model, nil
	}
	return nil, "", ErrNoProvider
}

// Names 返回已注册提供方列表（首个为默认）。
func (r *Registry) Names() []string { return r.order }
