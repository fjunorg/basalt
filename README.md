# Basalt（玄武）

> 纯 Go · 纯开源 · AI 原生 · 面向 ToB/ToG 的通用后端平台框架
> Pure-Go, fully open-source (Apache-2.0), AI-native backend platform framework for B2B & GovTech.

> **命名说明**：仓库所在目录名为 `AiGoAdmin`（历史工作代号）；项目正式名称为 **Basalt（玄武）**，Go module `github.com/basalt-go/basalt`——文档与发布物统一以 Basalt 为准。

![Status](https://img.shields.io/badge/release-v1.0.0-blue) ![License](https://img.shields.io/badge/license-Apache--2.0-blue) ![Go](https://img.shields.io/badge/go-1.26%2B-00ADD8) ![Tests](https://img.shields.io/badge/tests-race--enabled-green)

## 已交付模块（API-first，单体形态）

| 层 | 模块 | 能力 | 版本 |
|----|------|------|------|
| L0 | kernel | 模块系统（依赖拓扑/生命周期）、gin Router 抽象、配置、slog、OTel 可观测、**国密 SM3 Provider**（GB/T 32905 向量验证）、CLI 脚手架 | ✅ |
| L1 | identity | 用户/角色 RBAC、bcrypt、JWT、权限点中间件（通配）、幂等引导管理员、prod 安全守卫 | v0.3 |
| L1 | tenant | 租户 CRUD、租户上下文实时解析、行级隔离 | v0.2 |
| L1 | audit | **防篡改审计链**（链式哈希、篡改定位、GORM 持久化重启续链、SM3 可选） | v0.2 |
| L1 | workflow | 轻量审批流（会签/或签/撤回、待办、守卫） | v0.1 |
| L1 | org | 组织架构（部门树 + 岗位） | v0.1\* |
| L1 | dict | 数据字典（字典类型 + 数据项，表单下拉/枚举统一维护） | v0.1\* |
| L1 | sysconfig | 参数配置（运行期动态 KV，登录级只读） | v0.1\* |
| L1 | notice | 通知公告（草稿/发布/全员已读视图） | v0.1\* |
| L1 | file | 文件管理（本地上传/下载/列表，本地存储目录） | v0.1\* |
| L1 | job | 轻量任务调度（间隔调度 + 启停/手动触发，运行态持久化、重启恢复） | v0.1\* |
| L1 | finance | 财务业务（报销/付款/发票登记/部门预算，审批联动与预算预检） | v0.1\* |
| L1 | document | 公文流转（收发文登记、密级缓急、审批联动、归档） | v0.1\* |
| L1 | contract | 合同管理（登记/审批联动/生效完结 + 到期扫描督办） | v0.1\* |
| L2 | ai-gateway | **OpenAI 兼容模型网关** + **SSE 流式**（include_usage 记账）+ 租户**月度账期**配额（持久化原子记账）+ 调用审计 | v0.2 |
| — | stores | GORM 持久化（SQLite 无 CGO，覆盖 13 个模块；openGauss-postgres 分支占位，达梦/OceanBase 经 OpenDB 注入） | v0.1 |
| L3 | console | Admin Console（Vue3 + Element Plus，登录/概览/RBAC/审批/审计链/AI 控制台 + 业务模块管理页，go:embed 单二进制） | v0.1\* |

> \* 标记项为 **Unreleased**（已合入主干，随下一版本发布）：9 个业务模块均具备 GORM 持久化与内存零依赖两条装配路径（workflow 审批流暂为内存态）；Console v0.1 详情见 [CHANGELOG](CHANGELOG.md)。

> 1.1 路线：gRPC/服务治理、RAG、Agent（Eino）、内容安全、SM2 签名、shared-db/dedicated-db 隔离（Admin Console 已提前交付 v0.1，见 CHANGELOG Unreleased）。详见 [CHANGELOG](CHANGELOG.md)。

## 定位

| 差异化能力 | 说明 |
|-----------|------|
| **AI 原生** | 模型网关、租户级 Token 配额与成本核算、AI 全链路审计——开箱内建，而非外挂 |
| **合规内建** | 防篡改审计链（已交付）、国密 SM2/SM3/SM4（Provider 就绪，M2 接 emmansun/gmsm）、等保三级基线（规划），默认开启而非可选项 |
| **信创适配** | 麒麟/统信 OS、鲲鹏(ARM64)/龙芯(LoongArch) 交叉编译矩阵已入 CI；达梦/openGauss/OceanBase 方言经 OpenDB 注入 |
| **纯开源承诺** | 全部功能单一 Apache 2.0 协议，永无闭源"商业版"——见 [OPEN_SOURCE_PLEDGE.md](OPEN_SOURCE_PLEDGE.md) |

竞品调研与架构决策记录见 [docs/](docs/) 目录。

## 快速开始

```bash
# 要求 Go >= 1.26
go run ./cmd/basalt serve

# 首启引导管理员：admin / Basalt@2026（生产环境必须显式配置）
curl -X POST localhost:8080/api/v1/identity/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"Basalt@2026"}'

# 生产形态推荐（持久化 + 国密审计链 + AI 接入）：
BASALT_STORAGE=sqlite \                      # 落盘：身份/租户/审计/AI 配额 + 9 业务模块（审批流暂内存态）
BASALT_DB_DSN=/var/lib/basalt/basalt.db \
BASALT_CRYPTO_ALGO=sm3 \                     # 审计链切换国密 SM3
BASALT_AI_PROVIDER_URL=http://vllm:8000 \    # 任意 OpenAI 兼容端点
BASALT_AI_PROVIDER_KEY=sk-xxx \
  basalt serve --env prod
```

生产部署（单二进制 / Docker 容器两条路径）与环境变量清单（15 项，含用途与默认值）见 [docs/deploy.md](docs/deploy.md) 与 [.env.example](.env.example)。

### 前端构建说明（console/dist 已入库）

`console/dist` 为**有意提交**的前端构建产物：`go:embed`（见 `console/embed.go`）依赖它，保证 `go build` 即得带 UI 的单文件二进制——这是信创离线单文件交付的硬约束。修改 `console/` 源码后需重新构建并随源码一并提交：

```bash
cd console && npm install && npm run build
```

## 架构

```
L3  前端控制台（Vue3 + Element Plus，go:embed 嵌入，v0.1 已交付）  console/
L2  AI 基础设施：模型网关 · 配额 · 审计（已交付）· RAG · Agent（规划）  pkg/ai/
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

## 路线图

| 里程碑 | 周期 | 交付 |
|--------|------|------|
| M1 | 0–3 月 | 内核完善（gin/cobra/otel/国密）+ 身份/多租户实装 + CLI 脚手架 |
| M2 | 3–6 月 | 轻量审批工作流 + 防篡改审计 + AI 网关与配额 |
| M3 | 6–12 月 | RAG/Agent（集成 Eino）+ 内容安全（Admin Console 已提前交付 v0.1） |

## 参与

- 贡献指南：[CONTRIBUTING.md](CONTRIBUTING.md)（DCO 签名必需）
- 安全漏洞：[SECURITY.md](SECURITY.md)（**请勿在公开 issue 报告安全问题**）
- 社区治理：[GOVERNANCE.md](GOVERNANCE.md)
- 行为准则：[CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md)

## 许可

[Apache License 2.0](LICENSE) · 版权所有 The Basalt Authors。
"Basalt" 名称与徽标为项目商标，不随代码许可授予。
