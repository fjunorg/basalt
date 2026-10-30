# Changelog

本项目遵循 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，版本语义 [SemVer](https://semver.org/)。

## [Unreleased]

### 新增

- **Admin Console v0.1**（Vue3 + Element Plus，构建产物 go:embed 单二进制）：
  登录、概览（身份/用量/链校验）、用户/角色/租户管理、审批中心（发起/待办/
  通过/驳回）、审计链查看（模块过滤 + 完整性徽章）、AI 控制台（SSE 流式
  对话 + 账期用量环图）、工作流定义页，以及 9 个新业务模块的管理页
  （组织/字典/参数/通知/文件/任务/财务/公文/合同）。hash 路由 + 相对
  资源路径，任意挂载路径可用。
- Router.MountSPA：API 路由优先，非 /api 未命中回退 index.html。
- **平台业务模块群 v0.1**（9 个，主流管理框架标配能力；均提供 GORM 持久化
  与内存零依赖两条装配路径；finance/document/contract 与 workflow 审批
  联动）：
  - org：组织架构（部门树 + 岗位）
  - dict：数据字典（字典类型 + 数据项，表单下拉/枚举统一维护）
  - sysconfig：参数配置（运行期动态 KV，区别于 env 启动配置；登录级只读）
  - notice：通知公告（草稿/发布/全员已读视图）
  - file：文件管理（本地上传/下载/列表；S3 兼容对象存储驱动经 Storage 接口预留）
  - job：轻量任务调度（内置任务注册表 + 间隔调度 + 启停/手动触发/运行记录，
    零外部依赖；运行态持久化、重启恢复，停机期间错过的触发点不补跑）
  - finance：财务业务（报销/付款/发票登记/部门预算；提交即按流程定义发起
    审批并预算预检，通过后回写单据占用预算，付款闭环）
  - document：公文流转（发文拟稿/收文登记、密级缓急标注、审批联动、归档）
  - contract：合同管理（登记/审批联动/生效完结 + 到期扫描入审计链供督办）
- **持久化与装配（stores/serve）**：
  - gormstore.Stores 聚合工厂新增 9 模块 store 工厂，与既有 store 共用
    OpenDB 迁移路径（达梦/openGauss 等 OpenDB 注入同源）
  - serve sqlite 装配分支为 13 个模块注入 GORM store（identity/tenant/audit/
    ai-gateway 配额 + 9 业务模块；workflow 审批流仍为内存态，持久化随后续
    版本）；新增 postgres/opengauss 存储分支占位（驱动接线随后续版本，
    当前显式报错并提示回退 sqlite/memory）
  - store 语义、重启续存与全装配冒烟测试（gormstore/module_stores_test）
- **工程与部署资产**：
  - CI（.github/workflows/ci.yml）：多 Go 版本测试矩阵（vet + test -race）、
    golangci-lint（.golangci.yml 标准五件套，基线存量外科式豁免）、
    govulncheck、信创交叉编译矩阵（linux/amd64、arm64 鲲鹏、loong64 龙芯，
    产物以 artifact 发布）
  - Dockerfile：三阶段构建（console 前端 → go:embed Go 构建 → alpine 运行时），
    非 root 用户、/healthz 健康检查、/var/lib/basalt 持久卷
  - .env.example（15 项环境变量模板与用途说明）、docs/deploy.md 部署指南
    （单二进制与容器两条路径）、.gitignore 与 .dockerignore

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
