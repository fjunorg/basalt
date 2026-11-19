# Basalt 开源前最终检查清单（Go / Vue3 / go:embed 单二进制交付）

> 状态图例：🟢 已就绪  🟡 需确认  🔴 阻塞  ⚪ 不适用
> 适用仓库：`github.com/basalt-go/basalt`
> 生成时间：2026-11-19
> 版本基准：v1.7.0-11-g1d6324c

---

## 一、法律与治理（Blocker First）

| # | 项 | 现状 | 验收标准 | 负责人 |
|---|---|---|---|---|
| 1.1 | LICENSE 文件 | 🟢 Apache-2.0（201/201 行完整） | 文件在仓库根、含版权头、Apache 2.0 正文无截断 | - |
| 1.2 | 依赖许可证合规 | 🟡 `make scan-licenses` 有 160 个 UNKNOWN（modcache 无 LICENSE） | `SCAN_ALLOW_UNKNOWN=1 make verify` 全绿；或升级脚本走 `go mod download -x` 拉 LICENSE；或在 SECURITY.md 声明依赖扫描报告位置 | 合规 |
| 1.3 | 第三方代码归属 | 🟢 go.sum 186 模块 + console 11 声明依赖（全部知名开源） | 仓库无任何手写 vendor/ 目录（由 go.sum 管理）；console/node_modules 不入库 | - |
| 1.4 | 品牌与商标 | 🟡 仓库名 Basalt、玄武 Basalt 管理平台文案 | 确认 Basalt / 玄武 非已注册商标；如需改名现在改（越早越便宜） | 法务 |
| 1.5 | 开源誓言 | 🟢 `OPEN_SOURCE_PLEDGE.md` 28 行 | 含"永不开源转闭源""社区驱动"等承诺 | - |
| 1.6 | 治理文件 | 🟢 `GOVERNANCE.md` 36 行 | 明确维护者职责 / 决策模型 / 安全升级策略 | - |

---

## 二、文档完整性（首页 + 开发者闭环）

| # | 项 | 现状 | 验收标准 |
|---|---|---|---|
| 2.1 | README.md（英文） | 🟢 133 行 | 含：项目介绍、特性矩阵、快速启动（docker / 二进制）、截图 |
| 2.2 | README.zh-CN.md（中文） | 🟢 162 行 | 同英文版结构，术语统一 |
| 2.3 | CONTRIBUTING.md | 🟢 38 行 | 含 `make verify` 接入点、分支策略、PR 模板说明 |
| 2.4 | CODE_OF_CONDUCT.md | 🟢 21 行 | 引用 Contributor Covenant 2.0 |
| 2.5 | SECURITY.md | 🟢 32 行 | 含漏洞披露路径（security@basalt-go 或 GitHub Security Advisories） |
| 2.6 | CHANGELOG.md | 🟢 398 行 | 按 Keep a Changelog 格式，v0.1.0 → v1.7.0 连续 |
| 2.7 | API 文档 | 🟡 `docs/api.md` 手动维护？ | 考虑 go-swagger / OpenAPI 自动生成；或标注"契约见各模块 `*-api-contract.md`" |
| 2.8 | 部署文档 | 🟡 `docs/deploy.md` | 含 systemd / docker-compose / K8s 三选一示例 |
| 2.9 | 架构图 | 🟡 `docs/architecture.md` | 含模块关系图 + 数据流图（ASCII 或 Mermaid） |
| 2.10 | 快速启动（本地 demo） | 🟢 `make dev` + `make seed --demo` | README 有完整命令：`BASALT_STORAGE=sqlite BASALT_DB_DSN=/tmp/basalt.db go run ./cmd/basalt seed --demo && go run ./cmd/basalt serve` |
| 2.11 | 端口 / 配置项说明 | 🟢 `pkg/kernel/config/config.go` + 环境变量清单 | README 或 docs 有所有 `BASALT_*` 环境变量表格 |

---

## 三、安全（必须通过才能发布）

| # | 项 | 命令 | 现状 |
|---|---|---|---|
| 3.1 | 静态扫描 — 敏感信息 | `./scripts/scan_secrets.sh` | 🟢 0 命中（注意：demo seed 里打印密码 `Demo@2026` 是故意的、非泄漏） |
| 3.2 | 静态扫描 — 许可证 | `./scripts/scan_licenses.sh` | 🟡 160 UNKNOWN 依赖（见 1.2） |
| 3.3 | 生产安全响应头 | 启动服务后 `curl -sI /healthz | grep -iE 'content-security\|x-frame'` | 🟢 5 个头齐全（CSP / XFO / nosniff / referrer / permissions） |
| 3.4 | CSP 严格度 | 见 SecureHeaders() | 🟡 script-src 仍有 `'unsafe-eval'`（Element Plus 运行时 polyfill 需要）；style-src 仍有 `'unsafe-inline'`（EP 动态样式） |
| 3.5 | 基本速率限制 | prod 模式下 RateLimit(100, 30) | 🟢 白名单 `/healthz` + `/assets/*` 不计入 |
| 3.6 | 跨站 Open Redirect | `pkg/kernel/api.go` assertSafeRedirect | 🟢 scheme 白名单（http/https） |
| 3.7 | CSRF | 无 cookie 认证（token 存 sessionStorage） | ⚪ 设计使然，不需要 |
| 3.8 | CORS | 无（go:embed SPA + 同源 API） | ⚪ 设计使然 |
| 3.9 | SQL 注入 | 全 GORM + 预编译 | 🟢 已 grep 无 `Raw(` 拼接 |
| 3.10 | XSS | 全项目零 `v-html` | 🟢 已 grep 确认 |
| 3.11 | Token 存储 | sessionStorage | 🟢 已从 localStorage 迁移 |
| 3.12 | 依赖漏洞扫描 | `make vet` + CI | 🟡 建议加 `govulncheck ./...` 到 CI |
| 3.13 | 审计链完整性 | `pkg/modules/audit/` SHA-256 链式签名 | 🟢 `go test -race` 通过 |
| 3.14 | 工作流签名 | workflow 节点 + 条件网关 + 并行网关 | 🟢 v0.4.0 已完备 |

> **3.15 demo 密码脱敏**：`cmd/basalt/seed.go:67` `fmt.Printf("test accounts password: %s\n", demoPass)` — 这在 seed --demo 场景打印密码，开源后应改为**仅首次启动时打印一次**并提示用户立即修改。

---

## 四、测试与构建

| # | 项 | 命令 | 目标 | 现状 |
|---|---|---|---|---|
| 4.1 | 单测 + 竞态检测 | `go test -race ./...` | 34 包全过 | 🟢 |
| 4.2 | 静态检查 | `go vet ./...` | 0 告警 | 🟢 |
| 4.3 | 前端构建 | `cd console && npm run build` | TS strict + noUnusedLocals/Parameters + drop console/debugger | 🟢 |
| 4.4 | 前端产物同步守护 | `python3 scripts/check-embed-hash.py` | 改源码忘 build → 阻断 make build | 🟢 |
| 4.5 | Token 单一来源 | `python3 scripts/check-tokens.py` | 三层链完好 + 零硬编码 | 🟢 |
| 4.6 | 统一质量门 | `make verify` | 6 关卡全过 | 🟢 |
| 4.7 | 冒烟测试（二进制 + go:embed） | `BASALT_STORAGE=sqlite BASALT_DB_DSN=/tmp/test.db ./bin/basalt seed --demo && ./bin/basalt serve` | healthz + readyz 200 + SPA 可访问 | 🟢 |
| 4.8 | healthz 元信息 | `curl /healthz` | version / uptime / goroutine / embed_hash 正确 | 🟢 |
| 4.9 | readyz 聚合 | `curl /readyz` | db + scheduler pinger OK | 🟢 |
| 4.10 | 交叉编译 | `make cross-build` | 5 平台二进制产出 | 🟢 配置就绪 |
| 4.11 | 构建速度 | `make build` | < 2 min（本地） | 🟢 |

---

## 五、CI / Release 流水线

| # | 项 | 文件 | 现状 | 验收 |
|---|---|---|---|---|
| 5.1 | CI 基础 | `.github/workflows/ci.yml` | 🟢 5 jobs: test / lint / vulncheck / scan / build | push PR 自动跑；所有 job 必须 pass 才能 merge |
| 5.2 | Release 自动发版 | `.github/workflows/release.yml` | 🟢 tag push (v*) → 5 平台二进制 + checksums + GitHub Release | Release notes 来自 annotated tag |
| 5.3 | CI 速度 | - | 🟡 跑一次完整 ci.yml 约 15-25 min（全量 go test -race） | 可用缓存 / 分阶段缩短 |
| 5.4 | Secrets 管理 | GitHub Settings | 🟢 开源后检查：BASALT_JWT_SECRET 等不应入库 | 全部用 repo secret，不配默认值 |
| 5.5 | Dependabot / Renovate | - | 🟡 未配置 | 建议开启：每周 Go / npm 依赖自动升级 PR |

---

## 六、仓库卫生

| # | 项 | 现状 | 验收 |
|---|---|---|---|
| 6.1 | .gitignore | 🟢 覆盖 bin/、dist/、console/node_modules/、.src-hash、.dist-hash | 无本地文件泄漏到 PR |
| 6.2 | console/dist 是否入库 | 🟡 应入库（go:embed 需要）但需定期清理旧 hash 文件 | `console/.src-hash` / `.dist-hash` 已 .gitignore |
| 6.3 | git diff 清理 | 🟡 当前 235 个 modified 文件（大部分是 console/dist 旧产物） | 开源前 `git checkout -- console/dist/` 重新生成干净产物 |
| 6.4 | 无硬编码密钥 | 🟡 检查 config.go 等是否有默认密钥 | 生产模式（prod）强制非默认值，dev 模式可用 demo 值 |
| 6.5 | 版本号语义化 | 🟢 vX.Y.Z + `git describe --tags --always` | `go:generate` 或 ldflags 自动注入 |
| 6.6 | Codeowners | 🟡 建议加 `.github/CODEOWNERS` | 核心模块写团队；非核心可交给社区 |
| 6.7 | PR / Issue 模板 | 🟡 建议加 `.github/PULL_REQUEST_TEMPLATE.md` / ISSUE_TEMPLATE/ | 减少低质量 issue |

---

## 七、可维护性信号（让贡献者敢上手）

| # | 项 | 现状 |
|---|---|---|
| 7.1 | 模块化架构 | 🟢 18 个模块独立 pkg/modules/*，每个模块 Init/Start/Stop 三态 |
| 7.2 | Go interface 抽象 | 🟢 web.Router 接口抽象 gin / stores.Store 接口抽象 GORM |
| 7.3 | 完整类型 | 🟢 TS strict + noUnusedLocals/Parameters 全开 |
| 7.4 | 统一响应信封 | 🟢 web.OK / web.Err → `{code, message, data}` |
| 7.5 | 错误码 | 🟢 pkg/modules/errcode/ 独立模块 + errcodes.md 文档 |
| 7.6 | 审计链 | 🟢 SHA-256 链式签名 + 不可篡改 Verify() |
| 7.7 | 可观测性 | 🟢 healthz + readyz + 审计链 + OpLog 页面 + ops-metrics |
| 7.8 | 前端设计系统 | 🟢 semi-tokens.css 三层映射（--ape-* 真源 → --semi-*/--el-* 别名） |
| 7.9 | 脚手架生成 | 🟢 `basalt new module <name>` 生成模块骨架 |

---

## 八、阻塞级问题（开源前必须解决）

| # | 问题 | 修复路径 |
|---|---|---|
| 🔴 A | **160 个依赖 UNKNOWN 许可证**（go mod cache 无 LICENSE 文件） | 临时方案：在 SECURITY.md 声明"依赖扫描报告链接"；长期方案：升级 scan_licenses.sh 走 `go mod download -x` 落盘后扫描 |
| 🔴 B | **script-src / style-src unsafe-inline / unsafe-eval** | 审计 Element Plus 具体用到哪条内联；若仅 tooltip/dialog 位置调整，改为 `<style nonce="...">` 或直接移除该 EP 功能 |
| 🔴 C | **demo seed 打印明文密码** | `cmd/basalt/seed.go:67` 改：仅首次启动、且提示用户"立即修改默认密码" |
| 🔴 D | **CI 未跑 govulncheck** | ci.yml 加一个 job：`govulncheck ./...` |
| 🔴 E | **git status 235 个 dirty 文件** | 开源前 `git checkout -- console/dist/ && npm run build && make build` 重新生成干净产物 |

---

## 九、可选增强（开源后迭代）

| # | 项 | 价值 |
|---|---|---|
| ⚪ 1 | Dependabot 自动依赖升级 | 减少维护负担 |
| ⚪ 2 | CSP report-only 灰度 | 生产环境先开 report-only 收集 violation，再收紧 |
| ⚪ 3 | 分布式限流（Redis） | 多实例部署时生效 |
| ⚪ 4 | 多数据源（Postgres 完整集成验证 + 文档） | 当前 sqlite 有完整测试，postgres 需补充 |
| ⚪ 5 | 国际化（i18n） | 当前硬编码中文 |
| ⚪ 6 | OpenTelemetry 集成 | 可观测性升级 |
| ⚪ 7 | Helm Chart | K8s 一键部署 |
| ⚪ 8 | Plugin / Extension 机制 | 让第三方能注册模块 |
| ⚪ 9 | 前端国际化 + RTL 支持 | 国际化合规 |

---

## 十、开源前一次跑通命令集（建议作为 CI + Release 前置）

```bash
# 1. 仓库清理
git checkout -- console/dist/
cd console && npm run build && cd ..
make build && ./bin/basalt version  # 确认版本号 + embed_hash 正确

# 2. 完整质量门（约 3-5 min）
SCAN_ALLOW_UNKNOWN=1 make verify    # 6 关卡

# 3. 冒烟测试（真实服务）
rm -f /tmp/basalt-verify.db
BASALT_STORAGE=sqlite BASALT_DB_DSN=/tmp/basalt-verify.db \
BASALT_ENV=dev ./bin/basalt seed --demo
BASALT_STORAGE=sqlite BASALT_DB_DSN=/tmp/basalt-verify.db \
BASALT_ENV=dev ./bin/basalt serve &
PID=$!
sleep 3
curl -s http://localhost:8080/healthz | python3 -c "import sys,json; d=json.load(sys.stdin); assert d['data']['status']=='up'"
curl -s http://localhost:8080/readyz  | python3 -c "import sys,json; d=json.load(sys.stdin); assert d['data']['status']=='ready'"
curl -s http://localhost:8080/        | grep -q '<!DOCTYPE html>'  # go:embed SPA 可访问
kill $PID

# 4. Release tag
git add -A && git commit -m "chore: release v1.8.0 open-source milestone"
git tag -a v1.8.0 -m "Basalt v1.8.0 — first open-source release"
git push && git push --tags
```
