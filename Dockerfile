# syntax=docker/dockerfile:1

# Basalt 多阶段构建：前端（console）→ Go（go:embed 嵌入 dist）→ 最小运行镜像。
# 交付形态：单静态二进制（CGO_ENABLED=0，glebarez/sqlite 纯 Go 驱动）。

# ---------- Stage 1/3: 前端构建 ----------
FROM node:22-alpine AS console-build
WORKDIR /app/console
COPY console/package.json console/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY console/ ./
# Vite outDir=dist → /app/console/dist
RUN npm run build

# ---------- Stage 2/3: Go 构建 ----------
FROM golang:1.26-alpine AS go-build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY pkg/ ./pkg/
COPY console/embed.go ./console/
COPY --from=console-build /app/console/dist ./console/dist
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "-s -w" \
    -o /out/basalt ./cmd/basalt

# ---------- Stage 3/3: 运行时 ----------
FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata \
 && addgroup -S basalt && adduser -S basalt -G basalt \
 && mkdir -p /var/lib/basalt/uploads \
 && chown -R basalt:basalt /var/lib/basalt
USER basalt
# 容器缺省：监听 8080，sqlite 落盘到卷，上传目录同卷（BASALT_ENV 由运行者注入，
# prod 时必须显式提供 BASALT_JWT_SECRET 与引导管理员口令，见 .env.example）
ENV BASALT_ADDR=:8080 \
    BASALT_STORAGE=sqlite \
    BASALT_DB_DSN=/var/lib/basalt/basalt.db \
    BASALT_UPLOAD_DIR=/var/lib/basalt/uploads
VOLUME /var/lib/basalt
EXPOSE 8080
COPY --from=go-build /out/basalt /usr/local/bin/basalt
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8080/healthz >/dev/null 2>&1 || exit 1
ENTRYPOINT ["basalt"]
CMD ["serve"]
