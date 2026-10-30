# /console — Admin Console（L3 前端控制台）

**状态：v0.1 已交付（Unreleased，随下一版本发布）。** 构建产物 `dist/` 已入库并经 `go:embed` 嵌入二进制。

## 技术栈

- Vue 3 + Vite + **Element Plus** + vue-router（**当前为 JS 实现**）
- 构建产物 `go:embed` 嵌入 Go 二进制，单文件交付；全部静态资源离线打包（私有化无外网场景）
- hash 路由 + 相对资源路径，任意挂载路径可用
- 技术选型决策记录见 [docs/04-frontend-ui-evaluation.md](../docs/04-frontend-ui-evaluation.md)；预留社区版 React Console 的契约生态位

## 页面清单（src/views，共 21 个）

- **核心**：登录、概览（身份/用量/链校验）、布局
- **身份与租户**：用户、角色、租户
- **审批与审计**：审批中心（发起/待办/通过/驳回）、工作流定义、审计链查看（模块过滤 + 完整性徽章）
- **业务模块**：组织、字典、参数、通知、文件、任务、财务、公文、合同
- **AI**：AI 控制台（SSE 流式对话 + 账期用量环图）

## 构建与更新

`console/dist` 为**有意提交**的构建产物：`go:embed`（见 `console/embed.go`）依赖它，保证 `go build` 即得带 UI 的单文件二进制。修改 `console/` 源码后需重新构建并随源码一并提交：

```bash
cd console && npm install && npm run build
```

## 已知限制（v0.1）

- 未迁移 TypeScript（TS 迁移待办，当前为 JS 实现）
- Element Plus 全量引入，主 chunk gzip 约 350KB（按需导入优化待办）
- 菜单未按权限点动态显隐（权限收敛待办，与 identity 权限中间件联动规划中）
