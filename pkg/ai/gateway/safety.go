package gateway

import "context"

// 内容安全审核动作（由 ai-safety 模块的策略决定）。
const (
	SafetyReject = "reject" // 拒绝：输入侧 422 拦截 / 输出侧断流
	SafetyReplace = "replace" // 替换：命中词替换为安全文本后放行
	SafetyMark    = "mark"    // 标记放行：原样透传，命中入审计链
)

// SafetyViolation 表示一次审核命中（nil 表示未命中，直通）。
type SafetyViolation struct {
	Action string   `json:"action"`          // reject | replace | mark
	Words  []string `json:"words,omitempty"` // 命中的敏感词
}

// Rejected 是否应拒绝本次调用（输入拦截或输出断流）。
func (v *SafetyViolation) Rejected() bool { return v != nil && v.Action == SafetyReject }

// Inspector 抽象内容安全审核，由 ai-safety 模块实现并经 SetInspector
// 注入网关。nil 或 Enabled()==false 时调用链零开销直通——审核是
// 可插拔增强，而非网关硬依赖。
type Inspector interface {
	// Enabled 全局审核开关。网关在每次调用前读取，关闭即直通。
	Enabled() bool

	// CheckInput 输入侧审核（全量消息）。返回 ok=false 表示按策略
	// 拒绝；filtered 为替换动作改写后的消息（其余动作原样返回）。
	CheckInput(ctx context.Context, tenantID string, msgs []Message) (ok bool, filtered []Message, violation *SafetyViolation)

	// CheckOutput 非流式输出审核。返回改写后的文本（reject 时文本
	// 无效，调用方应拒绝下发）。
	CheckOutput(ctx context.Context, tenantID string, text string) (filtered string, violation *SafetyViolation)

	// NewStreamChecker 构造流式输出检查器：分块送入 Chunk，安全
	// 前缀经替换后下发，残余缓冲在 Finish 冲洗——处理词跨 chunk
	// 拆分的边界命中。
	NewStreamChecker(tenantID string) StreamChecker
}

// StreamChecker 是单次流式调用的输出侧检查器（非并发安全）。
type StreamChecker interface {
	// Chunk 处理一段增量文本，返回可安全下发的文本。violation 非
	// nil 且 Rejected() 时调用方应立即断流（已下发内容不撤回）。
	Chunk(text string) (filtered string, violation *SafetyViolation)
	// Finish 流结束时冲洗残余缓冲（[DONE] 前调用）。
	Finish() (filtered string, violation *SafetyViolation)
}
