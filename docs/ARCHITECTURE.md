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
  server/      router / 中间件（中文日志、recover、traceId、agent token）
  render/      统一响应体与错误体写出 + traceId 上下文（handler 与 server 共用的叶子包）
  apperr/      带业务码与 HTTP 状态的领域错误（叶子包，供各层共用，避免反向依赖）
  embedweb/    服务内嵌前端 + SPA 回退处理器（内嵌指令 //go:embed all:web/dist 置于根包 embed.go，因 Go embed 不能跨上级目录）
  handler/     仅请求编解码 → 调 service（无业务逻辑）
  service/     事务、规则校验、触发长轮询唤醒、写审计
  repository/  各表纯 GORM CRUD
  runtime/     registry.go(内存注册) health.go(TTL扫描) longpoll/hub.go(waiter 注册 + 唤醒)
  merge/       merge.go(深合并) codec.go(yaml/json/properties) digest.go(md5)
  model/       6 个 GORM 实体 + enums
  store/       db.go(GORM 连接 + AutoMigrate) logger.go
  pkg/log/     中文分级日志
web/           React(Vite+TS)，dist/ 被内嵌
agent/         Kotlin/TabooLib，五模块（实现 ADR-0005 抽象层）：
                 agent-api（纯 Java8 只读契约，业务插件 compileOnly）/ agent-core（平台无关核心，零具体库依赖：
                 transport·codec 接口 + BeaconApiClient + 生命周期 + 快照 + applier + 退避）/
                 agent-adapters（OkHttp + kotlinx 适配器，唯一碰具体库）/
                 agent-bukkit（打包 BeaconAgent jar）/ agent-bungee（打包 BeaconAgentProxy jar）
```

`runtime` 是唯一持有可变全局态的域，由 `main.go` 装配后注入 service（依赖注入，不手写有状态单例）。`merge` 全为无副作用纯函数，便于穷举单测。

## 3. 数据模型（MySQL / GORM，六表）

通用约定：`id BIGINT PK`（GORM `autoIncrement`）、`created_at/updated_at`、软删 `deleted_at`；时间统一 UTC；**禁用 MySQL 专有特性**（枚举落 `VARCHAR`+应用层校验、json 落 `TEXT`、不写 `gorm:"type:..."` 方言类型），切 Postgres 仅改 driver + DSN。

| 表 | 职责 | 要点 |
|---|---|---|
| `namespace` | 环境隔离（prod/test） | `code` 唯一 |
| `config_item` | 配置项标识 + scope 维度 + 当前版本指针 | 见下 |
| `config_revision` | 每次发布的不可变快照（append-only） | 回滚 = 读旧版内容作新版发布，`source_revision` 记来源 |
| `zone_assignment` | serverId → (group, zone) 权威指派 | `(namespace, server_id)` 唯一，换区改这一行 |
| `audit_log` | 审计（append-only） | `operator/action/target/detail(json文本)/result` |
| `instance` | 注册元数据镜像 | **MVP 不建**，运行态以内存为准，仅注册写一条 audit |

`config_item` 关键字段：`(namespace_code, group_code, data_id, scope_level, scope_target)` 唯一定位覆盖链中的一格；`content` + `content_md5` 冗余在行上（热路径直读）；`current_revision`、`version`（单调递增，回滚也 +1）、`enabled`。`scope_level ∈ {global, group, zone, server}`；global 层 `group_code='__GLOBAL__'`（保留字）。

**软删唯一键**：`deleted_at` 默认值用**固定哨兵** `1970-01-01 00:00:00`（非 NULL）并纳入唯一键，软删时填真实时间——避免 NULL 不参与唯一比较导致"未删重复挡不住"，且 MySQL/Postgres 行为一致（见 [ADR-0008](adr/0008-config-soft-delete-and-effective-md5.md)）。

## 4. REST 接口（概览，详见 [API.md](API.md)）

- **agent 侧 `/beacon/v1/agent/*`**：`register`（只报 serverId，Beacon 解析回填 group/zone）、`heartbeat`、`config/effective`（长轮询）、`report`、`discovery`。
- **admin 侧 `/admin/v1/*`**：配置 CRUD/发布/回滚/diff/历史、实例与健康、zone 分配、审计、namespace。
- 统一错误体 `{code, message, traceId}`；agent 端 `X-Beacon-Token` 仅防误连（非安全边界，鉴权属 P2）。

## 5. 有效配置解析（scope 覆盖链）

agent 只给 `(namespace, serverId)`，服务端按 `zone_assignment` 解析出 `(group, zone)`，拉 global/group/zone/server 四层候选，**按 dataId 键级深合并**：

- 优先级 `global < group < zone < server`，高层覆盖低层。
- 标量覆盖、map 递归深合并、**list 整体替换**、**高层显式 `null` = 删键**。
- 仅对结构化格式（yaml/json）做键级合并；properties 按整 key 覆盖。
- 序列化键序固定，保证相同输入恒得相同 md5（长轮询比对依赖此幂等）。
- **整体 md5 = `md5(concat(dataId + ":" + 单dataId_md5))`**，把 dataId 名纳入哈希，避免集合碰撞误判（见 [ADR-0008](adr/0008-config-soft-delete-and-effective-md5.md)）。
- 发布时做结构化 parse 校验（坏 yaml/json 拒绝发布，不推爆全网）；同一 dataId 跨层 format 必须一致。

agent 收到的是**已合并的有效配置文本**，不感知覆盖链。

## 6. 动态热更：REST 长轮询（"唤醒即重算比对"）

无 Redis/MQ，纯进程内通知：

1. agent 带当前有效配置 md5 发起 `GET .../config/effective`。
2. 服务端**先注册 waiter，再算当前 md5**：不等则立即返回新配置（摘除 waiter）；相等才 `select` 挂起（含超时与客户端断连）。此顺序消除"注册前发布丢唤醒"窗口（P0 修正）。
3. 管理台发布/回滚/改派，**事务提交成功后**按 scope 算**最小受影响 serverId 集合**（global→该 ns 全部、group→该 group、zone→反查 DB `zone_assignment`、server/改派→单 serverId），仅唤醒集合内 waiter。
4. 被唤醒的 goroutine **重跑解析比对 md5**：真变才 200 下发，未变（被高层覆盖）则继续挂起。
5. 无变更到超时 → 304，agent 立即续杯。

内存结构：`waiters map[ns+serverId][]*Waiter`，每 waiter 一个缓冲为 1 的 notify channel；Registry / Hub / Health **三锁独立不嵌套**，DB IO 全在锁外，结构上杜绝死锁。

## 7. 服务注册 / 发现 / 健康

- **注册**：agent 只报 serverId + 元数据标签（`role/version/capacity/weight` + 自定义 metadata，**capacity/weight 为一等字段，metadata 仅 `map<string,string>`**，无 canary —— 对应 P0 修正）。Beacon 按 `zone_assignment` 解析回填 group/zone 写入内存注册表。
- **重复 serverId 守卫**：按 `lastHeartbeat` 新鲜度判定 —— 旧条目超心跳周期未续约视为僵尸，允许新 address 顶替并告警；仍新鲜的不同 address 才拒绝（409）。避免故障换机被误杀（P0 修正）。
- **健康**：单后台 goroutine 定期扫描，按 TTL 推进 `online → lost → offline`；收到心跳即回 online。offline 条目保留不移除（管理台可见历史），手动下线才移除。
- **发现**：按标签（zone/group/role/status）过滤在线实例。agent 侧走 `/beacon/v1/agent/discovery`（归 agent 前缀 + token，P0 修正），管理台用 `/admin/v1/instances`。

## 8. agent（数据面接入）

`agent-core` 依赖**抽象接口**而非具体库（见 [ADR-0005](adr/0005-agent-transport-codec-abstraction.md)）：`HttpTransport`（默认 OkHttp 适配器，可换）+ `JsonCodec`（默认 kotlinx.serialization 适配器，可换），由 `BeaconApiClient` 收口五个 REST 语义调用。

生命周期：读 bootstrap（控制面地址 + serverId + env/group 提示 + token + 超时）→ 注册 → 心跳循环 → 长轮询循环 → **先写本地快照** → TabooLib reload apply（异步线程，**不阻塞 MC 主线程**）→ `report` 回报 → 断连指数退避重连。控制面不可用时用本地快照继续（接入方业务插件须自带内置默认以防首启无快照）。对同服业务插件暴露 **Java 8 只读 API**（读有效配置 + 查发现/拓扑）。

zone 由控制面权威指派（[ADR-0004](adr/0004-zone-authority-control-plane.md)），agent 不声明 zone，从注册/拉取响应得到自己的归属；换区只改 `zone_assignment` 一行，agent 零改动。

## 9. 部署

docker-compose 仅两容器：`beacon`（单二进制，API 与 UI 同端口）+ `mysql`（mysql healthcheck + beacon `depends_on: service_healthy` + 命名卷持久化）。多阶段 Dockerfile：node 构建前端 dist → `go build` 内嵌（`//go:embed all:dist`）→ alpine 极小镜像、非 root、`CGO_ENABLED=0` 静态链接。前端以相对路径 `/admin/v1` 同源访问（无 CORS）；非 API、非静态文件的路径回退 `index.html`（SPA history）。敏感项（DB 密码、token）走 env，不入库。

## 10. 关键裁决与不做项

**关键裁决**：自研而非用 Nacos · Go + 内嵌 React 单二进制 · MVP 去 Redis（REST 长轮询）· zone 由控制面 DB 权威指派 · agent 传输/序列化抽象层 · 长轮询"唤醒即重算"。每条的背景与理由见 [adr/](adr/)。

**第一期不做（P2/P3）**：配置灰度/Beta、流量调度（落位均衡/canary 引流/drain）、版本发布编排（蓝绿/滚动换 jar）、虚拟合区运行时玩家通道、鉴权/加密、控制面 HA、Redis。当前不预留空壳，到时按域新增包。
