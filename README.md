# Beacon

> 面向 Minecraft 多群组服务器的自研中间件控制面 —— 集群调度 · 区服治理 · 可观测审计。

[![version](https://img.shields.io/badge/version-v0.18.0-blue)](CHANGELOG.md)
[![license](https://img.shields.io/badge/license-MIT-green)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](go.mod)
[![CI](https://github.com/wcpe/Beacon/actions/workflows/ci.yml/badge.svg)](https://github.com/wcpe/Beacon/actions/workflows/ci.yml)

Beacon 是一个独立的后端控制面：用 **Go** 提供 API、内嵌 **React** 管理台、编译为**单个二进制**（管理台与 API 同端口）；Minecraft 的 BungeeCord 代理与 Bukkit / Paper 子服各跑一个轻量 **Kotlin/TabooLib agent** 接入。第二版聚焦把多个 BC + 子服串成可治理的集群调度中间件，用 Web 管理 namespace、BC 集群、大区、小区、子服身份、健康调度、跨服消息、审计与告警。

**控制面挂 ≠ 数据面挂**：agent 持本地配置快照 fail-static，控制面不可用时按快照继续运行，绝不阻断玩家进服。

## 核心特性

- **Agent 自连接与身份绑定**：agent 通过 Beacon 地址、token、namespace、serverId 自连接；首启生成 `identityId` 并在后台人工确认后绑定，避免误改 serverId 造成区数据隔离错误。
- **namespace 强隔离**：namespace 默认禁止跨域调度、消息和 Agent 操作；跨 namespace 互通必须后台显式配置互通信任关系，并额外审计。
- **区服治理**：后台统一管理环境、BC 集群、大区、小区、子服和默认入口，未确认 / 未分配的 agent 不可调度。
- **健康调度**：基于 TPS、CPU、在线人数、连接、告警、容量和延迟生成健康值，业务插件通过本机 `agent-api` 获取健康服务器，禁止直接 HTTP 调 Beacon。
- **连接与消息可观测**：采集玩家流、连接流、调度决策、跨服消息与异常链路；payload 可存储但查看必须填写原因并写审计。
- **热冷数据生命周期**：近期数据留热库，2 个月以上默认归档到同实例独立 database / schema；预留独立归档 DSN，支持冷查询与归档清理。
- **高密度管理台**：面向 1000+ 子服的运维总览、服务器资产、区分配、集群拓扑、可观测和系统设置页面。
- **配置与文件能力 V2**：第一版配置中心、文件同步、文件树预览先进入维护态；第二版按配置中心 V2、文件树预览 V2、文件同步 V2 分阶段重建。
- **在线自更新**：单二进制自我替换（下载校验 → rename 让位 → 重启），无需第二监督进程；换版失败自动回退、可手动回滚到上一版本；正式 / 滚动预发布双渠道。
- **鉴权**：管理面 Bearer 登录令牌；运行时 API 密钥（full / readonly 两级角色，只读密钥对任何写端点一律 403）。
- **数据面 agent**：Kotlin/TabooLib 双端（Bukkit / Bungee），fail-static、env 覆盖配置、内置可选跨服消息中间件；并提供只读 SDK 供业务插件接入。
- **简单优先**：面向约 50 服规模，单节点 + REST/SSE，**不引入 Redis / MQ / DI 框架**（见 [ADR-0003](docs/adr/0003-no-redis-in-mvp.md)）；数据库经 GORM 抽象，MySQL / SQLite，可切 Postgres。

> 完整功能需求与验收见 [docs/PRD.md](docs/PRD.md)，第二版路线见 [docs/ROADMAP.md](docs/ROADMAP.md)，逐版变更见 [CHANGELOG.md](CHANGELOG.md)。

## 为什么是独立服务而非代理插件

代理（BC）是玩家入口（数据面）；控制面是管理面。二者**故障域必须隔离**：控制面崩溃绝不能拖垮玩家入口，玩家入口崩溃时控制面仍能改配置 / 回滚。因此 Beacon 是独立进程，BC / Bukkit 仅跑轻量 agent，并持本地快照 fail-static。

## 架构一览

```
                 浏览器 ──HTTP──┐
                               ▼
   ┌──────────────────────────────────────────────┐
   │  Beacon 控制面（单 Go 二进制 + 内嵌 React）       │  单节点
   │  /admin/v1 管理台 API      /beacon/v1 agent API  │
   │  内存真源：在线连接 + 健康 TTL + SSE waiters       │  ← 在线态
   │  MySQL 真源：身份 / 分配 / 审计 / 指标 / 归档索引   │  ← 治理权威
   └──────────────────────────────────────────────┘
        ▲ REST 注册/心跳/拉配置/上报 · SSE 变更推送
        │
  ┌─────┴───────┬───────────────┐
  ▼             ▼               ▼
agent          agent           agent     （Kotlin/TabooLib，identityId 绑定 serverId）
Bukkit 子服    Bukkit 子服     Bungee 代理   本地快照 fail-static
```

- **真源切分**：在线连接与健康 TTL 在 Go 进程内存；身份绑定、区服分配、审计、指标和归档索引在 MySQL。二者互不阻塞。
- 设计细节与决策见 [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) 与 [docs/adr/](docs/adr/)。

## 快速开始

### 部署控制面

```bash
cp .env.example .env      # 填 MySQL 密码、agent token、管理台账号口令、令牌签名密钥
docker compose up -d      # 起 beacon + mysql；mysql 就绪后自动建表(AutoMigrate)+预置 prod/test 两环境
# 管理台与 API 同端口： http://localhost:8848
```

浏览器打开 `http://localhost:8848`，用 `BEACON_ADMIN_USERNAME`（默认 `admin`）+ `BEACON_ADMIN_PASSWORD` 登录（自 v0.2.0 起 `/admin/v1/*` 需登录令牌）。

> 也可单二进制直接运行（默认 SQLite、首启自动释放 `config.yml`，开箱即跑）。部署、升级、备份恢复与排障见 [docs/OPERATIONS.md](docs/OPERATIONS.md)。

### 从源码构建

```bash
make package    # 控制面单二进制（内嵌前端）+ 双端 agent jar
# 或分别构建： make web（前端） · make build（控制面，含前端） · make agent（agent jar）
```

> 需 Go 1.26+、Node + pnpm（构建前端）、JDK 21（构建 agent）。

### 业务插件接入 agent

业务插件不直连控制面，而是 `compileOnly` 依赖只读 SDK、运行期由 `BeaconAgent` 提供：

```kotlin
repositories { mavenLocal() /* 或贵方私有远程仓库 */ }
dependencies {
    compileOnly("top.wcpe.beacon:beacon-agent-api:0.18.0") // 只读契约
    compileOnly("top.wcpe.beacon:beacon-agent-kit:0.18.0") // 便捷门面（推荐）
}
```

接入步骤、最小示例与回退判据见 [docs/SDK.md](docs/SDK.md)。

## 仓库结构

```
Beacon/
├── cmd/beacon/          # Go 入口
├── internal/            # 控制面实现：server / handler / service / repository /
│                        #   runtime / merge / model / store / sse / metrics / secret …（单向分层）
├── web/                 # React(Vite+TS) 管理台，dist/ 被 go:embed 内嵌
├── agent/               # Kotlin/TabooLib：agent-core / -api / -kit / -bukkit / -bungee / -adapters
├── test/e2e/            # 跨平台 Go E2E（自管控制面 + 真 Paper/Waterfall）
├── docs/                # 入库文档：PRD / ARCHITECTURE / API / ADR / OPERATIONS …
├── Dockerfile  docker-compose.yml  Makefile
└── .tmp/                # 过程文档（不入库）
```

## 文档导航

| 文档 | 说明 |
|---|---|
| [docs/PRD.md](docs/PRD.md) | 产品需求（目标 / 角色 / 功能需求 / 验收） |
| [docs/ROADMAP.md](docs/ROADMAP.md) | 第二版路线图（版本线 / 阶段目标 / GA 准入） |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | 架构设计：控制面/数据面、数据模型、机制、部署 |
| [docs/API.md](docs/API.md) | REST 契约（agent 侧 + admin 侧） |
| [docs/SDK.md](docs/SDK.md) | 业务插件接入指南 |
| [docs/adr/](docs/adr/) | 架构决策记录（为什么自研、为什么 Go、为什么去 Redis …） |
| [docs/OPERATIONS.md](docs/OPERATIONS.md) | 运维手册（部署 / 升级 / 备份恢复 / 排障 / 测试运行） |
| [docs/CONTRIBUTING.md](docs/CONTRIBUTING.md) | 演进与维护指南（文档随代码更新、防漂移、分支模型） |
| [SECURITY.md](SECURITY.md) | 安全说明（信任模型、密钥、鉴权边界） |
| [CHANGELOG.md](CHANGELOG.md) | 更新日志 |

## 技术栈

后端 Go + chi + GORM（MySQL / SQLite，可切 Postgres）；前端 React(Vite + TS) + shadcn-ui，经 `go:embed` 内嵌为单二进制同端口；agent Kotlin + TabooLib（Gradle）。

## 第二版路线

- **Legacy（0.1.0 - 0.19.x）**：第一版探索期，历史冻结，不再作为第二版验收基准。
- **P0（0.20.x）**：规格冻结、旧入口维护态、第二版 PRD / 路线图对齐。
- **P1（0.21.x）**：Agent 身份、注册确认、namespace 隔离、区服权威模型。
- **P2（0.22.x）**：采样、健康值、调度决策、本机 agent-api。
- **P3（0.23.x）**：每连接明细、跨服消息、payload 审计、拓扑链路。
- **P4（0.24.x）**：热冷归档、冷查询、归档清理。
- **P5（0.25.x）**：核心集群管理页面重做。
- **P6（0.26.x）**：可观测、系统设置、演示模式。
- **P7（0.27.x）**：配置中心 V2。
- **P8（0.28.x）**：文件树预览 V2。
- **P9（0.29.x）**：文件同步 V2。
- **P10 RC（0.30.x）**：RC 稳定、兼容性冻结、GA 准入。

> 版本线细节以 [docs/ROADMAP.md](docs/ROADMAP.md) 为准，各 FR 的状态与验收标准以 [docs/PRD.md](docs/PRD.md) 为准。

## 约定

- 所有注释、日志、提交信息**使用简体中文**（见 [.claude/rules/](.claude/rules/)）。
- 简单优先：不引入无明确收益的重型件；第二版面向 1000+ 子服规模时优先采用分页、批处理、流式处理和可审计的后台任务。

## 许可

本项目采用 [MIT 许可证](LICENSE)。
