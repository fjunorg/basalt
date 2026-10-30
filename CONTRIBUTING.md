# 贡献指南

感谢关注 Basalt！本指南说明如何参与贡献。

## 环境要求

- Go ≥ 1.23（`go version` 确认）
- 本地验证一键化：`make test`（build + vet + test）

## 分支与提交

- trunk-based 开发：短生命周期分支，频繁向 `main` 合并
- 提交信息遵循 [Conventional Commits](https://www.conventionalcommits.org/zh-hans/)（`feat:` / `fix:` / `docs:` / `refactor:` ...）
- **DCO 签名必需**：提交时加 `-s`（`git commit -s`），表明你有权以本项目协议提交该贡献
- 企业雇员的大额贡献需签署企业 CLA（CLA Assistant 流程，M1 上线前以 issue 协调）

## PR 门禁

CI 必须全部通过：

1. `go build ./...`、`go vet ./...`、`go test ./...`（多 Go 版本矩阵）
2. 交叉编译矩阵：linux/amd64、linux/arm64（鲲鹏）、linux/loong64（龙芯）、darwin/arm64
3. golangci-lint
4. 新增模块不得直接 import 其他模块实现包（模块间仅通过接口与 Registry 协作，架构守护原则见 docs/01）

## 新模块提案

先提 RFC（`docs/rfcs/` 目录，模板：问题 / 方案 / 替代方案 / 影响面），经一名 maintainer 批准后开工。垂直业务模块（商城/CRM/ERP 等）不在主线范围，走生态插件。

## 依赖引入红线

- 仅接受 Apache-2.0 / MIT / BSD 等宽松协议依赖，GPL/AGPL/SSPL 一律拒绝
- 每次新增依赖必须同步更新根目录 NOTICE 的第三方清单
- 信创交付优先标准库与轻依赖（见 docs/01 L0 层选型说明）

## 行为准则

参与即表示同意遵守 [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md)。
