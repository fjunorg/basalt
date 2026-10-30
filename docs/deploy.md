# Basalt 部署指南

> 交付形态：单静态二进制（CGO_ENABLED=0，前端经 `go:embed` 嵌入），适配信创离线场景。
> 全部环境变量说明见 [`.env.example`](../.env.example)（15 项，含用途与默认值）。

## 生产环境最低要求（prod 安全守卫）

`--env prod` / `BASALT_ENV=prod` 下，以下两项**必须显式配置**，否则进程拒绝启动（SECURITY.md 基线）：

- `BASALT_JWT_SECRET`：强随机值（如 `openssl rand -hex 32`）
- `BASALT_BOOTSTRAP_ADMIN_PASS`：不得等于 dev 默认口令

建议同时：`BASALT_STORAGE=sqlite` + `BASALT_DB_DSN` 指向持久目录；国密场景加 `BASALT_CRYPTO_ALGO=sm3`。

## 路径一：单二进制交付

```bash
# 本机构建（要求 Go >= 1.26，本机 go 在 /usr/local/go/bin）
make build                # → bin/basalt

# 信创交叉编译矩阵：linux/amd64 · arm64(鲲鹏) · loong64(龙芯) · darwin
make cross-build          # → dist/basalt-<os>-<arch>
```

目标机运行（systemd 示例，`/etc/basalt/basalt.env` 由 `.env.example` 复制而来）：

```ini
# /etc/systemd/system/basalt.service
[Service]
ExecStart=/usr/local/bin/basalt serve --env prod
EnvironmentFile=/etc/basalt/basalt.env
StateDirectory=basalt
Restart=on-failure
```

```bash
sudo systemctl enable --now basalt
curl -fsS http://127.0.0.1:8080/healthz   # 存活探针（不依赖模块状态）
```

> `console/dist` 已随源码入库，`go build` 即得带 UI 的二进制；改前端后需 `cd console && npm install && npm run build` 重建并提交。

## 路径二：容器交付

```bash
# 多阶段构建：node 构建前端 → go 嵌入编译 → alpine 最小运行镜像（非 root + 内建 HEALTHCHECK）
docker build -t basalt:latest .

docker run -d --name basalt \
  -p 8080:8080 \
  -v basalt-data:/var/lib/basalt \
  -e BASALT_ENV=prod \
  -e BASALT_JWT_SECRET="$(openssl rand -hex 32)" \
  -e BASALT_BOOTSTRAP_ADMIN_PASS='替换为强口令' \
  basalt:latest

docker inspect --format '{{.State.Health.Status}}' basalt   # healthy 即就绪
```

镜像缺省值：`BASALT_STORAGE=sqlite`、`BASALT_DB_DSN=/var/lib/basalt/basalt.db`、`BASALT_UPLOAD_DIR=/var/lib/basalt/uploads`——数据全部落在 `basalt-data` 卷，重建容器不丢。

## 首启验证

```bash
# 引导管理员登录（口令即 BASALT_BOOTSTRAP_ADMIN_PASS，默认 admin / Basalt@2026 仅限 dev）
curl -X POST localhost:8080/api/v1/identity/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"Basalt@2026"}'

# 浏览器打开 http://<host>:8080/ 进入 Admin Console
```
