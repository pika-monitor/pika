# 版本号（可通过 make VERSION=1.0.0 指定）
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")

# Agent 版本号基于 pkg/agent/ 目录的最后修改提交生成
# 格式：{最近修改 pkg/agent 的提交短hash}
AGENT_VERSION ?= $(shell git log -1 --format=%h -- pkg/agent 2>/dev/null || echo "dev")

GIT_REVISION=$(shell git rev-parse HEAD)
GO_VERSION=$(shell go version)
BUILD_TIME=$(shell date +%Y-%m-%d_%H:%M:%S)

# UPX 默认使用快速压缩。可通过 make UPX_FLAGS=... 覆盖。
UPX_FLAGS ?=

# Go 构建参数
LDFLAGS=-s -w -X 'github.com/pika-monitor/pika/pkg/version.Version=$(VERSION)' -X 'github.com/pika-monitor/pika/pkg/version.AgentVersion=$(AGENT_VERSION)'
AGENT_LDFLAGS=-s -w -X 'github.com/pika-monitor/pika/pkg/version.Version=$(VERSION)' -X 'github.com/pika-monitor/pika/pkg/version.AgentVersion=$(AGENT_VERSION)'
GOFLAGS=CGO_ENABLED=0

# 默认主题默认拉取远端最新代码，显式指定目录时使用该目录的源码。
DEFAULT_THEME_DIR ?=
DEFAULT_THEME_OUTPUT_DIR ?= themes/default

.PHONY: build-web build-default-theme

# 构建官方管理前端和独立的默认主题，并组装到统一发布目录。
build-web:
	npm ci --prefix web
	npm run build --prefix web
	$(MAKE) build-default-theme

build-default-theme:
	@set -eu; \
	theme_dir="$(DEFAULT_THEME_DIR)"; \
	if [ -z "$$theme_dir" ]; then \
		tmp_dir=$$(mktemp -d); \
		trap 'rm -rf "$$tmp_dir"' EXIT; \
		git clone --depth 1 https://github.com/pika-monitor/pika-default-theme.git "$$tmp_dir"; \
		theme_dir="$$tmp_dir"; \
	fi; \
	test -f "$$theme_dir/package-lock.json"; \
	test -f "$$theme_dir/pika-theme.json"; \
	(cd "$$theme_dir" && npm ci && npm run build); \
	rm -rf "$(DEFAULT_THEME_OUTPUT_DIR)"; \
	mkdir -p "$(DEFAULT_THEME_OUTPUT_DIR)"; \
	cp "$$theme_dir/pika-theme.json" "$(DEFAULT_THEME_OUTPUT_DIR)/pika-theme.json"; \
	cp -R "$$theme_dir/dist" "$(DEFAULT_THEME_OUTPUT_DIR)/dist"

# 构建服务端（开发）
build-server:
	$(GOFLAGS) GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="$(LDFLAGS)" -o bin/pika-linux-amd64 cmd/serv/main.go
	upx $(UPX_FLAGS) bin/pika-linux-amd64

build-servers:
	$(GOFLAGS) GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="$(LDFLAGS)" -o bin/pika-linux-amd64 cmd/serv/main.go
	$(GOFLAGS) GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="$(LDFLAGS)" -o bin/pika-linux-arm64 cmd/serv/main.go

	upx $(UPX_FLAGS) bin/pika-linux-amd64
	upx $(UPX_FLAGS) bin/pika-linux-arm64

# 构建所有平台的 Agent
build-agents:
	@echo "Building agents for all platforms..."
	@echo "Server version: $(VERSION)"
	@echo "Agent version: $(AGENT_VERSION)"
	@mkdir -p bin/agents

	# Linux
	$(GOFLAGS) GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="$(AGENT_LDFLAGS)" -o bin/agents/pika-agent-linux-amd64 ./cmd/agent
	$(GOFLAGS) GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="$(AGENT_LDFLAGS)" -o bin/agents/pika-agent-linux-arm64 ./cmd/agent
	$(GOFLAGS) GOOS=linux GOARCH=arm GOARM=7 go build -trimpath -ldflags="$(AGENT_LDFLAGS)" -o bin/agents/pika-agent-linux-armv7 ./cmd/agent
	$(GOFLAGS) GOOS=linux GOARCH=loong64 go build -trimpath -ldflags="$(AGENT_LDFLAGS)" -o bin/agents/pika-agent-linux-loong64 ./cmd/agent

	# macOS
	$(GOFLAGS) GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags="$(AGENT_LDFLAGS)" -o bin/agents/pika-agent-darwin-amd64 ./cmd/agent
	$(GOFLAGS) GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags="$(AGENT_LDFLAGS)" -o bin/agents/pika-agent-darwin-arm64 ./cmd/agent

	# Windows
	$(GOFLAGS) GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="$(AGENT_LDFLAGS)" -o bin/agents/pika-agent-windows-amd64.exe ./cmd/agent
	$(GOFLAGS) GOOS=windows GOARCH=arm64 go build -trimpath -ldflags="$(AGENT_LDFLAGS)" -o bin/agents/pika-agent-windows-arm64.exe ./cmd/agent

	@echo "All agents built successfully!"
	@echo "Compressing agents with UPX..."
	@upx $(UPX_FLAGS) bin/agents/pika-agent-linux-amd64
	@upx $(UPX_FLAGS) bin/agents/pika-agent-linux-arm64
	@upx $(UPX_FLAGS) bin/agents/pika-agent-linux-armv7
	@echo "All agents compressed successfully!"
	@ls -lh bin/agents/

# 构建所有（release版本）
build-release:
	$(MAKE) build-web
	$(MAKE) build-agents
	$(MAKE) build-servers

# 清理编译产物
clean:
	rm -rf bin/*
	rm -rf web/dist
	rm -rf themes/default

# 运行测试
test:
	go test -v ./...

# 代码格式化
fmt:
	go fmt ./...

# 代码检查
lint:
	golangci-lint run

# 生成 Wire 代码
wire:
	cd internal && wire

# 显示版本信息
version:
	@echo "Server Version: $(VERSION)"
	@echo "Agent Version: $(AGENT_VERSION)"
	@echo "Git Revision: $(GIT_REVISION)"
	@echo "Go Version: $(GO_VERSION)"
	@echo "Build Time: $(BUILD_TIME)"
