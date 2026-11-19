VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
# embed_hash：构建时把 console/dist 产物总 hash 注入二进制（healthz 可暴露）
EMBED_HASH := $(shell find console/dist -type f -exec shasum -a 256 {} \; | sort | shasum -a 256 | cut -d' ' -f1)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.embedHash=$(EMBED_HASH)

# 信创交叉编译矩阵：linux/arm64=鲲鹏，linux/loong64=龙芯
XTARGETS := linux/amd64 linux/arm64 linux/loong64 darwin/arm64 darwin/amd64

.PHONY: help dev build test vet lint fmt cross-build clean verify scan-licenses scan-secrets check-tokens check-embed-hash

help: ## 显示帮助
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

dev: ## 本地运行
	go run ./cmd/basalt serve

build: check-embed-hash ## 编译到 bin/（含前端产物 hash 一致性守卫）
	go build -ldflags "$(LDFLAGS)" -o bin/basalt ./cmd/basalt

test: vet ## vet + 全量测试
	go test -race ./...

vet: ## 静态检查
	go vet ./...

verify: vet test scan-licenses scan-secrets check-tokens check-embed-hash ## 聚合质量门（vet+test+许可+敏感+token+前端同步）；CI 接入行：make verify

scan-licenses: ## 依赖许可证白名单扫描（非白名单退出非零）
	./scripts/scan_licenses.sh

scan-secrets: ## 敏感信息泄漏扫描（命中退出非零）
	./scripts/scan_secrets.sh

check-tokens: ## 前端 semi-tokens.css 单一来源校验 + 硬编码反向扫描
	python3 ./scripts/check-tokens.py

check-embed-hash: ## console/src ↔ dist 产物 hash 同步守护（防止改源码忘 npm build）
	python3 ./scripts/check-embed-hash.py

lint: ## golangci-lint（需本地安装）
	golangci-lint run

fmt: ## 格式化
	gofmt -s -w .

cross-build: $(XTARGETS) ## 信创 + 主流平台交叉编译到 dist/
$(XTARGETS):
	@mkdir -p dist
	GOOS=$(word 1,$(subst /, ,$@)) GOARCH=$(word 2,$(subst /, ,$@)) \
		go build -ldflags "$(LDFLAGS)" -o dist/basalt-$(word 1,$(subst /, ,$@))-$(word 2,$(subst /, ,$@)) ./cmd/basalt

clean: ## 清理产物
	rm -rf bin dist
