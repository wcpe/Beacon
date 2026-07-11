# 规格：指标采样、健康值与调度闭环（第二版）

> 状态：草拟 · 关联 FR：FR-144, FR-146, FR-147, FR-148 · 阶段：P4（0.24.x）
>
> 共享实体（`namespace` / `zone` / `server` / `agent_identity` 等）与全仓建表约定、路由 / 鉴权约定以 `v2-zone-authority.md` 与 `docs/API.md` 第二版通用约定为权威，本文引用不复制。

## 1. 背景与目标

Beacon 第二版的定位是集群调度中间件控制面（PRD §1.1）。P3 解决"谁是谁"（身份、namespace、区服权威）之后，P4 要解决"谁健康、派谁去"：

- agent 以 1s 粒度采样基础指标（TPS / CPU / 内存 / 在线 / 连接摘要），5s 批量上报，控制面异步批量入库（FR-144）。
- 控制面基于指标 + 告警 + 容量 + 延迟计算每台服务器的健康分、等级与不可调度原因，权重可配置、结果可回放解释（FR-147）。
- 每次调度决策记录请求、候选、逐台排除原因、最终选择与失败原因，可追溯排查（FR-146）。
- 业务插件只通过本机 `agent-api` 获取调度候选与健康事实；Beacon 不可用时 agent 按本地快照降级，绝不阻断玩家入口（FR-148，fail-static）。

本阶段同时把 `/dashboard`（健康与调度概览部分）与 `/service-analysis` 从 mock 接真（FR-154 部分 / FR-157 部分的数据供给，页面职责见 `docs/UX.md` §2）。

## 2. 范围

### 2.1 做什么

- agent 双端（proxy / backend）1s 采样、环形缓冲、5s 批量上报、断连补报与丢弃策略。
- 控制面指标接收端点、内存最新态维护、有界队列 + 后台写入协程的异步批量入库、日期后缀指标表。
- 健康值模型：因子归一化、权重配置（热更 + 版本化 + 审计）、score / level / schedulable / reasons 输出、内存实时计算 + 周期快照入库。
- schedulable 判定全枚举与排空（draining）开关。
- 调度决策端点、决策记录日表、降级期本地决策补报。
- agent-api 本机调度 / 健康接口（Kotlin 契约）、候选缓存、落盘快照与 fail-static 降级语义。
- `/dashboard`、`/service-analysis` 所需管理面查询端点。

### 2.2 明确不做

- 玩家流 / 连接流 / 告警事件的采集与页面（P5，`v2-connection-message-storage.md`）；本文健康公式的"告警因子"只定义输入口径，P4 期输入恒为 0。
- 每连接明细（FR-145，P5）；本文的"连接摘要"仅为计数与可达性统计。
- 指标 / 快照 / 决策日表的归档与清理执行（P6，`v2-hot-cold-archive.md`）；本文只定义表命名、保留期配置键与边界。
- 落位均衡、canary 引流等流量调度编排（不在第二版 PRD 范围）；调度只做"单次取候选"。
- 跨 namespace 调度的信任关系模型（`v2-namespace-isolation.md` 权威）；本文只定义默认拒绝与排除原因码。
- 游戏玩法逻辑（传送执行、玩家名单等），控制面只做决策与记录（架构不变量 §1）。

## 3. 数据模型

> 通用约束：全部经 GORM、零方言（禁 ENUM / SET / JSON 列），枚举落 `VARCHAR` + 应用层校验，结构化明细落 `TEXT`（json 序列化），必须可切 Postgres。大表用日期后缀表名分片（基座 §1），**禁分区表语法**。日表在首次写入当日数据时按需建表（`CREATE TABLE IF NOT EXISTS` 语义经 GORM Migrator）。

### 3.1 指标批表 `metric_sample_YYYYMMDD`（日表）

每行 = 一台服务器一个 5s 批的**批内聚合**（原始 1s 样本不落库，只用于 agent 批内聚合与控制面健康计算，见 §8 待定 1）。

| 列 | 类型 | 说明 |
|---|---|---|
| id | BIGINT 自增 | GORM 抽象主键 |
| namespace_id | BIGINT | 归属 namespace |
| server_id | VARCHAR(64) | ns 内唯一 serverId |
| kind | VARCHAR(16) | `proxy` / `backend` |
| bucket_start_ms | BIGINT | 5s 桶起点（agent 采样时钟，`ts − ts%5000`） |
| sample_count | INT | 桶内实际样本数（1~5） |
| cpu_pct_avg / cpu_pct_max | DOUBLE | 进程 CPU 使用率 % |
| mem_used_mb_avg | DOUBLE | 已用堆内存 MB |
| mem_max_mb | INT | 最大堆内存 MB |
| tps_avg / tps_min | DOUBLE | 仅 backend；proxy 恒 0 |
| online_avg / online_max | INT | 仅 backend：在线玩家数 |
| max_online | INT | 仅 backend：容量上限 |
| conn_avg / conn_max | INT | 仅 proxy：当前连接数 |
| backend_up / backend_total | INT | 仅 proxy：可达后端 / 配置后端数 |
| backend_rtt_ms_avg | DOUBLE | 仅 proxy：可达后端 TCP RTT 均值，不可用为 -1 |
| report_rtt_ms | INT | agent 上一批上报 HTTP RTT，未知为 -1 |
| created_at | DATETIME | 入库时间 |

- 唯一索引 `(server_id, bucket_start_ms)`：补报 / 重放幂等去重（冲突即忽略，经 GORM 可移植 upsert-ignore 子句）。
- 索引 `(namespace_id, bucket_start_ms)`、`(server_id, bucket_start_ms)` 支撑 `/service-analysis` 时序与对比查询。
- 不适用列写缺省值（0 / -1），按 `kind` 解释，不特判存 NULL。

### 3.2 健康快照表 `health_snapshot_YYYYMMDD`（日表）

每行 = 一台服务器一次周期快照（默认 30s 一轮，全量在册实例）。

| 列 | 类型 | 说明 |
|---|---|---|
| id | BIGINT 自增 | 主键 |
| ts_ms | BIGINT | 快照时刻 |
| namespace_id | BIGINT / server_id VARCHAR(64) / kind VARCHAR(16) | 同上 |
| score | INT | 0-100 健康分 |
| level | VARCHAR(16) | `healthy` / `degraded` / `unhealthy` |
| schedulable | BOOLEAN | 是否可调度 |
| reasons | TEXT | json 数组，不可调度原因码（§4.5） |
| factors | TEXT | json 数组：`[{factor, raw, normalized, weight, applicable}]`，回放解释用 |
| weights_rev | INT | 计算时使用的权重配置版本 |
| created_at | DATETIME | 入库时间 |

- 索引 `(server_id, ts_ms)`、`(namespace_id, ts_ms)`。

### 3.3 健康权重版本表 `health_weights_rev`（普通表，非日表）

| 列 | 类型 | 说明 |
|---|---|---|
| rev | INT 主键 | 单调递增版本号 |
| config | TEXT | 完整权重 + 阈值配置 json（§4.4 结构） |
| operator | VARCHAR(64) | 操作人 |
| created_at | DATETIME | 生效时间 |

- 每次修改权重设置即插入新 rev（旧行不改不删）；快照与决策带 rev 即可精确回放"当时为什么是这个分"。

### 3.4 调度决策表 `sched_decision_YYYYMMDD`（日表）

| 列 | 类型 | 说明 |
|---|---|---|
| id | BIGINT 自增 | 主键 |
| trace_id | VARCHAR(36) | 决策唯一标识（UUID），唯一索引 |
| ts_ms | BIGINT | 决策时刻 |
| namespace_id | BIGINT | 请求方 namespace |
| cross_namespace | BOOLEAN | 跨 namespace 候选进入决策时为 true（v2-namespace-isolation.md §4.1 要求的标记；跨域放行依其信任关系） |
| requester_server_id | VARCHAR(64) | 发起决策的 agent 所在服 |
| plugin | VARCHAR(64) | 业务插件名（agent-api 透传，可空） |
| purpose | VARCHAR(128) | 业务用途说明（可空，如 `lobby-transfer`） |
| zone_name | VARCHAR(64) | 请求目标小区（按名称寻址，见 §8 待定 8） |
| strategy | VARCHAR(32) | 本版恒 `highest_score`（记录决策所用策略名） |
| source | VARCHAR(16) | `control_plane` / `local_fallback`（降级补报） |
| weights_rev | INT | 决策时权重版本（source=control_plane 时有值） |
| candidate_count | INT | 进入评估的候选总数 |
| excluded | TEXT | json 数组：`[{serverId, reason}]` 逐台排除原因 |
| chosen_server_id | VARCHAR(64) | 最终选择（失败为空） |
| chosen_score | INT | 选择时健康分（失败为 -1） |
| fail_reason | VARCHAR(255) | 失败原因（成功为空），如 `no_candidate` / `zone_not_found` |
| duration_ms | INT | 决策耗时 |
| created_at | DATETIME | 入库时间 |

- 索引 `(trace_id)` 唯一、`(namespace_id, ts_ms)`、`(chosen_server_id, ts_ms)`、`(requester_server_id, ts_ms)`。
- 决策记录经与指标相同的异步批量写入通道入库，不在请求 goroutine 落库（§4.3）。

### 3.5 `server` 实体的排空列（引用，权威在 `v2-zone-authority.md`）

- `server.draining`（BOOLEAN，默认 false）及其切换端点已收编入 `v2-zone-authority.md`（§3.6 列定义、§5 端点 `PUT /admin/v2/servers/{serverId}/draining`，路径与语义不变）：排空中不再接受新调度、存量玩家不受影响，切换写审计。本文仅消费该事实作 schedulable 输入（§4.5），不再重复定义。
- 禁用状态不在本文新增列——以 `agent_identity.status`（`v2-agent-identity.md` 权威）中的禁用态作为 schedulable 输入。

### 3.6 内存真源（不落库部分）

- 每实例**最新指标窗口**：控制面内存保留每服最近 60s（12 批）批聚合样本环，`RWMutex` 保护，供健康计算与 `/dashboard` 实时读。
- 每实例**当前健康视图**：score / level / schedulable / reasons / factors / 计算时刻，内存真源；快照表仅为回放副本（真源切分：注册 / 健康真源 = Go 进程内存，架构不变量 §3）。
- 指标 / 健康 / 决策三类内存结构各自独立加锁，不嵌套；DB IO 一律在锁外。

### 3.7 保留边界（清理执行归 P6）

| 数据 | 配置键（运维设置，键名契约归 `v2-hot-cold-archive.md` §3.3） | 默认 |
|---|---|---|
| 指标批日表 | `archive.retention-days.metric-sample` | 14 天 |
| 健康快照日表 | `archive.retention-days.health-snapshot` | 30 天 |
| 调度决策日表 | `archive.retention-days.sched-decision` | 60 天 |

超期日表由 P6 归档器整表归档 + 校验后删除（`v2-hot-cold-archive.md` 权威）；P4 只保证表名可按日期枚举、配置键就位（默认值以本表为准，已与归档规格域注册表对齐）。

## 4. 机制与状态机

### 4.1 agent 采样与环形缓冲

> 实现状态（agent 侧，FR-144）：**已实现**。采样字段 / 环形缓冲 / 批内聚合在 agent-core `core/sampling/`（`MetricSample`·`MetricSampleBuffer`·`MetricBatchAggregator`·`MetricSampleFactory`）；1s 采样 + 5s 批上报由 `lifecycle/MetricsSamplingCoordinator` 编排（仿既有 gen 代 + 自我重调度）。主线程原子埋点在 bukkit 壳 `BukkitTickInstrumentation`（每 tick 零成本计数 + 每秒读一次在线数写 volatile，采样线程只读推算），替换旧「async 线程反射调 `Bukkit.getOnlinePlayers()`/`getTPS()`」做法；proxy 后端可达性经 `BungeeProxyMetricsCache` 慢刷缓存、采样只读。控制面接收端在另一 worktree 实现。

- **采样周期 1s**，在 agent 独立采样线程执行（TabooLib async 调度），**绝不在 MC 主线程做采样或 IO**（架构不变量 §5）。主线程只承担零成本埋点：backend 每 tick 自增原子计数 / 更新最近 tick 时间戳，采样线程读取原子值推算 TPS；在线人数由 tick 任务维护的 volatile 计数提供，采样线程不直接调线程不安全的 Bukkit API。
- **采样字段集**（每秒一条样本）：

| 字段 | proxy | backend | 来源 |
|---|---|---|---|
| tsMs | ✓ | ✓ | agent 本地时钟 |
| cpuPct | ✓ | ✓ | OperatingSystemMXBean 进程 CPU% |
| memUsedMb / memMaxMb | ✓ | ✓ | MemoryMXBean 堆 |
| tps | — | ✓ | tick 计数滑动推算（0~20） |
| onlineCount / maxOnline | — | ✓ | 在线玩家数 / 容量上限 |
| connCount | ✓ | — | 代理当前在线连接数 |
| backendUp / backendTotal | ✓ | — | 后端 TCP 连接探测（独立探测线程池，超时按 down） |
| backendAvgRttMs | ✓ | — | 可达后端 TCP RTT 均值，无可达为 -1 |
| reportRttMs | ✓ | ✓ | 上一批上报的 HTTP 往返毫秒，未知为 -1 |

- **环形缓冲**：容量默认 600 条（10 分钟）。写满未及上报 → 覆盖最旧样本并 `droppedCount++`（agent 记 WARN 中文日志）。缓冲仅内存，不落盘。

### 4.2 5s 批量上报协议（含断连补报 / 丢弃）

> 实现状态（agent 侧，FR-144）：**已实现**。5s 固定节奏批上报、断连保留缓冲重试、恢复后一次补报积压多桶、`droppedSinceLast` 随成功批上报，均由 `MetricsSamplingCoordinator` 落地；单批 ≤120 桶结构由缓冲容量（600 条 1s 样本）保证。控制面 429 / 202 / 400 语义与去重待其接收端实现联调。

- 每 5s 一个上报 tick：取缓冲中全部**未上报样本**按 tsMs 升序打包，单批上限 120 条；超过上限则本 tick 报最旧 120 条，下一 tick 继续，直到追平（断连补报即此机制的自然结果，无特殊分支）。
- 上报失败（网络错误 / 5xx / 429）：样本保留在缓冲，本 tick 放弃，下一 tick 重试（固定 5s 节奏，不做指数退避——缓冲即背压）。断连超过 10 分钟的样本被环形覆盖，永久丢弃。
- 恢复后首个成功批携带 `droppedSinceLast`（上次成功上报以来被覆盖丢弃的样本数），控制面记 WARN 并回显在服务器详情，保证"丢了多少"可见（错误不静默，ADR-0057 精神）。
- **幂等**：控制面按 `(server_id, bucket_start_ms)` 唯一键去重，重放 / 重试安全；响应回报 `deduplicated` 数。
- **时钟约束**：请求携带 `agentTimeMs`，控制面校验与自身时钟偏移；偏移 > 5 分钟整批拒绝 400 `clock_skew_too_large`（提示校时，见 §8 待定 9）。样本落库用 agent 时钟原值。
- **活性信号**：v2 不设独立心跳端点，指标批上报兼作活性信号；> 30s 无成功批 → 该实例判 `lost`（见 §8 待定 4）。
- 人工确认前的 agent（`agent_identity.status` 非已确认）调本端点返回 403（基座 §2：确认前只能调注册与待确认轮询接口）。

### 4.3 控制面异步批量入库

- 接收 handler 只做：鉴权（token↔namespace、identity 绑定校验）→ 请求体校验（samples 为 agent 已按 5s 桶预聚合的行，见 §3.1「agent 批内聚合」与 §5.1；控制面不二次聚合）→ 更新内存最新指标窗口（锁内纯内存操作）→ 将 5s 桶行推入**有界内存队列**（channel，默认容量 4096 批）→ 立即返回 202。**请求 goroutine 不碰 DB**。
- 队列满 → 返回 429 `{code:"metrics_ingest_busy"}`，agent 视为上报失败保留缓冲重试（控制面过载保护，数据不丢在 agent 侧）。
- **写入协程池**（默认 2 个 goroutine）：从队列取批，攒到 200 行或 500ms 超时即 flush，一次事务批量 INSERT 到当日 `metric_sample_YYYYMMDD`（日表不存在则先建）。写失败：WARN 日志 + 有限重试（3 次退避），仍失败丢弃该 flush 并累计丢弃计数暴露到控制面自身健康（`/system`），不阻塞队列。
- 健康快照与调度决策记录复用同一写入通道（不同表路由），共享背压与批量语义。
- 规模校验：1000 子服 × 每 5s 一批 = 200 请求/s、约 200 聚合行/s，单 MySQL 批量写裕量充足；不需要也不允许引入 MQ（架构不变量 §2）。

### 4.4 健康值计算

**计算时机**：独立健康计算 goroutine 每 5s 一轮，对内存中全部已确认实例重算；权重配置热更后下一轮即生效。

**因子输入**：取该实例最近 60s（12 批）内存窗口的加权均值（cpu 用 avg、tps 用 avg、容量与连接用 max，取保守值）。窗口内无任何数据：沿用上一轮结果最多 30s，超过即判 `lost`（不再输出分数，score 置 0、level=`unhealthy`）。

**因子归一化**（各归一到 0~100，越高越健康；参数均可配，括号内为默认）：

| 因子 | 适用 | 公式（clamp 到 [0,1] 后 ×100） |
|---|---|---|
| tps | backend | `(tps − tpsBad) / (tpsGood − tpsBad)`（tpsGood=19.5，tpsBad=10） |
| cpu | 两者 | `(cpuBad − cpuPct) / (cpuBad − cpuGood)`（cpuGood=40，cpuBad=90） |
| capacity | backend | 占用率 `r=online/maxOnline`：`(capBad − r) / (capBad − capGood)`（capGood=0.6，capBad=0.95） |
| conn | proxy | 占用率 `r=conn/connSoftLimit`：同 capacity 式（connSoftLimit=2000） |
| latency | 两者 | rtt=backend 取 reportRttMs、proxy 取 backendAvgRttMs：`(latBad − rtt) / (latBad − latGood)`（latGood=50ms，latBad=500ms）；rtt=-1 → 因子不适用 |
| alert | 两者 | `100 − activeAlerts × alertPenalty` 下限 0（alertPenalty=25）；P4 期 activeAlerts 恒 0，P5 由告警事件域供给 |

**综合分**：`score = round( Σ(w_i × f_i) / Σ(w_i) )`，仅对**适用**因子求和（proxy 无 tps / capacity，backend 无 conn，rtt 缺失剔除 latency），权重自动重归一，不因角色差异失真。

**默认权重**：tps=30、cpu=20、capacity=20、conn=10、latency=10、alert=10。

**权重配置对象**（存运维设置，完整结构版本化进 `health_weights_rev`）：

```json
{
  "weights": {"tps":30, "cpu":20, "capacity":20, "conn":10, "latency":10, "alert":10},
  "normalize": {"tpsGood":19.5, "tpsBad":10, "cpuGood":40, "cpuBad":90,
                "capGood":0.6, "capBad":0.95, "connSoftLimit":2000,
                "latGoodMs":50, "latBadMs":500, "alertPenalty":25},
  "levels": {"healthyMin":80, "degradedMin":50}
}
```

- 修改经管理面端点（§5.2）：应用层校验（权重非负、good/bad 边界有序、阈值 0<degradedMin<healthyMin≤100）→ 事务内写设置 + 插入新 `health_weights_rev` 行 + 写审计 → 内存配置原子替换。P4 提供 API 与存储；`/settings` 页面接真在 P6（FR-158），期间可经 API 调整。

**等级**：`score ≥ healthyMin → healthy`；`≥ degradedMin → degraded`；否则 `unhealthy`。

**快照**：每 30s 把全量实例健康视图（含 factors 明细与 weights_rev）经写入通道批量落 `health_snapshot_YYYYMMDD`，供 `/service-analysis` 回放与 FR-171（P9 观察窗）消费。

### 4.5 schedulable 判定（全枚举）

`schedulable = true` 当且仅当以下原因**全部不成立**。原因码可叠加（reasons 数组）：

| 原因码 | 判定条件 | 事实来源 |
|---|---|---|
| `kind_not_schedulable` | kind ≠ `backend`（proxy 不作调度候选，健康仅展示） | server 实体 |
| `pending_confirm` | agent 身份未人工确认 | `agent_identity.status`（v2-agent-identity 权威） |
| `unassigned` | 未分配到小区（zone_id 为空） | server 实体（v2-zone-authority 权威） |
| `disabled` | 身份被禁用 | `agent_identity.status` |
| `draining` | 排空中 | `server.draining`（v2-zone-authority §3.6 权威，本文 §3.5 引用） |
| `lost` | 超过 30s 无指标批（失联） | 内存活性判定（§4.2） |
| `unhealthy` | 健康等级为 `unhealthy` | 健康计算（§4.4） |

- **degraded 仍可调度**：仅作为决策排序劣势，不进排除表（见 §8 待定 10）。
- 判定在每轮健康计算时一并更新内存视图，管理面与调度决策共用同一份判定结果（单一真源）。

### 4.6 调度决策流程

**正常路径（source=control_plane）**：

1. 业务插件调 agent-api `acquireCandidate(zone, purpose)` → agent 异步 HTTP `POST /beacon/v2/agent/schedule/decide`。
2. 控制面在内存注册表 + 健康视图上执行（全程内存，无 DB 读）：
   - 解析 zone（请求方 namespace 内按名称，找不到 → 失败 `zone_not_found`）；
   - 跨 namespace 请求默认拒绝 `cross_namespace`（信任放行规则归 `v2-namespace-isolation.md`）；
   - 枚举 zone 内全部 server 为候选，逐台按 §4.5 判定，不可调度者记入 `excluded[{serverId, reason}]`（取第一条命中原因码）；
   - 剩余候选按 `highest_score` 策略：分数最高者胜，同分优先容量占用率低者，再同随机；
   - 无剩余候选 → 失败 `no_candidate`。
3. 生成 traceId，决策记录推入异步写入通道（不阻塞响应），响应返回选择结果 + traceId + 解释摘要。
4. agent 把结果透传给业务插件；决策耗时目标 < 5ms（纯内存）。

**降级路径（source=local_fallback，fail-static）**：

1. agent 每 10s 拉取 `GET /beacon/v2/agent/schedule/candidates` 刷新本地候选缓存，并原子落盘快照文件（agent 数据目录 `candidates-snapshot.json`），重启后仍可用。
2. `decide` 调用失败（网络 / 5xx / 超时 800ms）→ agent 立即用本地快照在目标 zone 的候选中**本地决策**（同 `highest_score`，按快照分数），玩家链路不等待、不阻断（架构不变量 §5 fail-static）。
3. 本地决策生成本地 traceId、记入 agent 内存补报队列（容量 512，满丢最旧）；控制面恢复后经 `POST /beacon/v2/agent/schedule/report-local` 批量补报入库（best-effort，见 §8 待定 7），使降级期决策同样可查。
4. 快照超龄（默认 > 10 分钟）仍继续使用（fail-static 优先可用性），但 agent-api 数据源状态置 `STALE`，agent 记 WARN。

### 4.7 并发与失败处理要点

- 三类内存结构（指标窗口 / 健康视图 / 补报队列在 agent 侧）独立加锁，控制面锁内禁 DB IO、禁网络 IO。
- 权重热更用"整对象原子替换"（指针交换），计算轮读到的配置内部一致，无半新半旧。
- 日表跨日切换：写入协程按行内 `bucket_start_ms` / `ts_ms` 决定目标表，跨日批自动拆分两表写入。
- 所有对外失败带 `{code, message, traceId}` 且脱敏（ADR-0057 沿用）；异步写失败计数暴露到 `/system` 控制面自身健康，不无声消失。

## 5. API 契约

### 5.1 agent 面（`/beacon/v2/agent/*`，鉴权 `X-Beacon-Token` + `X-Beacon-Identity`）

> 实现状态（agent 侧，FR-144）：`POST /beacon/v2/agent/metrics/report` 客户端**已实现**（`BeaconApiClient.reportMetricsBatch`）：信封与 samples 元素键**全 camelCase**（v2 API 通用约定；控制面接收结构体 json tag 同为 camelCase，再映射到 §3.1 的 snake_case DB 列——§3.1 是库表列名、非线上键）。信封 `{namespace, serverId, kind, agentTimeMs, droppedSinceLast, samples[]}`，samples 每元素为 5s 批聚合行、17 个 camelCase 键（`bucketStartMs`/`sampleCount`/`cpuPctAvg`/`memUsedMbAvg`/`tpsAvg`/`connAvg`/`backendRttMsAvg`/… 不适用维度写 0 / -1 缺省），响应 202/429/403/400 已映射。响应体 `self.*` 健康字段本片忽略（P4b 用）。其余三行（candidates / decide / report-local）为 FR-146/148 范围，本片未实现。

| 方法 | 路径 | 请求要点 | 响应要点 |
|---|---|---|---|
| POST | `/beacon/v2/agent/metrics/report` | `{namespace, serverId, kind, agentTimeMs, droppedSinceLast, samples[]}`；samples 按 §4.1 字段集，单批 ≤120 | 202 `{accepted, deduplicated, self:{score, level, schedulable, reasons[]}}`（顺带回传自身健康，agent-api `selfHealth` 数据源）；429 忙；400 `clock_skew_too_large` |
| GET | `/beacon/v2/agent/schedule/candidates` | 无参（服务端按请求方 namespace 圈定） | `{generatedAtMs, zones:[{zone, candidates:[{serverId, score, level, schedulable, onlineCount, maxOnline}]}]}`，仅含 schedulable 或 degraded 候选 |
| POST | `/beacon/v2/agent/schedule/decide` | `{zone, purpose?, plugin?}` | 200 `{traceId, chosen:{serverId, score}?, candidateCount, excludedCount, failReason?}`；404 `zone_not_found`；403 `cross_namespace` |
| POST | `/beacon/v2/agent/schedule/report-local` | `{decisions:[{localTraceId, tsMs, zone, plugin?, purpose?, candidateCount, excluded[], chosenServerId?, failReason?}]}` ≤100 条/批 | 202 `{accepted, deduplicated}`（按 localTraceId 幂等） |

> **实现状态**：`POST /beacon/v2/agent/metrics/report`（FR-144 采样入库半边）已实现——token↔namespace + identity 鉴权中间件（未确认 403），接收端只校验 + 更 60s 内存窗口 + 非阻塞入队回 202，后台写入池事务批插当日 `metric_sample_YYYYMMDD`（唯一键幂等去重、跨日拆表、队列满 429、时钟偏移 400），`self` 暂占位 `null`（健康模型 FR-147 属 P4b）。
> **落地口径澄清**：接收端**不再对 1s 原始样本做 5s 桶聚合**——`samples[]` 由 agent 端已聚合为 5s 桶（每条含 `bucketStartMs`/`sampleCount`/各 `*Avg`/`*Max`/`*Min`），控制面只做校验 / 去重 / 入库。这与 §4.3「批内按 5s 桶聚合」的措辞在**聚合发生位置**上不同（聚合前移到 agent）；若需以本口径为准，应写新 ADR 取代 §4.3 该措辞，此处仅登记现状不擅改决策正文。`GET /schedule/*` 与管理面查询、健康模型（FR-147）尚待实现。

### 5.2 管理面（`/admin/v2/*`，沿用登录令牌 / API 密钥）

| 方法 | 路径 | 说明 | 消费页面 |
|---|---|---|---|
| GET | `/admin/v2/metrics/summary` | 集群聚合概览：分角色实例数、在线合计、平均 TPS / CPU、level 分布、schedulable 计数（内存实时） | `/dashboard` |
| GET | `/admin/v2/metrics/series?serverId=&from=&to=&step=` | 单服 / 多服时序（step 服务端聚合 5s 行为桶 avg/max；跨日自动并表；1000+ 子服强制 serverId 筛选，禁全量扫） | `/service-analysis` |
| GET | `/admin/v2/health` | 全部服务器当前健康列表（内存实时；分页 + namespace / zone / level / schedulable 筛选） | `/dashboard`、`/servers` 详情 |
| GET | `/admin/v2/health/{serverId}` | 单服健康详情：score / level / schedulable / reasons / factors 因子分解 / weights_rev | `/servers` 详情、`/service-analysis` |
| GET | `/admin/v2/health/snapshots?serverId=&from=&to=` | 健康快照回放（含当时 factors 与 weights_rev，可关联 `health_weights_rev` 解释） | `/service-analysis` |
| GET | `/admin/v2/sched-decisions?namespaceId=&zone=&serverId=&result=&from=&to=&page=` | 决策记录分页查询（默认只查热库近 N 天，时间范围必填） | `/dashboard` 调度概览下钻 |
| GET | `/admin/v2/sched-decisions/{traceId}` | 单条决策详情（候选数、逐台排除原因、选择、失败原因、耗时、source） | 同上 |
| GET | `/admin/v2/sched-decisions/summary?window=1h` | 决策概览：总数、成功率、失败原因 Top、降级补报占比 | `/dashboard` |
| GET | `/admin/v2/settings/health-weights` | 当前权重配置 + rev + 历史 rev 列表 | `/settings`（页面 P6 接真） |
| PUT | `/admin/v2/settings/health-weights` | 全量替换权重配置：校验 → 写设置 + 新 rev + 审计 → 热更生效 | 同上 |

排空切换端点 `PUT /admin/v2/servers/{serverId}/draining` 已收编至 `v2-zone-authority.md` §5（路径与语义不变，消费方为本文 schedulable 判定），不在本表重复。

### 5.3 agent-api 本机接口（Kotlin，业务插件唯一入口）

位于 agent-core `beacon.agent.api` 包，业务插件经 `BeaconAgentApi.scheduling()` 获取（仅本 JVM，禁止业务插件直连 Beacon HTTP——直连不作为契约，随时可变）。HTTP / JSON 实现只存在于适配器（ADR-0005 延续），本接口不暴露任何传输细节。

```kotlin
interface BeaconScheduling {

    /**
     * 在指定小区内取一台可调度候选。
     * 异步返回（内部走独立线程，绝不阻塞调用线程之外的 MC 主线程）；
     * 控制面不可用时自动降级为本地快照决策，future 仍正常完成（fail-static）。
     */
    fun acquireCandidate(zone: String, purpose: String? = null): CompletableFuture<ScheduleResult>

    /** 列出指定小区当前候选快照（本地缓存，O(1) 读，可在主线程调用；非实时，最长滞后一个刷新周期） */
    fun candidatesInZone(zone: String): List<CandidateView>

    /** 查询某台服务器的健康视图（本地缓存快照；缓存未覆盖该服时返回 null） */
    fun healthOf(serverId: String): HealthView?

    /** 查询本服自身健康视图（随每次指标上报响应刷新，约 5s 新鲜度；从未上报成功时返回 null） */
    fun selfHealth(): HealthView?

    /** 当前数据来源状态与快照年龄 */
    fun dataSource(): DataSourceState
}

data class ScheduleResult(
    val chosen: CandidateView?,      // 为空表示本次调度失败
    val traceId: String,             // 控制面决策为服务端 traceId；本地降级为本地 traceId
    val source: DecisionSource,      // CONTROL_PLANE / LOCAL_FALLBACK
    val failReason: String?          // 失败原因码，成功为 null
)

data class CandidateView(
    val serverId: String, val zone: String,
    val score: Int, val level: HealthLevel,
    val onlineCount: Int, val maxOnline: Int
)

data class HealthView(
    val serverId: String, val score: Int, val level: HealthLevel,
    val schedulable: Boolean, val reasons: List<String>, val sampledAtMs: Long
)

data class DataSourceState(
    val source: DataSource,          // CONTROL_PLANE / LOCAL_SNAPSHOT
    val fresh: Boolean,              // 快照年龄 ≤ 10 分钟为 true；超龄仍可用但标 STALE
    val snapshotAgeMs: Long
)

enum class HealthLevel { HEALTHY, DEGRADED, UNHEALTHY }
enum class DecisionSource { CONTROL_PLANE, LOCAL_FALLBACK }
enum class DataSource { CONTROL_PLANE, LOCAL_SNAPSHOT }
```

**降级语义（fail-static）汇总**：Beacon 不可用时——`acquireCandidate` 走本地快照决策照常返回；`candidatesInZone` / `healthOf` 继续供给最后快照（含落盘恢复）；一切方法**不抛因控制面不可达导致的异常、不阻塞玩家进服链路**；恢复后自动切回 CONTROL_PLANE 并补报降级期决策。

## 6. 与其他规格的边界

| 相关规格 | 依赖 / 交付关系 |
|---|---|
| `v2-agent-identity.md` | 依赖其身份鉴权（token + identity 头）与 `agent_identity.status`（未确认 / 禁用作 schedulable 输入）；活性判定（§4.2 的 30s lost）以本文为权威——v2 无独立心跳，身份域不另设活性判定（其 pending 注册过期 TTL 与活性无关），`lost` 不是身份状态 |
| `v2-zone-authority.md` | 依赖 server / zone / region 实体、zone 分配事实（unassigned 输入）与「zone 名 namespace 内唯一」约束（按名寻址前提，其 §3.5）；`server.draining` 列与切换端点已由其收编（其 §3.6 / §5），本文仅消费 |
| `v2-namespace-isolation.md` | 调度默认拒绝跨 namespace（本文只出 `cross_namespace` 错误码 / 排除码）；信任关系下的跨域调度放行与额外审计规则归它 |
| `v2-connection-message-storage.md`（P5） | 告警事件与连接明细归它；其落地后向本文健康公式供给 `activeAlerts` 因子输入，并补全 `/dashboard` 玩家流 / 连接流 |
| `v2-hot-cold-archive.md`（P6） | 本文三张日表的归档、校验、清理与冷查询路由归它；本文只交付表命名规则与 §3.7 保留期默认值（键名契约归其 §3.3） |
| `v2-delivery-orchestration.md`（P9） | FR-171 生效观察窗消费本文健康视图与快照 API（健康分 / TPS 走 `/admin/v2/health*`），本文不为其新增字段 |
| `docs/UX.md` | `/dashboard`（健康与调度概览）与 `/service-analysis` 本期接真；页面 mock 已在 P2 拍板，本文只供数据契约 |

## 7. 验收标准

**FR-144（采样与批量入库）**

1. backend / proxy agent 均以 1s 采样入环形缓冲，字段集与 §4.1 表一致（proxy 无 tps/online，backend 无 conn/backend 探测）；采样与上报全程不在 MC 主线程（代码审查 + 压测无主线程卡顿）。
2. 每 5s 批量上报；控制面 202 后请求 goroutine 无 DB 操作（代码路径可证）；批聚合行落当日 `metric_sample_YYYYMMDD`，唯一键重放去重（重复上报 `deduplicated` 计数正确）。
3. 断连 ≤10 分钟：恢复后补报追平、样本零丢失；断连 >10 分钟：最旧样本被覆盖、`droppedSinceLast` 上报且控制面 WARN 可见。
4. 控制面写入队列打满时返回 429，agent 保留缓冲重试不丢数据；写入失败计数暴露于 `/system`。

**FR-146（调度决策记录）**

5. 每次 `decide` 产生唯一 traceId 的记录：请求方、zone、候选数、逐台排除原因、最终选择、失败原因、耗时齐全；`GET /admin/v2/sched-decisions/{traceId}` 可查并解释"为什么选 / 不选某台"。
6. 无候选、zone 不存在、跨 namespace 各返回明确失败码并落记录；降级期本地决策恢复后补报可查（source=`local_fallback`），按 localTraceId 幂等。

**FR-147（健康值模型）**

7. 每台在册服务器输出 score(0-100) / level / schedulable / reasons；proxy 与 backend 因子集自动适配、权重重归一（穷举单测覆盖各因子边界与不适用剔除）。
8. schedulable 六类原因（§4.5）逐一可触发、可叠加，degraded 不排除；判定单测穷举。
9. `PUT /admin/v2/settings/health-weights` 热更下一轮生效、非法配置被拒、变更产生新 rev 并入审计；历史快照带 factors + weights_rev，配合 `health_weights_rev` 可精确回放解释任一历史分数。

**FR-148（agent-api 与降级）**

10. 业务插件仅经 `BeaconScheduling` 接口取候选 / 健康，接口签名与 §5.3 一致；agent 对业务插件只读暴露，无改配置 / 改 zone 旁路。
11. fail-static 实测：杀掉控制面后 `acquireCandidate` 走本地快照正常返回、玩家进服与传送链路不阻断、无未捕获异常；agent 重启后凭落盘快照仍可降级决策；控制面恢复后自动回 CONTROL_PLANE 且补报入库。

**页面接真（配合 FR-154 / FR-157 本期部分）**

12. `/dashboard` 健康与调度概览、`/service-analysis` 指标趋势与健康回放从 mock 切真，真机验收通过；1000+ 子服模拟下列表 / 时序查询分页可用、无全量扫描。

## 8. 风险 / 待定（默认决定集中登记，待拍板）

1. **热库不落 1s 原始样本**：只落 5s 批聚合行（avg/min/max）。理由：1s 原始行在 1000 子服下约 8600 万行/天不可持续，聚合后约 1700 万行/天 + 日表 + 14 天保留可控；原始 1s 粒度仅存在于 agent 缓冲与控制面 60s 内存窗口。若需要 1s 级回放再议。
2. **保留期默认值**：指标 14 天 / 健康快照 30 天 / 决策 60 天（§3.7）。与"2 个月以上归档"的全局基调相比指标更短，因其量级最大且趋势查询很少回看两周以上。**已拍板（2026-07-07）：维持 14/30/60 分层。**
3. **延迟因子输入口径**：backend 取自身上报 HTTP RTT（agent→Beacon），proxy 取其后端 TCP 探测平均 RTT。前者度量的是"到控制面"而非"玩家体感"，是当前零新增采集成本下的最合理近似。
4. **活性判定归属**：已收口——v2 不设独立心跳，指标批上报兼作活性信号、>30s 无批判 `lost`，该判定以本文为权威；`v2-agent-identity.md` 未另设心跳 / 活性判定（其 pending 注册过期 TTL 与活性无关，保留），`lost` 不是身份状态、不落 `agent_identity`。
5. **告警因子 P4 恒 0**：公式与权重占位齐全（这是 PRD FR-147 明列因子，非空壳），输入源随 P5 告警事件域接入。
6. **`server.draining` 列与切换端点**：已收口——已收编入 `v2-zone-authority.md`（其 §3.6 列定义、§5 端点，路径与语义不变），本文仅引用消费（§3.5 / §4.5）。
7. **降级决策补报为 best-effort**：agent 内存队列 512 条、满丢最旧、不落盘。降级期决策的可查性让位于实现简单性；若审计要求降级决策零丢失再升级为落盘队列。
8. **调度请求按 zone 名称寻址**：agent / 业务插件不持有 zoneId。已收口——`v2-zone-authority.md` §3.5 已落「zone 名 namespace 内唯一」应用层约束（建 zone / 改名时校验），按名寻址成立。
9. **时钟偏移阈值 5 分钟**：超过整批拒绝，倒逼运维校时；不做服务端时钟改写（避免样本时间失真）。
10. **degraded 仍可调度**：仅 `unhealthy` 排除。理由：亚健康即摘除会在高峰期放大雪崩；degraded 通过分数排序自然靠后。
11. **调度策略只做 `highest_score`**：同分先比容量占用率再随机；strategy 字段仅为决策记录的解释数据，不是策略扩展占位。
12. **健康权重 P4 先出 API 与存储**：`/settings` 页面接真按路线图在 P6（FR-158）；P4 期间权重调整走管理面 API。
13. **候选刷新 10s / 快照超龄阈值 10 分钟 / 决策超时 800ms / 队列容量（缓冲 600、ingest 4096、补报 512）**：均为工程默认值，随真机压测调整，不视为契约。
