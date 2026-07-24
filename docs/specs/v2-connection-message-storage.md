# 规格：连接明细与跨服消息存储（第二版）

> 状态：草拟 · 关联 FR：FR-145, FR-149, FR-150 · 阶段：P5（0.25.x）
>
> 需求真源：`docs/PRD.md` §3「连接与 payload 查询可控」与 FR-145/149/150。共享实体（namespace / server / zone 等）与全仓建表约定以 `v2-zone-authority.md` 为权威，本文只引用不复制。

## 1. 背景与目标

第二版要求跨服通信和玩家连接「可追踪、可排查、可审计」：proxy 侧每一条玩家连接的建立与断开要留痕；每一条跨服消息要能按消息 ID / 来源 / 目标 / 时间还原完整链路，失败有原因；消息 payload 可查但受控——默认只查元数据，看 payload 必须有权限、填原因、写审计。同时这两类数据是全系统写入量最大的表，必须从第一天就按日期后缀分表落库，为 P6 热冷归档（`v2-hot-cold-archive.md`）提供可整表搬移的物理形态，并以强制查询条件防止管理面慢查询拖垮热库。

本规格权威定义：连接明细采集与存储、日期后缀分表机制（三类表共用）、跨服消息传输通道（agent 面）与追踪元数据模型、payload 分日存储与查看流程、拓扑异常链路的数据供给、本域 agent 面与管理面 API。

## 2. 范围

### 2.1 做什么

- proxy 侧 agent 采集每玩家连接的 open / close 事件，批量上报，控制面异步批量入库。
- 日期后缀分表：`conn_detail_YYYYMMDD` / `msg_trace_YYYYMMDD` / `msg_payload_YYYYMMDD` 的建表时机、表名规则、跨表查询边界。
- 跨服消息经控制面中转的传输通道（上行 REST、下行长轮询、回执），及由此产生的追踪元数据（消息 ID、来源、目标、耗时、状态、链路 hops、中转跳数）。
- payload 与元数据分表存储；payload 查看 = 权限点 + 必填原因 + 写审计。
- 管理面查询防护：无精确 ID 时必须带服务器（或玩家）过滤 + 时间范围，禁全量扫描。
- 为 `/topology` 异常链路、`/dashboard` 玩家流 / 连接流提供聚合数据端点。

### 2.2 明确不做

- 消息寻址三种：定向（server）、按玩家（player）、**广播（broadcast，FR-180 / [ADR-0065](../adr/0065-message-broadcast-addressing.md)）**——广播按注册表在线集合 fan-out（本 namespace 全部在线服，或 `targetZone` 定向该 zone），**不建订阅状态表**（订阅是 agent 本地按 topic 分发）、可丢语义（只投在线、离线不补、沿用 TTL / 重投 / 溢出规则）、跨 namespace 广播一律拒绝。不做订阅式 pub/sub 与离线留存（Legacy 的 Redis Streams / pub-sub 通道随 Legacy 冻结，不迁移——第二版禁 Redis / MQ）。
- 不做消息离线补投队列：目标 agent 长时间不在线的消息按 TTL 过期，不做持久化待投（避免控制面变 MQ，违反基座禁重型件）。
- 不做 payload 静态加密、不做 payload 内容检索（只按元数据查，PRD §1.3）。
- 不做连接明细里逐次后端切换的事件流水（只记摘要字段，见 §3.2）；玩家在服内的行为数据一律不采集（控制面禁游戏逻辑）。
- 归档、冷查询、清理不在本域：表结构与命名保证可被 P6 归档器整表识别搬移即止，其余交 `v2-hot-cold-archive.md`。
- 保留期、TTL、查询窗口等参数的运维设置页面归 FR-158（P6）；本规格给出默认值并要求走运维设置热更。

## 3. 数据模型

三类表全部按 UTC 日期后缀分表。落库方言经 GORM 可移植抽象：枚举落 `VARCHAR` + 应用层校验，json 落 `TEXT`，禁 MySQL 专有类型。

### 3.1 日期后缀分表机制（三类表共用）

- **表名规则**：`<基名>_<YYYYMMDD>`（UTC 日期），如 `conn_detail_20260706`。基名固定三个：`conn_detail`、`msg_trace`、`msg_payload`。
- **禁分区语法**：不使用 `PARTITION BY` 等任何数据库分区特性；分片完全靠独立物理表 + 应用层路由（数据库可移植不变量）。
- **建表时机**（双保险）：
  1. 后台任务每日 00:00 UTC 预建「当日 + 明日」两张表；
  2. 写入路径兜底：按需 `CREATE TABLE IF NOT EXISTS`（经 GORM Migrator 以模型 + 动态表名创建，禁手写方言 DDL），进程内缓存「已确认存在」的表名集合避免重复探测。
- **落表日判定**：以行主键 UUIDv7 内嵌时间戳（UTC）所在日期为准——保证「由 ID 即可定位物理表」恒成立。控制面校验 ID 时间与自身时钟偏差：超过 5 分钟记 WARN 日志，超过 24 小时拒绝写入（防 agent 时钟漂移把数据写进错误分片）。
- **跨表查询边界**：时间范围映射为日表集合，应用层逐表查询、按 `(时间, id)` 游标合并分页（不做跨库 UNION 视图）；单次查询允许跨表上限默认 8 张（≈7 天窗口），超限返回 400 并提示缩小范围。表不存在视为该日无数据，跳过不报错。
- **归档衔接**：日表整表归档 / 删除由 P6 归档器执行；本域查询默认只路由热库中存在的日表，`includeArchived` 冷查询路由归 `v2-hot-cold-archive.md`。

### 3.2 `conn_detail_YYYYMMDD`（连接明细，会话行）

每条玩家连接一行（会话行，而非事件行）：open 事件插入行，close 事件更新同一行。close 上报携带 `connId`（UUIDv7），控制面由 ID 定位 open 日所在表更新——跨日长连接始终落在 open 日表。

| 字段 | 类型 | 说明 |
|---|---|---|
| `conn_id` | VARCHAR(36) PK | proxy agent 在 open 时生成的 UUIDv7 |
| `namespace_id` | BIGINT | 所属 namespace（引用基座实体） |
| `proxy_server_id` | VARCHAR(64) | 采集方 proxy 的 serverId |
| `player_uuid` | VARCHAR(36) | 玩家 UUID |
| `player_name` | VARCHAR(16) | 登录时玩家名 |
| `client_ip` | VARCHAR(45) | 客户端地址（IPv4/IPv6） |
| `protocol_version` | INT | MC 协议号 |
| `opened_at` | DATETIME(3) | 连接建立时间（UTC） |
| `closed_at` | DATETIME(3) NULL | 断开时间，未断开为 NULL |
| `duration_ms` | BIGINT NULL | close 时计算的会话时长 |
| `status` | VARCHAR(16) | `open` / `closed`（应用层校验） |
| `close_kind` | VARCHAR(32) NULL | 断开分类：`quit` / `kick` / `timeout` / `proxy_shutdown` / `error` |
| `close_reason` | VARCHAR(255) NULL | 断开原文（超长截断） |
| `first_backend_server_id` | VARCHAR(64) NULL | 首个后端子服 |
| `last_backend_server_id` | VARCHAR(64) NULL | 断开时所在后端子服 |
| `backend_switch_count` | INT | 会话内后端切换次数（摘要，不记逐次流水） |

索引：`(namespace_id, proxy_server_id, opened_at)`、`(player_uuid, opened_at)`、`(status, opened_at)`（对账遗留 open 会话用）。

proxy 宕机产生的「孤儿 open 行」：proxy agent 重启后首次上报携带 `bootId`（本次进程随机 ID），控制面把该 proxy 此前 `status=open` 且 bootId 不同的行批量补 close（`close_kind=proxy_shutdown`）。

### 3.3 `msg_trace_YYYYMMDD`（消息元数据）

每条跨服消息一行。RPC（请求-响应）场景响应本身也是一条消息，经 `correlation_id` 关联请求。

| 字段 | 类型 | 说明 |
|---|---|---|
| `message_id` | VARCHAR(36) PK | 源 agent 发送时生成的 UUIDv7 |
| `namespace_id` | BIGINT | 来源 namespace |
| `source_server_id` | VARCHAR(64) | 来源 serverId |
| `msg_type` | VARCHAR(64) | 业务消息类型（业务插件定义）；非空且 UTF-8 编码 ≤64 字节，冒号始终是合法字符（如 `lodestone:roster`） |
| `target_kind` | VARCHAR(16) | `server` / `player` / `broadcast`（FR-180） |
| `target_server_id` | VARCHAR(64) NULL | 定向目标（target_kind=server） |
| `target_player` | VARCHAR(36) NULL | 按玩家寻址的玩家 UUID |
| `target_zone` | VARCHAR(64) NULL | 广播 zone 级定向（target_kind=broadcast 且指定 zone 时） |
| `fanout_total` | INT NULL | 广播 fan-out 目标数（仅广播行，一条广播一行防 ×N 写放大） |
| `delivered_count` | INT NULL | 广播送达计数（仅广播行） |
| `failed_count` | INT NULL | 广播失败计数（仅广播行） |
| `expired_count` | INT NULL | 广播过期计数（仅广播行） |
| `resolved_server_id` | VARCHAR(64) NULL | 控制面据连接明细名册解析出的实际目标服 |
| `target_namespace_id` | BIGINT NULL | 跨域时的目标 namespace |
| `cross_namespace` | BOOLEAN | 是否跨 namespace（须有 `namespace_trust` capability=`message` 才放行） |
| `correlation_id` | VARCHAR(36) NULL | RPC 关联：请求消息自引用填自身 message_id、响应消息填请求 message_id——按 correlation_id 查询可得整条往返两条（§4.2）；非 RPC 单向消息为空 |
| `status` | VARCHAR(16) | `accepted` / `dispatched` / `delivered` / `failed` / `expired` |
| `fail_reason` | VARCHAR(255) NULL | 失败 / 过期原因（脱敏后文案） |
| `created_at` | DATETIME(3) | 控制面接收时间 |
| `dispatched_at` | DATETIME(3) NULL | 被目标 agent 长轮询取走时间 |
| `delivered_at` | DATETIME(3) NULL | 目标 agent 回执的送达时间 |
| `duration_ms` | BIGINT NULL | created_at → delivered_at 全链路耗时 |
| `hop_count` | INT | 中转跳数 = 链路中转发节点数（v2 经控制面单跳中转恒为 1；按玩家寻址含名册解析仍为 1，字段值由 hops 实算而非写死） |
| `hops` | TEXT | 链路事件 json 数组，见下 |
| `payload_size` | INT | payload 字节数 |
| `payload_stored` | BOOLEAN | payload 是否已落 `msg_payload` 表 |

`hops` 结构（json 数组，按 `seq` 递增）：

```json
[
  {"seq": 0, "node": "lobby-1",  "event": "sent",       "at": "2026-07-06T08:00:00.001Z"},
  {"seq": 1, "node": "beacon",   "event": "received",   "at": "...", "costMs": 3},
  {"seq": 2, "node": "beacon",   "event": "dispatched",  "at": "...", "costMs": 120},
  {"seq": 3, "node": "game-7",   "event": "delivered",  "at": "...", "costMs": 15}
]
```

`event` 枚举：`sent`（源 agent 发出）/ `received`（控制面收到）/ `resolved`（按玩家寻址解析出目标服）/ `dispatched`（目标取走）/ `delivered`（目标业务 handler 处理完）/ `failed`（任一环失败，含原因节点）。`costMs` 为与上一事件的间隔。`hop_count` 统计 `node` 中承担转发职责的节点数（源与终点不计）。

索引：`(namespace_id, source_server_id, created_at)`、`(namespace_id, resolved_server_id, created_at)`、`(status, created_at)`、`(correlation_id)`。

### 3.4 `msg_payload_YYYYMMDD`（payload，与元数据分离）

与同日 `msg_trace` 一一对应（`payload_stored=true` 时存在），同一 DB 事务内写入两表。分表分离的目的：元数据查询永不触碰 payload 数据页；payload 归档 / 清理可与元数据采用不同保留期。

| 字段 | 类型 | 说明 |
|---|---|---|
| `message_id` | VARCHAR(36) PK | 同 `msg_trace.message_id` |
| `payload` | TEXT | 中转 / 保存文本：JSON string 保存业务原文且不含额外 JSON 引号；object / array / number / boolean 保存对应 JSON 文本 |
| `sha256` | VARCHAR(64) | 中转 / 保存文本摘要，供归档校验与完整性核对 |
| `size` | INT | 中转 / 保存文本的 UTF-8 字节数 |
| `created_at` | DATETIME(3) | 写入时间 |

HTTP wire 的 payload 接受任意 JSON 值：object / array / string / number / boolean / null。string 的中转 / 保存文本就是业务原文，不做二次 JSON 编码；object / array / number / boolean 以 JSON 文本中转并保存，poll 下发时还原为原 JSON 类型；null 表示无 payload，不写 `msg_payload` 行。空字符串在 poll 中仍保持空字符串，但因保存文本大小为 0，与 null 一样不写 `msg_payload` 行。payload 上限默认 64KB：Agent 先按 payload 的 JSON 编码文本 UTF-8 字节数做前置校验，控制面再按上述中转 / 保存文本的 UTF-8 字节数做硬校验；因此含大量转义字符的 string 可能被 Agent 更早拒绝。任一校验超限都拒绝发送，不截断存半截。payload 永不写日志、不进审计 detail、不出现在任何列表接口。

## 4. 机制与状态机

### 4.1 连接明细采集（proxy 侧）

1. 玩家连接建立：proxy agent 生成 `connId`（UUIDv7），组装 open 事件入本地有界缓冲。
2. 玩家断开：组装 close 事件（含 `connId`、close_kind/reason、last_backend、switch_count、duration）。后端切换只累加本地计数，不产生独立事件。
3. **批量上报**：每 5s 或缓冲达 200 条（先到者触发）批量 POST 一次，单批上限 500 条，open / close 混合。采集与上报全部在 TabooLib async 线程，不碰 MC/BC 主线程。
4. **幂等**：open 按 `conn_id` 插入冲突即忽略；close 按 `conn_id` 更新、目标行已 closed 即忽略——agent 重试安全。
5. **fail-static**：控制面不可用时事件进本地有界缓冲（默认上限 10000 条），恢复后补报；溢出丢弃最旧并累计丢弃计数，随下一批上报（控制面记 WARN），绝不阻塞玩家连接处理。
6. 控制面侧：请求线程只做鉴权 + 结构校验 + 入内存有界队列即返回，独立 worker 批量 upsert 入日表（对齐基座「异步批量入库、禁请求主线程长耗时」）；队列满返回 429，agent 退避后重报。

连接明细同时充当「玩家 → 所在服」名册的事实来源：控制面在内存维护 `player_uuid → resolved server` 快照（open/close/switch 摘要驱动），供 §4.2 按玩家寻址解析。快照属注册健康类内存事实，进程重启后由 `status=open` 行重建。

### 4.2 跨服消息传输与状态机

第二版消息经**控制面单跳中转**：上行 REST、下行沿用 agent 长轮询基调（ADR-0006），不引入 Redis/MQ（Legacy ADR-0016 的 Redis 通道决策在第二版由新 ADR 取代，见 §8-1）。业务插件只走本机 agent-api 的 `send` / `call`，禁止直连 Beacon。

状态机（`msg_trace.status`）：

```
accepted ──目标 agent 长轮询取走──▶ dispatched ──目标回执成功──▶ delivered
   │                                   │
   │ TTL 内无人取走（目标离线/失联）        │ 回执失败 / 重投数用尽仍无回执
   ▼                                   ▼
 expired                             failed
（accepted 阶段校验失败——目标不存在、跨域无信任、payload 超限——直接 failed，含原因）
```

- **发送（上行）**：源 agent POST 消息（元数据 + payload）；控制面校验 namespace 隔离（跨域须 `namespace_trust` 存在 capability=`message`，否则 403 记失败）、目标存在性、payload 上限，校验通过即入目标服的内存投递队列、状态机在控制面内存置 `accepted`（**请求 goroutine 不碰 DB**，对齐 §4.1 异步入库与验收 #10 写入不阻塞）。`msg_trace` + `msg_payload` 不在 send 时落库，而在消息到达终态（delivered/failed/expired）时经异步日表通道**同一事务一次性写入两表**——in-flight（accepted/dispatched，≤TTL）消息暂存内存、落库前不可查，终态后可按 messageId 查得完整链路。
- **按玩家寻址**：控制面查 §4.1 名册快照解析 `resolved_server_id`（hops 记 `resolved` 事件）；玩家不在线 → `failed`（`fail_reason=player_not_online`）。
- **广播寻址（FR-180，[ADR-0065](../adr/0065-message-broadcast-addressing.md)）**：控制面按当前在线服集合（注册 / 健康内存事实）解析接收方——无 `targetZone` = 发送者 namespace 全部在线服（backend + proxy，含发送者自身）、有 `targetZone` = 该 zone 在线服——逐服入既有投递队列；每目标独立走 dispatched / ack / TTL / 重投规则。**一条广播只落一行 `msg_trace`**（聚合 fanout / delivered / failed / expired 计数），全部目标终态后一次性落库；payload 只存一份；广播行不入 §4.5 edge 聚合（无单一目标边）。跨 namespace 广播一律拒绝；poll 下发消息体携带广播标记（additive 键），agent 据此路由到 topic 订阅分发（与定向 `on(type)` 分发表隔离）。
- **下发（下行）**：目标 agent 独立长轮询消息通道（与命令通道分离——命令通道只做控制面编排，消息是业务通信面）；取走即置 `dispatched` 并携带 payload 下发。
- **回执**：目标 agent 把消息交业务 handler 后批量 ACK（成功 → `delivered`、失败 → `failed` 带原因）；ACK 同时上报 agent 侧 hop 事件，控制面合并进 `hops` 并计算 `duration_ms` / `hop_count`。
- **重投**：`dispatched` 后 10s 未收到 ACK 重投（重新入队），最多 2 次；仍无回执 → `failed`（`fail_reason=ack_timeout`）。业务侧以 `message_id` 幂等去重。
- **TTL**：`accepted` 停留超过 30s 无人取走 → `expired`。内存投递队列每服有界（默认 1000 条），溢出即对最旧消息判 `expired`（`fail_reason=queue_overflow`）。
- **RPC**：`call` = 一条请求消息（`correlation_id` 自引用为自身 message_id，供 agent 侧入站区分请求/响应/单向三路分发）+ 一条 `correlation_id` 指回请求的响应消息；请求方 agent-api 本地维护 Future 超时。链路查询按 `correlation_id` 把往返两条串成一次调用。
- **降级**：控制面不可用时消息功能快速失败（agent-api 返回不可用错误，`isAvailable()=false`），**不做本地缓冲重发**——实时消息过时即无意义；配置 / 调度缓存的 fail-static 不受影响，玩家进服不受影响。
- 终态一次性落库按 `message_id`（UUIDv7 推导）定位日表；状态机中间态在内存演进、无中间态 DB UPDATE，落库写放大仅单行单次 INSERT。

### 4.3 默认查询防护（管理面）

所有连接 / 消息列表查询强制满足其一，否则 400（错误信息说明要求，按 ADR-0057 展示）：

1. **精确 ID 直查**：携带 `connId` / `messageId` / `correlationId`——由 ID 定位单表单行（或一对），免时间范围。
2. **条件查询**：必须同时携带
   - 至少一个选择性过滤：`serverId`（proxy 或来源/目标服）或 `playerUuid`；
   - 显式时间范围 `from`/`to`：跨度 ≤ 168h（8 张日表），前端默认填最近 1h。

补充防护：分页强制（默认 50 / 上限 200，游标分页）；逐表查询按时间倒序短路（凑满一页即停，不预扫全部日表）；聚合统计端点（§5.2 stats）只走预定义分桶聚合，不接受自由维度。窗口上限、默认窗口经运维设置热更（FR-158 页面 P6 接真，本阶段配置文件可改）。

### 4.4 payload 查看流程（权限 + 原因 + 审计）

1. 消息详情页默认只展示元数据与链路，payload 区域为「需授权查看」占位。
2. 查看者点击「查看 payload」→ 弹窗必填**原因**（非空、≤255 字）→ 前端 POST `/admin/v2/messages/{messageId}/payload`。
3. 控制面校验：操作者具备权限点 `message.payload.view`（管理面登录令牌 / API 密钥沿用既有机制，本域只新增该权限点；P9 FR-168 统一风险分级时收编为高风险档）→ 无权限 403。
4. 校验通过：**先写审计、后返回内容**（同请求内审计写入失败则整个请求失败，不允许「看了没记录」）。审计条目：action=`message.payload.view`，含 messageId、msg_type、来源/目标、原因原文、操作者、traceId；**不含 payload 内容**。
5. 返回 payload 原文 + sha256。查看行为可在 `/audits` 按 action 过滤追溯（PRD 角色地图：消息明细 → 原因弹窗 → 记录入 `/audits`）。
6. 跨 namespace 消息的 payload 查看在审计条目上追加 `cross_namespace=true` 标记，满足「跨域行为额外审计」。

跨域消息本身的额外审计：为避免高频消息逐条刷审计，按（来源 ns、目标 ns、msg_type、UTC 小时）聚合写一条审计（含条数、失败数）；逐条明细仍可在 `msg_trace` 按 `cross_namespace=true` 过滤查全。

### 4.5 失败链路的数据供给（拓扑异常链路吃什么）

`/topology` 异常链路视图（FR-156）与 `/dashboard` 流量卡片（FR-154 P5 补全）不直接扫日表，消费本域预聚合端点：

- **消息异常边**：按（source_server → resolved_server）边聚合窗口内 `failed` / `expired` 计数、失败率、p95 耗时、top fail_reason，附最近 N 条失败 `message_id` 样本供下钻到消息详情（再跳 `/audits` 查关联操作）。
- **连接异常点**：按 proxy 聚合窗口内 `close_kind ∈ {timeout, error}` 的异常断开计数与占比，附最近样本 `conn_id`。
- **玩家流 / 连接流**：按时间桶（1m/5m）聚合 open 数、close 数、存量 open 会话估算，供 dashboard 折线。
- 聚合在控制面内存做短窗缓存（默认 30s），只扫窗口命中的日表且带上述索引，不provide自由查询。调度失败的排查数据归 `v2-metrics-health-scheduling.md`（调度决策记录），拓扑页组合两域数据但各自真源不混。

## 5. API 契约

路由与鉴权遵循基座 §2：agent 面带 `X-Beacon-Token` + `X-Beacon-Identity`；管理面沿用登录令牌 / API 密钥。错误统一 `{code, message, traceId}`。时间一律 UTC ISO8601。

### 5.1 agent 面（`/beacon/v2/agent/*`）

| 方法 | 路径 | 请求要点 | 响应要点 |
|---|---|---|---|
| POST | `/beacon/v2/agent/connections/batch` | proxy 专用；`{bootId, droppedCount, events:[{kind: open\|close, connId, playerUuid, playerName, clientIp?, protocolVersion?, openedAt, closedAt?, closeKind?, closeReason?, firstBackend?, lastBackend?, backendSwitchCount?}]}`，单批 ≤500 | `202 {accepted, duplicated}`；队列满 `429` 带退避提示 |
| POST | `/beacon/v2/agent/messages/send` | `{messageId, msgType, targetKind: server\|player\|broadcast, targetServerId?, targetPlayerUuid?, targetZone?, correlationId?, payload, sentAt}`；`msgType` 非空且 UTF-8 编码 ≤64 字节，冒号合法；payload 为任意 JSON 值（object / array / string / number / boolean / null），broadcast 时 targetZone 可选做 zone 级定向（FR-180） | `200 {messageId, status}`；跨域无信任 / 跨域广播 `403`；Agent 按 JSON 编码文本、控制面按中转 / 保存文本分别执行 64KB 校验，控制面超限返回 `400 payload_too_large`；目标无效 `200 status=failed` 带原因 |
| POST | `/beacon/v2/agent/messages/poll` | `{waitSec ≤25, max ≤50}` 长轮询取本服待投消息 | `200 {messages:[{messageId, msgType, sourceServerId, correlationId?, broadcast?, payload, createdAt}]}`；payload 保持 send 时的 JSON 类型，string 保持业务原文且不被二次 JSON 编码，null 表示无 payload（`broadcast: true` 为广播投递标记，agent 据此路由 topic 订阅分发，FR-180）；无消息超时 `204` |
| POST | `/beacon/v2/agent/messages/ack` | `{results:[{messageId, status: delivered\|failed, reason?, deliveredAt, handlerCostMs?}]}` 批量回执 | `200 {applied}`；未知 messageId 忽略计入 `ignored` |

业务插件不感知以上端点，只调本机 agent-api 的 `send(target, type, payload)` / `call(...) → Future` / `on(type, handler)` / `isAvailable()`；agent-api 与降级语义的宿主约束（不阻塞主线程、HTTP/JSON 只在适配器）随 `v2-metrics-health-scheduling.md` 的 agent-api 章节统一收口。

### 5.2 管理面（`/admin/v2/*`）

| 方法 | 路径 | 请求要点 | 响应要点 |
|---|---|---|---|
| GET | `/admin/v2/connections` | Query：`connId` 直查，或 `serverId`/`playerUuid` + `from`&`to`（≤168h）+ 游标分页；可选 `status`、`closeKind`、`namespaceId` | 连接明细列表（§3.2 字段全集）；违反查询防护 `400` |
| GET | `/admin/v2/connections/{connId}` | 路径 ID 定位日表 | 单连接详情 |
| GET | `/admin/v2/connections/stats` | `serverId?`+`from`&`to`+`bucket: 1m\|5m` | 时间桶聚合：open/close 数、异常断开数、存量估算（dashboard 连接流 / 玩家流数据源） |
| GET | `/admin/v2/messages` | Query：`messageId`/`correlationId` 直查，或 `serverId`（来源或目标）/`playerUuid` + `from`&`to` + 游标分页；可选 `status`、`msgType`、`crossNamespace`、`namespaceId` | 元数据列表（**永不含 payload**） |
| GET | `/admin/v2/messages/{messageId}` | 路径 ID 定位日表 | 元数据 + `hops` 链路明细 + `correlation_id` 关联消息摘要；payload 仅返回 `payloadSize`/`payloadStored` |
| POST | `/admin/v2/messages/{messageId}/payload` | `{reason}` 必填 ≤255 字 | 校验权限点 `message.payload.view` → 先写审计后返回 `{payload, sha256, size}`；无权限 `403`、缺原因 `400` |
| GET | `/admin/v2/messages/stats` | `from`&`to` + `groupBy: edge\|type`（独立 bucket 时间桶维度暂缓——无契约类型与前端消费方，需要时先在 `packages/contracts` 定型再做） | 异常链路聚合：边级失败计数/失败率/p95 耗时/top 原因 + 失败样本 ID（`/topology` 异常链路数据源） |

### 5.3 与前端页面的对应

- `/topology`（P5 接真）：`messages/stats(groupBy=edge)` + `connections/stats` 画消息流、请求拓扑与异常链路，样本 ID 下钻消息 / 连接详情。
- `/dashboard` P5 补全项：`connections/stats`（玩家流 / 连接流卡片）。
- `/audits`：payload 查看与跨域聚合审计按 action 过滤（审计页本身归 FR-157，不在本域）。

## 6. 与其他规格的边界

| 相关规格 | 依赖 / 交付 |
|---|---|
| `v2-zone-authority.md` | 依赖其 namespace / server 实体与 serverId 权威；本域只引用不复制 |
| `v2-namespace-isolation.md` | 跨域消息放行依赖其 `namespace_trust`（capability=`message`）判定；本域负责跨域打标与聚合审计的落地 |
| `v2-agent-identity.md` | agent 面鉴权（token + identity）由其权威定义；未确认 agent 不可调用本域 agent 面端点 |
| `v2-metrics-health-scheduling.md` | agent-api 本机门面（消息方法与调度候选共用一个门面）与批量上报基础设施基调由其统一；本域复用其「异步批量入库」机制不另造 |
| `v2-hot-cold-archive.md` | 本域交付：可整表搬移的日表命名与 `sha256` 校验列；归档触发、冷查询路由、清理页面全归其权威 |
| `v2-delivery-orchestration.md`（FR-168） | P9 统一权限风险分级时收编 `message.payload.view` 为高风险档；P5 先以独立权限点落地 |
| FR-154/156/157 页面 | 本域只供数据端点（§5.2/§5.3），页面交互细节按 P2 mock 拍板结论走 |

## 7. 验收标准

1. **连接采集闭环**（FR-145）：真机 proxy 上玩家登入 / 登出后，`conn_detail_当日` 出现对应会话行，close 后 `duration_ms`、`close_kind`、`last_backend`、`backend_switch_count` 正确；上报与采集不在 BC 主线程（代码审查 + 压测无主线程卡顿）。
2. **分表与定位**（FR-145）：跨日保持的连接（open 于 D 日、close 于 D+1 日）行留在 D 日表且被正确 close；不存在任何 `PARTITION` 语法；控制面在 MySQL 与 sqlite（E2E）下建表、写入、查询行为一致。
3. **查询防护**（FR-145 / PRD §3）：不带精确 ID 且缺服务器/玩家过滤或缺时间范围的列表请求返回 400；时间范围 >168h 返回 400；`connId` 直查免时间范围可命中任意日表。
4. **消息链路可追踪**（FR-149）：真机 A 服以 string 与结构化 object / array payload `send`/`call` B 服，目标 handler 收到的 JSON 类型与内容不变；能按 messageId 查到 `hops` 完整链路（sent→received→dispatched→delivered）、`duration_ms`、`hop_count=1`，并按来源 / 目标 / 时间 / correlationId 检索。自动契约测试须覆盖 object / array / string / number / boolean / null 往返及带冒号 `msgType`。
5. **失败有原因**（FR-149）：目标离线 → `expired`；玩家不在线 → `failed(player_not_online)`；跨域无信任 → 拒绝且记 `failed`；ACK 超时重投 2 次后 → `failed(ack_timeout)`——各状态 `fail_reason` 非空且前端可见。
6. **payload 受控**（FR-150）：消息列表与详情接口响应体中不出现 payload 字段（契约测试断言）；无 `message.payload.view` 权限查看返回 403；缺原因返回 400；成功查看后 `/audits` 出现含原因、操作者、messageId 的审计条目且不含 payload 内容。
7. **元数据 / payload 分离**（FR-150）：`msg_trace` 与 `msg_payload` 分表且同事务写入；杀掉 payload 表（模拟归档后）不影响元数据与链路查询。
8. **拓扑供给**（FR-156 联验）：构造一批失败消息后，`messages/stats(groupBy=edge)` 返回对应异常边的失败计数与样本 ID，`/topology` 能据此展示异常链路并下钻到消息详情。
9. **fail-static**：杀控制面后 proxy 玩家照常进出服，连接事件本地缓冲并在控制面恢复后补报（丢弃计数可见）；agent-api 消息 `isAvailable()=false` 快速失败不抛异常到业务插件主流程。
10. **写入不阻塞**：连接 / 消息上报接口 p99 响应仅含队列入队耗时（压测验证）；入库由后台批量 worker 完成，队列满时 429 生效。
11. **广播可用**（FR-180）：A 服 `publish(topic, payload)` 后同 namespace 全部在线服的 `subscribe(topic)` handler 收到（真 agent e2e）；`publish(topic, payload, zone)` 只投该 zone；离线服不补投、TTL 过期计入 `expired_count`；跨 namespace 广播被拒；`msg_trace` 广播行聚合计数正确、管理面列表可按 `targetKind=broadcast` 过滤且不含 payload；**真机**广播场景可用（Lodestone / 探针）。

## 8. 风险 / 待定（默认决定集中登记，待拍板）

1. **消息经控制面单跳中转**：Legacy ADR-0016 决策为「消息不经控制面、走 Redis」，与第二版禁 Redis 冲突。本规格默认改为控制面中转 + 长轮询下发，**需要新 ADR 正式取代 ADR-0016**（不静默违背）。延迟量级（长轮询下发百 ms 级）能否满足业务插件预期需真机压测确认。**已拍板（2026-07-07）：确认控制面中转；[ADR-0063](../adr/0063-cross-server-message-control-plane-relay.md) 已取代 ADR-0016 落地。**
2. **主键用 UUIDv7 并以 ID 内嵌时间定位日表**：换取「免日期提示按 ID 直查」；代价是依赖 agent 时钟（已加 24h 拒收护栏）。备选是控制面接收时间定表 + 查询按 ID 需扫多表，未采用。
3. **连接明细取「会话行」而非事件流水**：后端切换只记 `backend_switch_count` 与首末后端摘要，不存逐次切换事件——玩家流以 proxy 聚合近似。若后续要求精确「服 → 服」玩家迁移图，需另立事件表（待需求确认再做）。
4. ~~消息目标只做 server / player 两种寻址~~ **已由 FR-180 / [ADR-0065](../adr/0065-message-broadcast-addressing.md) 补齐广播寻址**（真机 Lodestone 依赖广播，已回 PRD 立项——正是本条预留的口子）；订阅式 pub/sub 与离线留存仍不做。
5. **可靠性档位**：TTL 30s、`dispatched` 后 10s 重投、最多 2 次、每服投递队列 1000 条——「至多短暂重试、不离线补投」。数值走运维设置热更，默认值待真机校准。
6. **payload 上限 64KB、超限拒发不截断**；payload 明文落库（PRD 明确不做静态加密），DB 层防护依赖部署侧（DSN 权限、备份加密）另行约定。
7. **查询窗口上限 168h / 8 张日表、默认窗口 1h、分页上限 200**：为「禁全量扫描」的具体化默认值，可调不可关。
8. **跨域消息审计按小时聚合**而非逐条（防审计刷屏），逐条事实留在 `msg_trace`。若安全侧要求逐条审计需权衡写入量。
9. **玩家名册以连接明细内存快照实现**（open 行重建）：进程重启后名册重建期间按玩家寻址可能短暂 `player_not_online` 误判，接受短暂错位（对齐 Legacy 同类取舍）。
10. **控制面不可用时消息不做本地缓冲重发**（实时语义过时即弃），仅连接事件缓冲补报——与「fail-static 保进服」边界一致，但业务插件需自行处理发送失败。
