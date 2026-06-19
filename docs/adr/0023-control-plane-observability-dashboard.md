# ADR-0023：控制面自带可观测看板（指标 + 历史趋势），时序落 MySQL

**状态**：已接受

## 背景

控制面当前的运行可见性只有「当前快照」一层，且不完整：

- 实例的负载指标只有 `PlayerCount` / `TPS`（[`registry.go`](../../internal/runtime/registry.go) 标注「仅展示不参与决策」），**没有内存、没有 CPU**。
- agent report 载荷（`BeaconApiClient.report`）只发 `playerCount` / `tps`，而 `AgentLifecycle` 当前**恒报 0**（壳层未注入真值），管理台看到的人数/TPS 是假的。
- 注册/健康是内存真源（[ADR-0003](0003-no-redis-in-mvp.md)），**只有此刻、没有历史**：无法回答「过去一段时间这台服人数怎么变化」「哪台服在掉 TPS」。
- [ADR-0020](0020-prometheus-metrics-observability.md) 已用 Prometheus client 暴露 `/metrics`，但那是给**外部**监控系统（Prometheus/Grafana）抓取的运维出口，需要另搭一套外部栈才能看图；运维想在 **Beacon 管理台内**直接看到全集群负载总览与趋势，目前无处可看。

需要一块**控制面自带的可观测看板**：补齐内存/CPU 采集、让人数/TPS 上报真值、提供历史趋势，且严格守住边界——只展示**负载数字（健康事实）**，不碰**玩家名单（身份/看人）**。

边界判据（锁定）：看板只展示**指标 / 计数**（人数、TPS、内存、CPU，属健康事实，**界内**）；**不展示玩家名单 / 身份**（看人，**越界**）。线 = 数字（负载）在界内、名单（身份）越界。名单看人归③层业务插件（Lodestone），与 [FR-31](../specs/agent-roster-read-api.md) / [ADR-0022](0022-agent-roster-read-api.md) 的「agent 只读暴露名册事实、看人业务仍在业务插件」一脉相承。

## 决策

控制面新增**可观测看板**，agent 扩采集，时序落 **MySQL**：

1. **看板范围 = 负载指标 + 历史趋势，不含名单**。展示：全集群总玩家数、每服人数、平均/分服 TPS、内存、CPU，以及上述指标的历史趋势图。**不展示任何玩家名单/身份**。

2. **agent 扩采集并上报真值**：
   - 壳层（bukkit/bungee）注入**真实** `playerCount` + `TPS`（取代当前恒报 0）。
   - 新增采集**内存**（JVM heap used / max）与 **CPU**（进程 / 系统负载，JVM 侧 `OperatingSystemMXBean`，为近似值）。
   - 扩 report 载荷新增 `memory` / `cpu` 字段。采集在 agent 既有上报线程内完成，**不在 MC 主线程做阻塞 IO**。

3. **控制面 Instance 加字段**：内存 / CPU 进 `runtime.Instance`（与 `PlayerCount` / `TPS` 同列健康事实，**仅展示不参与决策**）；report handler 解析新字段，缺省按 0 处理（向后兼容旧 agent）。

4. **时序落 MySQL 表 `metric_sample`**：新增表存采样点，字段（建议）：`id`、`namespace`、`server_id`、`sampled_at`、`player_count`、`tps`、`mem_used`、`mem_max`、`cpu_load`。**全部基础类型**，枚举/状态如有落 `VARCHAR` + 应用层校验，**禁 JSON/ENUM 列与方言专有 SQL**，经 GORM 抽象（守 DB 可移植）。控制面按固定间隔（如 15~30s，可配）对**在线**实例采样落表，带保留期（如 24h / 7d，可配）滚动清理过期样本。

5. **新增聚合与趋势端点**：聚合端点返回当前快照统计（总玩家数、每服人数、平均 TPS / 内存 / CPU）；趋势端点按时间窗 + 聚合粒度查询 `metric_sample`。两类端点均归 admin 面、走管理台鉴权。

6. **web 加 Dashboard 页与图表**：管理台新增 Dashboard 页，复用现有 React / shadcn 栈展示总览卡片与趋势图。图表若需新依赖，**走依赖审批**（不在本 ADR 擅自定库）。

7. **与 [ADR-0020](0020-prometheus-metrics-observability.md) 并存不冲突**：`/metrics` 供**外部**监控系统抓取（pull、不持久化、counter 语义）；本看板是 **Beacon 内自带**的可视化与历史（采样持久化到 MySQL、管理台直接看图）。二者面向不同消费者，各自独立，互不取代。

## 理由

- 看板只展示**健康事实**（人数/TPS/内存/CPU 都是负载数字），不触及玩家身份/名单——守住 [architecture-invariants](../../.claude/rules/architecture-invariants.md) #1「控制面只存事实、不写游戏逻辑；看人归业务插件」的边界。
- 时序落 **MySQL** 而非引 TSDB/Redis：本规模（约 50 服、采样间隔 15~30s）单表 + 保留期清理足够，**不引重型件**（守简单优先与 scope-discipline）；字段全基础类型、GORM 抽象，可切 Postgres（守 DB 可移植）。
- 内存/CPU 是判断子服压力的核心健康维度，补齐后管理台才有完整负载画像；趋势历史让运维能回看而非只看此刻。
- 复用既有 report 通道扩字段、复用既有 Instance 与 admin 面，最小化新增面。

## 后果

- 新增**采样写库**与**保留期清理**两项后台职责：控制面多一个按间隔跑的采样器与清理逻辑，`metric_sample` 表随时间增长（受保留期上界约束），需运维关注库容量与采样频率的平衡。
- agent **report 载荷扩字段**（`memory` / `cpu`）：信封只增不改，旧 agent 不发新字段时控制面按 0 处理，**向后兼容**。
- **CPU 为近似值**：JVM `OperatingSystemMXBean` 的进程/系统负载在不同 JDK / 容器环境下口径与精度有差异，看板呈现的 CPU 仅供趋势参考、不作精确计费或调度依据。
- 看板**只指标不名单**：玩家名单/看人不在本看板，归③层业务插件（Lodestone）。若未来要在 Beacon 内做名单展示 = 越界，须先立新 ADR 推翻本边界。

## 备选方案

- **纯内存环形缓冲存趋势**：实现最轻、零 DB 改动，但控制面重启即丢全部历史、且单节点内存放不下长窗口多实例样本，**不满足历史趋势的持久化要求**。落选。
- **复用 `/metrics` + 外部 Grafana 出图**：[ADR-0020](0020-prometheus-metrics-observability.md) 已暴露 `/metrics`，理论上搭 Prometheus + Grafana 即可出图——但那要求运维**另搭并维护一整套外部监控栈**，不是「Beacon 内自带、开箱即看」的统一面板，与本需求「管理台内直接看」目标不符。落选（两者并存：外部抓取走 `/metrics`，内部看板走本 ADR）。
- **引入时序数据库（如 VictoriaMetrics / InfluxDB）存样本**：专业时序库压缩与查询更强，但属重型运行期组件，撞「禁重型件 / 简单优先」红线，本规模不必要。否决。
