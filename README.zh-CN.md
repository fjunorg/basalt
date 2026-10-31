# Basalt（玄武）

> 纯 Go · 纯开源 · AI 原生 · 面向 ToB/ToG 的通用后端平台框架
> Pure-Go, fully open-source (Apache-2.0), AI-native backend platform framework for B2B & GovTech — [English](README.md)

> **命名说明**：仓库所在目录名为 `AiGoAdmin`（历史工作代号）；项目正式名称为 **Basalt（玄武）**，Go module `github.com/basalt-go/basalt`——文档与发布物统一以 Basalt 为准。

![Status](https://img.shields.io/badge/release-v1.7.0-blue) ![License](https://img.shields.io/badge/license-Apache--2.0-blue) ![Go](https://img.shields.io/badge/go-1.26%2B-00ADD8) ![Tests](https://img.shields.io/badge/tests-race--enabled-green)

## 五分钟上手

```bash
# 要求 Go >= 1.26
make build

# 演示数据集初始化（幂等，仅 sqlite；prod 环境拒绝执行，--force 显式覆盖）：
#   组织三级部门树、数据权限各档角色、6 个跨部门用户、字典、公告、
#   示例租户+套餐、财务/公文/合同、知识库、Agent 定义、通知模板、两步审批流。
#   演示账号统一口令 Demo@2026：demo-ceo / demo-dev / demo-sale /
#   demo-viewer / demo-fin / demo-tenant
BASALT_STORAGE=sqlite BASALT_DB_DSN=/tmp/basalt.db ./bin/basalt seed --demo

# 启动服务，浏览器打开 http://localhost:8080，demo-ceo / Demo@2026 登录
BASALT_STORAGE=sqlite BASALT_DB_DSN=/tmp/basalt.db ./bin/basalt serve
```

首启引导管理员为 `admin / Basalt@2026`（**生产环境必须显式覆盖**——prod 守卫拒绝默认凭据启动）。

生产形态推荐（持久化 + 国密审计链 + AI 接入）：

```bash
BASALT_STORAGE=sqlite \                      # 全部 22 模块落盘（含审批流、通知与动态菜单）
BASALT_DB_DSN=/var/lib/basalt/basalt.db \
BASALT_CRYPTO_ALGO=sm3 \                     # 审计链切换国密 SM3
BASALT_AI_PROVIDER_URL=http://vllm:8000 \    # 任意 OpenAI 兼容端点
BASALT_AI_PROVIDER_KEY=sk-xxx \
  basalt serve --env prod
```

生产部署（单二进制 / Docker 容器两条路径）与环境变量清单（26 项，含用途与默认值）见 [docs/deploy.md](docs/deploy.md) 与 [.env.example](.env.example)。

### 前端构建说明（console/dist 已入库）

`console/dist` 为**有意提交**的前端构建产物：`go:embed`（见 `console/embed.go`）依赖它，保证 `go build` 即得带 UI 的单文件二进制——这是信创离线单文件交付的硬约束。修改 `console/` 源码后需重新构建并随源码一并提交：

```bash
cd console && npm install && npm run build
```

## 已交付模块（API-first，单体形态）

| 层 | 模块 | 能力 | 版本 |
|----|------|------|------|
| L0 | kernel | 模块系统（依赖拓扑/生命周期）、gin Router 抽象、配置、slog、OTel 可观测、**国密 Provider**（SM3 摘要 GB/T 32905 + SM2 签名 GB/T 32918.2，官方向量验证）、**数据权限 Scope 模型**、CLI（serve/version/new/secure/seed） | ✅ |
| L1 | identity | 用户/角色 RBAC、bcrypt、JWT（jti + 会话注册表 + 踢下线）、权限点中间件（通配）、**OAuth2 2.1/SSO 应用管理（PKCE/客户端凭证/轮换刷新/钉钉三方登录）**、**角色数据权限五档（all/dept/dept_and_below/self/custom）**、幂等引导管理员、prod 安全守卫 | v0.3 |
| L1 | tenant | 租户 CRUD、租户上下文实时解析、行级隔离；v1.4.0 起 dedicated-db 库级隔离（v1.6.0 扩至 14 个业务模块请求级路由，平台级模块固定主库，见 [docs/tenant-isolation.md](docs/tenant-isolation.md)） | v0.2 |
| L1 | audit | **防篡改审计链**（链式哈希防单点篡改、链尾 SM2 签名防整库重算伪造历史、篡改定位、算法切换多算法逐段核验、GORM 持久化重启续链、SM3 可选） | v0.2 |
| L1 | workflow | 轻量审批流（会签/或签/**依次审批**、待办、守卫、**抄送/转办/委派/加签/驳回至节点（轮次机制）**；实例与定义 GORM 持久化、重启恢复） | v0.2 |
| L1 | org | 组织架构（部门树 + 岗位；树级数据权限裁剪接入） | v0.1 |
| L1 | dict | 数据字典（字典类型 + 数据项，表单下拉/枚举统一维护） | v0.1 |
| L1 | sysconfig | 参数配置（运行期动态 KV，登录级只读） | v0.1 |
| L1 | notice | 通知公告（草稿/发布/全员已读视图） | v0.1 |
| L1 | **notify** | **通知中心**（短信/邮件/站内信三渠道；模板渲染、凭据环境变量注入不落库、发送日志、站内信收件箱、阿里云短信自研签名/SMTP 零新依赖） | v0.1 |
| L1 | **menu** | **动态菜单**（菜单 CRUD + 角色绑定 + 按权限过滤的菜单树下发，API 空时前端回退静态映射；种子三分组 IA） | v0.1 |
| L1 | **tenantpkg** | **租户套餐**（菜单/权限集合模板 CRUD + 租户绑定 + PermFilter 单点裁剪，与动态菜单自动联动） | v0.1 |
| L1 | **errcode** | **错误码管理**（注册表 CRUD + 现有模块错误码种子） | v0.1 |
| L1 | file | 文件管理（本地上传/下载/列表；v1.7.0 起 **S3 兼容多云存储**——自研 SigV4 零 SDK 依赖，MinIO/OSS/COS，混合存量路由与预签名直链，见 [docs/file-s3.md](docs/file-s3.md)） | v0.1 |
| L1 | job | 轻量任务调度（间隔 + **cron 表达式双模式**、启停/手动触发、**在线动态 HTTP 任务**、执行日志持久化查询，重启恢复含 NextRun 重算） | v0.1 |
| L1 | finance | 财务业务（报销/付款/发票登记/部门预算，审批联动与预算预检；部门+申请人双维数据权限接入） | v0.1 |
| L1 | document | 公文流转（收发文登记、密级缓急、审批联动、归档；拟稿部门数据权限接入） | v0.1 |
| L1 | contract | 合同管理（登记/审批联动/生效完结 + 到期扫描督办） | v0.1 |
| L1 | **oplog** | **操作/API 日志与运行监控**（异步采集不阻塞请求、用户字段完整验签、BASALT_OPLOG_MODE 采样、日志分页过滤 + 90 秒滑动窗口 QPS/错误率指标） | v0.1 |
| L2 | ai-gateway | **OpenAI 兼容模型网关** + **SSE 流式**（include_usage 记账）+ 租户**月度账期**配额（持久化原子记账）+ 调用审计；v1.2.0 起集成内容安全审核 | v0.2 |
| L2 | ai-safety | **AI 内容安全**（敏感词库、租户可配策略、调用链审核：reject/replace/mark，默认从严回落） | v0.1 |
| L2 | rag | **知识库与检索增强**（库/文档/切片/向量化/相似检索；OpenAI 兼容 embeddings，sqlite blob 向量起步，VectorSearcher 接口预留专用向量库） | v0.1 |
| L2 | agent | **轻量 Agent 编排**（定义/运行/工具调用循环/SSE 运行事件；LLM 经 ai-gateway 全链路，内置 RAG 检索工具；自研循环不引 Eino，ToolRegistry 预留生态对接） | v0.1 |
| — | stores | GORM 持久化（SQLite 无 CGO，sqlite 装配下覆盖全部 22 个模块；openGauss-postgres 分支占位，达梦/OceanBase 经 OpenDB 注入） | v0.1 |
| L3 | console | Admin Console（Vue3 + TypeScript + Element Plus 按需导入，32 视图：RBAC/**动态菜单（后端下发+静态回退）**/租户隔离与套餐/审批/审计链/AI 对话/Agent/通知中心/SSO/在线用户/错误码 + 业务模块管理页，Semi Design 视觉，go:embed 单二进制） | v0.1 |

> 平台业务模块（L1）与 AI 基础设施模块（L2）均具备 GORM 持久化与内存零依赖两条装配路径（`BASALT_STORAGE=sqlite|memory`，serve 层全装配冒烟测试锁定）；版本明细见 [CHANGELOG](CHANGELOG.md)。

> 后续路线：gRPC/服务治理、SM4/GMTLS（已提前交付：Admin Console v1.1.0、RAG 与内容安全 v1.2.0、Agent 编排与 dedicated-db 隔离 v1.3.0、SM2 签名 v1.4.0、P0 平台完备性批次 v1.6.0、P1a 效率批次 v1.7.0）；shared-db 隔离与支付系统经评估不自研（见 [docs/tenant-isolation.md](docs/tenant-isolation.md) §4 与 CHANGELOG 战略决策记录）。详见 [CHANGELOG](CHANGELOG.md)。

## CLI 命令

| 命令 | 说明 |
|------|------|
| `basalt serve` | 启动 HTTP 服务（`--addr` / `--env dev\|test\|staging\|prod`） |
| `basalt version` | 打印版本 |
| `basalt new module <name>` | 生成模块脚手架 |
| `basalt seed --demo` | 演示数据集初始化（幂等）：组织三级部门树/跨部门角色用户/数据权限各档角色/字典/多状态公告/示例租户+套餐绑定/财务单据/公文合同/知识库/Agent 定义/通知模板/演示审批流。**prod 环境拒绝执行**（`--force` 显式覆盖）；仅 sqlite 存储；重复执行数据量不变。演示账号统一口令 `Demo@2026`（demo-ceo / demo-dev / demo-sale / demo-viewer / demo-fin / demo-tenant） |
| `basalt secure --check` | 等保基线自查：七项检查（凭据默认值/加密算法合规/上传目录/审计链完整性/租户隔离开关/密钥暴露面/SSO 与三方登录密钥），三态 PASS/WARN/FAIL；退出码 **0=全过 / 1=有 WARN / 2=有 FAIL**（CI 可直接消费） |

## 架构

```
L3  前端控制台（Vue3 + TS + Element Plus 按需导入，go:embed 嵌入）  console/
L2  AI 基础设施：模型网关 · 配额 · 审计 · 内容安全 · RAG · Agent（已交付）  pkg/ai/ + pkg/modules/
L1  平台业务：身份 · 多租户 · 工作流 · 审计 · 业务模块群（组织/字典/参数/
    通知/文件/任务/财务/公文/合同）（可插拔模块）           pkg/modules/
L0  内核：HTTP · 配置 · 日志 · 服务治理 · 国密 · CLI        pkg/kernel/
横切 信创适配（麒麟/龙芯/鲲鹏 · 达梦/openGauss）· 部署 · 安全
```

详见 [docs/01-architecture.md](docs/01-architecture.md)。

## 编写一个模块

```go
type Hello struct{}

func (*Hello) Name() string           { return "hello" }
func (*Hello) Version() string        { return "0.1.0" }
func (*Hello) Dependencies() []string { return nil }

func (*Hello) Init(_ context.Context, a *app.App) error {
    a.Mux().HandleFunc("GET /api/v1/hello", func(w http.ResponseWriter, _ *http.Request) {
        web.OK(w, map[string]string{"msg": "hello"})
    })
    return nil
}
func (*Hello) Start(context.Context) error { return nil }
func (*Hello) Stop(context.Context) error  { return nil }

// 注册：a.Registry().Register(&Hello{})
```

模块经 Registry 做依赖拓扑排序，Init/Start 正序、Stop 逆序驱动——这是 Basalt 可插拔架构的核心机制（`pkg/kernel/app`）。

## 文档索引

| 文档 | 内容 |
|------|------|
| [CHANGELOG](CHANGELOG.md) | 版本历史（v1.0.0 → v1.7.0）、逐模块版本与战略决策 |
| [docs/01-architecture.md](docs/01-architecture.md) | 分层架构与设计决策 |
| [docs/02-oss-governance.md](docs/02-oss-governance.md) | 开源治理与依赖政策 |
| [docs/03-gap-analysis.md](docs/03-gap-analysis.md) | 竞品差距分析（芋道 × Spring AI × Eino） |
| [docs/04-frontend-ui-evaluation.md](docs/04-frontend-ui-evaluation.md) | 前端技术选型评估 |
| [docs/deploy.md](docs/deploy.md) | 部署指南（单二进制与 Docker） |
| [docs/tenant-isolation.md](docs/tenant-isolation.md) | 租户库隔离：模式、边界与运维手册 |
| [docs/file-s3.md](docs/file-s3.md) | S3 兼容存储：矩阵、混合路由与隔离 |
| [docs/sm2-ops.md](docs/sm2-ops.md) | SM2 链尾签名运维 |

## 路线图

| 里程碑 | 周期 | 交付 |
|--------|------|------|
| M1 | 0–3 月 | 内核完善（gin/cobra/otel/国密）+ 身份/多租户实装 + CLI 脚手架 |
| M2 | 3–6 月 | 轻量审批工作流 + 防篡改审计 + AI 网关与配额 |
| M3 | 6–12 月 | 已全部提前交付：Admin Console（v1.1.0）、RAG 与内容安全（v1.2.0）、Agent 编排（v1.3.0，自研轻量实现而非集成 Eino） |

## 参与

- 贡献指南：[CONTRIBUTING.md](CONTRIBUTING.md)（DCO 签名必需）
- 安全漏洞：[SECURITY.md](SECURITY.md)（**请勿在公开 issue 报告安全问题**）
- 社区治理：[GOVERNANCE.md](GOVERNANCE.md)
- 行为准则：[CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md)

## 许可

[Apache License 2.0](LICENSE) · 版权所有 The Basalt Authors。
"Basalt" 名称与徽标为项目商标，不随代码许可授予。
