# Beacon 架构设计（第二版）

> 本文是**第二版（0.20.x 起）架构真源**：面向 MC 集群的调度中间件控制面「怎么建」的总览与导航，与 [PRD.md](PRD.md)（要什么）、[ROADMAP.md](ROADMAP.md)（何时建）、[UX.md](UX.md)（前端交互）、[API.md](API.md)（REST 契约）、[specs/](specs/)（各域权威规格）、[adr/](adr/)（决策）配套。细节一律引用规格 / ADR，不在此复制表结构、端点表与状态机正文。
>
> **Legacy 边界**：`v0.1.0` – `v0.19.x` 为第一版探索期，实现冻结在仓库中（`web/` 与现有 Go 代码在 P1 迁移 / 冻结前仍是物理现状）；其第一版架构描述以 git 历史与 Legacy ADR 追溯，本文不再维护第一版细节。

## 1. 定位与边界

Beacon 是**集群调度中间件的控制面（control plane）**：集中存储"事实"（身份、区服权威、健康、调度决策、消息追踪、配置与交付编排、审计），对外提供查询 / 下发与编排，**不写任何游戏逻辑**。BungeeCord 代理与 Bukkit / Paper 子服是**数据面（data plane）**：跑玩家与游戏逻辑，各接一个轻量 agent。

| | 控制面（Beacon） | 数据面（BC / Bukkit + agent） |
|---|---|---|
| 职责 | 身份确认、区服权威、健康值与调度决策、消息单跳中转、配置 / 文件资产 / 变更单编排、审计告警 | 跑游戏、采样上报、执行交付与生效、向业务插件提供本机 agent-api |
| 变更频率 | 低频（分钟级管理操作） | 高频（秒级采样与玩家行为） |
| 形态 | 独立 Go 进程 + 内嵌 React 管理台，单二进制 | Kotlin/TabooLib 插件 |
| 故障影响 | 管理与调度决策暂不可用，**玩家照常进服**（agent fail-static） | 玩家受影响（真正的入口单点） |

四条硬边界（PRD §3，违反即架构漂移，见 [.claude/rules/architecture-invariants.md](../.claude/rules/architecture-invariants.md)）：

- **fail-static**：agent 持本地快照（候选缓存、配置、身份），控制面不可用时按快照降级继续，绝不阻断玩家进服、绝不阻塞 MC 主线程。
- **业务插件只走本机 agent-api**：调度候选、健康事实、跨服消息一律经 agent 本机门面获取；直连 Beacon HTTP 不作为契约、随时可变（[API.md](API.md)「agent-api」节）。
- **namespace 强隔离**：注册 / 调度 / 消息 / Agent 操作 / 配置 / 变更单六个面默认禁止跨 namespace，跨域仅经后台显式单向信任关系（capability 级）开放并额外审计；配置与变更单绝对禁止跨域（[v2-namespace-isolation.md](specs/v2-namespace-isolation.md) §4）。
- **首次接入人工确认**：新 agent 注册后处于待确认态，未确认、未分配区服前不可调度（[v2-agent-identity.md](specs/v2-agent-identity.md)）。

## 2. 仓库布局与模块

### 2.1 monorepo 工作区布局（随 P1 · 0.21.x 落地，FR-173）

pnpm workspace + Turborepo，`turbo run lint / test / build` 全仓一键：

```
apps/
  server/      Go 控制面（cmd/ + internal/ 已迁入；go:embed、Makefile、CI、脚本同步适配）
  agent/       Kotlin/TabooLib agent
  web/         第二版管理台（新建，栈见 §6；发布二进制只嵌它）
  ui-wiki/     UI 控件博物馆（每个 @beacon/ui 导出控件必有展示页，覆盖率进 CI 门禁，FR-175）
packages/
  ui/          @beacon/ui 通用控件包（新管理台组件一律取自此，禁止页面私建通用控件）
  devmock/     MSW handlers 按 API 域组织，浏览器与测试双端共享（P2 全量 mock 底座）
  eslint-config/ typescript-config/   共享工程配置（静态检查最严档三线，FR-176）
```

当前物理现状：Go 控制面在 `apps/server/`，agent 在 `apps/agent/`，UI 包与控件博物馆已提升到 `packages/ui` 与 `apps/ui-wiki`；Legacy 前端仍在 `web/`（P1 起整体冻结不演进，FR-138）。布局与栈的决策见 [ADR-0060](adr/0060-monorepo-layout-and-v2-frontend-stack.md)（monorepo 与第二版前端栈）、[ADR-0061](adr/0061-strictest-static-analysis-three-lines.md)（静态检查最严档三线）。

### 2.2 控制面（Go）分层原则（沿用）

单 Go module、单二进制，分层 `router → handler → service → repository` 依赖单向向下；handler 不碰 GORM 与内存结构；进程内可变运行态单列 `runtime` 域（注册表 / 健康 / 长轮询 hub 等），由 main 装配注入 service，锁独立不嵌套、DB IO 一律在锁外；合并 / 校验 / 脱敏等纯逻辑做成无副作用叶子包便于穷举单测。多表写在 DB 事务内原子完成，**事务提交成功后**才触发长轮询 / SSE 唤醒。

### 2.3 agent（Kotlin）五模块抽象（沿用 [ADR-0005](adr/0005-agent-transport-codec-abstraction.md)）

`agent-api`（纯 Java8 只读契约，业务插件 compileOnly）/ `agent-core`（平台无关核心，零具体库依赖，transport·codec 只依赖接口）/ `agent-adapters`（OkHttp + kotlinx 适配器，唯一碰具体库）/ `agent-bukkit` / `agent-bungee`（双端打包）。agent 自管身份文件，不依赖 CoreLib；阻塞 IO 一律 TabooLib async，绝不上 MC 主线程。

## 3. 领域模型（概览）

权威表结构统一定义在 [v2-zone-authority.md](specs/v2-zone-authority.md) §3（**全仓建表约定 + 权威实体表**），其余规格按表名 / 字段引用、不得复制另定。

**建表约定要点**（zone-authority §3.1）：表名单数 snake_case（GORM 显式 `TableName()`）；主键 `id` BIGINT 自增经 GORM 抽象；**不建 DB 级外键**，引用完整性由 service 层事务校验；枚举落 VARCHAR + 应用层校验、json 落 TEXT、时间 UTC；禁 MySQL 专有特性（ENUM/SET/JSON 列、分区表语法），必须可切 Postgres；v2 内容指纹统一 sha256。

| 实体 | 一句话职责 | 状态机 / 枚举权威 |
|---|---|---|
| `namespace` | 强隔离边界；持接入 token 哈希（明文仅创建 / 轮换时一次性返回） | [v2-namespace-isolation.md](specs/v2-namespace-isolation.md) |
| `namespace_trust` | 单向互通信任行（from → to + capability），收回 / 复活复用同一行 | capability / status 枚举归 namespace-isolation §3 |
| `env` + `env_namespace` | 纯展示 / 过滤维度，一 env 映射 1..N namespace；不参与隔离、调度与作用域链 | zone-authority §4.1 |
| `bc_cluster` / `region` / `zone` | 区服结构三层（BC 集群 → 大区 → 小区）；zone 是调度单元，名在 namespace 内唯一 | zone-authority §4.6 |
| `server` | 子服 / BC 节点；kind 双挂归属（proxy→bc_cluster、backend→zone），含默认入口与排空标记；**归属只由控制面指派**（[ADR-0004](adr/0004-zone-authority-control-plane.md) 延续，agent 不声明 zone） | zone-authority §3.6 / §4 |
| `agent_identity` | agent 首启身份与 `namespace + serverId` 的绑定；列形态锁在 zone-authority，状态机在身份域 | [v2-agent-identity.md](specs/v2-agent-identity.md) §4.3 |

配置作用域链与之完全同构：`namespace → bc_cluster → region → zone → server` 五层低到高覆盖（[v2-config-center.md](specs/v2-config-center.md) §4.1）。其余各域实体（指标批 / 健康快照 / 调度决策、连接 / 消息、归档任务、配置文件 / 层版本、文件资产、变更单三层）由各自规格 §3 权威定义。

## 4. 存储架构

- **MySQL + GORM 可移植**（沿用）：遵 §3 建表约定，禁一切方言专有；MySQL 与 sqlite（E2E 基线）行为一致，可切 Postgres。
- **热库 / 归档库双 database**（[v2-hot-cold-archive.md](specs/v2-hot-cold-archive.md)）：热库存近期数据；到保留期由进程内归档器（goroutine 定时器，无外部调度组件）**先归档、行数 + 抽样哈希校验通过后、才删热库**。归档落同 MySQL 实例独立 database `beacon_archive`（同名同构建表），配置预留独立归档 DSN 可迁出；归档库不可达仅降级归档能力、不阻断控制面启动。冷查询默认只查热库，显式 `includeArchived=true` 才跨热 / 冷合并（挂各查询域端点）。
- **日期后缀分表**：大流量时序数据一律 `<基名>_YYYYMMDD` 日表、首写当日按需建表（禁分区表语法）——指标域 `metric_sample` / `health_snapshot` / `sched_decision`（[v2-metrics-health-scheduling.md](specs/v2-metrics-health-scheduling.md) §3），连接消息域 `conn_detail` / `msg_trace` / `msg_payload`（[v2-connection-message-storage.md](specs/v2-connection-message-storage.md) §3.1，payload 与元数据分表、可按不同保留期归档）。跨表查询映射为日表集合逐表游标合并，强制时间窗 / 精确 ID 防全量扫描；配置版本等低频小表不分表、不归档。
- **真源切分**（沿用）：注册 / 在线 / 健康实时态的真源 = Go 进程内存（健康周期快照另入库仅供回放）；身份、区服权威、指标批、调度决策、连接消息、配置、变更单、审计等事实真源 = MySQL。两者不互为权威、不互相阻塞。
- **写入纪律**：agent 批量上报走「请求线程只校验入有界内存队列即回 202、后台 worker 批量入库」，队列满 429 退避；禁请求主线程长耗时（PRD NFR）。

## 5. 通信架构

base path 分面与跨域通用约定（认证、错误体、分页、命名风格）的权威真源在 [API.md](API.md)「第二版契约草案 · 通用约定」，端点明细在各规格 §5（全域索引亦见 API.md）：

| 面 | base path | 机制基调 |
|---|---|---|
| 管理面 | `/admin/v2/*` | 登录令牌 / API 密钥（full / readonly）；实时进度用 SSE（如变更单 events） |
| agent 面 | `/beacon/v2/agent/*` | `X-Beacon-Token`（namespace 级）+ `X-Beacon-Identity`（注册期另带 `X-Beacon-Boot`）；身份确认前仅可调 register / registration |
| 流式数据面 | `/beacon/v2/stream/*` | 同 agent 面双 header + blob 归属校验；当前仅交付域 |

- **agent 面 REST + 长轮询基调**（沿用 [ADR-0006](adr/0006-rest-long-poll-push.md)）：状态长轮询无变化 304（身份 registration）、队列长轮询无消息 204（消息 poll）；agent 命令下发沿用既有长轮询命令通道，v2 各域只登记新命令类型（如 `asset_rescan` / `asset_read`）、不另建通道，命令 payload 与审计 detail 绝不携带文件内容。
- **跨服消息经控制面单跳中转**（[v2-connection-message-storage.md](specs/v2-connection-message-storage.md) §4）：上行 REST `messages/send`、下行长轮询 `messages/poll` + `ack` 回执，不引入 Redis / MQ。此决策**将由新 ADR 取代 Legacy [ADR-0016](adr/0016-agent-cross-server-messaging-middleware.md)**（其 Redis 通道与第二版禁 Redis 冲突），新 ADR 在 P5 开工前补写（已拍板 2026-07-07，不静默违背）。
- **交付数据面：流式 HTTP + 控制面中转 blob**（[v2-delivery-orchestration.md](specs/v2-delivery-orchestration.md) §5.3）：命令通道只做编排；文件内容由模板源 agent 流式 PUT 到控制面 blob 存储（sha256 寻址去重）、目标 agent 流式 GET（Range 断点续传），HEAD 判存在性与断点。
- **控制面不直连 agent**：管理面一切对 agent 的动作（重扫、取内容、交付回执）经命令通道 + agent 面回传端点完成，agent 不开端口、控制面不反向连接。

## 6. 前端架构（apps/web，随 P1 建立）

- **技术栈**：Vite + React Router + TanStack Query（服务器状态）+ Zustand（客户端状态）+ react-i18next + MSW（经 `packages/devmock`，浏览器与测试共享 handlers）。服务器态 / 客户端态归属边界写入规范（FR-174）。
- **UI 供给**：组件一律取自 `packages/ui`（@beacon/ui）；每个导出控件在 apps/ui-wiki 有展示页，覆盖率检查进 CI 门禁（FR-175）。
- **mock-first 交付**：P2 以演示模式做出全量 mock 管理台（FR-159 / FR-172），页面数据形状只依赖 API 契约草案、mock 覆盖空态 / 常规 / 超大量 / 异常四形态、逐页过 mockup 评审门拍板；P3 起后端按域落地时**同阶段把对应页面从 mock 切真并真机验收**，禁止攒到最后统一联调（[ROADMAP](ROADMAP.md) §4）。
- **信息架构**：顶层总览 + 集群 / 可观测 / 交付 / 系统四大域，页面清单、唯一职责与全局交互契约以 [UX.md](UX.md) §2 / §4 为真源；评审门规则见 [.claude/rules/ux-spec.md](../.claude/rules/ux-spec.md)。

## 7. 关键时序（简要）

- **agent 首次接入（人工确认）**：agent 首启生成并持久化身份文件 → 携 token + identityId 注册 → 控制面落待确认、出现在 `/servers` 待确认列表 → 管理员 approve（可同时落区）/ reject → agent 经 registration 长轮询秒级感知 → 确认且分配后方可调度。冲突 / 禁用 / 解绑同一状态机（[v2-agent-identity.md](specs/v2-agent-identity.md) §4）。
- **换区工单**：已分配服改归属必须解绑 + 重新人工确认——`server-rezones` 整批事务解绑、清归属、记预填目标 → agent 自动重入待确认（换区中不可调度）→ 管理员重确认按预填落区 → 调度候选 / 配置作用域 / 拓扑按分配变更契约重算（[v2-zone-authority.md](specs/v2-zone-authority.md) §4.7 / §4.5）。
- **配置发布经变更单灰度生效**：配置中心只管编辑 / 校验 / 版本，保存不下发（[v2-config-center.md](specs/v2-config-center.md)）→ 变更单把模板源文件差异 + 配置版本绑成一单 → 影响预览 → 审批 → 启动后按批次推进：流式 blob 推送 → 生效（restart / hot_reload / push_only）→ 观察窗 → 人工放行下一批；失败率 / 健康恶化自动熔断，支持暂停 / 紧急终止与整单回滚（文件备份还原 + 配置版本回退 + 重新生效，[v2-delivery-orchestration.md](specs/v2-delivery-orchestration.md) §4）。
- **消息追踪链路**：业务插件经 agent-api `send` / `call` → agent 面上行 → 控制面同事务写 `msg_trace`（+ 可选 `msg_payload`）→ 目标 agent 长轮询取走、`ack` 回执更新状态与 hops → 管理台按 messageId 追链路；payload 查看必须权限 + 填原因 + 先审计（[v2-connection-message-storage.md](specs/v2-connection-message-storage.md) §4）。

## 8. 架构块 → 阶段映射（对齐 [ROADMAP.md](ROADMAP.md) §1）

| 架构块 | 阶段 · 版本线 |
|---|---|
| §2.1 monorepo 布局、apps/web 脚手架、UI 博物馆、静态检查三线、Legacy 前端冻结；§3 权威实体落库 + 身份确认 / namespace 隔离 / 区服权威基础闭环 | P1 · 0.21.x |
| §6 全量 mock 管理台（四大域 IA + 演示模式，逐页拍板） | P2 · 0.22.x |
| 集群管理页接真深化：`/servers` `/zones` `/namespaces` 接入 P1 v2 API，补齐换区工单、冲突处置、zone-tree 与 env 映射体验 | P3 · 0.23.x |
| 指标采样入库、健康值、调度决策、本机 agent-api（接真 `/dashboard` `/service-analysis`） | P4 · 0.24.x |
| §5 消息单跳中转（新 ADR 取代 ADR-0016）、连接明细日表、payload 审计（接真 `/topology` 与可观测页） | P5 · 0.25.x |
| §4 热冷归档双库、冷查询、归档清理（接真系统设置页） | P6 · 0.26.x |
| 配置中心 V2（五层作用域、版本、校验） | P7 · 0.27.x |
| 文件资产 V2（清单索引、预览、安全审计） | P8 · 0.28.x |
| 交付编排 V2（变更单、流式数据面、灰度生效、整单回滚） | P9 · 0.29.x |
| 契约冻结、RC 验收、GA 准入 | P10 · 0.30.x → GA 1.0.0 |

## 9. 关键裁决与不做项

- 不引入 Redis / MQ / DI 框架 / 分布式一致性组件（[ADR-0003](adr/0003-no-redis-in-mvp.md) 精神延续）；编排推进由控制面进程内驱动 + 状态落 MySQL。
- 不建插件制品库，变更单载荷只来自黄金模板源与配置中心；不做自动依赖解析与蓝绿切换（PRD §1.3）。
- 不用命令通道传大文件；不做跨 namespace 的配置与变更单。
- 控制面不实现游戏玩法（经济 / 匹配 / 传送 / 跨服看人 UI），只做决策、编排与事实存储。
- 技术栈锁定：Go + chi + GORM、React（Vite + TS）内嵌单二进制、agent Kotlin/TabooLib；换栈 / 换框架走新 ADR（[ADR-0002](adr/0002-go-react-embedded-stack.md) 延续）。
