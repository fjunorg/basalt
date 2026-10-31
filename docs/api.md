# Basalt API 参考

> 自动提取自各模块路由注册（脚本核对，186 端点 / 21 模块），与代码同步维护。

## 1. 通用约定

- **Base URL**：`http://<host>:8080/api/v1`（下述路径均相对此前缀）
- **认证**：`Authorization: Bearer <token>`——JWT（`/identity/login` 签发）或 OAuth2 access token（`/identity/oauth2/token`）；标「登录级」的端点仅需有效 token，标权限点的端点还需对应权限（admin 内置角色持有通配 `*`）
- **响应包装**：`{"code": 0, "message": "ok", "data": <payload>}`；错误时 code/message 表意（HTTP 状态码同 code 字段）
- **分页**：列表端点普遍支持 `offset`/`limit`（默认 limit 50，上限 200），返回 `{"items": [...], "total": n}`
- **时间**：一律 UTC RFC3339；金额：一律 int64 分

### 错误码语义（常见）

| HTTP | 场景 |
|---|---|
| 400 | 参数缺失/JSON 非法 |
| 401 | 未认证/令牌过期 |
| 403 | 无权限点/非本人资源 |
| 404 | 资源不存在 |
| 409 | 唯一约束冲突/状态机非法迁移/依赖存在 |
| 422 | 业务校验失败（预算超限/内容安全拦截 safety.*） |
| 429 | 租户 Token 配额耗尽 |

## 2. 端点索引（按模块）

### agent — Agent 编排（9）

定义/运行/工具循环/SSE 七类事件。

| 方法 | 路径 | 权限 |
|---|---|---|
| Post | `/agents` | errcode:manage |
| Get | `/agents` | workflow:read |
| Get | `/agents/{id}` | workflow:read |
| Patch | `/agents/{id}` | errcode:manage |
| Delete | `/agents/{id}` | errcode:manage |
| Post | `/agents/{id}/runs` | agent:run |
| Get | `/agents/{id}/runs` | workflow:read |
| Get | `/runs/{id}` | workflow:read |
| Get | `/tools` | workflow:read |

### aisafety — AI 内容安全（11）

敏感词库/租户策略/审核试算（gateway 双侧集成见 gateway 章）。

| 方法 | 路径 | 权限 |
|---|---|---|
| Get | `/terms` | errcode:list |
| Post | `/terms` | errcode:manage |
| Patch | `/terms/{id}` | errcode:manage |
| Delete | `/terms/{id}` | errcode:manage |
| Post | `/terms/import` | errcode:manage |
| Get | `/terms/export` | errcode:list |
| Get | `/policies` | errcode:list |
| Put | `/policies/{tenant}` | errcode:manage |
| Get | `/settings` | errcode:list |
| Put | `/settings` | errcode:manage |
| Post | `/check` | errcode:manage |

### audit — 审计链（5）

防篡改链式审计（SM3/SHA-256 可切换、SM2 链尾签名）。

| 方法 | 路径 | 权限 |
|---|---|---|
| Get | `/events` | workflow:read |
| Get | `/verify` | workflow:read |
| Post | `/chain/signatures` | audit:sign |
| Get | `/chain/signatures` | workflow:read |
| Get | `/chain/signatures/verify` | workflow:read |

### contract — 合同管理（5）

登记/会签/生效/完结/到期扫描。

| 方法 | 路径 | 权限 |
|---|---|---|
| Post | `` | （登录级） |
| Get | `` | （登录级） |
| Get | `/export` | workflow:read |
| Post | `/{id}/submit` | （登录级） |
| Post | `/{id}/finish` | errcode:manage |

### dict — 数据字典（9）

字典类型与数据项。

| 方法 | 路径 | 权限 |
|---|---|---|
| Get | `/types` | dict:type:list |
| Post | `/types` | dict:type:manage |
| Patch | `/types/{id}` | dict:type:manage |
| Delete | `/types/{id}` | dict:type:manage |
| Get | `/datas` | dict:type:list |
| Post | `/datas` | dict:data:manage |
| Patch | `/datas/{id}` | dict:data:manage |
| Delete | `/datas/{id}` | dict:data:manage |
| Get | `/datas/by-type/{code}` | （登录级） |

### document — 公文流转（4）

发文/收文/密级/审批联动/归档。

| 方法 | 路径 | 权限 |
|---|---|---|
| Post | `` | （登录级） |
| Get | `` | （登录级） |
| Post | `/{id}/submit` | （登录级） |
| Post | `/{id}/archive` | errcode:manage |

### errcode — 错误码（5）

错误码注册表。

| 方法 | 路径 | 权限 |
|---|---|---|
| Get | `` | errcode:list |
| Get | `/{id}` | errcode:list |
| Post | `` | errcode:manage |
| Patch | `/{id}` | errcode:manage |
| Delete | `/{id}` | errcode:manage |

### file — 文件服务（5）

本地上传/下载与 S3 兼容多云驱动。

| 方法 | 路径 | 权限 |
|---|---|---|
| Get | `` | errcode:list |
| Post | `` | file:upload |
| Delete | `/{id}` | file:delete |
| Get | `/{id}/download` | （登录级） |
| Get | `/{id}/url` | （登录级） |

### finance — 财务中心（10）

报销/发票/预算/付款，workflow 联动。

| 方法 | 路径 | 权限 |
|---|---|---|
| Post | `/expenses` | （登录级） |
| Get | `/expenses` | （登录级） |
| Get | `/expenses/export` | finance:expense:list |
| Post | `/expenses/{id}/submit` | （登录级） |
| Get | `/invoices` | finance:invoice:manage |
| Post | `/invoices` | finance:invoice:manage |
| Get | `/budgets` | finance:budget:manage |
| Put | `/budgets/{dept}` | finance:budget:manage |
| Get | `/payments` | finance:payment:manage |
| Post | `/payments` | finance:payment:manage |

### identity — 身份与认证（28）

用户/角色 RBAC、JWT 登录登出、OAuth2/SSO、在线会话。

| 方法 | 路径 | 权限 |
|---|---|---|
| Get | `/info` | （登录级） |
| Post | `/users` | identity:user:create |
| Get | `/users` | identity:user:list |
| Get | `/users/export` | identity:user:list |
| Post | `/users/import` | identity:user:create |
| Get | `/users/{id}` | identity:user:get |
| Patch | `/users/{id}/status` | identity:user:status |
| Put | `/users/{id}/roles` | identity:role:manage |
| Post | `/roles` | identity:role:manage |
| Get | `/roles` | identity:role:manage |
| Patch | `/roles/{id}` | identity:role:manage |
| Delete | `/roles/{id}` | identity:role:manage |
| Post | `/login` | （登录级） |
| Get | `/me` | （登录级） |
| Post | `/logout` | （登录级） |
| Get | `/sessions` | identity:session:manage |
| Delete | `/sessions/user/{id}` | identity:session:manage |
| Post | `/apps` | identity:sso:manage |
| Get | `/apps` | identity:sso:manage |
| Get | `/apps/{id}` | identity:sso:manage |
| Patch | `/apps/{id}` | identity:sso:manage |
| Delete | `/apps/{id}` | identity:sso:manage |
| Post | `/apps/{id}/secret` | identity:sso:manage |
| Get | `/authorize` | （登录级） |
| Post | `/authorize` | （登录级） |
| Post | `/token` | （登录级） |
| Get | `/social/{provider}/authorize-url` | （登录级） |
| Get | `/social/{provider}/callback` | （登录级） |

### job — 定时任务（6）

interval/cron 双调度、代码注册与 HTTP 回调动态任务、执行日志。

| 方法 | 路径 | 权限 |
|---|---|---|
| Get | `` | errcode:list |
| Patch | `/{id}/status` | errcode:manage |
| Post | `/{id}/trigger` | errcode:manage |
| Get | `/{id}/runs` | errcode:list |
| Post | `/http` | errcode:manage |
| Delete | `/{id}` | errcode:manage |

### menu — 动态菜单（7）

菜单 CRUD/角色绑定/按权限过滤下发。

| 方法 | 路径 | 权限 |
|---|---|---|
| Get | `/me` | （登录级） |
| Get | `` | errcode:list |
| Post | `` | errcode:manage |
| Patch | `/{id}` | errcode:manage |
| Delete | `/{id}` | errcode:manage |
| Get | `/{id}/menus` | （登录级） |
| Put | `/{id}/menus` | （登录级） |

### notice — 通知公告（7）

全员公告（草稿/发布/撤回）——与 notify 站内信语义分离。

| 方法 | 路径 | 权限 |
|---|---|---|
| Get | `` | errcode:list |
| Post | `` | errcode:manage |
| Patch | `/{id}` | errcode:manage |
| Delete | `/{id}` | errcode:manage |
| Post | `/{id}/publish` | notice:publish |
| Post | `/{id}/revoke` | notice:publish |
| Get | `/published` | （登录级） |

### notify — 通知中心（16）

三渠道通知：模板/SMTP 账号/短信渠道/发送日志/站内信收件箱。

| 方法 | 路径 | 权限 |
|---|---|---|
| Post | `/templates` | errcode:manage |
| Get | `/templates` | errcode:list |
| Patch | `/templates/{id}` | errcode:manage |
| Delete | `/templates/{id}` | errcode:manage |
| Post | `/smtp-accounts` | errcode:manage |
| Get | `/smtp-accounts` | errcode:list |
| Delete | `/smtp-accounts/{id}` | errcode:manage |
| Post | `/sms-channels` | errcode:manage |
| Get | `/sms-channels` | errcode:list |
| Delete | `/sms-channels/{id}` | errcode:manage |
| Post | `/send` | errcode:manage |
| Post | `/resend/{id}` | errcode:manage |
| Get | `/logs` | errcode:list |
| Get | `/inbox` | （登录级） |
| Get | `/inbox/unread-count` | （登录级） |
| Post | `/inbox/{id}/read` | （登录级） |

### oplog — 操作日志（2）

操作/API 日志查询与运行指标。

| 方法 | 路径 | 权限 |
|---|---|---|
| Get | `/logs` | workflow:read |
| Get | `/metrics` | workflow:read |

### org — 组织架构（8）

部门树与岗位。

| 方法 | 路径 | 权限 |
|---|---|---|
| Get | `/depts/tree` | org:dept:list |
| Post | `/depts` | org:dept:manage |
| Patch | `/depts/{id}` | org:dept:manage |
| Delete | `/depts/{id}` | org:dept:manage |
| Get | `/posts` | org:post:list |
| Post | `/posts` | org:post:manage |
| Patch | `/posts/{id}` | org:post:manage |
| Delete | `/posts/{id}` | org:post:manage |

### rag — 知识库 RAG（12）

知识库/文档/切片/向量化/语义检索。

| 方法 | 路径 | 权限 |
|---|---|---|
| Post | `/kbs` | rag:kb:manage |
| Get | `/kbs` | workflow:read |
| Get | `/kbs/{id}` | workflow:read |
| Patch | `/kbs/{id}` | rag:kb:manage |
| Delete | `/kbs/{id}` | rag:kb:manage |
| Post | `/kbs/{id}/documents` | rag:doc:manage |
| Get | `/kbs/{id}/documents` | workflow:read |
| Get | `/documents/{id}` | workflow:read |
| Delete | `/documents/{id}` | rag:doc:manage |
| Post | `/documents/{id}/chunk` | rag:doc:manage |
| Post | `/documents/{id}/embed` | rag:doc:manage |
| Post | `/kbs/{id}/search` | workflow:read |

### sysconfig — 系统参数（5）

运行期 KV 配置。

| 方法 | 路径 | 权限 |
|---|---|---|
| Get | `` | errcode:list |
| Post | `` | errcode:manage |
| Patch | `/{id}` | errcode:manage |
| Delete | `/{id}` | errcode:manage |
| Get | `/by-key/{key}` | （登录级） |

### tenant — 租户管理（6）

租户 CRUD 与三模式隔离（shared-schema 行级/dedicated-db 路由）。

| 方法 | 路径 | 权限 |
|---|---|---|
| Post | `/tenants` | workflow:create |
| Get | `/tenants` | errcode:list |
| Get | `/tenants/{id}` | tenant:get |
| Patch | `/tenants/{id}` | tenant:update |
| Get | `/current` | （登录级） |
| Get | `/users` | （登录级） |

### tenantpkg — 租户套餐（7）

套餐模板与租户绑定，权限/菜单裁剪。

| 方法 | 路径 | 权限 |
|---|---|---|
| Post | `/packages` | errcode:manage |
| Get | `/packages` | errcode:manage |
| Get | `/packages/{id}` | errcode:manage |
| Patch | `/packages/{id}` | errcode:manage |
| Delete | `/packages/{id}` | errcode:manage |
| Put | `/tenants/{id}/package` | errcode:manage |
| Get | `/tenants/{id}/package` | errcode:manage |

### workflow — 流程审批（19）

流程定义/实例/待办/六项审批操作（委派转办加签抄送任意驳回依次审批）。

| 方法 | 路径 | 权限 |
|---|---|---|
| Post | `/instances` | workflow:create |
| Get | `/instances/{id}` | workflow:read |
| Post | `/instances/{id}/approve` | （登录级） |
| Post | `/instances/{id}/reject` | （登录级） |
| Post | `/instances/{id}/cancel` | （登录级） |
| Get | `/todo` | （登录级） |
| Get | `/cc` | （登录级） |
| Get | `/instances/mine` | （登录级） |
| Get | `/instances/all` | workflow:read |
| Post | `/instances/{id}/delegate` | （登录级） |
| Post | `/instances/by-definition/{key}` | （登录级） |
| Post | `/instances/{id}/cc` | （登录级） |
| Post | `/instances/{id}/delegate-act` | （登录级） |
| Post | `/instances/{id}/addsign` | （登录级） |
| Post | `/instances/{id}/reject-to` | （登录级） |
| Get | `/definitions` | workflow:read |
| Post | `/definitions` | workflow:def:manage |
| Patch | `/definitions/{id}` | workflow:def:manage |
| Delete | `/definitions/{id}` | workflow:def:manage |
### ai-gateway — AI 网关（4）

OpenAI 兼容对话端点（非流式/SSE）、提供方列表、租户配额管理。内容安全（ai-safety）在调用链双侧自动生效（输入拦截/输出流式分块检查，命中按租户策略 reject/replace/mark）。

| 方法 | 路径 | 权限 |
|---|---|---|
| POST | `/ai/chat/completions` | （登录级） |
| GET | `/ai/providers` | （登录级） |
| PUT | `/ai/quota/{tenant}` | ai:quota:manage |
| GET | `/ai/quota/{tenant}` | （登录级） |

## 3. 关键端点示例（真实请求/响应）

以下示例均可在 `basalt seed --demo` 灌入的演示数据上直接复现（口令 `Demo@2026`；管理员 admin 账号由 serve 首启创建，口令见 SECURITY 基线）。

### 登录取token（identity）

```bash
curl -s http://localhost:8080/api/v1/identity/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"demo-ceo","password":"Demo@2026"}'
```
```json
{"code":0,"message":"ok","data":{"token":"eyJhbGciOi...","expires_at":"2026-10-31T12:00:00Z"}}
```

### 创建租户（tenant，dedicated-db 示例）

```bash
curl -X POST http://localhost:8080/api/v1/tenant/tenants \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"code":"gov-a","name":"某局","isolation_mode":"dedicated-db","db_ref":"gov-db"}'
```
```json
{"code":0,"message":"created","data":{"id":"tn-...","code":"gov-a","name":"某局","isolation_mode":"dedicated-db","db_ref":"gov-db","status":"active"}}
```
> dedicated-db 的 DSN 经环境变量 `BASALT_TENANT_DSN_GOV_DB` 注入（凭据不落库，详见 docs/tenant-isolation.md）。

### 发起报销并进入审批（finance → workflow 联动）

```bash
curl -X POST http://localhost:8080/api/v1/finance/expenses \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"reason":"季度云资源采购","dept_id":"demo-d-plat","items":[{"name":"云主机","amount":98000}]}'
```
```json
{"code":0,"message":"created","data":{"id":"e-...","amount":98000,"status":"draft","business_key":"expense-e-..."}}
```
随后 `POST /finance/expenses/{id}/submit` 触发默认审批流，`POST /wf/instances/{instID}/approve` 审批通过后预算自动占用（金额单位：分）。

### 用户导入（identity，xlsx）

```bash
curl -X POST http://localhost:8080/api/v1/identity/users/import \
  -H "Authorization: Bearer $TOKEN" --data-binary @users.xlsx
```
```json
{"code":0,"message":"ok","data":{"imported":2,"failed":1,"errors":[{"row":3,"reason":"用户名已存在（跳过，不覆盖）"}]}}
```

### 通知发送（notify，三渠道）

```bash
curl -X POST http://localhost:8080/api/v1/notify/send \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"channel":"inbox","receiver":"demo-u-ceo","template_code":"demo-inbox-notice","vars":{"title":"上线通知","content":"v1.7 已发布"}}'
```
```json
{"code":0,"message":"ok","data":{"id":"L-...","channel":"inbox","status":"sent"}}
```

### AI 对话（ai-gateway，SSE 流式）

```bash
curl -N http://localhost:8080/api/v1/ai/chat/completions \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"model":"test-model","stream":true,"messages":[{"role":"user","content":"介绍玄武平台"}]}'
```
```
data: {"choices":[{"delta":{"content":"玄武"}}]}
data: {"choices":[{"delta":{"content":"平台是..."}}]}
data: {"choices":[],"usage":{"total_tokens":128}}
data: [DONE]
```

### 语义检索（rag）

```bash
curl -X POST http://localhost:8080/api/v1/rag/kbs/{kbID}/search \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"query":"如何快速开始","top_k":3}'
```
```json
{"code":0,"message":"ok","data":{"hits":[{"doc_title":"快速开始","score":0.83,"text":"三步完成快速开始..."}]}}
```

## 4. SSE 事件流

### AI 对话（`/ai/chat/completions`，`stream:true`）

`text/event-stream`，逐块 `data: {...}`（OpenAI 兼容 delta 格式），末尾 usage 块（配额记账）与 `data: [DONE]`。内容安全命中 reject 时下发 `data: {"error":{"code":"safety.output_rejected",...}}` 后断流（详见 pkg/ai/gateway/safety.go 注释与 docs）。

### Agent 运行（`/agents/runs/{id}/stream`，七类事件）

| 事件 | 载荷 | 说明 |
|---|---|---|
| `run.started` | run 快照 | 运行开始 |
| `llm.delta` | `{content}` | 模型输出增量 |
| `llm.done` | usage | 单轮完成 |
| `tool.call` | `{name, args}` | 工具调用发起 |
| `tool.result` | `{name, result}` | 工具返回 |
| `run.finished` | 最终消息 | 终态（含 max rounds 截断） |
| `run.failed` | `{error}` | 异常终态 |

事件格式：`data: {"type":"<事件>","data":{...}}`；断线可 `GET /agents/runs/{id}/messages` 拉取全量回放（消息持久化）。

## 5. OpenAPI 3 评估

**结论：暂不随本文档产出完整 spec，演进路径如下。**

- 端点机器清单已具备：本文档由提取脚本生成（模块路由注册 → 方法/路径/权限矩阵），同一脚本可低成本输出 OpenAPI 3 的 paths 骨架（方法+权限+空 schema）；
- 完整 spec 的成本在逐端点请求/响应 JSON Schema——22 模块约 186 端点，人工维护必然漂移；正确路径是「契约先行」（docs/04 预留的 mod-codegen：由 OpenAPI 契约生成 handler 脚手架，spec 成为源头而非产物）；
- 近期演进：提取脚本入库（scripts/api_extract.py）+ CI 校验「文档端点数 == 路由注册数」防漂移；mod-codegen 落地时切换为 spec 驱动。
