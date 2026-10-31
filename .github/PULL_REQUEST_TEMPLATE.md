<!-- 
Pull Request 检查单。新模块 / 架构变更类 PR 请先确认 RFC 已获 maintainer 批准（CONTRIBUTING.md）。
提交前请完成以下勾选；不适用项请标注 N/A 并简述原因。
-->

## 变更说明

<!-- 做了什么、为什么；关联 issue（如 Closes #123） -->

## 检查单

- [ ] 提交信息遵循 Conventional Commits，且**每条提交带 DCO 签名**（`git commit -s`）
- [ ] 本地验证通过：`go build ./...` + `go vet ./...` + `go test ./...`
- [ ] 新增/修改的业务逻辑有对应测试覆盖（说明测试位置）
- [ ] 未引入新依赖；若引入：许可证为 Apache-2.0/MIT/BSD 宽松类，且已同步更新 NOTICE 第三方清单
- [ ] 模块间未新增直接 import 其他模块实现包（仅经接口与 Registry 协作）
- [ ] 用户可见行为变更已更新 CHANGELOG.md（Unreleased 段）
- [ ] 配置/环境变量变更已同步 .env.example
- [ ] 无硬编码密钥、个人路径等敏感信息

## 验证结果

<!-- 贴关键验证输出（测试摘要 / 构建结果），便于 reviewer 复核 -->
