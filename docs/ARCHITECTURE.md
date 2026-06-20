# Beacon 架构设计

> 面向 MC 集群的自研控制面：配置中心 + 服务发现 + 健康检查。本文是第一期（MVP）的架构真源，与 [API.md](API.md)、[adr/](adr/) 配套。

## 1. 定位与边界

Beacon 是**控制面（control plane）**：集中存储"事实"（配置、拓扑、注册/健康）并对外提供查询/下发，**不写任何游戏逻辑**。Minecraft 的代理与子服是**数据面（data plane）**：跑玩家与游戏逻辑，各接一个轻量 agent。

| | 控制面（Beacon） | 数据面（BC/Bukkit + agent） |
|---|---|---|
| 职责 | 配置中心、版本/回滚、服务发现、健康、zone 指派、审计 | 跑游戏、路由玩家、应用配置 |
| 变更频率 | 低频（分钟级） | 高频（秒级） |
| 形态 | 独立 Go 进程 + 内嵌 React | Kotlin/TabooLib 插件 |
| 故障影响 | 管理暂不可用，**玩家照常进服**（agent fail-static） | 玩家受影响（真正的入口单点） |

**核心原则：控制面挂 ≠ 数据面挂。** agent 持本地配置快照，控制面不可用时按快照继续运行，绝不阻断玩家。

分小区 / 虚拟大区 / 合区**不是专门引擎**，而是数据 + 下游解释：
- **分小区** = Beacon 权威记录"哪台服属于哪个 zone"（`zone_assignment`）+ 发现按 zone 过滤。
- **虚拟大区 / 合区** = 一份普通配置对象（如 `topology/merge = {大区A:[zone1,zone2]}`），版本化、可热推；下游业务插件订阅后自己实现跨服行为。Beacon MVP 不做运行时玩家数据通道。

## 2. 模块与依赖

控制面单 Go module、单二进制，分层 `router → handler → service → repository`，依赖单向向下；进程内运行态单列 `runtime` 域。

```
cmd/beacon/main.go                 # 装配 + 启动
internal/
  config/      Beacon 自身配置（yaml + env 覆盖）
  server/      router / 中间件（中文日志、recover、traceId、agent token、管理面登录令牌）
  auth/        管理面鉴权叶子包：凭据校验 + 无状态 HMAC 签名令牌签发/校验 + 操作者上下文（见 ADR-0009）
  secret/      敏感配置加解密叶子包：AES-256-GCM 原语，密钥由调用方从 env 注入（见 ADR-0018）
  render/      统一响应体与错误体写出 + traceId 上下文（handler 与 server 共用的叶子包）
  apperr/      带业务码与 HTTP 状态的领域错误（叶子包，供各层共用，避免反向依赖）
  embedweb/    服务内嵌前端 + SPA 回退处理器（内嵌指令 //go:embed all:web/dist 置于根包 embed.go，因 Go embed 不能跨上级目录）
  handler/     仅请求编解码 → 调 service（无业务逻辑）
  service/     事务、规则校验、触发长轮询唤醒、写审计
  repository/  各表纯 GORM CRUD
  runtime/     registry.go(内存注册) health.go(TTL扫描) longpoll/hub.go(waiter 注册 + 唤醒)
  sse/         event.go(SSE 事件编码纯函数，server→agent 推送 FR-24，与 merge 平级)
  metrics/     Prometheus 指标：独立 registry + 注册/健康 gauge collector(抓取时读内存注册表) + 发布/推送 counter + /metrics handler(见 ADR-0020)
  merge/       merge.go(深合并) codec.go(yaml/json/properties) digest.go(md5)
  filetree/    resolve.go(通道B 整文件覆盖 + manifest + fileTreeMd5，纯函数，与 merge 平级)
  model/       GORM 实体 + enums
  store/       db.go(GORM 连接 + AutoMigrate) logger.go
  pkg/log/     中文分级日志
web/           React(Vite+TS) + shadcn-ui（Tailwind v4，默认 neutral 主题，组件源码入库 src/components/ui/）+ Monaco 编辑器（`@monaco-editor/react`，配置中心页面使用 VS Code 风格布局：左侧资源管理器树 + 右侧 Monaco 编辑器 + 底部历史修订面板），dist/ 被内嵌（设计系统见 ADR-0012；配置中心为单页面固定布局 `h-screen overflow-hidden`，详情用独立路由页/Sheet/Dialog，不内联展开）；新增 Dashboard 页（FR-32，[ADR-0023](adr/0023-control-plane-observability-dashboard.md)）：总览卡片（总玩家数 / 平均 TPS·内存·CPU）+ 每服明细 + 趋势图（近 1h / 6h / 24h），复用既有 React / shadcn 栈；zone 分配页采用看板式归派（FR-35，纯 UI 增强 FR-8）：左侧未指派 server 卡片池 + 右侧按大区分桶的 zone 容器，拖卡指派/改派、拖回未指派取消（@dnd-kit 拖放，复用既有 `PUT/DELETE /zones/assignments`、后端零改动；onDragEnd 落点解析为纯函数 `resolveDragAction`）
agent/         Kotlin/TabooLib，五模块（实现 ADR-0005 抽象层）：
                 agent-api（纯 Java8 只读契约，业务插件 compileOnly）/ agent-core（平台无关核心，零具体库依赖：
                 transport·codec 接口 + BeaconApiClient + 生命周期 + 快照 + applier + 退避）/
                 agent-adapters（OkHttp + kotlinx 适配器，唯一碰具体库）/
                 agent-bukkit（打包 BeaconAgent jar）/ agent-bungee（打包 BeaconAgentProxy jar）
```

`runtime` 是唯一持有可变全局态的域，由 `main.go` 装配后注入 service（依赖注入，不手写有状态单例）。`merge` 全为无副作用纯函数，便于穷举单测。

## 3. 数据模型（MySQL / GORM）

通用约定：`id BIGINT PK`（GORM `autoIncrement`）、`created_at/updated_at`、软删 `deleted_at`；时间统一 UTC；**禁用 MySQL 专有特性**（枚举落 `VARCHAR`+应用层校验、json 落 `TEXT`、不写 `gorm:"type:..."` 方言类型），切 Postgres 仅改 driver + DSN。

| 表 | 职责 | 要点 |
|---|---|---|
| `namespace` | 环境隔离（prod/test） | `code` 唯一 |
| `config_item` | 配置项标识 + scope 维度 + 当前版本指针 | 见下 |
| `config_revision` | 每次发布的不可变快照（append-only） | 回滚 = 读旧版内容作新版发布，`source_revision` 记来源 |
| `config_gray` | 配置灰度 / Beta（FR-9）：某 `config_item` 的临时灰度版本 + cohort 名单 | 一个 item 至多一个未软删灰度；promote 把内容并入 `config_revision` 后软删、abort 直接软删（[ADR-0021](adr/0021-config-gray-cohort-version-selection.md)） |
| `file_object` | 文件树托管（通道B）整文件 blob + scope 维度 + 当前版本指针 | 见下；与 `config_item` 平行但**整文件覆盖、不深合并**（[ADR-0010](adr/0010-file-tree-hosting-blob-channel.md)） |
| `file_revision` | 文件每次发布的不可变快照（append-only） | 与 `config_revision` 同款回滚思路 |
| `file_override_set` | 三方插件文件覆盖集（FR-15）：目标插件目录 + 成员文件 + 一条受限重载命令 | scope 维度同 `file_object`；命令执行已接入运行期、随 agent 本地白名单生效（见 [ADR-0011](adr/0011-third-party-file-override-and-restricted-reload-command.md) 与 §8） |
| `file_override_set_revision` | 覆盖集每次发布的不可变快照（append-only） | 同款回滚思路 |
| `zone_assignment` | serverId → (group, zone) 权威指派 | `(namespace, server_id)` 唯一，换区改这一行 |
| `audit_log` | 审计（append-only） | `operator/action/target/detail(json文本)/result` |
| `instance` | 注册元数据镜像 | **MVP 不建**，运行态以内存为准，仅注册写一条 audit |
| `metric_sample` | 指标时序样本（FR-32）：按间隔采在线实例的负载快照 | 时序表，与配置/版本/审计等事实表并列、真源属 DB；带保留期滚动清理，见 §7.1 与 [ADR-0023](adr/0023-control-plane-observability-dashboard.md) |

`config_item` 关键字段：`(namespace_code, group_code, data_id, scope_level, scope_target)` 唯一定位覆盖链中的一格；`content` + `content_md5` 冗余在行上（热路径直读）；`current_revision`、`version`（单调递增，回滚也 +1）、`enabled`；`sensitive`（为真则 `content` 加密落库，at-rest，FR-20，见 [ADR-0018](adr/0018-config-encryption-at-rest.md)）；`gray_version`（灰度发布乐观锁版本，发布前以其做 CAS 串行化同一 item 的并发灰度发布、从源头消除「先软删后建」在 `uk_gray_item` 上的死锁，FR-9，内部令牌不外泄）。`scope_level ∈ {global, group, zone, server}`；global 层 `group_code='__GLOBAL__'`（保留字）。`content_md5` 始终基于**明文**（敏感项解密后再算），`config_revision` 同步带 `sensitive` 并对敏感快照同样加密。

`file_object` 关键字段：`(namespace_code, group_code, path, scope_level, scope_target)` 唯一定位覆盖链中的一格（唯一键含 `path`）；`content`（整文件文本，落 `TEXT` 经 GORM size 抽象不绑方言）+ `content_md5` 冗余在行上；`current_revision`、`version`、`enabled`。同 `config_item` 的 scope 维度，但解析为**整文件覆盖**（取覆盖链上拥有该 `path` 的最高层那份，见 §5.1）。

`config_gray` 关键字段：`config_item_id`（关联所属配置项，进唯一键 + 软删哨兵 → 一个 item 至多一个未软删灰度）；`namespace_code`（供按 ns 批量取活跃灰度，避免 N+1）；`content` + `content_md5`（灰度内容，敏感项与所属 item 镜像加密，md5 按明文算）；`cohort`（目标 serverId 名单，**JSON 数组文本落 `TEXT`**，可移植可读）；`format`/`sensitive`/`operator`/`comment`。灰度作用在"版本选择"层而非新增覆盖层（见 §5、[ADR-0021](adr/0021-config-gray-cohort-version-selection.md)）。

`metric_sample` 关键字段：`id`、`namespace`、`server_id`、`role`（`bukkit`/`bungee`，落 `VARCHAR`）、`sampled_at`（采样时刻 UTC）、`player_count`、`tps`、`mem_used`、`mem_max`、`cpu_load`，以及 **bc（bungee 代理）专属可空列**（FR-34，[ADR-0025](adr/0025-bc-proxy-metrics-and-netty-traffic.md)）：`proxy_conn`（代理连接数 INT）、`thread_count`（JVM 线程数 INT）、`uptime_ms`（运行时长 BIGINT）、`backend_up`/`backend_total`（后端可达/总数 INT）、`backend_avg_latency_ms`（后端平均延迟浮点，`-1`=不可用）。**全部基础类型**（计数 / 浮点 / 时间），枚举如 `role` 落 `VARCHAR` + 应用层校验，**禁 JSON/ENUM 列与方言专有 SQL**、经 GORM 抽象（守 DB 可移植，可切 Postgres）；BC 列 `NOT NULL DEFAULT`，AutoMigrate 加列对既有行兼容、bukkit 行恒为默认值。它是**时序样本表**（与 §7.1 采样器配套），与配置 / 版本 / 审计等事实表并列、真源属 DB；趋势端点（§4）按时间窗 + 聚合粒度查询本表，保留期到期样本被滚动清理（FR-32，[ADR-0023](adr/0023-control-plane-observability-dashboard.md)）。趋势降采样与 summary 的**平均 TPS / 平均 CPU 仅统计 `role=bukkit`**（bungee 作纯代理 tps 恒为 0，不进这两个平均的分母）；总玩家数 / 平均内存仍计全部样本；summary 的 `bc` 维度聚合**仅统计 `role=bungee`**（代理数 / 连接 / 线程 / 后端可达性·延迟），与 bukkit 聚合分流互不影响（FR-34）。网络吞吐入/出字节本期不采（BungeeCord 无干净 Netty 注入点，标待定，见 ADR-0025）。

**软删唯一键**：`deleted_at` 默认值用**固定哨兵** `1970-01-01 00:00:00`（非 NULL）并纳入唯一键，软删时填真实时间——避免 NULL 不参与唯一比较导致"未删重复挡不住"，且 MySQL/Postgres 行为一致（见 [ADR-0008](adr/0008-config-soft-delete-and-effective-md5.md)）。`file_object` 同款哨兵软删。

## 4. REST 接口（概览，详见 [API.md](API.md)）

- **agent 侧 `/beacon/v1/agent/*`**：`register`（只报 serverId，Beacon 解析回填 group/zone）、`heartbeat`、`stream`（FR-24 单条 SSE 推送流，合并三通道变更通知 + 连接即对账）、`config/effective`/`files/manifest`/`files/content`（通道B 文件树）、`override-sets`/`override-sets/content`（FR-15 三方覆盖集投递；后三组退化为 SSE 通知后的"按 md5 取内容"端点）、`report`、`discovery`。
- **admin 侧 `/admin/v1/*`**：登录 / 登出（`auth/login` / `auth/logout`，各记一条 `auth.login` / `auth.logout` 审计；登出仅留审计痕迹，令牌无状态不可吊销）、配置 CRUD/发布/回滚/diff/历史、实例与健康、zone 分配、审计（含按操作者过滤，FR-30）、namespace（建环境记 `namespace.create` 审计）、指标看板（`metrics/summary` 聚合快照 + `metrics/trend` 历史趋势，FR-32）、控制面自身状态（`system/status`：版本/运行时长/DB 连通/在线实例数/采样器状态 + Go 运行时资源，供页眉展示，区别于 FR-32 的 agent 网络聚合，FR-33）。
- **运维侧 `/metrics`**：Prometheus 文本格式运行指标（注册数/健康分布/配置发布与推送累计），与 agent 端点同属内网信任面、不挂管理台鉴权（FR-30，见 [ADR-0020](adr/0020-prometheus-metrics-observability.md)）。
- 统一错误体 `{code, message, traceId}`；agent 端 `X-Beacon-Token` 仅防误连（非安全边界，语义不变）。
- **管理面鉴权**（自 P2 前移本批，见 [ADR-0009](adr/0009-control-plane-auth-pulled-forward.md)）：单操作者登录换无状态 HMAC 签名令牌，`/admin/v1/*`（登录除外）经令牌中间件校验，认证操作者注入 context；写操作 `operator` 以认证身份为准入审计，取代前端手填值。凭据/密钥走 env、不落库（不引 Redis/会话存储，遵简单优先）。
- **敏感配置 at-rest 加密**（FR-20，见 [ADR-0018](adr/0018-config-encryption-at-rest.md)）：标记 `sensitive` 的配置项 `content` 以 AES-256-GCM（标准库）加密落库（`config_item`/`config_revision` 的 `content` 列存 `enc:v1:` 前缀的 base64 密文），加解密只在 `internal/repository` 两个配置仓库的写/读边界发生——**service 层始终只见明文**，md5 / scope 合并 / 发布前 schema 校验零改。密钥仅从 env `BEACON_CONFIG_ENCRYPTION_KEY`（base64 的 32 字节）读取，绝不入库 / 不入仓 / 不打日志；库中已有敏感项却无密钥 → 控制面 fail-fast 拒绝启动。解密后下发明文到 agent（数据面内网可信不变，agent 不持密钥）。是 FR-26 经 Beacon 下发 Redis 密码的前置。

## 5. 有效配置解析（scope 覆盖链）

agent 只给 `(namespace, serverId)`，服务端按 `zone_assignment` 解析出 `(group, zone)`，拉 global/group/zone/server 四层候选，**按 dataId 键级深合并**：

- 优先级 `global < group < zone < server`，高层覆盖低层。
- 标量覆盖、map 递归深合并、**list 整体替换**、**高层显式 `null` = 删键**。
- 仅对结构化格式（yaml/json）做键级合并；properties 按整 key 覆盖。
- 序列化键序固定，保证相同输入恒得相同 md5（长轮询比对依赖此幂等）。
- **整体 md5 = `md5(concat(dataId + ":" + 单dataId_md5))`**，把 dataId 名纳入哈希，避免集合碰撞误判（见 [ADR-0008](adr/0008-config-soft-delete-and-effective-md5.md)）。
- 发布时做结构化 parse 校验（坏 yaml/json 拒绝发布，不推爆全网）；同一 dataId 跨层 format 必须一致。
- 发布前再做 schema/类型校验（FR-27）：非空内容顶层必须是键值映射（拒裸标量/列表根，否则深合并会整体冲掉其它层）、键必须非空（递归），不通过返 `CONTENT_SCHEMA_INVALID`。校验为 `merge.ValidateSchema` 纯函数、由 service 层 `validateContent` 统一接入（`Create`/`Publish`/`Rollback` 三条写路径全覆盖），handler 不碰校验细节；规则刻意保守，不引入按 dataId 的字段级 schema 注册表。

agent 收到的是**已合并的有效配置文本**，不感知覆盖链。

**admin 有效预览（FR-22，[ADR-0013](adr/0013-admin-effective-config-preview-and-provenance.md)）**：`GET /admin/v1/configs/effective` 复用同一解析，额外给出**逐键来源层 provenance**（每个叶子键最终来自 global/group/zone/server 的哪层、哪些键被 `null` 减量删除），供管理台「服务器视角 / 文件覆盖矩阵」展示"这台最终生效什么、每个值来自哪层"。provenance 经 `merge` 包的**平行纯函数** `MergeDataIDWithProvenance` 计算，**不改 `DeepMerge`/`MergeDataID` 这条 agent 热路径**，并以"合并结果与 `MergeDataID` 逐一致"的交叉测试防双实现漂移。只读、不挂长轮询、不强制注册。

**配置灰度叠加（FR-9，[ADR-0021](adr/0021-config-gray-cohort-version-selection.md)）**：灰度作用在"某 dataId 用哪个版本的内容"这一**版本选择**层，与 scope 覆盖链**正交叠加**——不新增覆盖层、不改 `merge` 纯函数。解析时拉完四层候选后，按 `namespace + 候选项集合`**一次性**取活跃灰度（`config_gray`，Map 命中、**无 N+1**），对"该 config_item 存在灰度且当前 `serverId` 在其 cohort 名单内"的候选项，把参与合并的 `content` 临时替换为灰度内容；其余层、合并算法、md5 计算全不变——**名单外 `serverId` 的解析结果与无灰度时逐字节相同**。admin 预览与 agent 热路径共用同一叠加逻辑，保证 cohort 内预览与下发一致。操作侧：`promote` 把灰度内容作为新稳定版本发布（version+1，走既有发布路径，过 FR-27 校验 / FR-20 加密）并软删灰度；`abort` 直接软删灰度。两者事务内写表 + 审计原子完成，**提交后按受影响 `serverId` 唤醒**（发布 / abort 唤醒 cohort 名单、promote 唤醒 item scope ∪ cohort 名单），复用既有长轮询 / SSE 唤醒集合，绝不全量盲唤醒。

### 5.1 有效文件树解析（通道B，scope 整文件覆盖）

文件树托管（通道B，[ADR-0010](adr/0010-file-tree-hosting-blob-channel.md)）与配置中心平行但语义不同——文件按相对 `path` 整文件覆盖，**绝不深合并**：

- 同 `(namespace, serverId)` 解析路径，拉 global/group/zone/server 四层候选文件；**按 `path` 分桶，每个 path 取覆盖链上层级最高的那一份整文件**（优先级同上）。
- 服务端算出 `manifest`（path→md5）+ 独立的 `fileTreeMd5`；`fileTreeMd5 = md5(concat(path + ":" + 单文件md5))`，把 `path` 名纳入哈希防集合碰撞（沿用 ADR-0008 思路）。
- `fileTreeMd5` 与有效配置 md5 **相互独立**，各自长轮询唤醒集合分开（见 §6），互不触发无谓重算。
- agent 比对本地已落盘 manifest，仅取/删变更文件，镜像落盘到插件真实 dataFolder（原子写，见 §8）。
- 解析逻辑落 `internal/filetree` 纯函数包（与 `merge` 平级、无副作用），便于穷举单测。

## 6. 动态热更：REST 长轮询（"唤醒即重算比对"）

无 Redis/MQ，纯进程内通知：

1. agent 带当前有效配置 md5 发起 `GET .../config/effective`。
2. 服务端**先注册 waiter，再算当前 md5**：不等则立即返回新配置（摘除 waiter）；相等才 `select` 挂起（含超时与客户端断连）。此顺序消除"注册前发布丢唤醒"窗口（P0 修正）。
3. 管理台发布/回滚/改派，**事务提交成功后**按 scope 算**最小受影响 serverId 集合**（global→该 ns 全部、group→该 group、zone→反查 DB `zone_assignment`、server/改派→单 serverId），仅唤醒集合内 waiter。
4. 被唤醒的 goroutine **重跑解析比对 md5**：真变才 200 下发，未变（被高层覆盖）则继续挂起。
5. 无变更到超时 → 304，agent 立即续杯。

内存结构：`waiters map[ns+serverId][]*Waiter`，每 waiter 一个缓冲为 1 的 notify channel；Registry / Hub / Health **三锁独立不嵌套**，DB IO 全在锁外，结构上杜绝死锁。

文件树托管（通道B）复用同一 `longpoll.Hub` 实现，但**另起一个独立 Hub 实例**：agent 走 `GET .../files/manifest` 带 `fileTreeMd5` 长轮询，文件发布只唤醒文件 Hub 的 waiter、配置发布只唤醒配置 Hub 的 waiter（唤醒集合独立）；唯 zone 改派同时影响两通道归属，故同时唤醒两 Hub。被唤醒返回的是 `manifest`（path→md5，不含内容），agent 再据差异逐个 `GET .../files/content` 取整文件。

### 6.1 单条 SSE 推送流（FR-24，[ADR-0015](adr/0015-sse-server-push-transport.md)，取代 [ADR-0006](adr/0006-rest-long-poll-push.md)）

把 配置/文件树/覆盖集 三条 server→agent 长轮询**合并为一条 SSE 推送流** `GET .../stream`（每 agent 往外连接由 ~4 降到 ~2：1 条 SSE + 心跳）：

- **流只发"变更通知"、不搬数据**：事件是轻量 JSON（`config-changed`/`file-changed`/`override-changed` + 新 md5），agent 收到后**用现有 `config/effective`、`files/manifest`、`override-sets` 端点取内容并应用**（取数据-应用逻辑不变，改的只是"如何得知有变更"）。blob/文件内容仍走 HTTP GET，不进流。
- **连接即对账，绝不丢更新**：agent 建流时上报各通道当前 md5（`configMd5`/`fileMd5`/`overrideMd5`），控制面 `StreamService` **先注册两 Hub 的 waiter（先注册后算，消除注册前发布丢唤醒窗口）→ 比对上报 md5 与当前 md5、对落后通道立即补发 `*-changed`（补齐断线期间落下的增量）→ 发 `ready` → 转直播**。直播阶段复用上面的"最小受影响 serverId 集合"唤醒（`cfgWaiter`/`fileWaiter` 的 `NotifyChan()` 在 `select` 中多路等待），被唤醒即重算比对、真变才发通知。这替代了长轮询天然自愈（每轮带 md5 比对）的能力。
- **健康判活独立于流活性**：online/lost/offline（§7）仍由独立心跳 + TTL 判定，**不**用"SSE 断开"判失联（抖动断流但服务器健在 → 误杀）。两者解耦。
- **fail-static 不破**：流断 → agent 按本地快照继续、玩家无感，带退避重连、重连即对账。
- **传输抽象（守 [ADR-0005](adr/0005-agent-transport-codec-abstraction.md) / 不变量 #5）**：core 新增 `StreamTransport`/`StreamEvent` 端口与纯逻辑 `SseFrameParser`（按空行分帧、注释行心跳忽略），SSE 客户端实现 `OkHttpStreamTransport`（纯 HTTP 读流、无 netty/无重型件）只在适配器。控制面 SSE 事件编码为纯函数 `internal/sse`，保活发 SSE 注释行（`: ping`），响应头带 `X-Accel-Buffering: no` 关反代缓冲。
- **迁移期兼容**：注入 `streamTransport` 时 agent 以单条 SSE 流取代三条长轮询循环；未注入则退回三条长轮询（[ADR-0015](adr/0015-sse-server-push-transport.md) 决策 8）。远程命令、[FR-29](PRD.md) 拓扑 watch 作为消费者复用本流（各自独立 FR）。
- **拓扑 watch（FR-29）接入本流**：新增 `topology-changed` 事件类型与一个 namespace 级唤醒 Hub（`topologyHub`，与配置/文件 Hub 同构、独立锁）。`StreamService` 在每条流上额外注册一个拓扑 waiter 并维护 namespace **拓扑摘要**（`runtime.TopologyDigest`：对"可用集合"按 `serverId|role|group|zone|status|address` 排序后取 md5，运行指标如 playerCount/tps 不入摘要）；实例上线/下线（注册 / 手动下线 / 健康转 lost·offline）/ 改派 zone 四处变更点经 `ChangeNotifier.NotifyTopologyChange(ns)` 唤醒该 namespace 全部拓扑 waiter，被唤醒即重算摘要、**真变才推**（摘要未变不推）。事件 `data` 仅携新摘要、不搬实例数据——agent 收到 `topology-changed` 后重查 `discovery` 端点取最新拓扑（守控制面/数据面边界：控制面只发"拓扑事实变更通知"）。

## 7. 服务注册 / 发现 / 健康

- **注册**：agent 只报 serverId + 元数据标签（`role/version/capacity/weight` + 自定义 metadata，**capacity/weight 为一等字段，metadata 仅 `map<string,string>`**，无 canary —— 对应 P0 修正）。Beacon 按 `zone_assignment` 解析回填 group/zone 写入内存注册表。
- **bc 后端归属事实**（FR-36，[ADR-0024](adr/0024-bc-backend-membership-as-fact.md)）：`runtime.Instance` 增 `backends []string` 字段——**仅 bc（bungee）非空**，存其当前代理的后端子服 serverId 集合（取自 agent 侧 `ProxyServerDirectory`，含 Beacon 注入 + 手工子服）。agent 经 register / report 附加可选 `backends` 上报（仅 bc 填、bukkit 恒空、旧 agent 缺键向后兼容；report 用「缺键不动 / 显式才刷新」区分），控制面**只存内存事实、随注册/上报刷新、不落 DB**（与注册/健康真源同源），经 `Registry` 锁内深拷贝更新、不涉 DB IO。实例视图（§实例与健康）输出 `backends` 供拓扑 bc→bukkit 连线消费（FR-37）。控制面只展示该事实、不据它做任何调度 / 连接决策（守「只存事实」边界）。
- **重复 serverId 守卫**：按 `lastHeartbeat` 新鲜度判定 —— 旧条目超心跳周期未续约视为僵尸，允许新 address 顶替并告警；仍新鲜的不同 address 才拒绝（409）。避免故障换机被误杀（P0 修正）。
- **健康**：单后台 goroutine 定期扫描，按心跳陈旧度推进 `online → degraded → lost → offline`（阈值 `degraded-after-sec < ttl-sec < offline-grace-sec` 可配，FR-28）；收到心跳即从任意异常态回 online。offline 条目保留不移除（管理台可见历史），手动下线才移除。
- **健康告警**（FR-28，[ADR-0019](adr/0019-health-alert-channel-abstraction.md)）：实例**进入异常态**（degraded/lost/offline）时主动告警，恢复 online 不告警。告警出口抽象为 `Alerter` 接口，`Dispatcher` 扇出到多个通道并**逐通道兜错**（某通道失败仅 WARN、不阻断扫描），第一版实现**站内信**（`InboxAlerter`，进程内环形缓存、独立锁不嵌套、管理台经 `GET /admin/v1/instances`… 同前缀的 `/admin/v1/alerts` 只读）与 **webhook**（`WebhookAlerter`，HTTP POST 告警 JSON，IO 在扫描循环里、不持注册表锁）；新增通道只实现 `Alerter` 接入。告警不落库（健康事件的派生，与"注册/健康真源在内存"一致，重启清零）。
- **发现**：按标签（zone/group/role/status + 自定义元数据 `tag.<key>`，多 tag 取交集，FR-29）过滤实例；agent 侧 `discovery` 返回**可用集合**（`online`+`degraded`），管理台 `/admin/v1/instances` 可按 status 任意过滤。agent 侧走 `/beacon/v1/agent/discovery`（归 agent 前缀 + token，P0 修正）。要实时感知拓扑变化订阅 §6.1 SSE 流的 `topology-changed` 事件（SDK `discovery().watch(listener)`），不必轮询。
- **Proxy 目录注入（服务发现延伸出口）**：BeaconAgentProxy 注册成功后周期调用 `discovery` 同步同 namespace 下 `role=bukkit` 且在线的实例，以 `serverId` 作为 Bungee `ServerInfo` 名称、以 agent 上报 `address` 作为连接地址，自动创建/更新**仅由 Beacon 管理**的服务器条目；若同名条目已由手工 Bungee 配置存在，则 WARN 并跳过、不覆盖手工配置。控制面只提供发现事实，不操作玩家连接，不引入持久化任务队列；控制面失联时按本地已注入目录继续（fail-static）。
- **流量调度（FR-10，落位均衡 / drain，[ADR-0017](adr/0017-traffic-scheduling-decision-vs-execution.md)）**：控制面**只给调度决策（query-only），不执行玩家连接**。`SchedulingService` 提供两件事：① **落位建议**——给定 `(namespace, group?, zone)`，读内存注册表（在线实例）+ DB drain 集合，经无副作用纯函数 `RankPlacement` 仅纳入 `online` 且未 drain 的实例、按 `weight` 降序 → `capacity` 降序 → `serverId` 升序确定性排序，返回候选事实（serverId/address/weight/capacity）供数据面据此落位；**不读** agent 上报的 `playerCount`/`tps`（二者仅展示、不参与决策），活跃负载精排归数据面。② **drain（排空 / 维护标记）**——运维决策，须跨控制面重启存活、要审计，故落 DB `server_drain` 表（与 `zone_assignment` 同源类别、同软删模式），事务内写表 + 审计原子完成，读落位时叠加剔除候选。注册/健康仍以进程内存为真源，drain 不改变有效配置 / 文件树归属、不触发长轮询唤醒。**canary 引流不做**（范围外，见 ADR-0017）。
- **指标聚合（FR-32，[ADR-0023](adr/0023-control-plane-observability-dashboard.md)）**：`runtime.Instance` 在 `playerCount`/`tps` 之外新增**内存 / CPU** 字段（agent 上报，**与 playerCount/tps 同列健康事实、仅展示不参与决策**，report handler 解析向后兼容旧 agent——缺内存字段缺省 0、缺 `cpuLoad` 缺省 -1.0 哨兵即"不可用"、聚合时剔除不计入平均）。聚合端点（§4）**从内存注册表实时计算**当前快照统计——全集群总玩家数、每服人数、平均 / 分服 TPS·内存·CPU，与发现 / 健康同走读内存真源、不落库、写路径零侵入。

### 7.1 指标采样器（FR-32，时序落 MySQL）

为支撑历史趋势（注册/健康只有"此刻"，见 [ADR-0023](adr/0023-control-plane-observability-dashboard.md)），控制面起一个**指标采样器**：按固定间隔（可配，如 15~30s）取**在线**实例的负载快照（role/playerCount/tps/内存/CPU + bc 专属 proxy 字段）批量写 `metric_sample` 表（`role` 取自注册表 `Instance.Role`，供趋势降采样区分 bukkit/bungee），**DB IO 在运行态三锁之外**（守锁外 IO 约定）；并按**保留期**（可配，如 24h / 7d）滚动清理过期样本，使表体量受上界约束、不无界增长。采样为派生健康事实落库，**不引 TSDB / Redis**（本规模 MySQL 单表 + 保留期清理足够，守简单优先与 DB 可移植）。bc（bungee）实例额外采**代理专属负载指标**（连接 / 线程 / 运行时长 / 后端可达性·延迟，FR-34）经 report `proxy` 子对象上报，落 `metric_sample` BC 列；bukkit 实例 BC 列恒为默认值，采样器照写不特判（[ADR-0025](adr/0025-bc-proxy-metrics-and-netty-traffic.md)）。
- **与 `/metrics` 的关系（FR-30 vs FR-32）**：`/metrics`（[ADR-0020](adr/0020-prometheus-metrics-observability.md)）供**外部**监控系统（Prometheus/Grafana）pull 抓取、不持久化；本看板的采样 + 趋势是 **Beacon 内自带**的可视化与历史（采样持久化到 MySQL、管理台直接看图）。二者面向不同消费者，**并存不冲突、互不取代**。

## 8. agent（数据面接入）

`agent-core` 依赖**抽象接口**而非具体库（见 [ADR-0005](adr/0005-agent-transport-codec-abstraction.md)）：`HttpTransport`（默认 OkHttp 适配器，可换）+ `JsonCodec`（默认 kotlinx.serialization 适配器，可换），由 `BeaconApiClient` 收口五个 REST 语义调用。

生命周期：读 bootstrap（控制面地址 + serverId + env/group 提示 + token + 超时）→ 注册 → 心跳循环 + **单条 SSE 推送流循环**（FR-24，注入 `streamTransport` 时取代配置/文件树/覆盖集三条长轮询循环；未注入则退回三条长轮询）→ 收到 `*-changed` 事件即取内容 → **先写本地快照** → TabooLib reload apply（异步线程，**不阻塞 MC 主线程**）→ `report` 回报 → 流断指数退避重连、重连即对账。控制面不可用时用本地快照继续（接入方业务插件须自带内置默认以防首启无快照）。对同服业务插件暴露 **Java 8 只读 API**（读有效配置 + 查发现/拓扑）。SSE 流细节见 §6.1。

**本地配置 env 覆盖（FR-33，见 [docs/specs/agent-config-env-override.md](specs/agent-config-env-override.md)）**：bootstrap 配置经 `EnvOverridingConfigReader` 装饰 `ConfigReader`——每个标量 / 列表项可被 `BEACON_AGENT_<点分路径大写、点与连字符转下划线>` 环境变量覆盖（env 优先于 `config.yml`，如 `identity.server-id` → `BEACON_AGENT_IDENTITY_SERVER_ID`），与控制面 env 覆盖（§9）对齐、便于容器化注入；`identity.metadata` 动态键 map 仅本地文件。core 仍不依赖具体环境读取（env 以函数注入），守 TabooLib-free。

zone 由控制面权威指派（[ADR-0004](adr/0004-zone-authority-control-plane.md)），agent 不声明 zone，从注册/拉取响应得到自己的归属；换区只改 `zone_assignment` 一行，agent 零改动。

**文件树同步（通道B，FR-14，[ADR-0010](adr/0010-file-tree-hosting-blob-channel.md)）**：注册成功后，agent 在配置长轮询循环之外**并行**启一条文件树长轮询循环（各自 `gen` / 退避，唤醒集合独立）。每轮带本地已落盘清单（`AppliedFileManifestStore`，落 agent 数据目录的 `fileTreeMd5`）发 `GET .../files/manifest`：200 拿到新 `manifest`（path→md5，不含内容）→ `FileSyncer` 纯差分算增/改/删 → 仅对增/改 `GET .../files/content` 取整文件 → `FileMirrorWriter` **原子写**镜像到插件 `plugins` 基目录（临时文件 → `FileChannel.force` 含父目录 fsync → `ATOMIC_MOVE`，补 `SnapshotStore` 未做 fsync 的缺口），删除目标已无的 path，**全部落盘成功后才写已落盘清单**（先文件后清单，崩溃可恢复）；304 续杯；连接失败退避。落盘相对 path 经 `RelativePathGuard` 校验，拒绝绝对/`..`穿越/反斜杠逃逸目标根。**fail-static 比配置更保守**：任一变更文件取内容失败（控制面不可用）即**整轮放弃**——不删任何既有文件、不写清单，下一轮重试，绝不臆测；首启无目标态时同样不动任何已落盘文件。全程经 `adapter.runAsync` 不上 MC 主线程；HTTP/JSON 仅在适配器、core 依 `HttpTransport`/`JsonCodec` 接口（[ADR-0005](adr/0005-agent-transport-codec-abstraction.md)）。

**三方覆盖集命令执行（FR-15，[ADR-0011](adr/0011-third-party-file-override-and-restricted-reload-command.md)）**：覆盖集是通道B 的一个 profile（在镜像落盘之上多做"覆盖前备份 + 覆盖后执行管理台预设的受限重载命令"），仅在文件树托管启用时接线。控制面 `OverrideEffectiveService` 按 scope 覆盖链解析某 server 适用的覆盖集（同名取最高层那份），经 agent-facing 端点 `GET .../override-sets`（长轮询带 `overrideMd5`——指纹含目标根 / 受限重载命令 / 成员 path 清单 **+ 各成员内容指纹**（复用 `file_object.content_md5`、按字节算），故成员「内容只改、不变 path」也改变 `overrideMd5`、触发 agent 重取落盘；复用文件 Hub 唤醒集合，但 md5 维度独立）投递"目标根 + 受限重载命令 + 成员 path"（成员内容指纹仅参与 md5、不投递），成员内容经 `GET .../override-sets/content` 取；覆盖集成员（`file_object.override_set_id>0`）从通用文件树清单排除，避免同 path 双写到错误根。agent 注册成功后并行启 override 长轮询循环（独立 gen/退避、复用单飞），`OverrideSyncApplier` 逐集编排：取齐成员 → `TargetRootSecurity`（agent 侧最终校验目标根落在 `plugins/<plugin>/` 内，防控制面被攻破下发逃逸目标根）→ `OverrideApplier`（`BackupManager` 备份 → `OverridePathSecurity` 成员路径 Path 级校验 → `FileMirrorWriter` 原子覆盖 → `ManagedFileTracker` 受管标记防震荡环）→ 全量落盘成功且命中 `CommandWhitelist`（**agent 本地白名单、默认空、控制面不下发**）才经 `ReloadCommandExecutor` 派发为控制台命令（**禁 shell**：core/适配器无任何进程执行 API，经 `PlatformAdapter.dispatchConsoleCommand`，不上 MC 主线程同步等结果）。**fail-static**：控制面不可用 / 取成员失败 / 目标根非法一律不动既有、不派发命令、不更新基准，下轮向控制面目标态收敛重做（幂等）；**回滚只还原文件、绝不重放重载命令**（命令本身可能就是失败根因）。命令执行 gate 在管理面鉴权之后（[ADR-0009](adr/0009-control-plane-auth-pulled-forward.md)）。

**跨服消息中间件（FR-26，[ADR-0016](adr/0016-agent-cross-server-messaging-middleware.md)）**：agent 内**独立可开关模块**（`messaging.enabled` 默认关），与配置同步/心跳**故障域隔离**（独立 Redis 连接 + 独立线程，Redis 挂不连累配置命脉、玩家照常进服游玩）。为③层业务插件提供与内容无关的通用服务器间传输，四种模式：定向发送（fire-and-forget）/ 请求-响应（RPC，关联ID + 超时）/ 主题发布订阅 / 按玩家所在服寻址。`agent-core` 的 `messaging/` 只依赖**抽象端口**（延续 [ADR-0005](adr/0005-agent-transport-codec-abstraction.md)）：消息信封 `Message`（type/version/correlationId/replyTo/source，演进「只增不改」保新老插件混跑兼容）、传输端口 `MessageTransport`、路由分发 + RPC `CompletableFuture`/超时引擎 `MessageBus`、玩家寻址端口 `PlayerLocator`，**core 不 import Jedis**。Redis 实现 `RedisMessageTransport` 只在 `agent-adapters`：可靠送达走 **Redis Streams + 每服消费组**（`beacon:msg:{serverId}`，目标离线留存、上线经消费组补消费、至少送达一次 + 业务侧幂等，`MAXLEN` 近似裁剪防内存无限增长）；主题与 RPC 回信走 **pub/sub**（`beacon:topic:{topic}` / `beacon:reply:{serverId}`，回信不持久化、靠 RPC 层超时兜底）。**Jedis 经 TabooLib `@RuntimeDependencies` 运行期下载、relocate 到 `top.wcpe.beacon.agent.lib.*`、不 shade、绝不经 CoreLib**（守不变量 #5，决策 14）。Redis 连接（host/port/db/password）由 **Beacon 配置中心下发**（约定 dataId 随有效配置，密码依赖 FR-20 加密先行），双端壳层据下发配置启停/重连，冷启动未取得配置时模块保持降级（`DisabledMessaging`，`isAvailable()=false` 供业务侧优雅降级）。**按玩家寻址依赖玩家位置名册**：BC 上的 beacon-proxy 经 Bungee 进服/换服/退出事件维护 `beacon:player-loc`（hash：玩家→所在子服，换服误删保护 + 重启全量重建），子服 agent 解析后走定向发送；解析落空走「找不到目标」兜底。消息只在 `agent ↔ Redis ↔ agent` 间流动，**控制面永不在消息路径上**；对③层经 `BeaconAgent.messaging()` 暴露 `Messaging` Java API（send/call/publish/subscribe/sendToPlayer/on + isAvailable）。仅通用传输，匹配/对战/存储/排行等**业务功能不在范围**（属③层独立业务插件）。

**玩家位置名册只读查询（FR-31，[ADR-0022](adr/0022-agent-roster-read-api.md)）**：把 FR-26 已躺在 agent 侧 Redis `beacon:player-loc`（玩家名→所在子服）里的「谁在哪个服」**位置事实**，经 agent-api `Discovery` 门面只读暴露给③层业务插件（如跨服看人做总览/人数/Tab 补全）——`roster()` 返回全量名册 `Map<玩家名, serverId>`（单一全局名册 `beacon:player-loc`，不按 namespace 分区，单 BC 前提下即全量），`rosterInZone(group, zone)` 返回某 zone 过滤后名册。`agent-core` 新增**只读端口 `RosterDirectory`**（单一职责「全表读名册」），与既有 `PlayerLocator`（单个解析）分立、不合并，**core 不 import Jedis**（守 [ADR-0005](adr/0005-agent-transport-codec-abstraction.md)）；`DiscoveryView` 组合实现两方法——`roster()` = `RosterDirectory` 全表读，`rosterInZone(group, zone)` = `instancesInZone` 解出该 zone 的 serverId 集合（**zone 归属权威来自控制面 DB**，[ADR-0004](adr/0004-zone-authority-control-plane.md)）∩ 全表名册，**名册本身不臆造 zone**（Redis hash 无 zone 维度，只能由控制面发现结果反查交集）。`RosterDirectory` 的 Redis 实现走 `HGETALL beacon:player-loc` 藏在 `agent-adapters`、复用 FR-26 messaging 模块的连接与线程不另起连接。依赖流向：业务插件 → agent-api `Discovery.roster()/rosterInZone()` → agent-core `DiscoveryView` + `RosterDirectory` → adapters Redis，**控制面不在此路径上**（零改动、不连 Redis、不持有名册）。messaging 模块未开 / Redis 未连上 / 名册为空时返回**空 Map**（非抛异常、非 null）优雅降级。仍只暴露**名册事实**，「看人」业务（聚合/分组/展示/补全/传送）归③层业务插件——事实暴露 ≠ 业务实现（与控制面只读暴露 instances 事实同构）。

**本地运维命令（FR-17，仅本地）**：双端壳注册根命令 `/beacon`（权限 `beacon.admin`）——`status`（打印生命周期状态 / 是否连上 / 有效配置 md5 / 心跳周期 / endpoint）、`reload`（`forcePollNow`：md5=null 强制立刻重拉一次有效配置并经 `ConfigApplier` 幂等守卫 apply，不等长轮询超时）、`reconnect`（`reconnectNow`：重置退避并重新接入，**不清空 store / 快照**以守 fail-static）。`resync`（`forceSyncFileTreeNow`：以 `fileTreeMd5=null` 旁路文件树长轮询 304 强制立刻重拉一次清单并由 `FileTreeApplier` 幂等差分落盘，不接管长轮询主循环、不改其代标识；仅在文件树托管启用（`file-tree.enabled`，FR-14）时生效，未启用则回提示）。命令体经 `adapter.runAsync` 落异步线程，core 控制方法不碰 Bukkit/Bungee（守 [ADR-0005](adr/0005-agent-transport-codec-abstraction.md)）；远程下发依赖鉴权（FR-11），本期不做。**注册单飞不变量**：注册有多触发点（心跳 404 / 长轮询 404 / 退避重试 / `reconnectNow`），由 `AtomicBoolean` 单飞门 + 注册「代」标识收口，保证**任意时刻只有一条 register→loops 在飞**，杜绝瞬时双注册、双循环。

## 9. 部署

docker-compose 仅两容器：`beacon`（单二进制，API 与 UI 同端口）+ `mysql`（mysql healthcheck + beacon `depends_on: service_healthy` + 命名卷持久化）。多阶段 Dockerfile：node 构建前端 dist → `go build` 内嵌（`//go:embed all:dist`）→ alpine 极小镜像、非 root、`CGO_ENABLED=0` 静态链接。前端以相对路径 `/admin/v1` 同源访问（无 CORS）；非 API、非静态文件的路径回退 `index.html`（SPA history）。敏感项（DB 密码、token）走 env，不入库。

**配置加载（`internal/config`，FR-25）**：生效优先级 真实环境变量 > 当前目录 `.env` > `config.yml`（`-config` 指定）> 内置默认。`cmd/beacon` 启动时把内置模板 `config.yml`（默认 sqlite，零依赖可跑，经根包 `//go:embed` 内嵌）释放到当前目录，**释放时把模板里留空的 `auth.password` / `auth.secret` 就地填入 `crypto/rand` 随机强值**（文件 0600、口令不入日志；agent 共享令牌用固定默认 `beacon-bootstrap-token`——仅防误连、非安全边界，与 agent 样例开箱匹配），**已存在则跳过、不覆盖**；随后读当前目录 `.env`（仅注入未设置的键、真实 env 优先），最后 `BEACON_*` 覆盖并校验。**不自动生成 `.env`**——凭据落在 `config.yml`（即真源），避免自动生成的 `.env` 因优先级更高而静默盖掉用户对 `config.yml` 的修改；`.env` 仅当运维手动放置时生效，用手写最小解析、不引第三方库。鉴权仍强制（[ADR-0009](adr/0009-control-plane-auth-pulled-forward.md)）——由 fail-fast 改为首启自助生成 `config.yml` 内的**强随机**凭据（非固定弱默认），使单二进制开箱即跑。

## 10. 关键裁决与不做项

**关键裁决**：自研而非用 Nacos · Go + 内嵌 React 单二进制 · MVP 去 Redis（REST 长轮询）· zone 由控制面 DB 权威指派 · agent 传输/序列化抽象层 · 长轮询"唤醒即重算" · 管理台设计系统用 shadcn-ui + Tailwind（ADR-0012）。每条的背景与理由见 [adr/](adr/)。

**第一期不做（P2/P3）**：配置灰度/Beta、流量调度（落位均衡/canary 引流/drain）、版本发布编排（蓝绿/滚动换 jar）、完整虚拟合区运行时玩家通道、跨服传送/看人/共享经济等运行时玩家数据通道、鉴权/加密、控制面 HA、Redis。玩家状态同步、跨服数据通道与控制面 HA 均按后续 P3 能力处理。
