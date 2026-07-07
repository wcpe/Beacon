# Beacon 一键打包 —— 控制面单二进制（内嵌前端）+ 双端 agent 插件 jar。
#
# 版本唯一来源为仓库根 VERSION（ADR-0007）：构建时注入控制面（-ldflags -X）与 agent（Gradle 读根 VERSION），三组件版本恒一致。
# 依赖：go、pnpm、JDK + apps/agent/gradlew；产物统一落 dist/（不入库）。
# 说明：打包目标兼容 Linux/macOS/CI 与 Windows 本地 make；lint 目标支持 PowerShell 7 + make。

# 版本号（唯一来源，ADR-0007）
ifeq ($(OS),Windows_NT)
VERSION := $(strip $(shell powershell -NoProfile -Command "(Get-Content -Raw -LiteralPath VERSION).Trim()"))
else
VERSION := $(strip $(shell cat VERSION))
endif
# 控制面版本注入点（go 包路径）
VERSION_PKG := github.com/wcpe/Beacon/apps/server/internal/version
# 链接参数：注入版本 + 裁剪符号表/调试信息（更小的发布二进制）
GO_LDFLAGS := -s -w -X $(VERSION_PKG).Version=$(VERSION)
# 控制面入口
CMD := ./apps/server/cmd/beacon
# 产物输出目录（不入库）
DIST := dist
# 当前平台可执行后缀（Windows 为 .exe，其余为空）
GOEXE := $(shell go env GOEXE)

# 双端 agent 部署插件 jar（库模块 agent-api/core/kit/adapters 与 E2E 插件不入包）
BUKKIT_JAR := apps/agent/agent-bukkit/build/libs/BeaconAgent-$(VERSION).jar
BUNGEE_JAR := apps/agent/agent-bungee/build/libs/BeaconAgentProxy-$(VERSION).jar

# 控制面 Go 模块路径（goimports 本地导入分组前缀，与 go.mod 一致）
GOIMPORTS_LOCAL := github.com/wcpe/Beacon
ifeq ($(OS),Windows_NT)
GO_FORMAT_CHECK := pwsh -NoProfile -File scripts/check-go-format.ps1 $(GOIMPORTS_LOCAL)
MKDIR_DIST := powershell -NoProfile -Command "New-Item -ItemType Directory -Force -Path '$(DIST)' | Out-Null"
COPY_TO_DIST = powershell -NoProfile -Command "Copy-Item -LiteralPath '$(1)' -Destination '$(DIST)' -Force"
LIST_DIST := powershell -NoProfile -Command "Get-ChildItem -LiteralPath '$(DIST)'"
REMOVE_DIST := powershell -NoProfile -Command "if (Test-Path -LiteralPath '$(DIST)') { Remove-Item -LiteralPath '$(DIST)' -Recurse -Force }"
GRADLE_BUILD := cd apps\agent && gradlew.bat clean build
GRADLE_CLEAN := cd apps\agent && gradlew.bat clean
else
GO_FORMAT_CHECK := bash scripts/check-go-format.sh $(GOIMPORTS_LOCAL)
MKDIR_DIST := mkdir -p $(DIST)
COPY_TO_DIST = cp $(1) $(DIST)/
LIST_DIST := ls -l $(DIST)
REMOVE_DIST := rm -rf $(DIST)
GRADLE_BUILD := cd apps/agent && ./gradlew clean build
GRADLE_CLEAN := cd apps/agent && ./gradlew clean
endif
.DEFAULT_GOAL := help
.PHONY: help version lint web build agent package clean

# 列出可用目标
help:
	@echo "Beacon build (version $(VERSION)) - targets:"
	@echo "  make package   full build (current platform): control-plane + both agents -> $(DIST)/"
	@echo "  make build     control-plane binary only (current platform, embeds web + injects version)"
	@echo "  make agent     both agent plugin jars only (gradle clean build)"
	@echo "  make web       build apps/web/dist only (embedded into control-plane)"
	@echo "  make lint      Go static checks: gofmt + goimports + golangci-lint (CRLF-safe, mirrors CI)"
	@echo "  make clean     remove $(DIST)/ and agent build outputs"

# 打印当前版本号
version:
	@echo $(VERSION)

# Go 本地一键静态检查 —— 镜像 CI 的 lint job（golangci-lint + gofmt + goimports）。
# 提交前必跑（见 .claude/rules/static-analysis.md §2）。
# CRLF 安全：.golangci.yml 的 formatters 不启用 gofmt/goimports，本目标把格式化交给下方
# 平台脚本，对本次改动的 .go 文件去 CR 后校验（快、等价 CI 的 LF 检查）。
lint:
	@echo "==== golangci-lint run（配置见 .golangci.yml）===="
	golangci-lint run ./...
	@$(GO_FORMAT_CHECK)
	@echo "==== Go 静态检查全部通过 ===="

# 前端构建产物 apps/web/dist（被控制面 go:embed 内嵌；必须先于 build）
web:
	pnpm install --frozen-lockfile && pnpm --filter @beacon/web build

# 控制面单二进制（当前平台，内嵌已构建的 apps/web/dist + 注入版本）
build: web
	@$(MKDIR_DIST)
	go build -trimpath -ldflags "$(GO_LDFLAGS)" -o $(DIST)/beacon$(GOEXE) $(CMD)
	@echo "control-plane -> $(DIST)/beacon$(GOEXE)"

# 双端 agent 插件 jar（clean 避免旧版本 jar 残留；gradle 读根 VERSION 注入版本号）
agent:
	$(GRADLE_BUILD)
	@$(MKDIR_DIST)
	$(call COPY_TO_DIST,$(BUKKIT_JAR))
	$(call COPY_TO_DIST,$(BUNGEE_JAR))
	@echo "agents -> $(DIST)/$(notdir $(BUKKIT_JAR)) , $(DIST)/$(notdir $(BUNGEE_JAR))"

# 当前平台全量打包：控制面 + 双端 agent -> dist/
package: build agent
	@echo "==== package done (version $(VERSION)) -> $(DIST)/ ===="
	@$(LIST_DIST)

# 注：多平台控制面二进制不在本地交叉编译——sqlite 经 go-sqlite3 需 CGO，交叉编译会关 CGO 致 sqlite 失效。
# 多平台原生发布由 CI 完成（.github/workflows/release.yml：矩阵在各平台原生 runner 上 CGO=1 构建并发 Release）。

# 清理产物
clean:
	$(REMOVE_DIST)
	$(GRADLE_CLEAN)
