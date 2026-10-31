# Changelog

本项目遵循 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，版本语义 [SemVer](https://semver.org/)。

## [Unreleased]

## [1.7.0] - 2026-10-31

P1a 效率批次（竞品 Gap 分析第二批）：seed 演示数据集、xlsx 导入导出、S3 多云存储、工作流第一批六项审批操作、操作/API 日志与运行监控。

### 新增

- **basalt seed --demo（演示数据集初始化）**：组织三级部门树+岗位、数据
  权限各档角色、跨部门用户×6、字典×3 类型、公告×3 状态、示例租户+套餐
  绑定、财务三态单据+发票+预算+付款、公文合同多状态、知识库+文本文档×2
  （向量化跳过——seed 零外部依赖，检索就绪度以切片为准）、Agent 定义×2、
  通知模板三渠道、演示两步审批流；幂等（二次执行数据量不变）；安全门：
  **prod 环境拒绝执行**（--force 显式覆盖）、仅 sqlite 存储（memory 提示）。
  **演示账号统一口令 `Demo@2026`**（demo-ceo / demo-dev / demo-sale /
  demo-viewer / demo-fin / demo-tenant，bcrypt MinCost 弱成本仅限演示）。
- **kernel xlsx 轻量读写库**：标准库 zip+xml 实现，零新依赖（选型：不自引
  excelize，避免 ~1.5MB 增量与 10 个传递依赖）；覆盖表头+行集子集，
  Write 生成最小合法 OOXML（Excel/WPS 兼容），Read 解析 inlineStr/
  sharedStrings/数字三形态；非目标：样式/多 sheet/图表。
- **Excel 导入导出**：identity 用户导入导出（xlsx）；finance/contract
  列表导出（金额元两位小数）。
- **file S3 兼容多云存储驱动**：AWS Signature V4 自研实现（约 300 行
  标准库，AWS 官方测试向量逐字节对齐，零 SDK 依赖）；PUT/GET/DELETE/
  ListObjectsV2/预签名 GET（15min TTL）；path-style（MinIO/自建）与
  virtual-host（OSS/COS）双风格；prefix 命名空间隔离；**混合存量路由**
  ——Meta.Storage 标记后端（local|s3，存量空值兼容），上传恒写主存储、
  下载/删除按标记路由，切换 S3 后旧本地文件继续可访问可删除（不强制
  迁移）；`BASALT_FILE_S3_ENDPOINT` 配置即启用（默认本地不变）；
  .env.example 补录 S3 七变量（共 25 项）；兼容矩阵与租户边界策略见
  docs/file-s3.md（OSS/COS S3 兼容模式同驱动，七牛等非 S3 协议留
  Storage 接口缝）。
- **workflow 第一批六项审批操作**（Mem/GORM/Routed 三实现语义一致）：
  依次审批 sequential（approvers 按序逐人，非轮到者 403）、抄送 cc
  （追加去重幂等，被抄送只读知见）、委派 delegate-act（一次性代办，
  本步本轮有效；与既有转办 delegate 永久转移语义对照见契约 §4）、
  加签 addsign（before/after 双向，1–5 人，步骤上限 20）、驳回至已过
  节点 reject-to（Round 轮次机制：旧轮动作失效、历史完整保留、重走
  重审）；实例创建支持可选 steps 多步直发（向后兼容）。
- **oplog（操作/API 日志与运行监控 v0.1）**：audit 链（合规取证）之外的
  日常排障查询面——异步采集不阻塞请求（非阻塞 chan 1024 缓冲、满则
  丢弃计 dropped、后台 100ms/64 条批量聚合、Stop 优雅排空）；数据保留
  默认内存环形缓冲（10000 条滚动覆盖，~2MB 上界），`BASALT_OPLOG_PERSIST=sqlite`
  时落库（operation_logs 表，运维自管清理）；用户字段经完整验签还原
  （可信）；采样策略 `BASALT_OPLOG_MODE`（all 默认 / write 仅变更 /
  off 关闭采集但查询端点可用）；GET /api/v1/oplog/logs（分页+
  module/user/时间窗/状态码过滤，权限点 oplog:read）与
  GET /api/v1/oplog/metrics（90 秒环形秒桶：QPS/错误率）。
- **Console（30 → 32 视图）**：系统日志与运行监控两页、用户导入导出与
  列表导出及存储后端展示配套、审批中心六项操作 UI（契约
  workflow-api-contract v1，含审批轮次展示修正）；菜单种子补两页入口。
- **serve 装配扩至 22 模块**（+oplog）。

### 修复

- notify：邮件/站内信标题变量渲染缺失。
- workflow：Action.StepIndex 历史缺陷——旧实现记推进后索引，多步+
  回退/代审组合必现 already acted 误判；改为动作发生时索引。

### 变更

- 二进制默认版本基线 bump 至 1.6.0（随发布 ldflags 覆盖）。

## [1.6.0] - 2026-10-31

P0 平台完备性批次（竞品 Gap 分析 `deliverables/09-competitive-gap-analysis.md` 第一批 P0 落地）：通知中心、部门级数据权限、OAuth2/SSO、动态菜单、job cron、在线用户、租户套餐、错误码管理。

### 新增

- **notify（通知中心 v0.1）**：短信/邮件/站内信三渠道——模板（三渠道共用，
  `{{var}}` 渲染纯函数）、SMTP 账号与短信渠道配置（凭据经环境变量注入
  不落库）、发送日志（sent/failed 双态 + 分页）、站内信独立收件箱（定向
  到人含已读态；notice 保持全员公告语义，选型分离）；Notifier 适配器
  抽象：SMTP 标准库实现零新依赖、阿里云短信 Dysmsapi RPC V1 自研签名、
  腾讯云留接口缝；失败不自动重试（明确失败 + 手动重发语义）；发送事件
  入审计链；权限点 notify:manage/list。
- **部门级数据权限（dataperm）**：pkg/kernel/dataperm Scope 纯模型 +
  ctx 注入（零业务依赖）；identity 角色新增 DataScope 五档
  （all/dept/dept_and_below/self/custom，多角色并集取宽，解析失败
  fail-closed 降 self）；声明式查询注入范式接入三模块——org（部门树
  裁剪）、finance（部门+申请人双维）、document（拟稿部门裁剪）；语义
  分层：租户隔离=硬边界（先裁剪）、数据权限=角色软范围（后裁剪），
  交集叠加正交不混淆；15 用例含五档×双模块矩阵与 sqlite 重启恢复。
- **OAuth2/SSO（identity 扩展）**：OAuth2 2.1 核心——授权码模式
  （PKCE S256 公共客户端强制、code 单次使用短时效、redirect_uri 逐字节
  双点校验）、客户端凭证模式（机器身份）、轮换式刷新（重放检测吊销
  整条授权链、scope 仅可收窄）；SSO 应用管理（CRUD + secret 一次性
  下发/轮换，SHA-256 哈希存储）；三方登录：SocialProvider 接口缝 +
  钉钉实装（state HMAC 防伪、自动注册/复用）+ 企微留缝；token 复用
  TokenIssuer 同密钥体系；`secure --check` 新增 sso-social-secrets
  检查项（六项扩至七项）。
- **menu（动态菜单 v0.1）**：菜单实体（name/path/icon/perm/parent/
  sort/enabled）CRUD（父级成环校验、删除保护 409）+ 角色绑定 +
  `/api/v1/menus/me` 按权限过滤菜单树下发（含 perms 快照与 source
  标识，过滤谓词复用 identity 通配规则）；API 下发为空时前端回退
  静态映射（契约 deliverables/menu-api-contract.md）；种子将现有页面
  静态结构全量结构化（工作台/系统管理/系统监控三分组 IA，幂等播种，
  已播种库不重播）；菜单管理页本身不入种子（前端固定渲染防死锁）。
- **job cron 增强**：自研标准 5 段 cron 解析器（约 200 行零新依赖；
  dom/dow Vixie OR 语义，Next 逐分钟扫描上限 4 年）；任务级二选一
  IntervalSec/CronExpr（cron 优先），错过不补跑语义两态一致；在线动态
  HTTP 回调任务（job:manage 权限，目标 http(s) 校验、30s 超时、重启
  LoadDynamic 恢复并重算 NextRun，内置任务不可删）；执行日志持久化 +
  分页查询 API；runs API 升级 {total,offset,items} 包装。
- **在线用户（identity 扩展）**：会话注册表（登录登记/登出单会话吊销/
  过期惰性清理，令牌指纹主键不存原始 token）；踢下线选型：用户级
  kicked_at 时间戳（O(1)、全端立即生效，优于短 TTL+refresh 与黑名单）；
  AuthMiddleware 吊销检查器可选装配（未注入保持纯无状态向后兼容）；
  端点 GET /sessions、DELETE /sessions/user/{id}、POST /logout。
- **tenantpkg（租户套餐 v0.1）**：套餐 CRUD（菜单/权限集合模板）+
  租户绑定/解绑 + 绑定计数/在用禁删；裁剪生效：identity Facade
  SetPermFilter 钩子单点交集过滤，menu /me 菜单树复用 Can 自动联动。
- **errcode（错误码管理 v0.1）**：错误码注册表（模块/码/描述）CRUD +
  现有各模块错误码盘点入库种子（合理抽样）。
- **Console（22 → 30 视图）**：新增通知中心、SSO 应用管理/授权确认/
  钉钉回调、菜单管理、在线用户、租户套餐、错误码、job 动态任务与执行
  日志增强页；**动态菜单改造**——菜单改后端下发驱动、API 空回退静态
  映射；交互层深度对齐 Semi Design（semi-interactions.css：motion
  节奏、对话框、深色 Toast，EP 默认定制覆盖约 95%，零模板/逻辑改动）。
- **serve 装配扩至 21 模块**（sqlite/memory 两形态一致，serve 层全量
  装配冒烟测试锁定）：dedicated-db 路由接入扩至 14 个模块（+notify/
  menu，RoutedNotifyStore/RoutedMenuStore 请求级路由）；tenantpkg/
  errcode 为平台级数据固定主库（与 identity/tenant/audit/quota 同
  边界，见 docs/tenant-isolation.md §2.3）；postgres/openGauss 分支
  仍为显式 pending。

### 修复

- **发版阻断**：serve sqlite 分支缺 notify/menu/tenantpkg/errcode 四模块
  接线（var 零值进注册列表，sqlite 形态启动 panic）——补接线并新增
  cmd/basalt/serve_smoke_test 永久锁定 serve 层 21 模块全装配盲区
  （此前 module_stores_test 全装配不经 serve 层故未拦截）。
- v1.4.0 终验三项遗留：BASALT_CRYPTO_ALGO 中途切换后审计链核验改
  多算法逐段核验（历史算法段不再误报断链）；RoutedStore 空视图
  data:null 统一兜底为 []（38 处 List 查询）；SM2 私钥运维文档化。
- JWT 无 jti 缺陷（同秒同用户重复签发令牌相同，单会话登出误伤全部
  会话）——签发增加 128 位随机 jti；RevokedAt 指针化修复 sqlite
  零值时间戳判定失效。
- job 测试 race 模式两处间歇性 flaky（tick 分钟边界竞态断言、
  动态任务生命周期用例 List() 二次调用索引错位）——修复后
  -race ×12 连跑全过（936f9a8）。

### 战略决策记录

- **支付系统维持不自研**：延续 docs/03 v0.1「商城/CRM/ERP/支付明确
  不做」边界（B 端收款经生态集成承接），本次竞品 Gap 分析复核确认。
- **行业业务域生态承接**：Mall/ERP/WMS/CRM/CMS/MES/HRM/PMS/IoT 等
  芋道全家桶域不自研，以模块插件机制 + 生态接入方式承接（竞品 Gap
  报告 §3 P2 建议）。

### 变更

- 二进制默认版本基线 bump 至 1.5.0（随发布 ldflags 覆盖）。

## [1.5.0] - 2026-10-30

Admin Console 视觉风格对齐 Semi Design——纯样式变更，技术栈保持 Element Plus（非组件库迁移）。

### 新增

- **Semi 风格 token 单一来源**（`console/src/styles/semi-tokens.css`，两层
  结构）：① `--semi-*` 原生 token 层（色板/语义色/字体栈/圆角/阴影，逐条
  注释真源 semi-theme-default 对照）；② `--el-*` 变量映射层（primary
  #0064FA 色系、四级文本色、极浅描边、圆角 3/6/12px、elevated 阴影、Semi
  中文字体栈；Element Plus hover 浅化 → Semi 深化语义对齐）。

### 变更

- **Console 全局视觉对齐 Semi Design 风格**（技术栈保持 Element Plus）：
  侧边栏浅色化（白底 + 右边框 + 块状高亮选中项，移除深色硬编码）、主区与
  页面底色 #F9F9F9、登录页改浅灰底白卡片大圆角；全仓 6 种硬编码色全部
  token 化，22 个页面共享全局 token 层，无新旧风格混杂；项目无既有暗色
  模式，本次不新增。纯样式改动：权限守卫/SSE/表单交互逻辑零触碰；主 CSS
  9.55KB → 13.77KB（+4.22KB 为 token 层），dist 总体积 1104KB → 1108KB。
- 二进制默认版本基线 bump 至 1.4.0（随发布 ldflags 覆盖）。

## [1.4.0] - 2026-10-30

租户 dedicated-db 全量路由、国密 SM2 套件（Provider + 审计链尾签名）、等保基线自查命令与 Console 租户隔离管理。

### 新增

- **租户 dedicated-db 全量路由**：`BASALT_TENANT_ROUTING=on` 时全部 12 个
  业务模块（org / dict / sysconfig / notice / file（元数据）/ job /
  finance / document / contract / workflow（实例+定义）/ rag / agent）经
  RoutedStores 请求级路由到租户专属库。接入边界（不路由，理由详见
  docs/tenant-isolation.md §2.3）：平台级 identity/tenant/audit/quota
  固定主库；aisafety Engine 为进程级快照，随按租户策略演进后接入；
  file 文件本体在共享磁盘（物理隔离随对象存储接入）。隔离模式与
  db_ref 仅创建时设置、PATCH 不支持在线切换（受控迁移流程）。
- **国密 SM2 套件**：
  - crypto SM2 签名 Provider（GB/T 32918.2 官方向量验证；emmansun/gmsm）：
    `BASALT_CRYPTO_ALGO=sm2` 为国密套件（SM3 摘要 + SM2 签名），私钥
    `BASALT_SM2_PRIVATE_KEY`、用户标识 `BASALT_SM2_UID`（默认国标值）
  - **审计链尾 SM2 签名**：哈希链防单点篡改，链尾签名防持有全部数据的
    攻击者整库重算伪造历史（私钥不入库，重算者无法对历史时点签名）；
    SignHead / VerifyHeadSignatures / ListHeadSignatures，公钥随签名
    持久化（验签不依赖当前私钥）；ChainSigner 接口缝预留 HSM；HTTP
    端点 POST/GET `/api/v1/audit/chain/signatures`（+verify），新权限点
    `audit:sign`（签名与读分离）；`BASALT_SM2_CHAIN_SIGN=on` 启用
    （默认 off 行为不变），私钥未配置时进程内生成并 Warn 日志
- **basalt secure --check（等保基线自查）**：六项检查——凭据默认值、
  加密算法合规（含链签名开关）、上传目录可写与路径安全、审计链完整性
  （复用 audit verify）、租户隔离开关、密钥明文暴露面；三态判定
  PASS/WARN/FAIL，逐项输出与整改建议。**退出码约定：全部通过 0 /
  存在 WARN 1 / 存在 FAIL 2**（CI 可直接消费）
- **Console 租户隔离管理**：租户页支持隔离模式（shared-schema /
  dedicated-db）与 db_ref 库引用配置（dedicated 模式必填，界面明示
  凭据经环境变量注入）

### 修复

- agent：SSE 异常终态补发 run.failed 事件（契约 §4.1 对齐）

### 变更

- .env.example 增至 20 项：补录 SM2 三变量（PRIVATE_KEY/UID/CHAIN_SIGN）
  与租户隔离变量（BASALT_TENANT_ROUTING、BASALT_TENANT_DSN_<REF> 约定）。
- 二进制默认版本基线 bump 至 1.3.0（随发布 ldflags 覆盖）。

## [1.3.0] - 2026-10-30

Agent 编排落地、租户 dedicated-db 数据库隔离实装（示范接入）与 Admin Console Agent 控制台。

### 新增

- **agent（agent v0.1）**：L2 轻量 Agent 编排模块——Agent 定义 → 运行 →
  工具调用循环 → SSE 运行事件。自研轻量循环（系统提示 → LLM → 工具调用
  → 结果回填 → 再 LLM → 终答），不引入 Eino（信创依赖树最小化约束），
  ToolRegistry 接口缝预留生态对接位；LLM 调用一律经 ai-gateway 统一层
  （配额/内容安全审核/记账/审计全链路），ai-safety 命中落 safety_rejected
  终态并经 SSE safety.rejected 事件下发；内置工具 RagSearchTool（RAG
  检索联动，HTTP 工具等后续扩展）；GORM 持久化（定义与运行记录）+ 独立单测。
- **Admin Console Agent 控制台**：Agent 定义管理（CRUD 覆盖全部定义字段，
  工具多选自 /tools 注册表，写操作按钮级 agent:manage 权限点）与 SSE
  运行页（七类运行事件：run.started / tool.call / tool.result 成对可观测 /
  llm.delta 按序拼接 / run.completed / run.failed / safety.rejected），
  按 agent-api-contract 契约 v1 对接。
- **租户 dedicated-db 数据库隔离（部分实装）**：TenantRouter + 路由型
  store 机制落地——租户记录仅存库引用名（DBRef），DSN 凭据经
  BASALT_TENANT_DSN_<REF> 环境变量注入、不落库；租户库首访懒初始化
  （复用 OpenDB 全量 AutoMigrate，建库即建表）；DSN 未配置 / 连接或迁移
  失败明确报错，绝不静默回退主库（防隔离破坏）；重启后 Router 缓存冷启动、
  按需重连。**当前端到端接入范围为 dict 模块**（BASALT_TENANT_ROUTING=on
  开启，默认关闭维持现状），其余模块接入模式与运维手册见
  docs/tenant-isolation.md；shared-db 模式经评估暂不实装（评估结论见
  该文档 §4）。tenant 模块新增库引用字段与 API 校验，identity 导出
  上下文注入接口。
- **serve 装配扩至 17 模块**：agent 双分支装配（sqlite GORM / memory
  降级）+ 工具注册表与 gateway 注入。

### 变更

- .env.example 补录 BASALT_AI_EMBED_MODEL（v1.2.0 rag 实读变量，共 16 项）。
- 二进制默认版本基线 bump 至 1.2.0（随发布 ldflags 覆盖）。

## [1.2.0] - 2026-10-30

AI 基础设施双模块（内容安全、RAG）落地，Admin Console 前端债（TS 迁移、按需导入）清偿。

### 新增

- **ai-safety（aisafety v0.1）**：L2 AI 内容安全模块（docs/03 P0 合规
  空白项兑现）——敏感词库管理、按租户可配审核策略、AI 调用链内容审核；
  三类命中动作：reject（输入侧 422 拦截 / 输出侧断流）、replace（命中词
  替换放行，替换词可配）、mark（观察模式，命中记入审计链）；策略回落链：
  租户策略 → 平台默认 → 内置默认 reject（默认从严）；GORM 持久化并入
  OpenDB 迁移；独立单测。
- **ai-gateway 内容安全集成**：审核接口经 Inspector 注入（ai-safety
  消费；未启用时零开销直通），输入/输出侧命中按租户策略拦截/替换/标记。
- **RAG（rag v0.1）**：L2 知识库与检索增强模块——知识库 → 文档 → 切片
  → 向量化 → 相似检索完整管道。向量存储起步方案：embedding 以 blob 落
  sqlite（与业务数据同一恢复面，重启后检索直接可用），检索在内存做余弦
  相似度排序；专用向量库（milvus 等）后续经 VectorSearcher 接口注入，
  模块层零改动。向量化客户端 OpenAI 兼容 /v1/embeddings，复用
  BASALT_AI_PROVIDER_* 环境变量族（向量子模型经 BASALT_AI_EMBED_MODEL
  指定，未配置时向量化步骤明确报错而非静默跳过）。文档登记双通道：直接
  文本输入，或引用 file 模块上传文件（FileResolver 抽象，file 包零改动）；
  GORM 持久化 + 独立单测。
- **serve 装配扩至 16 模块**：aisafety/rag 接入 sqlite GORM 装配，
  memory 分支保留零依赖降级路径。
- **Admin Console TypeScript 全量迁移**：src 全量 TS（tsconfig strict +
  noUnused*，20 个视图行类型按实际消费字段定义，无 any 兜底），构建链路
  接入 vue-tsc --noEmit 类型检查；RouteMeta 声明合并（title/perms）。
- **Element Plus 按需导入**：unplugin-vue-components +
  ElementPlusResolver（importStyle: css）；命令式 API（ElMessage/
  ElMessageBox）经 src/element.ts 集中导出并手动注入样式；v-loading
  经 ElLoading 插件注册；ElConfigProvider 注入中文 locale；
  components.d.ts 入库。体积实测（构建输出）：主 chunk gzip
  350.63KB → 64.13KB（-81.7%），dist 总体积 1544KB → 1072KB
  （-30.6%），主 CSS 361.41KB → 9.55KB。

### 变更

- 二进制默认版本注入基线 bump 至 1.1.0（main.version，随发布 ldflags 覆盖）。

## [1.1.0] - 2026-10-30

Admin Console 交付、平台业务模块群（9 个）实装、全模块 GORM 持久化与工程资产兑现。

### 新增

- **Admin Console v0.1**（Vue3 + Element Plus，构建产物 go:embed 单二进制）：
  登录、概览（身份/用量/链校验）、用户/角色/租户管理、审批中心（发起/待办/
  通过/驳回）、审计链查看（模块过滤 + 完整性徽章）、AI 控制台（SSE 流式
  对话 + 账期用量环图）、工作流定义页，以及 9 个业务模块的管理页
  （组织/字典/参数/通知/文件/任务/财务/公文/合同）。hash 路由 + 相对
  资源路径，任意挂载路径可用。
- **Console 菜单与路由权限显隐**：页面级权限映射（路由 `meta.perms`，
  满足任一权限点即可进入），菜单显隐与路由守卫共用同一映射；
  无权限访问落 403 引导页（Forbidden）。
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
- **workflow v0.2 持久化**：审批实例 Store + 流程定义 DefStore（GORM），
  跨重启恢复经测试验证；状态机重构为导出纯函数（行为不变）。
- **持久化与装配（stores/serve）**：
  - gormstore.Stores 聚合工厂新增 9 模块 store 工厂 + workflow 双 store，
    与既有 store 共用 OpenDB 迁移路径（达梦/openGauss 等 OpenDB 注入同源）
  - serve sqlite 装配分支自此为**全部 14 个模块**注入 GORM store
    （memory 分支保留零依赖降级路径）；新增 postgres/opengauss 存储分支
    占位（驱动接线随后续版本，当前显式报错并提示回退 sqlite/memory）
  - store 语义、重启续存与全装配冒烟测试（gormstore/module_stores_test）
- **工程与部署资产**：
  - CI（.github/workflows/ci.yml）：多 Go 版本测试矩阵（vet + test -race）、
    golangci-lint（.golangci.yml 标准五件套；生产代码存量违规已清偿、
    豁免清零，仅保留 Close 惯例与测试助手风格两类风格性豁免）、
    govulncheck、信创交叉编译矩阵（linux/amd64、arm64 鲲鹏、loong64 龙芯，
    产物以 artifact 发布）
  - Dockerfile：三阶段构建（console 前端 → go:embed Go 构建 → alpine 运行时），
    非 root 用户、/healthz 健康检查、/var/lib/basalt 持久卷
  - .env.example（15 项环境变量模板与用途说明）、docs/deploy.md 部署指南
    （单二进制与容器两条路径）、.gitignore 与 .dockerignore
- **测试**：9 个业务模块补齐独立单测（9 文件 2128 行，handler 层契约 +
  持久化重启恢复用例）；workflow 状态机语义与跨重启恢复测试

### 修复

- org：部门创建缺名称时返回 400 而非 200 空响应（补入参校验，
  与 dict/sysconfig 校验风格对齐）

### 已知限制（Console）

- JS 版（TS 迁移待办）；Element Plus 全量引入（主 chunk gzip ~350KB，
  按需导入优化待办）。

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
