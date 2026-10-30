package rag

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// EmbeddingClient 抽象向量化服务：真实提供方（OpenAI 兼容 /embeddings）
// 与测试 stub（确定性假向量）都实现本接口。
type EmbeddingClient interface {
	// Embed 批量向量化；返回顺序与输入一致，且与输入等长。
	Embed(ctx context.Context, texts []string) ([][]float64, error)
	// ModelName 返回向量模型标识（写入 Document.EmbedModel 供审计追溯）。
	ModelName() string
}

// 无 provider 配置时向量化步骤明确报错（而非静默跳过）的哨兵错误。
var ErrEmbedNotConfigured = errors.New("rag: embedding provider not configured (set BASALT_AI_PROVIDER_URL and BASALT_AI_EMBED_MODEL)")

var ErrEmbedBadUpstream = errors.New("rag: embedding upstream error")

// OpenAIEmbeddings 是 OpenAI 兼容 /v1/embeddings 端点的极薄客户端。
//
// 刻意自含于 rag 包、不依赖 gateway 包：gateway 协议面收敛在
// chat/completions（docs/03 设计边界），且 AI 系模块并行迭代，
// rag 侧接线独立演进互不阻塞。提供方配置复用 BASALT_AI_PROVIDER_*
// 环境变量族（与 serve 装配同一来源），向量模型经 BASALT_AI_EMBED_MODEL
// 指定（各兼容端点模型名不一，不设臆造默认值）。
type OpenAIEmbeddings struct {
	baseURL string // 形如 https://api.example.com（不含 /v1）
	apiKey  string
	model   string
	http    *http.Client
}

// NewOpenAIEmbeddings 显式构造（测试/装配均可）。
func NewOpenAIEmbeddings(baseURL, apiKey, model string) *OpenAIEmbeddings {
	return &OpenAIEmbeddings{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		model:   model,
		http:    &http.Client{Timeout: 120 * time.Second},
	}
}

// NewOpenAIEmbeddingsFromEnv 复用 BASALT_AI_PROVIDER_URL/KEY 与
// BASALT_AI_EMBED_MODEL 构造。URL 或模型未配置时返回
// ErrEmbedNotConfigured——调用方应显式处理（装配期允许为 nil，
// 向量化请求期明确报错）。
func NewOpenAIEmbeddingsFromEnv() (*OpenAIEmbeddings, error) {
	url := os.Getenv("BASALT_AI_PROVIDER_URL")
	model := os.Getenv("BASALT_AI_EMBED_MODEL")
	if url == "" || model == "" {
		return nil, ErrEmbedNotConfigured
	}
	return NewOpenAIEmbeddings(url, os.Getenv("BASALT_AI_PROVIDER_KEY"), model), nil
}

func (e *OpenAIEmbeddings) ModelName() string { return e.model }

type embeddingsResp struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float64 `json:"embedding"`
	} `json:"data"`
}

// Embed 分批调用 /v1/embeddings（单批上限 embedBatchSize，超出自动
// 拆分），按 index 还原输入顺序。
func (e *OpenAIEmbeddings) Embed(ctx context.Context, texts []string) ([][]float64, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	if e.model == "" {
		return nil, ErrEmbedNotConfigured
	}
	out := make([][]float64, 0, len(texts))
	for start := 0; start < len(texts); start += embedBatchSize {
		end := start + embedBatchSize
		if end > len(texts) {
			end = len(texts)
		}
		batch, err := e.embedBatch(ctx, texts[start:end])
		if err != nil {
			return nil, err
		}
		out = append(out, batch...)
	}
	return out, nil
}

// embedBatchSize 与主流 OpenAI 兼容端点的单批输入上限对齐。
const embedBatchSize = 64

func (e *OpenAIEmbeddings) embedBatch(ctx context.Context, texts []string) ([][]float64, error) {
	body, err := json.Marshal(map[string]any{"model": e.model, "input": texts})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		e.baseURL+"/v1/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if e.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.apiKey)
	}
	resp, err := e.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrEmbedBadUpstream, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("%w: status %d: %s", ErrEmbedBadUpstream, resp.StatusCode, string(b))
	}
	var out embeddingsResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("%w: decode: %v", ErrEmbedBadUpstream, err)
	}
	if len(out.Data) != len(texts) {
		return nil, fmt.Errorf("%w: got %d embeddings for %d inputs",
			ErrEmbedBadUpstream, len(out.Data), len(texts))
	}
	// 按 index 对齐输入顺序（兼容端点不保证 data 有序）。
	ordered := make([][]float64, len(texts))
	for _, d := range out.Data {
		if d.Index < 0 || d.Index >= len(texts) {
			return nil, fmt.Errorf("%w: embedding index %d out of range", ErrEmbedBadUpstream, d.Index)
		}
		ordered[d.Index] = d.Embedding
	}
	for i, v := range ordered {
		if len(v) == 0 {
			return nil, fmt.Errorf("%w: empty embedding at %d", ErrEmbedBadUpstream, i)
		}
	}
	return ordered, nil
}
