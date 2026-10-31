# /console — Admin Console（L3 前端控制台）

**状态：v0.1 已交付（随 v1.1.0 发布；v1.2.0 TS 迁移与按需导入；v1.3.0 Agent 控制台；v1.4.0 租户隔离管理；v1.5.0 视觉对齐 Semi Design；v1.6.0 P0 平台完备性八页 + 动态菜单；v1.7.0 日志监控两页 + 审批六操作 + 导入导出）。** 构建产物 `dist/` 已入库并经 `go:embed` 嵌入二进制。

## 视觉风格

**Semi Design 风格，技术栈保持 Element Plus（非组件库迁移）**：v1.5.0 起经 `src/styles/semi-tokens.css` 的 **token 定制机制**对齐——该文件为 token 单一来源，两层结构：`--semi-*` 原生 token 层（逐条注释 semi-theme-default 真源对照）+ `--el-*` 变量映射层（主色 #0064FA 色系、文本/描边/圆角/阴影/中文字体栈，并做 EP hover→Semi 深化语义对齐）。v1.6.0 追加 `src/styles/semi-interactions.css` 交互层深化（motion 节奏、对话框、深色 Toast，EP 默认定制覆盖约 95%）。全仓硬编码色已全部 token 化；无暗色模式。调整视觉时只改 token/交互层样式文件，不逐页改样式。

## 技术栈

- Vue 3 + **TypeScript**（tsconfig strict + noUnused*，构建链路含 `vue-tsc --noEmit` 类型检查，视图行类型按实际消费字段定义、无 any 兜底）+ Vite + **Element Plus（按需导入）** + vue-router
- Element Plus 经 unplugin-vue-components + ElementPlusResolver 按需注入（importStyle: css）；命令式 API（ElMessage/ElMessageBox）经 `src/element.ts` 集中导出并手动注入样式；components.d.ts 随库入库
- 构建产物 `go:embed` 嵌入 Go 二进制，单文件交付；全部静态资源离线打包（私有化无外网场景）
- hash 路由 + 相对资源路径，任意挂载路径可用
- **动态菜单 + 权限显隐**（v1.6.0 起）：菜单改**后端下发驱动**——`GET /api/v1/menus/me` 按当前用户角色权限过滤下发菜单树（含 perms 快照），API 下发为空时自动**回退静态映射**（`meta.perms`，满足任一权限点即可进入）；菜单显隐与路由守卫共用同一权限判定；无权限访问落 403 引导页（Forbidden）；菜单管理页自身固定渲染不入动态菜单（防死锁）
- 技术选型决策记录见 [docs/04-frontend-ui-evaluation.md](../docs/04-frontend-ui-evaluation.md)；预留社区版 React Console 的契约生态位

### 按需导入体积收益（构建输出实测）

| 指标 | 基线（JS + 全量引入） | TS 迁移后 | 按需导入后 |
|------|----------------------|-----------|------------|
| 主 chunk gzip | 350.60KB | 350.63KB（编译期，无变化） | **64.13KB（-81.7%）** |
| dist 总体积 | 1544KB | — | **1072KB（-30.6%）** |
| 主 CSS | 361.41KB | — | **9.55KB** |

## 页面清单（src/views，共 32 个）

- **核心**：登录、概览（身份/用量/链校验）、布局、403 引导页（Forbidden）
- **身份与租户**：用户（xlsx 导入导出）、角色、租户（隔离模式与 db_ref）、租户套餐
- **平台治理**：菜单管理、在线用户（踢下线）、错误码、通知中心（模板/渠道/日志/收件箱）、SSO 应用管理、SSO 授权确认、钉钉登录回调、系统日志（oplog 分页过滤）、运行监控（QPS/错误率）
- **审批与审计**：审批中心（发起/待办/通过/驳回 + **依次审批/抄送/转办/委派/加签/驳回至节点六操作**，轮次展示）、工作流定义、审计链查看（模块过滤 + 完整性徽章）
- **业务模块**：组织、字典、参数、通知公告、文件（存储后端展示，S3/本地）、任务（cron 动态任务与执行日志）、财务（列表导出）、公文、合同（列表导出）
- **AI**：AI 控制台（SSE 流式对话 + 账期用量环图）、Agent 定义管理（agent:manage）、Agent 运行页（SSE 七类事件）

## 构建与更新

`console/dist` 为**有意提交**的构建产物：`go:embed`（见 `console/embed.go`）依赖它，保证 `go build` 即得带 UI 的单文件二进制。修改 `console/` 源码后需重新构建并随源码一并提交：

```bash
cd console && npm install && npm run build
```

## 已知限制

- v1.1.0 披露的两项前端债（JS 未迁移 TS、Element Plus 全量引入 ~350KB）已于 v1.2.0 全部清偿，当前无已知限制。
