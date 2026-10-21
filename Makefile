# ============================================================
# Quant Trading - Service Build & Deploy Makefile
# Usage: make [target] VERSION=v1.0.0
# ============================================================

.PHONY: all build push clean test lint agent-compat help

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "local")
BUILD_TIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
REGISTRY ?= ghcr.io/ruoxizhnya

# Default target
all: build

# ============================================================
# Build targets - Individual services
# ============================================================
build-analysis:
	@echo "🔨 Building analysis-service:${VERSION}..."
	docker build \
		--build-arg BUILD_VERSION=${VERSION} \
		--build-arg BUILD_COMMIT=${COMMIT} \
		--build-arg BUILD_TIME=${BUILD_TIME} \
		-t ${REGISTRY}/tellurion-analysis:${VERSION} \
		-t ${REGISTRY}/tellurion-analysis:latest \
		-f cmd/analysis/Dockerfile .

build-data:
	@echo "🔨 Building data-service:${VERSION}..."
	docker build \
		--build-arg BUILD_VERSION=${VERSION} \
		--build-arg BUILD_COMMIT=${COMMIT} \
		--build-arg BUILD_TIME=${BUILD_TIME} \
		-t ${REGISTRY}/tellurion-data:${VERSION} \
		-t ${REGISTRY}/tellurion-data:latest \
		-f cmd/data/Dockerfile .

# Build all services
#
# 曾经这里还有 build-execution / build-risk —— 那两个服务在 ODR-021
# （P1-15）里已并进 analysis（in-process 的 risk.RiskManager + live.MockTrader），
# 目录也删了，于是 `make build` 一定失败在找不到的 Dockerfile 上。
# 留着死目标比删掉更糟：它看起来还能用，只在动手时才炸。
# build-ai 同样于 2026-09-18 移除（cmd/ai 已删，见 TASKS P2-5）。
# build-strategy 于 REV-16（2026-10-17）移除：cmd/strategy 服务按 ADR-012 关闭裁决删除。
build: build-analysis build-data
	@echo ""
	@echo "✅ All services built successfully!"
	@echo "   Version: ${VERSION}"
	@echo "   Commit:  ${COMMIT}"

# ============================================================
# Push targets
# ============================================================
push-analysis: build-analysis
	docker push ${REGISTRY}/tellurion-analysis:${VERSION}
	docker push ${REGISTRY}/tellurion-analysis:latest

push-data: build-data
	docker push ${REGISTRY}/tellurion-data:${VERSION}
	docker push ${REGISTRY}/tellurion-data:latest

push: push-analysis push-data

# ============================================================
# Docker Compose targets
#
# 用根目录的 docker-compose.yml，不是 docker-compose.services.yml：
# 后者是两份 compose 互相漂移留下的那一份（P1-8 / P2-7），它引用了 ODR-021
# 删掉的 cmd/execution 与 cmd/risk 的 Dockerfile，docker compose 一读就炸。
# 现已删除；维护中的那份是根 compose（postgres/redis/data/strategy/analysis）。
# ============================================================
up:
	docker compose up -d

down:
	docker compose down

restart:
	docker compose restart

logs:
	docker compose logs -f

ps:
	docker compose ps

# ============================================================
# Utility targets
# ============================================================
clean:
	@echo "🧹 Cleaning up Docker resources..."
	docker system prune -f
	@echo "✅ Cleanup complete"

test:
	@echo "🧪 Running tests..."
	# REV-21：与 CI 口径对齐（-count=1 -race + 覆盖率门禁）。修掉原先
	# 「Makefile 无 -race/-count=1、CI 无 -coverprofile」的双向分叉。
	# 阈值与 .github/workflows/ci.yml 同一份（改就两边一起改）。
	go test ./... -count=1 -race -coverprofile=coverage.out
	go run ./tools/coveragegate -profile=coverage.out \
		-total-min=52 \
		-pkg-min=tellurion/pkg/backtest=65 \
		-pkg-min=tellurion/pkg/domain=75
	@echo "✅ Tests completed"

lint:
	@echo "🔍 Running linter..."
	golangci-lint run ./...
	@echo "✅ Linting complete"

# QT-18：过渡期（S0~S3）agent 编译期依赖 core 源码，而 W0/W1 的改动全是 breaking
# （abi v2 换 host 函数、错误分级改 getSignals 返回…）。要让「core 绿、agent 静默烂」
# 在 core 侧当场暴露，就得拿当前 core 工作区去编一遍 agent。
#
# 逻辑只在 tools/ci/agent_compat.sh 里有一份（CI 的 agent-compat job 调同一条脚本）。
# 本机 Windows + Git Bash **没有 make**，所以降级门禁的正子是那条脚本：
#     sh tools/ci/agent_compat.sh
# 本目标只是给有 make 的环境留一个顺手入口。
agent-compat:
	@sh tools/ci/agent_compat.sh

help:
	@echo ""
	@echo "📦 Quant Trading - Build & Deploy System"
	@echo "=========================================="
	@echo ""
	@echo "Usage: make [target] [VARIABLE=value]"
	@echo ""
	@echo "Variables:"
	@echo "  VERSION      Image version tag (default: git describe or 'dev')"
	@echo "  COMMIT       Git commit hash (default: short SHA)"
	@echo "  BUILD_TIME   Build timestamp (default: current UTC time)"
	@echo "  REGISTRY     Container registry (default: ghcr.io/ruoxizhnya)"
	@echo ""
	@echo "Build Targets:"
	@echo "  build              Build all services"
	@echo "  build-analysis     Build analysis service only"
	@echo "  build-data         Build data service only"
	@echo ""
	@echo "Push Targets:"
	@echo "  push               Push all services to registry"
	@echo "  push-analysis      Push analysis service only"
	@echo "  (and so on for each service)"
	@echo ""
	@echo "Docker Compose:"
	@echo "  up                 Start all services with docker-compose"
	@echo "  down               Stop and remove containers"
	@echo "  restart            Restart all services"
	@echo "  logs               View service logs"
	@echo "  ps                 Show service status"
	@echo ""
	@echo "Utilities:"
	@echo "  test               Run Go tests"
	@echo "  lint               Run golangci-lint"
	@echo "  agent-compat       Shorthand for tools/ci/agent_compat.sh (QT-18 cross-repo compile guard; run the script directly when make is unavailable, e.g. Git Bash on Windows)"
	@echo "  clean              Clean up Docker resources"
	@echo "  help               Show this help message"
	@echo ""
