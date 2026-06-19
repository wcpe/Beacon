# 功能规格：控制面可观测看板（指标 + 历史趋势图）

> 状态：草拟　·　关联 PRD：FR-32　·　分支：feature/control-plane-observability-dashboard

## 1. 背景与目标

控制面现有的运行可见性只有「当前快照」一层且不完整：实例负载指标只有 `PlayerCount` / `TPS`（标注「仅展示不参与决策」），**没有内存、没有 CPU**；agent report 只发 `playerCount` / `tps` 且**壳层恒报 0**（未注入真值）；注册/健康是内存真源，**只有此刻、没有历史**，无法回看负载随时间的变化。

本功能（FR-32，属 P2 增强）为控制面补一块**自带可观测看板**：补齐内存/CPU 采集、让人数/TPS 上报真值、把样本落库形成历史趋势，并在管理台直接出图。**边界锁定**：看板只展示**负载数字（健康事实）**，不碰**玩家名单/身份（看人）**——看人归③层业务插件（Lodestone）。架构决策见 [ADR-0023](../adr/0023-control-plane-observability-dashboard.md)，本规格不重复决策正文。

## 2. 需求（要什么）

### 2.1 当前快照聚合
- 全集群**总玩家数**。
- **每服人数**（按 serverId）。
- **平均值**：平均 TPS、平均内存占用、平均 CPU 负载（可同时给分服明细）。

### 2.2 历史趋势
- 上述指标（人数 / TPS / 内存 / CPU）的**历史趋势图**，按时间窗（如近 1h / 6h / 24h）+ 聚合粒度查询。

### 2.3 采集补齐
- 人数 / TPS 上报**真实值**（现恒报 0）。
- 新增**内存**（JVM heap used / max）与 **CPU**（进程 / 系统负载，近似值）采集与上报。

- 范围内：上述快照聚合、历史趋势、采集补齐、管理台 Dashboard 页与图表。
- **不做（范围外）**：玩家**名单 / 身份 / 看人**（属业务插件，越界）；TSDB / Redis / 外部 Grafana 面板；告警规则（健康告警归 FR-28）；把指标用于调度决策（内存/CPU 同 PlayerCount/TPS，仅展示不参与决策）。

## 3. 设计（怎么做）

> 三层改动：agent 采集上报 → 控制面接收/存储/聚合 → web 出图。涉及架构决策（边界、时序落 MySQL、与 /metrics 关系）见 [ADR-0023](../adr/0023-control-plane-observability-dashboard.md)。

### 3.1 agent：采集与 report 扩字段
- 壳层（bukkit / bungee）注入**真实** `playerCount` 与 `TPS`，取代 `AgentLifecycle` 当前恒报 0 的壳层占位。
- 新增采集：
  - **内存**：JVM heap used / max（`MemoryMXBean` 或等价）。
  - **CPU**：进程 / 系统负载，经 `OperatingSystemMXBean`，**为近似值**。
- 扩 `BeaconApiClient.report` 载荷：在既有 `playerCount` / `tps` 之外新增 `memory`（used / max）与 `cpu`（load）字段。信封**只增不改**，向后兼容。
- 采集在 agent 既有上报线程内完成，**不在 MC 主线程做阻塞 IO**（守架构不变量 #5）。

### 3.2 控制面：Instance 加字段与 handler 解析
- `runtime.Instance` 新增内存 / CPU 字段，与 `PlayerCount` / `TPS` 同列健康事实，**仅展示不参与决策**。
- report handler 解析新字段；旧 agent 不发新字段时按 0 处理（缺省安全、向后兼容）。

### 3.3 时序存储 `metric_sample`
- 新增 MySQL 表 `metric_sample`，字段（建议）：
  - `id`（自增，GORM 抽象）
  - `namespace`（VARCHAR）
  - `server_id`（VARCHAR）
  - `sampled_at`（时间戳，建索引便于按时间窗查询）
  - `player_count`、`tps`、`mem_used`、`mem_max`、`cpu_load`（基础数值类型）
- **DB 可移植**：全部基础类型，枚举/状态如有落 `VARCHAR` + 应用层校验，**禁 JSON / ENUM 列与方言专有 SQL**，经 GORM 抽象，必须能切 Postgres。
- 走分层 `router → handler → service → repository`，repository 持 GORM、handler 不碰 GORM。

### 3.4 采样器与保留期清理
- 新增**采样器**：按固定间隔（如 15~30s，可配）从内存注册表取**在线**实例快照，批量写入 `metric_sample`。
- **保留期清理**：按保留期（如 24h / 7d，可配）滚动删除过期样本，控制表体量。
- 采样间隔与保留期均**走配置 / 常量，不硬编码**；采样属后台任务，DB IO 在运行态锁外。

### 3.5 聚合 / 趋势端点
- **聚合端点**：返回当前快照统计（总玩家数、每服人数、平均 TPS / 内存 / CPU），数据源为内存注册表实时计算。
- **趋势端点**：按时间窗 + 聚合粒度查询 `metric_sample`，返回时间序列点供出图。
- 均归 admin 面、走管理台鉴权；契约写入 `docs/API.md`。

### 3.6 web：Dashboard 页与图表
- 管理台新增 **Dashboard 页**：总览卡片（总人数 / 平均 TPS / 平均内存 / 平均 CPU）+ 每服明细 + 趋势图。
- 复用现有 React / shadcn 栈。
- **图表库**：若现有依赖不含合适图表库，需引入新前端依赖——**走依赖审批 / 单独确认**，本规格不擅自定库。

## 4. 任务拆分

### agent 层
- [ ] 壳层（bukkit / bungee）注入真实 `playerCount` / `TPS`。
- [ ] 新增内存（JVM heap）+ CPU（OperatingSystemMXBean）采集。
- [ ] 扩 `BeaconApiClient.report` 载荷加 `memory` / `cpu` 字段（含测试）。

### 控制面层
- [ ] ADR-0023：可观测看板决策（提议中 → 接受）。
- [ ] `runtime.Instance` 加内存 / CPU 字段；report handler 解析新字段（含测试）。
- [ ] `metric_sample` 表 + repository（GORM、可移植、含测试）。
- [ ] 采样器（按间隔采在线实例）+ 保留期清理（含测试）。
- [ ] 聚合端点 + 趋势端点（handler/service，含测试）。

### web 层
- [ ] Dashboard 页 + 总览卡片 + 趋势图（图表库待确认）。

### 文档同步
- [ ] PRD 状态、ARCHITECTURE（指标聚合 / metric_sample 数据模型 / 采样机制 / web 看板）、API（聚合与趋势端点）、CHANGELOG。

## 5. 验收标准
- 启动一台真实子服，管理台 Dashboard **人数 / TPS 显示真实非 0 值**（不再恒 0）。
- agent 上报并在看板展示**内存（heap used/max）与 CPU 负载**。
- 趋势端点按时间窗返回时间序列，Dashboard **趋势图能按近 1h / 6h / 24h 出图**。
- 采样器按配置间隔向 `metric_sample` 写样本；**保留期到期样本被清理**，表体量不无界增长。
- `metric_sample` **无 JSON/ENUM 列与方言专有 SQL**，GORM 抽象，可切 Postgres（DB 可移植）。
- 看板与端点**无任何玩家名单/身份泄漏**（仅聚合数字）。
- 控制面 `go test ./...` 全绿；agent `gradle test` 全绿；web `build` 通过。

## 6. 风险 / 待定
- **CPU 近似值**：JVM `OperatingSystemMXBean` 在不同 JDK / 容器下口径与精度有差异，CPU 仅供趋势参考，不作精确依据。
- **采样频率与库压**：采样间隔越短样本越密、表增长越快；需在间隔（如 15~30s）与保留期（如 24h / 7d）间权衡，二者可配。
- **图表库依赖**：若需引入新前端图表库，**走依赖审批**，未确认前不定库。
