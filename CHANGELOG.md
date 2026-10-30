# Changelog

本项目遵循 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，版本语义 [SemVer](https://semver.org/)。

## [Unreleased]

### 新增

- **Admin Console v0.1**（Vue3 + Element Plus，构建产物 go:embed 单二进制）：
  登录、概览（身份/用量/链校验）、用户/角色/租户管理、审批中心（发起/待办/
  通过/驳回）、审计链查看（模块过滤 + 完整性徽章）、AI 控制台（SSE 流式
  对话 + 账期用量环图）。hash 路由 + 相对资源路径，任意挂载路径可用。
- Router.MountSPA：API 路由优先，非 /api 未命中回退 index.html。

### 已知限制（Console）

- JS 版（TS 迁移待办）；Element Plus 全量引入（主 chunk gzip ~350KB，
  按需导入优化待办）；菜单未按权限点动态显隐。

## [1.0.0] - 2026-10-28

首个正式发布：单体形态的 AI 原生 B/G 端后端平台（API-first）。

### 新增

- **内核（L0）**：模块系统（依赖拓扑排序生命周期）、gin Router 抽象（模块零 gin 依赖）、
  配置（环境变量）、slog 日志、OpenTelemetry trace/metrics（无端点 no-op）、
  国密 Provider（SM3/SHA-256 可切换）、CLI（serve/version/new module 脚手架）
- **身份（identity v0.3）**：用户/角色 RBAC（权限点 + 通配）、bcrypt、JWT、
  权限点中间件、幂等引导管理员、生产环境默认密钥/口令拒绝启动
- **多租户（tenant v0.2）**：租户 CRUD、租户上下文实时解析、行级隔离
  （三模式枚举就绪：shared-db/dedicated-db 随后续存储路由落地）
- **防篡改审计（audit v0.2）**：链式哈希审计链，篡改/删除精确定位；
  identity/workflow/AI 调用全量入链；GORM 持久化（重启续链）；
  哈希算法跟随配置自动切换国密 SM3（GB/T 32905 官方向量验证）
- **审批流（workflow v0.1）**：会签/或签状态机、撤回、待办、403/409 守卫
- **AI 网关（ai-gateway v0.2）**：OpenAI 兼容协议统一层（vLLM/Ollama/
  Qwen/GLM/DeepSeek 一套通吃）、SSE 流式转发（include_usage 注入记账）、
  租户月度账期 Token 配额（跨期限额延续、GORM 持久化原子记账）、
  429 超限封锁、全链路调用审计
- **持久化（stores/gormstore）**：纯 Go SQLite（无 CGO，信创交叉编译友好）；
  openGauss 走 postgres 方言、达梦/OceanBase 经 OpenDB 注入
- **工程**：多 Go 版本 CI + golangci-lint + 信创交叉编译矩阵
  （鲲鹏 arm64/龙芯 loong64）+ govulncheck；开源治理八件套（Apache-2.0）
  含纯开源承诺书

### 已知范围外（1.1 路线）

gRPC 与服务治理（微服务形态）、RAG 管道、Agent 编排（Eino）、AI 内容安全、
Vue3 Admin Console、SM2 签名（待密钥管理体系）、shared-db/dedicated-db 隔离实装、
达梦/openGauss 真机 CI 矩阵。

## [0.1.0-skeleton] - 2026-10-27

项目骨架：模块化内核 + 治理文件 + CI。
