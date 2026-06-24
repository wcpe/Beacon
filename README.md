# Beacon

> 面向 Minecraft 服务器集群的自研控制面 —— 配置中心 · 服务发现 · 健康检查（"MC 版 Nacos"）。

[![version](https://img.shields.io/badge/version-v0.13.0-blue)](CHANGELOG.md)
[![license](https://img.shields.io/badge/license-MIT-green)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](go.mod)
[![CI](https://github.com/wcpe/Beacon/actions/workflows/ci.yml/badge.svg)](https://github.com/wcpe/Beacon/actions/workflows/ci.yml)

Beacon 是一个独立的后端控制面：用 **Go** 提供 API、内嵌 **React** 管理台、编译为**单个二进制**（管理台与 API 同端口）；Minecraft 的 BungeeCord 代理与 Bukkit 子服各跑一个轻量 **Kotlin/TabooLib agent** 接入。它为整个集群提供**集中配置（动态热更、版本回滚）、服务注册/发现、健康检查**，并以"配置 + 拓扑"的形式支撑分小区、虚拟大区与合区。

**控制面挂 ≠ 数据面挂**：agent 持本地配置快照 fail-static，控制面不可用时按快照继续运行，绝不阻断玩家进服。

## 核心特性

- **配置中心**：namespace / group / dataId 四层 scope 覆盖链与深合并；动态热更（长轮询 + SSE 推送、秒级生效）；版本历史与一键回滚；发布前 schema/类型校验；敏感配置 AES-256-GCM at-rest 加密；配置灰度（cohort 定向 + promote/abort）。
- **服务注册 / 发现**：agent 自动注册上报，按 role / zone / group / 自定义 tag 过滤发现；进程内存为真源，无外部依赖。
- **健康检查**：online → degraded → lost → offline 分级状态机；进入异常态主动告警（站内信 + webhook，通道可扩展）。
- **zone 指派**：serverId 由 agent 上报、zone 归属由控制面 DB 权威指派；管理台看板式拖拽归派/改派。
- **文件树托管 / 配置导入**：整文件 blob 托管并镜像落 agent 真实 dataFolder；可正向上传一份 plugins 目录，或从在线实例反向抓取，ingest 为组/实例级覆盖。
- **可观测**：内嵌 Dashboard（人数 / TPS / 内存 / CPU 趋势、BC 代理专属指标、集群拓扑图）；Prometheus `/metrics`；全量操作审计可查。
- **管理台**：React + shadcn-ui；配置中心 VS Code 风格编辑器（Monaco），含生效预览与逐键来源 provenance。
- **鉴权**：管理面 Bearer 登录令牌；运行时 API 密钥（full / readonly 两级角色，只读密钥对任何写端点一律 403）。
- **数据面 agent**：Kotlin/TabooLib 双端（Bukkit / Bungee），fail-static、env 覆盖配置、内置可选跨服消息中间件；并提供只读 SDK 供业务插件接入。
- **简单优先**：面向约 50 服规模，单节点 + REST/SSE，**不引入 Redis / MQ / DI 框架**（见 [ADR-0003](docs/adr/0003-no-redis-in-mvp.md)）；数据库经 GORM 抽象，MySQL / SQLite，可切 Postgres。

> 完整功能需求与验收见 [docs/PRD.md](docs/PRD.md)，逐版变更见 [CHANGELOG.md](CHANGELOG.md)。

## 为什么是独立服务而非代理插件

代理（BC）是玩家入口（数据面）；控制面是管理面。二者**故障域必须隔离**：控制面崩溃绝不能拖垮玩家入口，玩家入口崩溃时控制面仍能改配置 / 回滚。因此 Beacon 是独立进程，BC / Bukkit 仅跑轻量 agent，并持本地快照 fail-static。

## 架构一览

```
                 浏览器 ──HTTP──┐
                               ▼
   ┌──────────────────────────────────────────────┐
   │  Beacon 控制面（单 Go 二进制 + 内嵌 React）       │  单节点
   │  /admin/v1 管理台 API      /beacon/v1 agent API  │
   │  内存真源：注册表 + 健康 TTL + 长轮询/SSE waiters │  ← 注册 / 健康
   │  MySQL 真源：配置 / 版本 / zone 分配 / 审计       │  ← 配置权威
   └──────────────────────────────────────────────┘
        ▲ REST 注册/心跳/拉配置/上报 · SSE 变更推送
        │
  ┌─────┴───────┬───────────────┐
  ▼             ▼               ▼
agent          agent           agent     （Kotlin/TabooLib，只报 serverId）
Bukkit 子服    Bukkit 子服     Bungee 代理   本地快照 fail-static
```

- **真源切分**：注册 / 健康在 Go 进程内存（map + RWMutex）；配置 / 版本 / 分配 / 审计在 MySQL（GORM，可切 Postgres）。二者互不阻塞。
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
    compileOnly("top.wcpe.beacon:beacon-agent-api:0.13.0") // 只读契约
    compileOnly("top.wcpe.beacon:beacon-agent-kit:0.13.0") // 便捷门面（推荐）
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

## 能力分期

- **第一期（MVP，已交付）**：配置中心（覆盖链 + 热更 + 版本/回滚）· 服务注册/发现 · 健康检查 · zone 指派 · React 管理台 · 审计 · 双端 agent。
- **P2（多数已交付）**：鉴权 + 只读 API 密钥 · 配置加密 · 文件树托管/导入 · 可观测看板与告警 · 流量调度决策 · 跨服消息中间件 · 配置灰度 · 集群拓扑。
- **P3（规划中）**：版本发布编排（蓝绿/滚动）· 虚拟合区运行时玩家通道 · 控制面 HA。

> 各 FR 的状态与验收标准以 [docs/PRD.md](docs/PRD.md) 为准。

## 约定

- 所有注释、日志、提交信息**使用简体中文**（见 [.claude/rules/](.claude/rules/)）。
- 简单优先：约 50 服规模，不引入 Redis / MQ / DI 框架等重型件。

## 许可

本项目采用 [MIT 许可证](LICENSE)。
