# ADR-0025：bc 代理专属负载指标采集集合与角色分流展示（扩展 ADR-0023）

**状态**：已接受（后端可达性探测机制于 [ADR-0035](0035-backend-reachability-tcp-connect.md) 修订：MC status-ping → TCP 连接；本 ADR 其余决策不变）

## 背景

[ADR-0023](0023-control-plane-observability-dashboard.md) 建立了控制面自带可观测看板：补齐 bukkit 子服的人数 / TPS / 内存 / CPU 采集，时序落 `metric_sample`，并按角色把平均 TPS·CPU 限定为只统计 bukkit（bungee 作纯代理 tps 恒 0，计入会拉低平均）。但 bc（bungee 代理）至今**没有任何专属负载画像**——看板只把它当成「tps=0、有内存/CPU」的普通实例，看不到代理本身关心的连接数、线程、运行时长、后端可达性等代理特有事实。

需要给 bc 补一组**代理专属负载指标**并在管理台按角色分流展示，同时严守既有边界：控制面只存事实不写游戏逻辑（[architecture-invariants](../../.claude/rules/architecture-invariants.md) #1）、agent 自管身份不在 MC 主线程做阻塞 IO（#5）、注册/健康真源在内存（#3）、DB 可移植（#4）、只采**负载计数事实**不采玩家名单/身份（看人归③层业务插件，[ADR-0022](0022-agent-roster-read-api.md)）。本 ADR **扩展但不取代** ADR-0023（指标模型、时序落 MySQL、与 `/metrics` 的并存关系均沿用 0023，本 ADR 只新增 bc 维度）。

## 决策

bc 把一组**代理专属负载指标**作为自身上报的只读事实附加到既有 report，控制面存内存事实 + 落 `metric_sample` 扩列 + 按角色聚合，管理台 Dashboard 新增 BC 面板：

1. **BC 专属指标集合（仅 bungee 填，bukkit 不填/恒空）**：
   - **在线连接数**（`ProxyServer.getOnlineCount()`）
   - **JVM 线程数**（`ThreadMXBean.getThreadCount()`）
   - **JVM 运行时长**（`RuntimeMXBean.getUptime()`，毫秒）
   - **后端子服可达性**（配置后端的 up/total 计数）+ **到可达后端的平均 ping 延迟**（`ServerInfo.ping`，无可达后端时为 -1.0 不可用哨兵）

2. **Netty 网络吞吐入/出字节数本期不采、不留占位**：BungeeCord 公开 API（`net.md_5.bungee.api.*`）**不暴露任何 Netty pipeline / channel / traffic 注入点**——其 `GlobalTrafficShapingHandler` 注入只能切进 `net.md_5.bungee.netty.PipelineUtils`（实现 jar、非公开 API）。在不引 BungeeCord 实现 jar 这一重依赖、不做脆弱反射 hack 的前提下，**无干净可行的吞吐计数注入点**。故本期吞吐**标「待真机/待定」不实现**，且**不在数据模型 / 契约里留占位列或占位字段**（守 [scope-discipline](../../.claude/rules/scope-discipline.md) #3「不为未来预留空壳」）。若未来确需，须先立新 ADR 评估引实现 jar / 事件钩子的代价。

3. **后端可达性探测不碰 MC 主线程**：`ServerInfo.ping` 是 BungeeCord 公开 API，回调发生在 Netty IO 线程；bc 壳在 agent 既有 async 上报线程内逐后端发 ping，用 `CountDownLatch` **有界等待**汇总（单后端超时即按不可达计），延迟/可达性聚合是 core 侧无副作用纯函数（[BackendReachability]）。绝不在 MC/调度主线程做阻塞 IO（守不变量 #5）。

4. **report 扩 `proxy` 子对象、向后兼容**：bc 上报时在既有载荷外附加 `proxy` 子对象；bukkit / 旧 agent 不发即缺键。控制面 report handler 用「缺键 vs 上报」区分：缺键不刷新实例 BC 字段（向后兼容），bc 上报才刷新。

5. **控制面存内存事实 + 落 metric_sample 扩列（可移植）**：`runtime.Instance` 加 BC 字段（与人数/TPS/内存/CPU 同列健康事实，仅展示不参与决策，bukkit 恒空）。`metric_sample` 加**可空数值列**（`proxy_conn` INT、`thread_count` INT、`uptime_ms` BIGINT、`backend_up`/`backend_total` INT、`backend_avg_latency_ms` 浮点），全部基础类型 + `NOT NULL DEFAULT`（AutoMigrate 加列对既有行兼容），**禁 JSON/ENUM 列与方言专有 SQL**、经 GORM 抽象（守 DB 可移植，可切 Postgres）。采样器把 BC 字段一并落库（bukkit 行 BC 列恒为默认 0，照写不特判）。

6. **按角色分流聚合与展示**：聚合 `Summarize` 增 bc 维度（仅 `role=bungee` 实例：代理数 / 连接合计 / 平均线程 / 后端 up·total 合计 / 平均延迟剔除 -1.0），summary 端点增 `bc` 子对象；既有 bukkit 聚合（总人数 / 平均 TPS·CPU·内存 / 每服明细）**零改动、无回归**。管理台 Dashboard 新增「BC 代理」面板展示 bc 指标，bukkit 视图不受影响。

## 理由

- **事实而非逻辑**：BC 指标是「这台代理此刻的负载计数」客观陈述（连接 / 线程 / 时长 / 后端可达性），与人数 / TPS 一样属健康事实，落控制面不越「只存事实」边界；不触及玩家名单/身份（守 ADR-0022 看人边界）。
- **复用既有上报通道、最小新增面**：搭既有 report 信封附加 `proxy` 子对象，不新增端点 / 表 / 中间件；采集复用 agent 既有 async 上报线程与 BungeeCord 公开 API。
- **吞吐宁缺勿脆**：无干净注入点时**不冒充、不 hack、不留占位**，把其余安全集做全——符合简单优先与范围纪律，避免为一个采不干净的指标引重依赖或埋脆弱反射债。
- **可移植**：metric_sample 扩列全基础类型、NOT NULL DEFAULT、GORM 抽象，可切 Postgres；扩列对既有行用默认值兼容（不破坏 ADR-0023 已建表的历史样本）。

## 后果

- agent report 载荷**增可选 `proxy` 子对象**（仅 bc 非空填充）：信封只增不改，旧 agent / bukkit 不发、旧控制面忽略，向后兼容。
- `metric_sample` **增 6 个 BC 可空列**：bc 行有真值、bukkit 行恒默认 0；AutoMigrate 加列对 ADR-0023 已建表的既有行用 NOT NULL DEFAULT 兼容。表宽略增，体量仍受 ADR-0023 保留期清理约束。
- **网络吞吐缺位**：BC 面板不含网络入/出字节，标「待定」；若未来要补，须先立新 ADR（评估引 BungeeCord 实现 jar 或上游钩子）。
- **后端延迟/可达性为近似值**：`ServerInfo.ping` 受网络抖动与有界等待超时影响，仅供趋势/概览参考，不作精确 SLA 或调度依据（同 ADR-0023 对 CPU 近似值的定位）。
- **边界保持**：本 ADR 只搬 bc 负载计数事实，不引入任何跨服玩家行为 / 看人；若未来据此做引流 = 越界，须先立新 ADR。

## 备选方案

- **Netty `GlobalTrafficShapingHandler` 反射注入 BungeeCord pipeline 采吞吐**：能拿到精确入/出字节计数，但须反射进 `net.md_5.bungee.netty.PipelineUtils`（非公开实现 jar），属脆弱反射 hack、随 BungeeCord 版本易碎，且要引实现 jar 这一重依赖。撞「不做脆弱反射 hack / 不引重依赖」红线。落选（本期标待定，未来另立 ADR 再评估）。
- **每后端人数（per-backend playerCount）**：可呈现「哪个后端多少人」，但与各 bukkit 自报 `playerCount` 冗余（控制面已逐服有人数），且按玩家维度拆分易触碰看人边界。落选（不做，避免冗余与越界）。
- **BC 指标新增专用上报端点 / 单独表**：语义更聚焦，但平白多端点 / 表 / 同步逻辑，而该事实天然可搭既有 report 与 metric_sample 顺带。落选（复用既有信封与表，守简单优先）。
- **把 BC 指标用于代理落位 / 引流决策**：超出「只存事实、看板只展示」边界，属流量调度（[ADR-0017](0017-traffic-scheduling-decision-vs-execution.md) 范围）。落选（BC 指标同人数/TPS，仅展示不参与决策）。
