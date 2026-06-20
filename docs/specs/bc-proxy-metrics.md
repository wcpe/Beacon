# 功能规格：BC 代理专属指标与角色分流展示

> 状态：开发中　·　关联 PRD：FR-34　·　分支：feature/fr-34-bc-metrics

## 1. 背景与目标

FR-32（[ADR-0023](../adr/0023-control-plane-observability-dashboard.md)）补齐了 bukkit 子服的负载画像（人数 / TPS / 内存 / CPU），并在 X2 修复里给 `metric_sample` 加了 `role` 列、把平均 TPS·CPU 限定为只统计 bukkit（bungee 作纯代理 tps 恒 0，计入会拉低平均）。但 bc（bungee 代理）至今**没有任何专属负载画像**：看板只把它当成一台「tps=0、内存/CPU」的普通实例，看不到代理本身关心的连接数、线程、运行时长、后端可达性等代理特有事实。

本功能（FR-34，属 P2 增强）给 bc 补一组**代理专属负载指标**，并在管理台 Dashboard 按角色分流展示——bukkit 视图不受影响，bc 多一块「BC 代理」面板。仍严守边界：只采**负载计数事实**，不采玩家名单 / 身份（看人归③层业务插件 Lodestone，守 [ADR-0022](../adr/0022-agent-roster-read-api.md)）。架构决策见 [ADR-0025](../adr/0025-bc-proxy-metrics-and-netty-traffic.md)（扩展但不取代 ADR-0023），本规格不重复决策正文。

## 2. 需求（要什么）

### 2.1 BC 专属采集（仅 bungee 填、bukkit 不填/恒空）
- **在线连接数**：代理当前在线连接数（`ProxyServer.getOnlineCount()`）。
- **线程数**：JVM 活动线程数（`ThreadMXBean.getThreadCount()`）。
- **运行时长**：JVM 运行毫秒数（`RuntimeMXBean.getUptime()`）。
- **后端子服可达性**：配置的后端子服 up / 总数计数（经 `ServerInfo.ping` 异步探活，超时即 down）。
- **到各后端的平均延迟**：可达后端 ping RTT 的平均毫秒（无可达后端时为不可用哨兵 -1）。

### 2.2 控制面
- report 请求体加 BC 专属字段（omitempty 指针 / 可空，旧 agent / bukkit 缺键不报错）。
- `runtime.Instance` 加对应 BC 字段（仅展示不参与决策，bukkit 恒空）。
- `metric_sample` 加对应**可空列**（线程数 INT、运行时长 BIGINT、后端 up/total INT、平均延迟浮点；零方言、可移植、AutoMigrate 加列对既有行兼容）。
- 采样器把 BC 字段一并落库；聚合 / `metric_handler` 暴露按 role 分离的 BC 视图（summary 增 BC 维度，不破坏既有 bukkit 聚合）。

### 2.3 前端
- Dashboard 新增「BC 代理」面板 / 分角色视图，展示上述 BC 专属指标；bukkit 视图（总览卡片 / 趋势 / 每服明细）不受影响。

### 2.4 范围内 / 不做

- 范围内：上述 BC 专属采集、控制面接收 / 存储 / 聚合、Dashboard BC 面板。
- **不做（范围外）**：
  - **网络吞吐入 / 出字节数**：BungeeCord 公开 API（`net.md_5.bungee.api.*`）**不暴露任何 Netty pipeline / channel / traffic 注入点**（其 Netty 内部 `net.md_5.bungee.netty.PipelineUtils` 属实现 jar、非公开 API）。在不引 BungeeCord 实现 jar 这一重依赖、不做脆弱反射 hack 的前提下，**无干净可行的 `GlobalTrafficShapingHandler` 注入点**。故本期网络吞吐**标「待真机 / 待定」不实现**，不留占位列 / 占位字段（守范围纪律「不为未来预留空壳」）。详见 [ADR-0025](../adr/0025-bc-proxy-metrics-and-netty-traffic.md) 决策与备选。
  - **每后端人数**：与各 bukkit 自报 `playerCount` 冗余（控制面已逐服有人数），且按玩家维度拆分易触碰看人边界，**不做**。
  - 玩家**名单 / 身份 / 看人**（属业务插件，越界）；把 BC 指标用于调度决策（同 bukkit，仅展示不参与决策）。

## 3. 设计（怎么做）

> 三层改动：agent 采集上报 → 控制面接收 / 存储 / 聚合 → web 出图。涉及架构决策（BC 指标集合、Netty 吞吐缺位、metric 模型扩列可移植）见 [ADR-0025](../adr/0025-bc-proxy-metrics-and-netty-traffic.md)。

### 3.1 agent：BC 专属指标采集与 report 扩字段
- core 新增**平台无关** BC 指标载体 `ProxyMetrics`（连接数 / 线程 / 运行时长 / 后端 up/total / 平均延迟），与 `RuntimeMetrics` 并列；`JvmRuntimeMetrics` 补线程数 / 运行时长（廉价 MXBean 读）。
- core `BeaconApiClient.report` 在既有载荷外**仅当传入 BC 指标时**附加 `proxy` 子对象（旧行为不附加，向后兼容）。
- bungee 壳 `BungeeMetricsCollector` 采 BC 指标：在线连接数（`ProxyServer.getOnlineCount`）、线程 / 运行时长（core MXBean）、后端可达性 + 延迟（遍历 `ProxyServer.getServers()` 各 `ServerInfo.ping` 异步回调，有界等待汇总 up/total/平均延迟）。
- **不碰 MC 主线程**：采集在既有 async 上报线程内完成；`ServerInfo.ping` 本身异步（Netty IO 线程回调），用 `CountDownLatch` 有界等待，绝不阻塞主线程；超时 / 异常按 down 计、延迟不可用回退 -1（守架构不变量 #5）。
- bukkit 壳**不采** BC 指标（`metricsProvider` 不带 proxy 段），bukkit 上报恒无 `proxy` 字段。

### 3.2 控制面：Instance 加字段与 handler 解析
- `runtime.Instance` 新增 BC 字段（连接数 / 线程 / 运行时长 / 后端 up/total / 平均延迟），与现有负载字段同列、仅展示不参与决策；bukkit 恒空（缺省 0 / -1）。
- report handler 解析 `proxy` 子对象（指针 / 可空，缺键 → 不更新 BC 字段，向后兼容旧 agent / bukkit）。

### 3.3 时序存储 `metric_sample` 扩列
- 加可空数值列：`thread_count`（INT）、`uptime_ms`（BIGINT）、`backend_up`（INT）、`backend_total`（INT）、`backend_avg_latency_ms`（浮点）、`proxy_conn`（INT，代理在线连接数，与 player_count 区分）。
- **DB 可移植**：全部基础数值类型、`NOT NULL DEFAULT`（AutoMigrate 加列对既有行用默认值兼容），**禁 JSON / ENUM 列与方言专有 SQL**，经 GORM 抽象，可切 Postgres。bukkit 行这些列恒为缺省值（采样器据 role 决定是否填真值）。

### 3.4 采样器
- 采样器把在线实例的 BC 字段一并写 `metric_sample`（bukkit 实例 BC 字段为缺省值，照写不特判）。

### 3.5 聚合 / 端点
- `Summarize` 增 BC 维度：bc 实例数、bc 连接数合计、bc 平均线程 / 平均后端可达率 / 平均延迟等（只对 `role=bungee` 样本统计），不改既有 bukkit 平均逻辑。
- `metric_handler` summary 视图增 `bc` 子对象（仅 bc 聚合数字）；bukkit 视图字段不变。
- 趋势端点本期不增 BC 折线（趋势已按 role 分流平均，bc 专属趋势属增量，范围外不做）。

### 3.6 web：Dashboard BC 面板
- Dashboard 在既有总览 / 趋势 / 每服明细之外，新增「BC 代理」面板：bc 实例数 / 总连接数 / 平均线程 / 后端可达率 / 平均延迟卡片。
- 复用现有 React / shadcn 栈与 `StatCard` 组件；无新前端依赖。bukkit 相关视图零改动。

## 4. 任务拆分

### agent 层
- [ ] core `ProxyMetrics` 载体 + `JvmRuntimeMetrics` 补线程 / 运行时长（含单测）。
- [ ] core `BeaconApiClient.report` 附加 `proxy` 子对象（含报文契约单测）。
- [ ] bungee `BungeeMetricsCollector` 采 BC 指标（连接 / 线程 / 运行时长 / 后端可达性·延迟）。

### 控制面层
- [ ] `runtime.Instance` 加 BC 字段；report handler 解析 `proxy`（含测试）。
- [ ] `metric_sample` 扩可空列（GORM、可移植、含 repository 测试）。
- [ ] 采样器落 BC 字段；`Summarize` 增 BC 维度；summary 视图增 `bc`（含穷举单测）。

### web 层
- [ ] Dashboard BC 代理面板 + api/client BC 类型（含 vitest）。

### 文档同步
- [ ] ADR-0025（扩展 ADR-0023）；adr/README 登记；API.md report/summary 契约；ARCHITECTURE 指标模型补充；PRD 状态；CHANGELOG。

## 5. 验收标准
- BC 面板显示**连接数 / 线程 / 运行时长 / 后端可达性·延迟**；bukkit 聚合（总人数 / 平均 TPS / 内存 / CPU / 每服明细）**无回归**。
- report `proxy` 子对象旧 agent / bukkit 缺键不报错（向后兼容）；`metric_sample` 扩列对既有行兼容、**无 JSON/ENUM 列与方言专有 SQL**、可切 Postgres。
- 看板与端点**无任何玩家名单 / 身份泄漏**（仅聚合数字）。
- agent BC 采集（含 `ServerInfo.ping`）**不阻塞 MC 主线程**。
- 控制面 `go test ./...` 全绿；agent `gradle test` 全绿；web `pnpm test` + `build` 通过。

## 6. 风险 / 待定
- **网络吞吐入 / 出字节数（待真机 / 待定）**：BungeeCord 无干净 Netty 注入点，本期不实现、不留占位（见 §2.4 与 [ADR-0025](../adr/0025-bc-proxy-metrics-and-netty-traffic.md)）。若未来确需，须先立新 ADR 评估引 BungeeCord 实现 jar 或事件钩子的代价。
- **后端延迟 / 可达性为近似值**：`ServerInfo.ping` 受网络抖动与有界等待超时影响，仅供趋势 / 概览参考，不作精确 SLA 依据。
- **真机维度待验**：真实 bungee 的后端 ping 延迟 / 可达率需真机环境验证，无环境时如实标「待真机验」。
