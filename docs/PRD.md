# Beacon 产品需求文档（PRD）

> 活文档（入库于 `docs/`，随需求变更同 PR 更新）。本文管 **WHAT/WHY**（需求、范围、验收）；HOW 见 `ARCHITECTURE.md`；演进流程见 `CONTRIBUTING.md`。
> **长期作用**：产品的**需求登记册 + 路线图**，不是一次性文档——生命周期全程都在记。每个需求在 §4 加一行（带期 + 状态），交付即标版本；§4 FR 表是迭代中最常动的部分（🔥 高频），分期（§7）只是其中很小、很静的一页粗线条规划。单功能的详细规格放 `docs/specs/`，PRD 只保留"一行 FR + 期 + 状态"的索引级。
> 状态：第一期（MVP）需求已锁定。

## 1. 背景与目标

### 背景
一个持续扩张的 Minecraft 服务器集群（5 子服/区 × 5 区 = 25 服/大区，当前 A、B 两大区共 50 服，后续持续加大区并进行虚拟合区）缺少集中的配置与服务治理。现状靠逐节点配置文件手工维护，改错即"爆炸"，且无统一的服务发现与健康视图。

### 目标（Goals）
- 提供**集中配置中心**：一处编辑、动态热更、版本可回滚。
- 提供**服务注册/发现 + 健康检查**：集群拓扑与在线状态可见、可查询。
- 以"配置 + 拓扑指派"的形式支撑**分小区、虚拟大区、合区**，而不把游戏逻辑塞进控制面。
- 控制面与玩家入口**故障域隔离**：控制面挂不阻断玩家进服。
- 运维友好：单节点 docker-compose 一键部署，带 Web 管理台。

### 非目标（Non-Goals，第一期不做）
- 配置灰度/Beta、流量调度（落位均衡/canary 引流/drain）。
- 版本发布编排（蓝绿/滚动重启换 jar）。
- 虚拟合区的**游戏功能**（跨服看人/传送/共享经济/匹配/对战/排行等）由业务插件自实现；Beacon agent 仅在 FR-26 下提供**通用消息传输管道**供其复用，**不实现这些游戏功能本身**（见 [ADR-0016](adr/0016-agent-cross-server-messaging-middleware.md)）。补充：FR-31 下 agent 可**只读暴露玩家位置名册事实**（谁在哪个服）供业务插件消费，但「看人」业务（聚合/分组/展示/补全）仍属业务插件——暴露事实 ≠ 实现游戏功能。
- 鉴权/配置加密、控制面 HA（多节点）。
- 进程内代码热替换（"热更"指配置热更，不是替换 jar 中的代码）。

## 2. 角色

| 角色 | 说明 | 主要诉求 |
|---|---|---|
| 运维（配置管理员） | 通过 Web 管理台操作 | 改配置即生效、改错能回滚、看清谁在线、给服分区 |
| 业务插件开发者 | 在 Bukkit 子服写经济等业务插件 | 通过 agent 的本地 API 读到有效配置、查集群拓扑/发现 |
| agent（系统角色） | Bukkit/Bungee 上的 Beacon 接入插件 | 注册、拉配置、心跳、控制面挂时本地兜底 |

## 3. 用户故事（节选）

- 作为运维，我想在管理台改一份配置并发布，**让对应的子服秒级热更**，无需重启或登录每台机。
- 作为运维，我想在改坏配置后**一键回滚到上一个版本**。
- 作为运维，我想设"全局默认 + 某大区覆盖 + 某台机再覆盖"的**分层配置**，而不必每台复制整份。
- 作为运维，我想**给某台服指派 zone / 给它换区**，只改一处、agent 不用动。
- 作为运维，我想看到**全集群实例的在线/失联状态**和它们的标签。
- 作为业务插件开发者，我想通过 agent 的本地 API **读到本服的有效配置**，控制面挂时也能拿到上次的值。

## 4. 功能需求（FR）

> **状态流转**：`计划` → `开发中` → `已交付@vX.Y.Z`。本表是**活的路线图**：新需求加一行（状态 `计划`），`sdd-develop-feature` 推进时改 `开发中`，随版本交付时标 `已交付@vX.Y.Z` 并**保留不删**（便于追溯）。完整变更流程见 `CONTRIBUTING.md` §4。

| 编号 | 能力 | 期 | 状态 |
|---|---|---|---|
| FR-1 | 配置中心：namespace(环境)/group(大区)/dataId + scope 覆盖链（全局←大区←zone←单服）合并下发 | P1 | 已交付@v0.1.0 |
| FR-2 | 动态热更：配置变更长轮询推送，agent 不重启 apply | P1 | 已交付@v0.1.0 |
| FR-3 | 配置版本 + 一键回滚（含 diff、历史） | P1 | 已交付@v0.1.0 |
| FR-4 | 服务注册/发现 + 元数据标签（role/version/capacity/weight/自定义）。延伸出口：BeaconAgentProxy 可周期同步 discovery 结果，把同 namespace 在线 role=bukkit 子服按 serverId 注入 BungeeCord ServerInfo 目录（仅管理 Beacon 创建的条目，同名手工配置不覆盖） | P1 | 已交付@v0.1.0 |
| FR-5 | 健康检查：心跳 + TTL 判活（online/lost/offline） | P1 | 已交付@v0.1.0 |
| FR-6 | React 管理台：配置/实例/zone 分配/审计/namespace | P1 | 已交付@v0.1.0 |
| FR-7 | 轻量审计：谁/何时/对什么/做了什么 | P1 | 已交付@v0.1.0 |
| FR-8 | zone 指派：serverId→zone 权威分配 + 改派热推 | P1 | 已交付@v0.1.0 |
| FR-9 | 配置灰度/Beta | P2 | 已交付@v0.4.0 |
| FR-10 | 流量调度（落位均衡/canary 引流/drain） | P2 | 已交付@v0.4.0 |
| FR-11 | 管理面鉴权：操作者认证 + 写操作授权 + 操作者入审计（命令执行/前端登录前置，自 P2 前移，见 [ADR-0009](adr/0009-control-plane-auth-pulled-forward.md)） | P2 | 已交付@v0.2.0 |
| FR-12 | 版本发布编排（蓝绿/滚动） | P3 | 计划 |
| FR-13 | 完整虚拟合区运行时通道 / 控制面 HA | P3 | 计划 |
| FR-14 | 文件树托管（通道B）：整文件 blob、scope 整文件覆盖（不深合并）、manifest 增量同步、管理台在线改+热推；agent 镜像落盘到插件真实 dataFolder，复用既有 File 加载器/热重载/本地回退（见 [ADR-0010](adr/0010-file-tree-hosting-blob-channel.md)） | P2 | 已交付@v0.2.0 |
| FR-15 | 三方插件文件覆盖兼容：基于通道B 的备份+整文件覆盖 + 受限重载命令，兼容无法改源码的三方插件；命令执行依赖鉴权（FR-11） | P2 | 已交付@v0.2.0 |
| FR-16 | 下游 SDK 接入包：发布 agent-api（+ 接入 kit），下游软依赖、agent 不可用按 isAvailable 回退本地文件 | P2 | 已交付@v0.2.0 |
| FR-17 | agent 运维命令：本地 reload/reconnect/resync 等基础控制；远程下发依赖鉴权（FR-11） | P2 | 已交付@v0.2.0 |
| FR-18 | 管理台前端增强：文件树浏览/任意文本编辑/文件级版本·diff·回滚/登录身份/发布前只读 dry-run 预览 | P2 | 已交付@v0.2.0 |
| FR-19 | SDK 与 agent-api 文档：发布坐标/版本对齐矩阵/接入示例/API 参考（见 [docs/SDK.md](SDK.md)） | P2 | 已交付@v0.2.0 |
| FR-20 | 配置加密（自 FR-11 拆分）；**提前为 FR-26 前置**——FR-26 经 Beacon 下发 Redis 密码需先有配置加密 | P3 | 已交付@v0.4.0 |
| FR-21 | 管理台 UI 重构：全量 shadcn-ui 默认样式 + 详情改模态/独立详情页（增强 FR-6/FR-18，纯 UI 不改业务行为，见 [ADR-0012](adr/0012-web-shadcn-ui-design-system.md) 与 [docs/specs/web-shadcn-ui-overhaul.md](specs/web-shadcn-ui-overhaul.md)） | P2 | 已交付@v0.3.0 |
| FR-22 | 配置有效预览 + 配置页双视图：admin 只读 `GET /configs/effective`（含逐键来源 provenance）+ 服务器视角/文件覆盖矩阵双视图，把"100 台共用基线 + 增量/减量"做成一等公民（增强 FR-1/FR-6，落在既有 scope 覆盖链上，见 [ADR-0013](adr/0013-admin-effective-config-preview-and-provenance.md) 与 [docs/specs/config-effective-preview.md](specs/config-effective-preview.md)） | P2 | 已交付@v0.3.0 |
| FR-23 | 配置中心 VS Code 风格编辑器：Monaco 编辑器（语法高亮、自动缩进、代码折叠、Diff 对比）+ 资源管理器树形结构（配置文件 + 实例/分组）+ 历史修订面板（可折叠，点击联动 Diff）+ 保存按钮 + Ctrl+S 快捷键（增强 FR-6/FR-18/FR-21，配置中心页面改为单页面固定布局，编辑器区域使用 Monaco `@monaco-editor/react`） | P2 | 已交付@v0.3.0 |
| FR-24 | agent↔控制面传输合并：把 配置/文件树/覆盖集 三条 server→agent 长轮询合并为**单条 SSE 推送流**（只发变更通知，agent 用现有端点取内容）；**连接即对账**（agent 上报各通道 md5 → 控制面补发落下的增量 → 再转直播），不丢更新；**心跳与 blob 取数据仍走 HTTP**，**健康判活独立于流活性**；fail-static 不变。作为**统一 server→agent 推送地基**，远程运维命令与 FR-29 拓扑 watch 复用此流（取代 [ADR-0006](adr/0006-rest-long-poll-push.md)、扩展 [ADR-0005](adr/0005-agent-transport-codec-abstraction.md)，见 [ADR-0015](adr/0015-sse-server-push-transport.md) 与 [docs/specs/sse-server-push-transport.md](specs/sse-server-push-transport.md)） | P2 | 已交付@v0.4.0 |
| FR-25 | 控制面首启脚手架 + .env 加载：单二进制首次启动自动释放配置模板（`config.yml`，默认 sqlite 可直接跑），**释放时把留空的管理员口令/签名密钥就地填入随机强值（0600）并直接启动**（开箱即跑、不再 fail-fast；`config.yml` 即真源，agent 共享令牌用固定默认 `beacon-bootstrap-token` 与 agent 样例开箱匹配）。**不自动生成 `.env`**（避免 `.env` 静默盖掉 `config.yml`）；`.env` 加载机制保留：运维手动放置的 `.env` 与真实环境变量按 `真实 env > .env > config.yml` 覆盖，降低单节点部署门槛。鉴权仍强制：口令/密钥强随机、不入库、不弱化 [ADR-0009](adr/0009-control-plane-auth-pulled-forward.md)；不用固定弱默认口令（见 [docs/specs/control-plane-bootstrap-scaffold.md](specs/control-plane-bootstrap-scaffold.md)） | P1 | 已交付@v0.4.0 |
| FR-26 | agent 内置跨服消息中间件：基于 Redis 的服务器间通用通信层——定向发送 / 请求-响应（RPC）/ 主题发布订阅 / 按玩家所在服寻址；可靠送达走 Redis Streams（消费组离线补偿），可丢事件走 pub/sub；作为 **agent 内独立可开关模块**，复用 agent 身份与 Beacon 地址簿，与配置同步/心跳**故障域隔离**；Redis 连接配置由 Beacon 下发、密码依赖 FR-20 加密先行。仅提供**通用传输**，匹配/实时对战/存储/排行榜等**业务功能不在本 FR 范围**（属③层业务插件，见 [ADR-0016](adr/0016-agent-cross-server-messaging-middleware.md) 与 [docs/specs/cross-server-messaging-middleware.md](specs/cross-server-messaging-middleware.md)） | P3 | 已交付@v0.4.0 |
| FR-27 | 配置发布前 schema/类型校验：发布前对配置做结构与类型校验（格式、类型、必填项），不通过则拒绝发布并给出明确错误，阻止下发坏配置导致目标服异常（增强 FR-1/FR-3） | P2 | 已交付@v0.4.0 |
| FR-28 | 健康分级 + 失联告警：在 online/lost/offline 之外引入 degraded（亚健康）判定，并在实例失联/状态异常时主动告警；告警通道做成可扩展抽象（接口），第一版实现站内信 + webhook 两种，后续新通道只需实现接口即可接入（增强 FR-5，阈值可配） | P2 | 已交付@v0.4.0 |
| FR-29 | 发现接口过滤 + watch 订阅：discovery / agent-api SDK 支持按 role/zone/tag 过滤查询，并支持订阅拓扑变更（实例上线/下线/改派）即时通知（增强 FR-4/FR-16，复用 FR-24 的 SSE server→agent 推送流） | P2 | 已交付@v0.4.0 |
| FR-30 | 可观测性：导出 Prometheus 运行指标（注册/健康/配置/推送流等）+ 审计查询 API（按操作者/对象/时间检索）（增强 FR-7） | P2 | 已交付@v0.4.0 |
| FR-31 | agent-api 玩家位置名册只读查询：在 Discovery 门面暴露 roster()（全量名册 玩家名→serverId，单一全局名册不按 namespace 分区）与 rosterInZone(group, zone)（zone 过滤后名册），把已躺在 agent 侧 Redis（FR-26 名册 beacon:player-loc）的「谁在哪个服」**事实**只读暴露给③层业务插件（如跨服看人），供总览/人数/Tab 补全；zone 归属权威来自控制面 DB（zone 集 ∩ 名册），名册不臆造 zone；Redis 不可用/名册空优雅降级返空；控制面零改动、不连 Redis、不持有名册。仅暴露**名册事实**，「看人」业务（聚合/分组/展示/补全/传送）仍属业务插件（扩展 [ADR-0016](adr/0016-agent-cross-server-messaging-middleware.md) 决策 5，见 [ADR-0022](adr/0022-agent-roster-read-api.md) 与 [docs/specs/agent-roster-read-api.md](specs/agent-roster-read-api.md)） | P3 | 已交付@v0.5.0 |
| FR-32 | 控制面可观测看板：补齐 agent 内存（JVM heap）/ CPU（OperatingSystemMXBean 近似值）采集并让人数/TPS 上报真值（现壳层恒报 0），控制面 Instance 加内存/CPU 字段（仅展示不参与决策），时序样本落 MySQL `metric_sample` 表（按间隔采在线实例、带保留期滚动清理），新增聚合（总玩家数/每服人数/平均 TPS·内存·CPU）与历史趋势端点，管理台新增 Dashboard 页与趋势图。**只展示负载指标（健康事实），不展示玩家名单/身份（看人归③层业务插件 Lodestone）**；与 [ADR-0020](adr/0020-prometheus-metrics-observability.md) 的 `/metrics`（外部抓取）并存不冲突（本看板为 Beacon 内自带可视化）。增强 FR-5/FR-7，见 [ADR-0023](adr/0023-control-plane-observability-dashboard.md) 与 [docs/specs/control-plane-observability-dashboard.md](specs/control-plane-observability-dashboard.md) | P2 | 已交付@v0.5.0 |
| FR-33 | 控制面自身状态页眉：新增 `GET /admin/v1/system/status`（版本/运行时长/DB 连接/在线实例数/采样器状态 + 控制面进程 CPU·内存·goroutine），管理台顶部新增页眉区展示控制面自身健康，区别于 FR-32 的 agent 网络聚合指标（增强 FR-6） | P2 | 开发中 |
| FR-34 | BC 代理专属指标与角色分流展示：BC 采集连接数/线程/运行时长/后端子服可达性·延迟/每后端人数 + 网络吞吐（agent 侧 Netty GlobalTrafficShapingHandler 只计数不限流、在 IO 线程不碰 MC 主线程）；report 扩字段、`metric_sample` 加列、Dashboard 新增 BC 面板与按角色分离视图；平均 TPS·CPU 仅统计 bukkit（依赖 metric_sample 角色化修复）。增强 FR-32，扩展 [ADR-0023](adr/0023-control-plane-observability-dashboard.md)（见 [ADR-0025](adr/0025-bc-proxy-metrics-and-netty-traffic.md) 与 [docs/specs/bc-proxy-metrics.md](specs/bc-proxy-metrics.md)） | P2 | 开发中 |
| FR-35 | zone 看板式归派管理台：未指派池 + 按大区分桶的 zone 容器 + 拖拽指派/改派（@dnd-kit），复用既有 `PUT/DELETE /zones/assignments`，后端零改动（纯 UI 增强 FR-8） | P2 | 开发中 |
| FR-36 | bc 后端归属事实上报：bc agent 将其 `ProxyServerDirectory` 的后端 serverId 集合经 register/report 附加字段上报（仅 bc 填、向后兼容），控制面存为 Instance 事实供拓扑连线；agent 上报自身事实、不改控制面"只存事实"边界（[ADR-0024](adr/0024-bc-backend-membership-as-fact.md)） | P2 | 开发中 |
| FR-37 | 集群拓扑可视化：新增 `GET /admin/v1/topology`（节点+边）与独立 `/topology` 页（ECharts graph，真实 bc→bukkit 连线、区分角色、按大区/zone 聚合，复用拓扑 watch 实时刷新）；实例与健康仅加角色列/徽标。依赖 FR-36（增强 FR-4/FR-29） | P2 | 计划 |
| FR-38 | 配置导入（上传通道）：管理台上传一份 plugins 目录到指定「组」的文件树（复用 FR-14 通道B 整文件托管）实现全局复用；操作入审计（沿用 [ADR-0010](adr/0010-file-tree-hosting-blob-channel.md)） | P2 | 开发中 |
| FR-39 | 配置导入（在线实例反向抓取）：经 server→agent 命令下发让目标 agent 读取本地 plugins 目录并回传 ingest 入库为组/实例级覆盖；含命令通道与 ingest 安全校验、操作入审计。依赖 FR-38 + server→agent 命令通道（需新 ADR：反向取文件通道与安全面） | P2 | 计划 |
| FR-40 | 新建/复制配置流程改善：新建配置选项动态化（namespace/group/zone/server 从 API 取、去硬编码）+ scope↔target 联动 + 「复制某配置到指定实例 server 层覆盖并改 diff」快捷路径（实例级覆盖能力已存在，优先级 实例>分组>全局）（增强 FR-1/FR-22） | P2 | 开发中 |
| FR-41 | agent 配置环境变量覆盖：给 agent（数据面）配置读取加一层环境变量覆盖（env 优先于 config.yml），变量名约定 BEACON_AGENT_ + 点分路径大写、句点与连字符转下划线（如 identity.server-id → BEACON_AGENT_IDENTITY_SERVER_ID），覆盖全部标量与列表项（identity.metadata 动态键 map 本版不做）；支持容器化用环境变量注入接入信息，并让 E2E 以 env 注入取代手写 config.yml。增强 agent 配置加载，控制面零改动、core 仍 TabooLib-free（见 [docs/specs/agent-config-env-override.md](specs/agent-config-env-override.md)） | P2 | 开发中 |

> **P1 范围说明（提示位归档 P2）**：心跳响应的 `configDirty` 优化提示位**不在 P1 实现、恒返 `false`**——变更感知由 FR-2 长轮询负责，agent 不依赖该位；作为 P2 优化（API 细节见 `docs/API.md` §2）。

## 5. 非功能需求（NFR）

- **可用性**：控制面单节点可秒级重启；**控制面挂 ≠ 数据面挂**，agent 本地快照 fail-static，不阻断玩家。
- **性能**：50 服规模；注册/健康在内存；agent 不在 MC 主线程做阻塞 IO。
- **可移植**：DB 经 GORM，禁 MySQL 专有特性，可切 Postgres。
- **简单优先**：不引入 Redis/MQ/DI 框架等重型件。
- **可维护**：中文注释/日志/提交；分级日志（ERROR/WARN/INFO/DEBUG）。
- **安全**：敏感项（DB 密码、token）走 env 不入库；管理面鉴权前移本批（见 [ADR-0009](adr/0009-control-plane-auth-pulled-forward.md)），数据面内网可信不变，配置加密仍属后期（FR-20）。

## 6. 验收标准（P1）

- 在管理台对某 dataId 发布新版本，**目标子服在数秒内 apply 新配置**，无需重启。
- 对配置回滚到历史版本，目标服恢复旧值。
- 设置全局 + 大区 + 单服三层覆盖，某服拉到的有效配置为**正确合并结果**。
- 给某 serverId 指派/改派 zone，**该服有效配置随之重算并热推**，agent 未改任何本地文件。
- 停掉一台子服心跳，管理台在 TTL 后显示其 lost/offline。
- **杀掉控制面进程**，子服仍按本地快照运行、玩家可正常进服；控制面恢复后 agent 自动重连。
- 两台子服误用同一 serverId：守卫拒绝/告警；但**故障换机（同 serverId 换新 IP）不被误杀**。
- 所有发布/回滚/改派/注册在审计中可查。

## 7. 分期（路线）

各期只描述**主题 / 目标**；**具体哪个 FR 属于哪期，以 §4 FR 表的"期"列为唯一来源**——加需求即在表里标期，本节不重复列编号、不随 FR 增长而改。

- **第一期 MVP**：把控制面立起来——配置中心 + 服务发现 + 健康 + 管理台（§4 中标 P1 的 FR）。
- **第二期**：治理增强——配置灰度、流量调度、鉴权 / 加密（P2）。
- **第三期**：规模化——版本发布编排、虚拟合区运行时、控制面 HA（P3）。
- 后续若有新的**大阶段**（P4…）才在此加一行主题——分期是**少数粗粒度阶段**，不是每个功能 / 版本一期。

> 某期是否完成，看 §4 表里该期 FR 的"状态"是否都 `已交付`——进度不在本节维护。
>
> **分期不会堆到上百**：期是粗粒度路线图横轴，数量很少（走到产品成熟通常 3~6 个），一期含很多 FR、跨很多版本。**产品成熟（1.0 后稳态迭代）就不再加"第 N 期"**，改按版本（CHANGELOG / tag）+ 功能（FR 表 / specs）组织。若期数往二十、上百涨，是把"期"误当版本 / 功能单位的滥用信号（同 ADR 增长过快）。

## 8. 术语表

| 术语 | 含义 |
|---|---|
| 控制面 / 数据面 | Beacon（管理） / BC·Bukkit+agent（serving） |
| namespace | 环境隔离（prod/test） |
| group / 大区 | 多个 zone 聚合成的逻辑大区 |
| zone / 小区 | 一组子服构成的区 |
| serverId | 子服固有身份（agent 上报） |
| scope 覆盖链 | global ← group ← zone ← server 的配置合并 |
| 有效配置 | 某子服按覆盖链合并后的最终配置 |
| agent | 子服/代理上的 Beacon 接入插件 |
| fail-static | 控制面不可用时 agent 按本地快照继续 |
