# 文件存储 S3 兼容驱动（多云）部署指引

> 适用：`BASALT_FILE_S3_*` 配置族（`.env.example` 文件存储段）。
> 本地存储（`BASALT_UPLOAD_DIR`）始终保留——既是默认后端，也是
> S3 启用后的混合存量回退。

## 1. 兼容矩阵与 endpoint 配置

驱动实现 AWS Signature V4 + S3 协议子集（PUT/GET/DELETE/
ListObjectsV2/预签名），零 SDK 依赖。

| 云 | endpoint 示例 | path_style | 说明 |
|---|---|---|---|
| MinIO / 自建网关 | `http://minio.internal:9000` | `true`（默认） | path 风格 `endpoint/bucket/key` |
| 阿里云 OSS | `https://oss-cn-hangzhou.aliyuncs.com` | `false` | S3 兼容模式，virtual-host `bucket.endpoint` |
| 腾讯云 COS | `https://cos.ap-guangzhou.myqcloud.com` | `false` | 同上 |
| 任意 S3 兼容端点 | 按网关文档 | 多数 `true` | 协议兼容即用 |

七牛（原生 API 非 S3 协议）等经 `file.Storage` 接口缝后续单独
接入驱动，不混入本驱动的协议分支。

## 2. 混合存量语义（切换策略）

`Meta.Storage` 标记字节所在后端（`local` | `s3`，存量空值视为
local）。上传恒写主存储（env 决定）；下载/删除按标记路由：

- 切换前上传的本地旧文件 → 继续从 `BASALT_UPLOAD_DIR` 读写删；
- 切换后上传的新文件 → 走 S3；
- 回切本地同理（S3 时代的文件继续从 S3 读）。

**不做自动迁移**（策略见 §4）；旧文件迁移属运维批处理任务
（列举 `Meta.Storage=s3` → 流式转存 → 批量更新标记）。

## 3. 与租户隔离的边界

dedicated-db 租户体系隔离的是**数据库行**；对象存储的字节隔离
两级方案（按合规要求选配）：

- **前缀级（默认，轻量）**：`BASALT_FILE_S3_PREFIX={tenantID}/`——
  同 bucket 内键空间隔离。注意：前缀是命名约定而非权限边界，
  bucket 策略（IAM/桶策略）应限制凭据仅可访问该前缀，否则属
  逻辑隔离。
- **bucket 级（强边界）**：每租户独立 bucket，凭据按租户下发
  （部署侧配合：动态 `BASALT_FILE_S3_BUCKET`，当前单实例配置
  形态下适用于单租户部署或网关侧按租户终结点）。

共享磁盘（本地存储 + dedicated-db）的字节边界问题同理由 prefix
方案收敛：`BASALT_UPLOAD_DIR` 按租户挂不同子目录。

## 4. 预签名直链

`GET /api/v1/files/{id}/url`（登录级）：S3 后端返回 15 分钟预签名
GET URL，客户端直连对象存储下载（服务端零流量代理）；本地后端
返回 501（无公网语义，用 `/download` 代理端点）。TTL 与访问令牌
生命周期同量级，避免直链长有效窗口。

## 5. 运维要点

- 凭据经 env/编排平台密钥注入，不落库；`.env` 文件 600 权限。
- `basalt secure --check` 的 secrets-hygiene 项覆盖 S3 凭据扫描。
- 驱动上传整体缓冲签名（32MB 上限内），超限报错明确；大文件
  分块上传（multipart）属后续演进。
