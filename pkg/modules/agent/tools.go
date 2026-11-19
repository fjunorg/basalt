package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/basalt-go/basalt/pkg/ai/gateway"
	"github.com/basalt-go/basalt/pkg/modules/rag"
)

// ---------- 工具注册表（接口缝：内置工具与后续扩展共用） ----------

// Tool 是 agent 可绑定的工具抽象。内置实现 RagSearchTool；HTTP 调用、
// DB 查询等后续工具经 Registry.Register 接入，引擎与 API 层零改动
// （与 Eino 生态对接时同样在此缝上适配）。
type Tool interface {
	// Def 工具声明（透传给模型的 function 定义）。
	Def() gateway.ToolDef
	// Execute 执行一次调用；argsJSON 是模型产出的原始参数 JSON 字符串，
	// 解析与校验责任在工具侧。返回结果全文（引擎负责截断下发）。
	Execute(ctx context.Context, argsJSON string) (string, error)
	// Ready 依赖就绪检查（如 rag 的 embedding 客户端）。运行前统一
	// 预检，未就绪返回错误（HTTP 503 明确报错而非静默失败）。
	Ready() error
}

// ToolRegistry 管理已注册工具（名唯一）。
type ToolRegistry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

func NewToolRegistry() *ToolRegistry { return &ToolRegistry{tools: map[string]Tool{}} }

// Register 注册工具（重名后写覆盖——装配期一次性完成，无并发语义）。
func (r *ToolRegistry) Register(t Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[t.Def().Name] = t
}

// Get 按名取工具。
func (r *ToolRegistry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// Names 已注册工具名（有序，稳定输出）。
func (r *ToolRegistry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.tools))
	for n := range r.tools {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// List 全部工具声明。
func (r *ToolRegistry) List() []gateway.ToolDef {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]gateway.ToolDef, 0, len(r.tools))
	for _, n := range r.Names() {
		out = append(out, r.tools[n].Def())
	}
	return out
}

// Defs 取指定名字集的工具声明；任一未注册即报错（定义创建/更新时
// 校验用，防止绑定拼写错误的工具名）。
func (r *ToolRegistry) Defs(names []string) ([]gateway.ToolDef, error) {
	out := make([]gateway.ToolDef, 0, len(names))
	for _, n := range names {
		t, ok := r.Get(n)
		if !ok {
			return nil, fmt.Errorf("agent: unknown tool %q (registered: %v)", n, r.Names())
		}
		out = append(out, t.Def())
	}
	return out, nil
}

// ErrNoTools 工具注册表未装配（装配期遗漏）。
var ErrNoTools = errors.New("agent: tool registry not configured")

// ---------- 内置工具：rag.search ----------

const RagSearchToolName = "rag.search"

// RagSearchTool 在指定知识库做语义检索，返回 top-k 命中片段。
type RagSearchTool struct {
	RAG *rag.Module
}

func (t *RagSearchTool) Def() gateway.ToolDef {
	return gateway.ToolDef{
		Name:        RagSearchToolName,
		Description: "在指定知识库（KB）中做语义检索，返回最相关的文本片段。回答知识库相关问题前应先检索。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"kb_id": map[string]any{
					"type":        "string",
					"description": "知识库 ID（用户已知或从上下文获取）",
				},
				"query": map[string]any{
					"type":        "string",
					"description": "检索查询文本",
				},
				"top_k": map[string]any{
					"type":        "integer",
					"description": "返回片段数，默认 5，最大 100",
				},
			},
			"required": []string{"kb_id", "query"},
		},
	}
}

func (t *RagSearchTool) Ready() error {
	if t.RAG == nil {
		return errors.New("agent: rag module not wired for rag.search")
	}
	if t.RAG.Embedder() == nil {
		return rag.ErrEmbedNotConfigured
	}
	return nil
}

type ragSearchArgs struct {
	KBID  string `json:"kb_id"`
	Query string `json:"query"`
	TopK  int    `json:"top_k"`
}

// maxToolResult 工具结果下发/回填的截断上限（保护消息体与上下文窗口）。
const maxToolResult = 4000

// Execute 执行检索并把命中格式化为带序号的片段清单。
func (t *RagSearchTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var args ragSearchArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("invalid arguments JSON: %w", err)
	}
	if args.KBID == "" || args.Query == "" {
		return "", errors.New("kb_id and query are required")
	}
	if args.TopK <= 0 {
		args.TopK = 5
	}
	if args.TopK > 100 {
		args.TopK = 100
	}
	hits, err := t.RAG.SearchText(ctx, args.KBID, args.Query, args.TopK)
	if err != nil {
		return "", err
	}
	if len(hits) == 0 {
		return "no relevant chunks found", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d hits:\n", len(hits))
	for i, h := range hits {
		fmt.Fprintf(&b, "[%d] (doc: %s, score %.3f) %s\n", i+1, h.DocName, h.Score, h.Content)
	}
	out := b.String()
	if len(out) > maxToolResult {
		out = out[:maxToolResult] + "\n... (truncated)"
	}
	return out, nil
}
