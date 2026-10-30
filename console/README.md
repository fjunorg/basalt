# /console — Admin Console（L3 前端控制台）

**状态：v0.1 已交付（随 v1.1.0 发布；v1.2.0 完成 TypeScript 全量迁移与 Element Plus 按需导入）。** 构建产物 `dist/` 已入库并经 `go:embed` 嵌入二进制。

## 技术栈

- Vue 3 + **TypeScript**（tsconfig strict + noUnused*，构建链路含 `vue-tsc --noEmit` 类型检查，视图行类型按实际消费字段定义、无 any 兜底）+ Vite + **Element Plus（按需导入）** + vue-router
- Element Plus 经 unplugin-vue-components + ElementPlusResolver 按需注入（importStyle: css）；命令式 API（ElMessage/ElMessageBox）经 `src/element.ts` 集中导出并手动注入样式；components.d.ts 随库入库
- 构建产物 `go:embed` 嵌入 Go 二进制，单文件交付；全部静态资源离线打包（私有化无外网场景）
- hash 路由 + 相对资源路径，任意挂载路径可用
- **菜单与路由按权限点动态显隐**：页面级权限映射（路由 `meta.perms`，满足任一权限点即可进入），菜单显隐与路由守卫共用同一映射；无权限访问落 403 引导页（Forbidden）
- 技术选型决策记录见 [docs/04-frontend-ui-evaluation.md](../docs/04-frontend-ui-evaluation.md)；预留社区版 React Console 的契约生态位

### 按需导入体积收益（构建输出实测）

| 指标 | 基线（JS + 全量引入） | TS 迁移后 | 按需导入后 |
|------|----------------------|-----------|------------|
| 主 chunk gzip | 350.60KB | 350.63KB（编译期，无变化） | **64.13KB（-81.7%）** |
| dist 总体积 | 1544KB | — | **1072KB（-30.6%）** |
| 主 CSS | 361.41KB | — | **9.55KB** |

## 页面清单（src/views，共 20 个）

- **核心**：登录、概览（身份/用量/链校验）、布局、403 引导页（Forbidden）
- **身份与租户**：用户、角色、租户
- **审批与审计**：审批中心（发起/待办/通过/驳回）、工作流定义、审计链查看（模块过滤 + 完整性徽章）
- **业务模块**：组织、字典、参数、通知、文件、任务、财务、公文、合同
- **AI**：AI 控制台（SSE 流式对话 + 账期用量环图）

## 构建与更新

`console/dist` 为**有意提交**的构建产物：`go:embed`（见 `console/embed.go`）依赖它，保证 `go build` 即得带 UI 的单文件二进制。修改 `console/` 源码后需重新构建并随源码一并提交：

```bash
cd console && npm install && npm run build
```

## 已知限制

- v1.1.0 披露的两项前端债（JS 未迁移 TS、Element Plus 全量引入 ~350KB）已于 v1.2.0 全部清偿，当前无已知限制。
