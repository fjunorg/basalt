// Package app 定义框架的模块系统与应用生命周期。
//
// 模块是 Basalt 的基本扩展单元：L1 平台业务模块（identity/tenant/...）
// 与 L2 AI 模块（gateway/quota/...）均实现 Module 接口，经 Registry
// 注册后由 App 统一驱动。模块间只允许通过接口与 App 提供的注册表协作，
// 禁止直接 import 对方实现包（由 CI 架构守护测试强制）。
package app

import "context"

// Module 是所有可插拔模块必须实现的核心接口。
//
// 生命周期时序：所有模块按依赖拓扑序执行 Init，全部成功后按同序执行
// Start；进程退出时按依赖逆序执行 Stop。Init 负责注册路由与依赖声明
// 解析，Start 可阻塞监听，Stop 必须可在 ctx 取消时返回。
type Module interface {
	// Name 返回模块唯一标识，建议使用小写连字符，如 "identity"。
	Name() string

	// Version 返回模块语义化版本。
	Version() string

	// Dependencies 返回本模块依赖的其他模块名列表。
	// Registry 保证被依赖模块先于本模块完成 Init 与 Start。
	Dependencies() []string

	// Init 完成一次性装配：注册路由、声明配置、准备资源。
	// Init 中不得启动长期运行的 goroutine，那属于 Start 的职责。
	Init(ctx context.Context, a *App) error

	// Start 启动模块运行期行为。可阻塞，ctx 取消即视为停止信号。
	Start(ctx context.Context) error

	// Stop 释放模块持有的资源。Stop 必须幂等且响应 ctx 超时。
	Stop(ctx context.Context) error
}

// Optional 接口：模块可选择实现以挂接额外能力（按需扩展，如
// 提供 RPC 服务注册、健康检查探针等），避免 Module 接口膨胀。
type HookProvider interface {
	Hooks() *Hooks
}

// Hooks 收集模块的可选挂载点，当前为占位，随内核能力扩展。
type Hooks struct {
	// HealthCheck 返回模块级健康检查函数，键为检查项名称。
	HealthChecks map[string]func(ctx context.Context) error
}

// Descriptor 是模块可选实现的自描述接口：暴露权限点等元信息，
// 供平台 API（GET /api/v1/platform/modules）与前端动态渲染。
// 未实现的模块仅上报名称与版本。
type Descriptor interface {
	Describe() ModuleInfo
}

// ModuleInfo 是模块的自描述元信息。
type ModuleInfo struct {
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Permissions []string `json:"permissions,omitempty"`
}
