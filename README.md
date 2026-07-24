# Beacon

> 面向 Minecraft 多群组服务器的集群调度中间件控制面  
> 区服治理 · 健康调度 · 跨服消息 · 可观测审计 · 配置与交付

[![version](https://img.shields.io/badge/version-1.0.0--rc-blue)](CHANGELOG.md)
[![license](https://img.shields.io/badge/license-MIT-green)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](go.mod)
[![CI](https://github.com/wcpe/Beacon/actions/workflows/ci.yml/badge.svg)](https://github.com/wcpe/Beacon/actions/workflows/ci.yml)

Beacon 把多个 **BungeeCord / Velocity 代理** 与 **Bukkit / Paper 子服** 串成可治理的集群：用独立 **Go 控制面**（内嵌 React 管理台，**单二进制同端口**）统一做身份绑定、区服分配、健康调度、跨服消息追踪、审计告警与灰度交付；游戏服只跑轻量 **Kotlin / TabooLib Agent**，业务插件只依赖本机 `agent-api`，禁止直连控制面。

**控制面挂 ≠ 数据面挂**：Agent 持本地快照 fail-static，控制面不可用时按快照继续跑，不阻断玩家进服。

> **发布状态**：仓库正准备 **`v1.0.0` 首个 RC**（根 `VERSION=1.0.0` 表示目标 GA 号）。在 GitHub 上出现真实 `v1.0.0-rc.N` / `v1.0.0` tag 与 Release 之前，**不得视为已正式发布**。在线更新只消费严格 `vX.Y.Z` GA。

---

## 为什么用 Beacon

| 痛点 | Beacon 的做法 |
|------|----------------|
| 多 BC + 上百子服靠配置硬维护 | Web 管理 namespace / BC 集群 / 大区 / 小区 / 子服与默认入口 |
| 误改 serverId 导致区数据串 | 首启 `identityId` 绑定，后台确认后才可调度 |
| 业务插件直连中控难降级 | 只走本机 Agent API；中控挂了可本地快照 |
| 跨服消息、选服失败难查 | 调度决策、消息链路、连接明细与审计可追踪 |
| 插件与配置发布靠手工 | 变更单 + 流式数据面 + 灰度批次与整单回滚 |

---

## 核心能力

- **Agent 自连接与身份绑定** — 地址 / token / namespace / serverId 接入；pending → 人工确认 → active  
- **namespace 强隔离** — 默认禁止跨域调度与消息；跨域须后台显式信任并额外审计  
- **区服治理** — 环境、BC 集群、大区、小区、默认入口、排空（draining）  
- **健康调度** — TPS / CPU / 在线 / 连接 / 告警等综合评分；业务插件 `scheduling()` 取候选  
- **跨服消息** — 定向、RPC、主题广播、按玩家寻址；控制面存元数据与受控 payload（非业务库）  
- **可观测** — 运维总览、服务分析、拓扑、命令 / 审计 / 告警、连接与消息链路  
- **配置与交付 V2** — 作用域配置、文件资产、变更单灰度、热重载 / 重启生效、整单回滚  
- **热冷数据** — 近期热库；过期归档与冷查询；清理前必归档  
- **在线自更新（GA only）** — 单二进制自我替换；只发现正式 GA，不把 RC 当自动更新源  

---

## 架构一览

```
                 浏览器 ──HTTP──┐
                               ▼
   ┌──────────────────────────────────────────────┐
   │  Beacon 控制面（Go 单二进制 + 内嵌 React）       │
   │  /admin/* 管理台 API    /beacon/* agent API     │
   │  内存：在线连接 · 健康 TTL · SSE               │
   │  MySQL：身份 · 区服 · 审计 · 指标 · 归档索引    │
   └──────────────────────────────────────────────┘
        ▲ REST 注册/心跳/拉配置/上报 · SSE 推送
        │
  ┌─────┴───────┬───────────────┐
  ▼             ▼               ▼
 Agent         Agent           Agent     （Kotlin / TabooLib）
 Bukkit        Bukkit          Bungee     本地快照 fail-static
```

设计细节见 [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) 与 [docs/adr/](docs/adr/)。

---

## 快速开始

### 1. 部署控制面

```bash
cp .env.example .env      # 填 MySQL、管理台账号、令牌签名密钥等
docker compose up -d      # beacon + mysql；就绪后 AutoMigrate
# 管理台与 API：http://localhost:8848
```

浏览器打开 `http://localhost:8848`，使用 `BEACON_ADMIN_USERNAME` / `BEACON_ADMIN_PASSWORD` 登录。

也可直接跑单二进制（默认 SQLite、首启释放 `config.yml`）。运维见 [docs/OPERATIONS.md](docs/OPERATIONS.md)。

### 2. 接入 Agent

将对应版本的 **BeaconAgent（Bukkit）** / **BeaconAgentProxy（Bungee）** 放入插件目录，配置控制面地址、namespace token 与 serverId。首次注册后在管理台 **服务器 → 待确认** 中确认并分配区服。

### 3. 业务插件（compileOnly）

```kotlin
repositories { mavenLocal() /* 或贵方私有仓库 */ }
dependencies {
    compileOnly("top.wcpe.beacon:beacon-agent-api:1.0.0") // 与正式 GA / 对齐的 RC 坐标一致
    compileOnly("top.wcpe.beacon:beacon-agent-kit:1.0.0")
}
```

调度、消息、配置读取示例见 [docs/SDK.md](docs/SDK.md)。  
**运行期版本**：部署的 Agent ≥ 编译所用 api/kit 版本。

### 4. 从源码构建

```bash
make package    # 控制面单二进制（内嵌前端）+ 双端 agent jar
# 或：make web · make build · make agent
```

需要 Go 1.26+、Node + pnpm、JDK 21。

---

## 发布与版本

| 阶段 | 标记 | 说明 |
|------|------|------|
| 开发产物 | Actions Artifact | master 质量门通过后上传，保留约 7 天；**非**正式发布 |
| RC | `v1.0.0-rc.N` | 不可变 prerelease；固定 commit 与一次构建资产 |
| GA | `v1.0.0` | 从最终 RC **原样复制**资产并核验 SHA-256，禁止 rebuild |

- 根目录 `VERSION` 是目标正式版本真源。  
- 规则与脚本：`docs/adr/0073-standard-rc-ga-release-lifecycle.md`、`scripts/release/`、`.github/workflows/rc.yml` / `release.yml`。  
- 变更记录：[CHANGELOG.md](CHANGELOG.md)

---

## 仓库结构

```
Beacon/
├── apps/server/     # Go 控制面
├── apps/web/        # 第二版 React 管理台（go:embed）
├── apps/agent/      # Kotlin Agent（bukkit / bungee / api / kit）
├── apps/ui-wiki/    # UI 控件博物馆
├── packages/        # ui · devmock · contracts · 共享配置
├── docs/            # PRD · ROADMAP · ARCHITECTURE · API · ADR · SDK · OPERATIONS
├── scripts/release/ # RC/GA 校验与晋级脚本
├── Dockerfile · docker-compose.yml · Makefile
└── web/             # Legacy 管理台（冻结，不进第二版产物）
```

---

## 文档

| 文档 | 说明 |
|------|------|
| [docs/PRD.md](docs/PRD.md) | 产品需求与 FR 状态 |
| [docs/ROADMAP.md](docs/ROADMAP.md) | 第二版阶段与 1.0.0 RC/GA 路线 |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | 架构 |
| [docs/API.md](docs/API.md) | REST 契约 |
| [docs/SDK.md](docs/SDK.md) | 业务插件接入 |
| [docs/OPERATIONS.md](docs/OPERATIONS.md) | 部署 / 升级 / 备份 / 排障 |
| [docs/adr/](docs/adr/) | 架构决策 |
| [SECURITY.md](SECURITY.md) | 安全边界 |
| [CHANGELOG.md](CHANGELOG.md) | 更新日志 |

---

## 第二版路线（摘要）

- **Legacy 0.1–0.19** — 探索期，冻结  
- **P0–P9（0.20–0.30）** — 规格、工程化、集群、调度、消息、归档、配置、资产、交付编排  
- **0.31 对齐中间版** — 管理台壳层（侧栏 / 页眉 / 指标 / 搜索语言通知刷新）  
- **P10 `v1.0.0-rc.N` → GA `v1.0.0`** — 不可变 RC，原样晋级 GA  

细节以 [docs/ROADMAP.md](docs/ROADMAP.md) 为准。

---

## 许可

[MIT License](LICENSE) · Copyright (c) wcpe
