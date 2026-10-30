VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

# 信创交叉编译矩阵：linux/arm64=鲲鹏，linux/loong64=龙芯
XTARGETS := linux/amd64 linux/arm64 linux/loong64 darwin/arm64 darwin/amd64

.PHONY: help dev build test vet lint fmt cross-build clean

help: ## 显示帮助
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

dev: ## 本地运行
	go run ./cmd/basalt serve

build: ## 编译到 bin/
	go build -ldflags "$(LDFLAGS)" -o bin/basalt ./cmd/basalt

test: vet ## vet + 全量测试
	go test -race ./...

vet: ## 静态检查
	go vet ./...

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
