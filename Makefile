# Beacon 一键打包 —— 控制面单二进制（内嵌前端）+ 双端 agent 插件 jar。
#
# 版本唯一来源为仓库根 VERSION（ADR-0007）：构建时注入控制面（-ldflags -X）与 agent（Gradle 读根 VERSION），三组件版本恒一致。
# 依赖：go、pnpm、JDK + agent/gradlew；产物统一落 dist/（不入库）。
# 说明：本 Makefile 用 POSIX shell 命令（mkdir -p / cp / rm -rf），Linux/macOS/CI 原生可用；
#       Windows 下经 Git Bash 运行（原生 cmd / PowerShell 无 make）。

# 版本号（唯一来源，ADR-0007）
VERSION := $(shell cat VERSION)
# 控制面版本注入点（go 包路径）
VERSION_PKG := github.com/wcpe/Beacon/internal/version
# 链接参数：注入版本 + 裁剪符号表/调试信息（更小的发布二进制）
GO_LDFLAGS := -s -w -X $(VERSION_PKG).Version=$(VERSION)
# 控制面入口
CMD := ./cmd/beacon
# launcher 监督进程入口（FR-96，ADR-0045）
LAUNCHER_CMD := ./cmd/beacon-launcher
# 产物输出目录（不入库）
DIST := dist
# 当前平台可执行后缀（Windows 为 .exe，其余为空）
GOEXE := $(shell go env GOEXE)

# 双端 agent 部署插件 jar（库模块 agent-api/core/kit/adapters 与 E2E 插件不入包）
BUKKIT_JAR := agent/agent-bukkit/build/libs/BeaconAgent-$(VERSION).jar
BUNGEE_JAR := agent/agent-bungee/build/libs/BeaconAgentProxy-$(VERSION).jar

.DEFAULT_GOAL := help
.PHONY: help version web build launcher agent package clean

# 列出可用目标
help:
	@echo "Beacon build (version $(VERSION)) - targets:"
	@echo "  make package   full build (current platform): control-plane + launcher + both agents -> $(DIST)/"
	@echo "  make build     control-plane binary only (current platform, embeds web + injects version)"
	@echo "  make launcher  launcher supervisor binary only (current platform, injects version)"
	@echo "  make agent     both agent plugin jars only (gradle clean build)"
	@echo "  make web       build frontend web/dist only (embedded into control-plane)"
	@echo "  make clean     remove $(DIST)/ and agent build outputs"

# 打印当前版本号
version:
	@echo $(VERSION)

# 前端构建产物 web/dist（被控制面 go:embed 内嵌；必须先于 build）
web:
	cd web && pnpm install --frozen-lockfile && pnpm build

# 控制面单二进制（当前平台，内嵌已构建的 web/dist + 注入版本）
build: web
	@mkdir -p $(DIST)
	go build -trimpath -ldflags "$(GO_LDFLAGS)" -o $(DIST)/beacon$(GOEXE) $(CMD)
	@echo "control-plane -> $(DIST)/beacon$(GOEXE)"

# launcher 监督进程单二进制（当前平台，注入同一版本；极薄、不内嵌前端，故不依赖 web）
launcher:
	@mkdir -p $(DIST)
	go build -trimpath -ldflags "$(GO_LDFLAGS)" -o $(DIST)/beacon-launcher$(GOEXE) $(LAUNCHER_CMD)
	@echo "launcher -> $(DIST)/beacon-launcher$(GOEXE)"

# 双端 agent 插件 jar（clean 避免旧版本 jar 残留；gradle 读根 VERSION 注入版本号）
agent:
	cd agent && ./gradlew clean build
	@mkdir -p $(DIST)
	cp $(BUKKIT_JAR) $(DIST)/
	cp $(BUNGEE_JAR) $(DIST)/
	@echo "agents -> $(DIST)/$(notdir $(BUKKIT_JAR)) , $(DIST)/$(notdir $(BUNGEE_JAR))"

# 当前平台全量打包：控制面 + launcher + 双端 agent -> dist/
package: build launcher agent
	@echo "==== package done (version $(VERSION)) -> $(DIST)/ ===="
	@ls -l $(DIST)

# 注：多平台控制面二进制不在本地交叉编译——sqlite 经 go-sqlite3 需 CGO，交叉编译会关 CGO 致 sqlite 失效。
# 多平台原生发布由 CI 完成（.github/workflows/release.yml：矩阵在各平台原生 runner 上 CGO=1 构建并发 Release）。

# 清理产物
clean:
	rm -rf $(DIST)
	cd agent && ./gradlew clean
