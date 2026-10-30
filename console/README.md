# /console — 前端控制台（L3）

**状态：占位，M3 里程碑启动。**

技术选型已定（详见 [docs/04-frontend-ui-evaluation.md](../docs/04-frontend-ui-evaluation.md)）：

- Vue 3 + TypeScript + Vite + **Element Plus**
- 构建产物 `go:embed` 嵌入 Go 二进制，单文件交付；全部静态资源离线打包（私有化无外网场景）
- 路由/菜单/权限点由后端模块经 `ui-contract` 协议动态下发
- 预留社区版 React Console 的契约生态位

M3 之前本目录仅存放契约文档与设计稿。
