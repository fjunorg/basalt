# 租户数据库隔离（tenant isolation）

> 状态：dedicated-db 已实装（端到端示范接入：dict 模块）；shared-db 经评估暂不实装（见 §4）。
> 关联代码：`pkg/stores/gormstore/tenant_router.go`、`pkg/modules/tenant`、`cmd/basalt/serve.go`。

## 1. 三模式总览

| 模式 | 状态 | 语义 |
|------|------|------|
| `shared-schema` | 默认，实装 | 共库共表，行级 `tenant_id` 列隔离（identity 行级过滤） |
| `shared-db` | 枚举就绪，未实装 | 共实例独立库/独立 schema——见 §4 评估结论 |
| `dedicated-db` | **已实装** | 租户独立数据库（独立实例/独立文件），请求级路由 |

## 2. dedicated-db 架构

### 2.1 库边界

- **主库**（现有连接 `BASALT_DB_DSN`）：identity（用户/角色，跨租户认证必须）、tenant（租户目录本身——路由控制数据）、audit（全局防篡改链）、quota、以及全部 `shared-schema` 租户与平台用户的业务数据。
- **租户库**（每 dedicated 租户一个）：该租户的业务数据。首访懒初始化：复用 `gormstore.OpenDB` 全量 AutoMigrate（与主库同一迁移路径，建库即建表）。

### 2.2 路由机制（`TenantRouter` + 路由型 store）

```
请求 → AuthMiddleware（userID 注入 ctx）
     → 业务 handler → store 方法(ctx)
     → dbFor(ctx)：
         userID → 用户记录 TenantID（主库）
       → 租户记录 IsolationMode/DBRef（主库）
       → dedicated-db？──否──→ 主库（现状行为）
              │是
              └→ TenantRouter.DB(DBRef) → 租户库连接（懒建+缓存）
```

- **DSN 凭据不落库**：租户记录只存引用名 `DBRef`（如 `gov-db`），实际连接串经环境变量 `BASALT_TENANT_DSN_<REF大写>`（如 `BASALT_TENANT_DSN_GOV_DB`）注入，运维侧管理密钥。
- **错误语义**：DSN 未配置 / DBRef 缺失 / 连接或迁移失败 → `gormstore.ErrTenantDBUnavailable` 明确报错，**绝不静默回退主库**（回退=租户数据落错库、隔离破坏）。
- **重启恢复**：租户库是普通库文件/网络库，重启后 Router 缓存冷启动、按需重连，数据延续。
- **解析成本**：每请求 1-2 次主库点查（用户→租户）。sqlite 本地毫秒级；生产高频场景可在 `dbFor` 层加 TTL 缓存（接口不变）。

### 2.3 端到端示范接入（dict 模块）

`RoutedDictStore` 实现 `dict.Store`：查询体与固定库 `DictStore` 共用包级函数（单一来源），仅连接按请求路由。serve 装配：

```
BASALT_STORAGE=sqlite BASALT_TENANT_ROUTING=on   # 默认 off，现状行为不变
```

开启后 dict 模块的 API 请求即按租户路由；关闭（或不设）时 dict 与其余模块走固定主库。**其余业务模块接入**：复制 RoutedDictStore 模式——查询体抽包级函数 + dbFor 路由（每个模块约 60 行机械代码，不改变模块层任何代码）。逐模块接入随需求排期，机制已验证。

## 3. 运维手册

```bash
# 1. 规划租户库（以 sqlite 文件为例）
mkdir -p /var/lib/basalt/tenants
export BASALT_TENANT_DSN_GOV_DB=/var/lib/basalt/tenants/gov.db

# 2. 开启路由（重启生效；主库行为不变）
export BASALT_TENANT_ROUTING=on

# 3. 创建 dedicated 租户（API）
POST /api/v1/tenant/tenants
{"code":"gov","name":"政府客户","isolation_mode":"dedicated-db","db_ref":"gov-db"}

# 4. 该租户用户登录后，dict API 的读写自动落在 gov.db
```

注意：`db_ref` 仅在 `dedicated-db` 模式下接受；`isolation_mode` 创建后不可改（改库属迁移操作，走运维流程）。

## 4. shared-db 评估结论（不实装决策）

**决策：保留枚举与文档说明，暂不实装。依据：**

1. **sqlite 无对应物**：sqlite 单文件即整库，"共享实例独立库"没有原生形态；ATTACH 多库对 GORM 透明性差（迁移/事务跨库语义复杂），收益不成比例。
2. **postgres schema 隔离需驱动先行**：go.mod 尚无 postgres 驱动（serve.go postgres 分支为结构化预留），schema-per-tenant 无法端到端验证——不为未验证路径写实现。
3. **需求已被覆盖**：共享场景的主要需求（数据混淆隔离）行级已满足；shared-db 的增量收益（schema 级故障/备份边界）在 dedicated-db 中以更强形态提供。
4. **落地路径**：postgres 方言接线后，以 schema-per-tenant 实装（TenantRouter 增加 schema 解析分支，路由型 store 模式不变），届时本节更新为实装说明。

## 5. 测试覆盖

`pkg/stores/gormstore/tenant_router_test.go`：
- 路由正确性 + 隔离性：两 dedicated 租户同 code 各自建库互不冲突、数据互不可见；shared-schema 租户与平台用户走主库
- 重启恢复：Router/主库连接重建后租户库数据延续
- 错误语义：DSN 缺失/DBRef 缺失/连接失败均 `ErrTenantDBUnavailable`，无静默回退
