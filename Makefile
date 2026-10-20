# order-sys 开发入口。所有 Go 命令都在 backend/ 下执行，失败即退出。
SHELL := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c

BACKEND := backend
COMPOSE := docker compose
GO := cd $(BACKEND) && go
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
# 镜像构建期拉取 Go 模块的代理。默认官方代理；网络受限时可覆盖：
#   make docker-build GOPROXY=https://goproxy.cn,direct
GOPROXY ?= https://proxy.golang.org,direct
LDFLAGS := -X github.com/vevila2020/order-sys/internal/version.Version=$(VERSION) \
           -X github.com/vevila2020/order-sys/internal/version.Commit=$(COMMIT) \
           -X github.com/vevila2020/order-sys/internal/version.BuildDate=$(BUILD_DATE)

.PHONY: help setup up down logs ps fmt fmt-check vet lint test test-race build run-api run-worker security sbom ci clean

help: ## 列出所有目标
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

setup: ## 生成 .env（随机密钥，不覆盖已有文件）
	@if [ -f .env ]; then \
		echo ".env already exists, skipping"; \
	else \
		cp backend/.env.example .env; \
		secret=$$(openssl rand -hex 32 2>/dev/null || head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n'); \
		token=$$(openssl rand -hex 24 2>/dev/null || head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n'); \
		sed -i.bak "s|^JWT_SECRET=.*|JWT_SECRET=$$secret|" .env; \
		sed -i.bak "s|^PAYMENT_CALLBACK_TOKEN=.*|PAYMENT_CALLBACK_TOKEN=$$token|" .env; \
		rm -f .env.bak; \
		echo ".env created with random secrets"; \
	fi

up: ## 启动依赖与应用
	$(COMPOSE) up -d --build
	@echo "api:      http://localhost:8080/health/live"
	@echo "grafana:  http://localhost:3000"
	@echo "jaeger:   http://localhost:16686"

down: ## 停止服务（保留数据卷）
	$(COMPOSE) down

logs: ## 跟踪应用日志
	$(COMPOSE) logs -f api worker

ps: ## 查看服务状态
	$(COMPOSE) ps

fmt: ## 格式化代码
	$(GO) fmt ./...
	cd $(BACKEND) && gofmt -w .

fmt-check: ## 检查格式（CI 门禁）
	@out=$$(cd $(BACKEND) && gofmt -l .); \
	if [ -n "$$out" ]; then echo "unformatted files:"; echo "$$out"; exit 1; fi

vet: ## go vet
	$(GO) vet ./...

lint: ## golangci-lint（未安装时提示）
	@cd $(BACKEND) && if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run ./...; \
	else \
		echo "golangci-lint not installed; see docs/runbooks/local-dev.md"; exit 1; \
	fi

test: ## 单元测试
	$(GO) test ./... -count=1

test-race: ## 单元测试（竞态检测，需要 CGO）
	cd $(BACKEND) && CGO_ENABLED=1 go test -race -count=1 ./...

build: ## 构建 api 与 worker
	cd $(BACKEND) && go build -trimpath -ldflags "$(LDFLAGS)" -o bin/api ./cmd/api
	cd $(BACKEND) && go build -trimpath -ldflags "$(LDFLAGS)" -o bin/worker ./cmd/worker

run-api: ## 本地运行 API（读取根目录 .env）
	@if [ -f .env ]; then set -a; . ./.env; set +a; fi; cd $(BACKEND) && go run -ldflags "$(LDFLAGS)" ./cmd/api

run-worker: ## 本地运行 worker（读取根目录 .env）
	@if [ -f .env ]; then set -a; . ./.env; set +a; fi; cd $(BACKEND) && go run -ldflags "$(LDFLAGS)" ./cmd/worker

security: ## govulncheck + secret scan
	@cd $(BACKEND) && if command -v govulncheck >/dev/null 2>&1; then \
		govulncheck ./...; \
	else \
		echo "govulncheck not installed; go install golang.org/x/vuln/cmd/govulncheck@latest"; exit 1; \
	fi
	@if command -v gitleaks >/dev/null 2>&1; then \
		gitleaks detect --source . --redact --exit-code 2; \
	else \
		echo "gitleaks not installed; see docs/runbooks/ci.md"; exit 1; \
	fi

sbom: ## 生成 SBOM 到 artifacts/
	@mkdir -p artifacts
	@cd $(BACKEND) && if command -v syft >/dev/null 2>&1; then \
		syft dir:. -o spdx-json=../artifacts/sbom.spdx.json; \
	else \
		echo "syft not installed; go install github.com/anchore/syft/cmd/syft@latest"; exit 1; \
	fi

ci: fmt-check vet test build ## 本地执行与 CI 同等的核心检查

docker-build: ## 构建 api/worker 镜像
	docker build -f $(BACKEND)/Dockerfile --build-arg TARGET=api --build-arg GOPROXY=$(GOPROXY) -t order-sys-api:$(VERSION) $(BACKEND)
	docker build -f $(BACKEND)/Dockerfile --build-arg TARGET=worker --build-arg GOPROXY=$(GOPROXY) -t order-sys-worker:$(VERSION) $(BACKEND)

clean: ## 清理构建产物（不动数据卷）
	rm -rf $(BACKEND)/bin artifacts coverage.out
