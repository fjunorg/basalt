# 依赖许可与敏感信息扫描

开源前固化的两项机扫能力（源自《上线就绪评估报告》建议），`make verify` 聚合可用。

## 快速使用

```bash
make scan-licenses   # 依赖许可证白名单扫描
make scan-secrets    # 敏感信息泄漏扫描
make verify          # 聚合质量门：vet + test + 两项扫描
```

任一扫描命中即退出码 1（CI/脚本可直接消费）。

## 依赖许可证扫描（scripts/scan_licenses.sh）

- **范围**：`go list -m all` 全部 Go 模块 + `console/package.json` 声明的前端依赖
  （经 node_modules 的 package.json license 字段）。
- **许可判别**：Go 模块在 GOMODCACHE 读 LICENSE 文件首部模式判别（Apache-2.0 /
  MIT / BSD-2/3-Clause / ISC / MPL-2.0 等）；懒下载模块自动回退同模块已有版本或
  `go mod download` 补拉；modcache 大写路径转义兼容。
- **白名单**（宽容集）：`MIT Apache-2.0 BSD-2-Clause BSD-3-Clause ISC BSD
  Unlicense Zlib MPL-2.0 PostgreSQL Python-2.0 Go-BSD 0BSD`。
- **通过基线**（2026-10-31 实测）：**169 依赖全绿**——go 160 模块 + console 9 依赖；
  分布：Apache-2.0×78 / MIT×49 / BSD-3-Clause×38 / BSD-2-Clause×3 / ISC×1。
- **新增依赖**：引入非白名单许可（GPL/LGPL/AGPL 等）时扫描将失败——需架构评审
  后在脚本 `ALLOWED` 显式追加并注明理由。
- 未识别许可证以 `[?]` 报告并计失败；复核确认为白名单等价文本后可用
  `SCAN_ALLOW_UNKNOWN=1` 豁免跑通（应同步修 classify_text 识别规则）。

## 敏感信息扫描（scripts/scan_secrets.sh）

- **检测模式**：AWS 访问键（AKIA/ASIA）、GitHub PAT、Slack token、
  OpenAI 风格 `sk-`、Google API key（AIza…）、PEM 私钥块、硬编码 JWT、
  ≥45 位高熵 hex 串。
- **排除**（评审过的良性内容）：`console/node_modules`、`dist`、`.git`、
  锁文件（go.sum 含模块哈希）、测试 fixture（`*_test.go`）、演示口令标注文件
  （`*.md` / `.env.example` / `seed.go`——Basalt@2026 / Demo@2026 等演示值）。
- **通过基线**：213 候选文件 0 命中（实测约 2 秒）。
- **检测能力自证**：注入六类伪造凭证样本（AWS/PAT/sk-/PEM/JWT/高熵 hex）
  全部命中、退出码 1；移除后恢复 0 命中。
- 报告误报时在脚本头部 `EXCLUDES`/豁免清单追加路径并注明理由。

## CI 接入

在 `.github/workflows/ci.yml` 的 job steps 中追加一行即可：

```yaml
      - name: License & secrets scan
        run: make scan-licenses scan-secrets
```

（或直接以 `make verify` 替换 vet+test 两步聚合；devops 线统一接线时二选一。）
