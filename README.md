# Beacon

> 面向 Minecraft 服务器集群的自研控制面 —— 配置中心 · 服务发现 · 健康检查（"MC 版 Nacos"）。

Beacon 是一个独立的后端控制面：用 **Go** 提供 API、内嵌 **React** 管理台，以 **docker-compose** 单节点部署；Minecraft 的 BungeeCord 代理与 Bukkit 子服各跑一个轻量 **Kotlin/TabooLib agent** 接入。它为整个集群提供**集中配置（动态热更、版本回滚）、服务注册/发现、健康检查**，并以"配置 + 拓扑"的形式支撑分小区、虚拟大区与合区。

> **当前状态**：第一期开发中，仓库与最小可运行骨架已落地——控制面可起服连库并返回环境列表，前端（Vite/React）与双端 agent（Kotlin/TabooLib）骨架均可构建，`docker compose up` 可起 beacon + mysql。管理台与配置中心等能力随后续里程碑逐步完善。详见 [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)。

## 为什么是独立服务而非代理插件

代理（BC）是玩家入口（数据面）；控制面是管理面。二者**故障域必须隔离**：控制面崩溃绝不能拖垮玩家入口，玩家入口崩溃时控制面仍能改配置/回滚。因此 Beacon 是独立进程，BC/Bukkit 仅跑轻量 agent。**控制面挂 ≠ 数据面挂**：agent 持本地配置快照 fail-static，控制面不可用时按快照继续运行，绝不阻断玩家进服。

## 架构一览

```
                 浏览器 ──HTTP──┐
                               ▼
   ┌──────────────────────────────────────────────┐
   │  Beacon 控制面（单 Go 二进制 + 内嵌 React）       │  单节点
   │  /admin/v1 管理台 API      /beacon/v1 agent API  │
   │  内存真源：注册表 + 健康 TTL + 长轮询 waiters     │  ← 注册 / 健康
   │  MySQL 真源：配置 / 版本 / zone 分配 / 审计       │  ← 配置权威
   └──────────────────────────────────────────────┘
        ▲ REST（注册 / 心跳 / 长轮询拉配置 / 上报）
        │
  ┌─────┴───────┬───────────────┐
  ▼             ▼               ▼
agent          agent           agent     （全新 Kotlin/TabooLib，只报 serverId）
Bukkit 子服    Bukkit 子服     Bungee 代理   本地快照 fail-static
```

- **真源切分**：注册/健康在 Go 进程内存（map+RWMutex）；配置/版本/分配/审计在 MySQL（GORM，可切 Postgres）。二者互不阻塞。
- **无 Redis/MQ**：单节点 + REST 长轮询即可（见 [ADR-0003](docs/adr/0003-no-redis-in-mvp.md)）。

## 能力

**第一期（MVP）**：配置中心（namespace/group/dataId + 覆盖链）· 动态热更 · 配置版本/回滚 · 服务注册/发现 · 健康检查 · React 管理台 · 审计。
**后续（P2/P3）**：配置灰度 · 流量调度 · 版本发布编排（蓝绿/滚动）· 虚拟合区运行时 · 鉴权/加密 · 控制面 HA。

## 规划中的仓库结构

```
Beacon/
├── cmd/beacon/            # Go 入口
├── internal/             # 控制面实现（server/handler/service/repository/runtime/merge/model/store）
├── web/                  # React(Vite+TS) 管理台，dist/ 被 go:embed
├── agent/               # Kotlin/TabooLib：agent-core / agent-bukkit / agent-bungee
├── docs/                # 入库文档（架构、API、ADR）
├── Dockerfile  docker-compose.yml
└── .tmp/                # 过程文档（PRD、实施计划），不入库
```

## 文档导航

| 文档 | 说明 |
|---|---|
| [docs/PRD.md](docs/PRD.md) | 产品需求（活文档：目标 / 角色 / 功能需求 / 验收） |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | 架构设计：控制面/数据面、数据模型、机制、部署 |
| [docs/API.md](docs/API.md) | REST 契约（agent 侧 + admin 侧） |
| [docs/adr/](docs/adr/) | 架构决策记录（为什么自研、为什么 Go、为什么去 Redis …） |
| [docs/CONTRIBUTING.md](docs/CONTRIBUTING.md) | 演进与维护指南（文档如何随代码更新、防漂移、分支模型） |
| [docs/OPERATIONS.md](docs/OPERATIONS.md) | 运维手册（部署 / 升级 / MySQL 备份恢复 / 排障） |
| [SECURITY.md](SECURITY.md) | 安全说明（信任模型、密钥、鉴权边界） |
| [CHANGELOG.md](CHANGELOG.md) | 更新日志 |

> 实施计划等**易朽过程文档**置于 `.tmp/`（不入库）；文档治理见 [docs/CONTRIBUTING.md](docs/CONTRIBUTING.md)。

## 快速开始

> 骨架已可运行（管理台功能随第一期里程碑逐步完善）：

```bash
cp .env.example .env      # 配置 MySQL 密码、agent token
docker compose up -d      # 起 beacon + mysql
# 管理台与 API 同端口： http://localhost:8848
# 验证：GET http://localhost:8848/admin/v1/namespaces 返回 prod/test 两个环境
```

## 约定

- 所有注释、日志、提交信息**使用简体中文**（见 `.claude/rules/`）。
- 简单优先：50 服规模，不引入 Redis/MQ/DI 框架等重型件。

## 许可

本项目采用 [MIT 许可证](LICENSE)。
