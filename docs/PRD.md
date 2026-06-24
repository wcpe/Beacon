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
| FR-33 | 控制面自身状态页眉：新增 `GET /admin/v1/system/status`（版本/运行时长/DB 连接/在线实例数/采样器状态 + 控制面进程 CPU·内存·goroutine），管理台顶部新增页眉区展示控制面自身健康，区别于 FR-32 的 agent 网络聚合指标（增强 FR-6） | P2 | 已交付@v0.6.0 |
| FR-34 | BC 代理专属指标与角色分流展示：BC 采集连接数/线程/运行时长/后端子服可达性·延迟；report 扩字段、`metric_sample` 加列、Dashboard 新增 BC 面板与按角色分离视图；平均 TPS·CPU 仅统计 bukkit（依赖 metric_sample 角色化修复）。网络吞吐与每后端人数本期不做（BungeeCord 无干净 Netty 注入点不做脆弱 hack / 每后端人数与子服自报人数冗余且涉看人边界，见 [ADR-0025](adr/0025-bc-proxy-metrics-and-netty-traffic.md)）。增强 FR-32，扩展 [ADR-0023](adr/0023-control-plane-observability-dashboard.md)（见 [ADR-0025](adr/0025-bc-proxy-metrics-and-netty-traffic.md) 与 [docs/specs/bc-proxy-metrics.md](specs/bc-proxy-metrics.md)） | P2 | 已交付@v0.6.0 |
| FR-35 | zone 看板式归派管理台：未指派池 + 按大区分桶的 zone 容器 + 拖拽指派/改派（@dnd-kit），复用既有 `PUT/DELETE /zones/assignments`，后端零改动（纯 UI 增强 FR-8） | P2 | 已交付@v0.6.0 |
| FR-36 | bc 后端归属事实上报：bc agent 将其 `ProxyServerDirectory` 的后端 serverId 集合经 register/report 附加字段上报（仅 bc 填、向后兼容），控制面存为 Instance 事实供拓扑连线；agent 上报自身事实、不改控制面"只存事实"边界（[ADR-0024](adr/0024-bc-backend-membership-as-fact.md)） | P2 | 已交付@v0.6.0 |
| FR-37 | 集群拓扑可视化：新增 `GET /admin/v1/topology`（节点+边）与独立 `/topology` 页（ECharts graph，真实 bc→bukkit 连线、区分角色、按大区/zone 聚合，复用拓扑 watch 实时刷新）；实例与健康仅加角色列/徽标。依赖 FR-36（增强 FR-4/FR-29） | P2 | 已交付@v0.6.0 |
| FR-38 | 配置导入（上传通道）：管理台上传一份 plugins 目录到指定「组」的文件树（复用 FR-14 通道B 整文件托管）实现全局复用；操作入审计（沿用 [ADR-0010](adr/0010-file-tree-hosting-blob-channel.md)） | P2 | 已交付@v0.6.0（v0.6.0 交付后回归：真实目录含 agent 自身目录被 normalizePath 整批拒绝；已按方案 D 放开控制面拦截、自我保护下沉到 agent observe-only 修复并回归，见 [ADR-0028](adr/0028-allow-hosting-agent-self-dir.md)，补丁随 v0.8.0 发布） |
| FR-39 | 配置导入（在线实例反向抓取）：经 server→agent 命令下发（复用 FR-24 SSE 流的 command-pending 事件 + HTTP 回执，见 [ADR-0015](adr/0015-sse-server-push-transport.md)）让目标在线 agent 读取其真实 plugins 目录的文本配置回传，ingest 入库为组/实例级文件树覆盖（复用 FR-38 通道B）；读取限死 plugins/ 内、排除 jar/二进制、大小/数量上限、agent 与控制面双校验、admin 鉴权触发、触发与 ingest 入审计。依赖 FR-38（已交付@v0.6.0）+ 新建 server→agent 命令通道（见 [ADR-0027](adr/0027-reverse-fetch-channel-and-security.md) 与 [docs/specs/config-import-reverse-fetch.md](specs/config-import-reverse-fetch.md)） | P2 | 已交付@v0.7.0（v0.7.0 交付后真机回归：agent 读盘含自身目录被控制面整批拒绝、反向抓取 100% 失效；已按方案 D 放开控制面拦截、自我保护下沉到 agent observe-only 修复并回归，见 [ADR-0028](adr/0028-allow-hosting-agent-self-dir.md)，补丁随 v0.8.0 发布） |
| FR-40 | 新建/复制配置流程改善：新建配置选项动态化（namespace/group/zone/server 从 API 取、去硬编码）+ scope↔target 联动 + 「复制某配置到指定实例 server 层覆盖并改 diff」快捷路径（实例级覆盖能力已存在，优先级 实例>分组>全局）（增强 FR-1/FR-22） | P2 | 已交付@v0.6.0 |
| FR-41 | agent 配置环境变量覆盖：给 agent（数据面）配置读取加一层环境变量覆盖（env 优先于 config.yml），变量名约定 BEACON_AGENT_ + 点分路径大写、句点与连字符转下划线（如 identity.server-id → BEACON_AGENT_IDENTITY_SERVER_ID），覆盖全部标量与列表项（identity.metadata 动态键 map 本版不做）；支持容器化用环境变量注入接入信息，并让 E2E 以 env 注入取代手写 config.yml。增强 agent 配置加载，控制面零改动、core 仍 TabooLib-free（见 [docs/specs/agent-config-env-override.md](specs/agent-config-env-override.md)） | P2 | 已交付@v0.6.0 |
| FR-42 | 管理面只读角色 + 运行时 API 密钥 + 密钥操作审计：引入 full（读写，等同操作者）/ readonly（只读）两级角色，让外部服务（业务管理后端）用一把**只读**密钥调 `/admin/v1/*` 读取拓扑/实例/zone 等事实、对任何写端点一律 403（"只读拒写"由鉴权中间件统一裁决，handler 不散落角色判断）；管理台新增"密钥管理"页可运行时创建（名称+角色+可选过期）/列出/吊销/重置密钥，密钥经独立请求头 `X-Beacon-Api-Key` 或 `Authorization: Bearer` 认证。密钥**落库只存 SHA-256 哈希**，明文仅创建/重置时返回一次、**不可二次读取**（丢失只能重置轮换）；创建/吊销/重置写入既有 `audit_log`（明文不入 detail），管理台复用审计页过滤可查。增强 FR-11，落库属新决策（补充 ADR-0009），不做细粒度/字段级权限·按端点 scope·自动轮换·速率限制·多租户（见 [ADR-0026](adr/0026-runtime-api-keys-and-readonly-role.md) 与 [docs/specs/admin-readonly-role-and-api-keys.md](specs/admin-readonly-role-and-api-keys.md)） | P2 | 已交付@v0.7.0 |
| FR-43 | 可观测看板 BC/子服双区块拆分：Dashboard 整体拆「子服(bukkit)」与「BC 代理」两大区块（总览/趋势/每服明细按 role 分区展示），平均内存/avgMemMax 由「÷全部实例」改「仅 bukkit」（与平均 TPS/CPU 口径一致，消除混算），完成 FR-34 未尽的角色分离（增强 FR-32/FR-34，扩展 [ADR-0023](adr/0023-control-plane-observability-dashboard.md)，见 [docs/specs/dashboard-role-split.md](specs/dashboard-role-split.md)） | P2 | 已交付@v0.8.0 |
| FR-44 | 文件树结构化深合并 + 按文件整文件豁免：通道B 结构化文件（yml/json/properties）跨 global/大区/小区/单服 四层按键深合并（复用 `internal/merge`：标量覆盖 / map 深合并 / list 整替 / null 删键 / 确定性键序与 md5），非结构化文件整文件覆盖兜底；每文件可标「整文件覆盖」豁免（保注释、不重渲染）。控制面渲染每服合并整文件经 `files/content` 下发，agent 镜像落盘不变（agent 保持哑、零改、第三方插件零改），fail-static 不变。**[ADR-0029](adr/0029-file-tree-structured-deep-merge.md) 取代 ADR-0010 决策1（整文件覆盖、绝不深合并 → 结构化默认深合并、可豁免）**；默认深合并会改变既有整文件托管生效结果，现网需复核（增强 FR-14，见 [docs/specs/file-tree-deep-merge.md](specs/file-tree-deep-merge.md)） | P2 | 已交付@v0.8.0 |
| FR-45 | 文件树有效预览 + 逐文件/逐键来源 provenance：把 FR-22 的有效配置预览 + 逐键来源扩展到文件树——admin 只读预览某服某文件的合并结果 + 每个键来自哪层（global/大区/小区/单服）+ 哪些键被 null 减量删除 + 文件是否豁免为整文件，作为前端 diff 视图「期望合并值」一侧的数据源。复用 merge 的 provenance 平行纯函数、以「与下发结果逐一致」交叉测试防双实现漂移（增强 FR-22，依赖 FR-44） | P2 | 已交付@v0.8.0 |
| FR-46 | 按需拓印回写 + 前端审核台：扩展 FR-39 反向抓取为主路径——admin 选在线服 + 文件 → agent 回传该文件当前磁盘内容（事实）→ 控制面 diff 期望合并结果 → admin 选层（单服/小区/大区/全局）并入 + 预览 → **单人自审门**（强制看过 diff 才能确认）→ 入库为该层覆盖、走正常下发；触发与入库入审计，运行时数据文件排除，改动必经控制面（不退化为全自动双向，守控制面/数据面边界）。前端 web 转为「diff + 单人自审 + 同步」轻量审核台，**FR-23 Monaco 重编辑器降级保留（仅小修）**（增强 FR-39/FR-23，依赖 FR-44+FR-45，见 [docs/specs/imprint-review-console.md](specs/imprint-review-console.md)） | P2 | 已交付@v0.8.0 |
| FR-47 | git 单向导出镜像（备份 / 灾备 / 外部可见）：MySQL 仍唯一真源，发布 / 回滚 DB 事务提交后**异步、非阻塞、best-effort** 把配置 / 文件树**源层**（global/大区/小区/单服 各层 + 修订）按目录导出 commit 到本地裸仓 + 可选推送远程（GitHub/Gitea），commit message 带审计信息；git 失败只告警、不阻断发布、不参与 agent 下发；敏感配置（FR-20 加密项）导密文不泄明文 + 新增**文件树 path 级敏感标记 / 排除导出**（防反向抓取来的第三方插件 DB 密码明文落 git）；灾备恢复走手动 / 脚本化重导入（保持单向、不引入入站同步）。git 为单向派生镜像、不作第二真源（守架构不变量 #3），需写新 ADR（净新 feat） | P3 | 已交付@v0.8.0 |
| FR-48 | 小区默认入口 + BC 默认服注入：beacon 新增 zone 级「默认入口」配置（每小区唯一，指向一个在线 bukkit serverId），经发现/下发让该 zone 下所有 BC agent 把默认入口注入为 BungeeCord 默认/fallback 服（修复「动态注入子服但无默认服→玩家加入报 Could not connect to a default or fallback server」），一 zone 可多 BC 共享同一入口；agent 注入 bukkit 地址时打 INFO 日志可观测。默认入口归属由控制面 DB 权威（扩展 FR-4 注入链路与 FR-8/[ADR-0004](adr/0004-zone-authority-control-plane.md) zone 权威），见 [ADR-0031](adr/0031-zone-default-entry-and-bc-injection.md) | P2 | 已交付@v0.8.0 |
| FR-49 | 实例主动下线态：管理台主动「下线」某实例落 DB 拒绝状态 → agent 注册/心跳被拒（区别于自然 lost/offline）→ agent 进入「下线态」停止重连不刷日志，取消下线后可重新接入；下线操作 UI 不再强制先筛环境（按行直接下线）。显式扩展 FR-17 当初「不做 offline/online」的范围边界（避免与健康 TTL 语义混淆），见 [ADR-0032](adr/0032-instance-active-offline-state.md)（下线拒绝状态机与存储，另见 [docs/specs/instance-active-offline-state.md](specs/instance-active-offline-state.md)） | P2 | 已交付@v0.8.0 |
| FR-50 | 前端 i18n 框架接入 + 全站文案 key 化：引入 react-i18next + 翻译文件（先 zh-CN 全量、框架就绪后可加 en），审计动作由英文枚举（config.publish 等）经 i18n key 映射显示中文，「zone 分配→区分配」等硬编码中文文案与状态/角色 label 一并 key 化（增强 FR-6/FR-7/FR-21），见 [ADR-0033](adr/0033-web-i18n-framework.md)（引入前端 i18n 框架，另见 [docs/specs/web-i18n.md](specs/web-i18n.md)） | P2 | 已交付@v0.8.0 |
| FR-51 | 维度输入统一可编辑下拉（combobox）：环境/大区/小区/serverId 输入统一为「下拉+可编辑」组件（候选从 API 取；筛选框/新建表单允许列表外新值，纯选择处严格选），覆盖实例与健康/审计筛选、各表单；集群拓扑环境选择改下拉并默认选第一个环境出图（增强 FR-6/FR-37/FR-40） | P2 | 已交付@v0.8.0 |
| FR-52 | 代理服管理页：新建独立管理页集中透明展示所有 BC 代理运行态——状态 + 底层参数（连接数/线程/运行时长/后端可达性·延迟[FR-34]、后端子服清单[FR-36]、默认入口[FR-48]、所属 zone），让运维完整了解 BC 服状态（增强 FR-6，依赖 FR-34/FR-36/FR-48） | P2 | 已交付@v0.8.0 |
| FR-53 | 环境（namespace）增删改查补全：补 Update（显示名 name 随时可改）与 Delete（守卫：该环境下有已注册实例 / 已指派 zone / 已有配置则禁删并明确提示），前端新增环境管理页（列表/新建/改名/删除），操作入审计（namespace.update/delete）（增强 FR-6） | P2 | 已交付@v0.8.0 |
| FR-54 | beacon 主命令帮助完善：参考 taboolib 命令规范补 help 子命令与各子命令用法说明、无参/错参友好提示（当前仅无参打印 USAGE_LINES），完善 agent 运维命令可发现性（增强 FR-17） | P2 | 已交付@v0.8.0 |
| FR-55 | 归派看板汇总树形化：zone 归派管理台的汇总区由底部扁平表格（大区/小区/服数/在线数）改为页面上方的树形节点图（大区→小区→子服层级），提升集群归属可读性（增强 FR-35） | P2 | 已交付@v0.8.0 |
| FR-56 | 嵌套 BC 多层代理（BC→BC→bukkit）：支持父 BC 把子 BC 也注入为后端、拓扑与分派/默认入口在多层代理下正确呈现（扩展 FR-48/FR-37/FR-36 的单层假设），需写新 ADR；本批不做、仅登记后续 | P3 | 计划 |
| FR-57 | 文件树结构化无损深合并：把通道B 多层结构化文件（yml/json/properties）的按键深合并由 FR-44 复用配置中心 `MergeDataID`（parse→reserialize 归一化值、丢注释）改为**无损**——叶子标量保留原文 token（`007` 保 `007`、`1.10` 保 `1.10`、日期保原样、JSON 大整数不失精度）、注释保留。**只改文件树通道**：在 `internal/merge` 新增 `MergeDataIDLossless`/`MergeDataIDLosslessWithProvenance` 供 filetree 调用，**配置中心 `MergeDataID` 维持有损不动**（类型化存储归一化可接受、且改它会让全集群配置首轮 md5 变重拉）。YAML 用 `yaml.v3` 的 `yaml.Node` 节点级递归合并、JSON 用 `json.Number`、properties 行式保注释，**不引新依赖**。合并语义完全不变（标量覆盖 / map 深合并 / list 整替 / null 删键 / 确定性键序与 md5 幂等），只改保真度；单层短路 / 豁免 / 坏内容回退三条不变；provenance 与下发结果仍逐一致。⚠️多层结构化文件 md5 由有损渲染改为无损渲染，升级首轮一次性重取重写盘（内容更正确）。增强 FR-44，[ADR-0034](adr/0034-file-tree-lossless-merge.md) 取代 ADR-0029「值归一化可接受」一条，见 [docs/specs/file-tree-lossless-merge.md](specs/file-tree-lossless-merge.md) | P2 | 已交付@v0.9.0 |
| FR-58 | 反向抓取受管任务 + 状态机（增强 FR-39，[ADR-0037](adr/0037-reverse-fetch-managed-task.md) 取代 [ADR-0027](adr/0027-reverse-fetch-channel-and-security.md) 决策5、扩展决策1，见 [docs/specs/reverse-fetch-managed-task.md](specs/reverse-fetch-managed-task.md)）：抓取改为受管任务（新 `reverse_fetch_task` 实体，状态机 scanning→pending-review→fetching→ingesting→done/failed/cancelled/expired + 进度），单实例同时仅一个活跃任务（互斥 409），两段式命令（scan 只回元信息清单不读内容·永不失败 / submit 仅回选定 path 内容）；超限文件不再整批失败（清单红标·提交须确认）。**需改 agent-core（双端 jar 重建）** | P2 | 已交付@v0.10.0 |
| FR-59 | 反向抓取审核 · 忽略规则 · 冲突确认（增强 FR-39，依赖 FR-58，沿用 [ADR-0037](adr/0037-reverse-fetch-managed-task.md) + 复用 FR-46 范式，见 [docs/specs/reverse-fetch-review.md](specs/reverse-fetch-review.md)）：持久忽略规则（新表，按 ns/组/实例，exact/prefix，扫描清单命中标 ignoredByRule）；入库前目标已有版本则进 conflict-review 状态、出 diff（抓取值⟷已有版本）、逐文件确认保留哪份（取抓取须 reviewedMd5 自审 / 保留已有则跳过）；忽略/确认/提交入审计。纯后端（agent 不改） | P2 | 已交付@v0.10.0 |
| FR-60 | 反向抓取审核台 + 任务台（增强 FR-46，依赖 FR-58/59，沿用 [ADR-0037](adr/0037-reverse-fetch-managed-task.md) + 复用 FR-46 拓印审核台范式，见 [docs/specs/reverse-fetch-console.md](specs/reverse-fetch-console.md)）：新「反向抓取」前端页——任务台（建扫描任务 + 历史 + 状态/进度/取消）+ 审核台（扫描清单全量列 + 大小 + 超阈值红标须确认 + 逐项/目录忽略[临时+保存持久规则] + 提交选定）+ 冲突 diff 确认（目标已有版本 diff、逐文件 overwrite[自审]/keep、resolve 落库）+ 忽略规则管理。纯前端（消费 FR-58/59 端点） | P2 | 已交付@v0.10.0 |
| FR-61 | 运维设置 store + 热生效（feat，[ADR-0038](adr/0038-ops-settings-store-hot-reload.md) 扩展 FR-25「config.yml 即真源」，见 [docs/specs/ops-settings-store.md](specs/ops-settings-store.md)）：新 `setting` key-value 表 + 设置 API（GET 只读列 / PUT full 改 + 审计 settings.update）+ 各热改消费者运行期从 store 读并热生效（健康 TTL/亚健康/离线宽限/扫描间隔、采样开关/间隔/保留、长轮询上限、日志级别 atomic、告警 webhook url/超时、反向抓取单文件上限）；config.yml 降为引导 + 首启 seed（store 无该 key 才填），**启动/安全项（http-addr/db/auth 口令密钥/agent-token/git-export 路径）仍留文件、不进 store/API/UI**（安全边界） | P2 | 已交付@v0.10.0 |
| FR-62 | 运维设置页（feat，依赖 FR-61，沿用 [ADR-0038](adr/0038-ops-settings-store-hot-reload.md)，纯前端消费 FR-61 端点）：新「运维设置」页（`/settings`），`GET /admin/v1/settings` 拉热改项按域分组（health/metric/longpoll/alert/log/reverse-fetch）展示（说明 + 当前值 + 默认），逐项按类型编辑（int 数字 / bool 开关 / string 文本，log.level 下拉枚举）+ 校验 + `PUT /admin/v1/settings/{key}` 保存 + 热生效回显；标注「启动/安全项（http-addr/db/auth/agent-token）在 config.yml、此处不可改」 | P2 | 已交付@v0.10.0 |
| FR-63 | 可观测看板环境筛选可清空（fix）：选环境后无法清回「全部聚合」，修为可一键清回（可并入 FR-64） | P2 | 已交付@v0.10.0 |
| FR-64 | 可观测看板瘦身为「集群粗览」（增强 FR-32/FR-43，见 [docs/specs/monitoring-restructure.md](specs/monitoring-restructure.md)）：紧凑、一屏不深滚，定位粗略系统运行总览（在线实例分角色/总人数/集群均值/健康分布 + 关键趋势缩略），删逐服明细表移交 FR-65。纯前端 | P2 | 已交付@v0.10.0 |
| FR-65 | 服务器详情页 = 代理服管理 + 实例与健康合并（增强 FR-52 + FR-5/FR-33/FR-49，见 [docs/specs/monitoring-restructure.md](specs/monitoring-restructure.md)）：「实例与健康」+「代理服管理」合并为统一「服务器」页（`/servers`），列全部服务器(bukkit+bungee) 状态 + 角色 + 深指标(bukkit TPS/人数/容量；bungee 连接/线程/运行时长/后端可达性) + 健康 + 下线/drain/区改派(复用 FR-71 ReassignDialog) 操作，单服详情 Sheet 含双类深指标 + 关系(后端清单/默认入口)；旧 /instances /proxies 重定向。纯前端(复用 FR-52 已齐的逐服深指标) | P2 | 已交付@v0.10.0 |
| FR-66 | 上传文件夹/文件 预览 + 审批后提交（增强 FR-38，见 [docs/specs/config-change-approval.md](specs/config-change-approval.md)）：上传（文件夹/多文件）先预览模态展示待传内容（全量列 path/大小/二进制/超大标记 + 文本内容预览，前端 FileReader 读），审批确认后才入库（防误传）。纯前端 | P2 | 已交付@v0.10.0 |
| FR-67 | 配置修改审批式（增强 FR-1/FR-3/FR-18，见 [docs/specs/config-change-approval.md](specs/config-change-approval.md)）：单文件在页编辑配置加轻量确认步（保存前弹确认对话框看 diff[编辑态⟷上一已保存版本]+备注，确认才发布），批量/上传走预览审批（重，并入 FR-66）。纯前端 | P2 | 已交付@v0.10.0 |
| FR-68 | 文件树预览全量 + 追踪态（增强 FR-45，复用 FR-58 扫描，见 [docs/specs/file-tree-full-preview.md](specs/file-tree-full-preview.md)）：文件树预览页加「全量预览」——复用 FR-58 受管任务 scan 拿全量磁盘清单，与 FR-45 有效树交叉比对，列「当前服务器 plugins 下所有文件」并区分 追踪(Beacon 管，可看合并/来源)/未追踪(磁盘有 Beacon 未管)；预览只读、读完即 cancel。纯前端 | P2 | 已交付@v0.10.0 |
| FR-69 | 拓印审核台 关键字搜索 + 性能 + 使用提示（增强 FR-46）：以关键字即时搜文件替代手输路径，大树不卡，加上手提示 | P2 | 已交付@v0.10.0 |
| FR-70 | 环境名称启用 + 筛选显示编码+名称（fix + 增强 FR-6/FR-53/FR-51）：环境管理「名称」未用上 → 启用；各筛选/下拉同时展示 编码 + 名称（不只编码） | P2 | 已交付@v0.10.0 |
| FR-71 | 区分配安全化（增强 FR-8/FR-35，[ADR-0036](adr/0036-zone-reassign-safety-drain-gate.md) 扩展 [ADR-0004](adr/0004-zone-authority-control-plane.md)，见 [docs/specs/zone-reassign-safety.md](specs/zone-reassign-safety.md)）：「zone 分配」更名「区分配」(zone→区，仅 i18n 展示)；前端取消拖拽即改 → 看板默认只读 + 解锁改派 + 显式改派 + 手输 serverId 复述确认（空服也拦防误触）；后端排空门纵深：**在线非空（online 且 playerCount>0）的服「改派/取消指派/首次指派」凡改变其区归属解析一律 409 `ZONE_SERVER_ONLINE_NONEMPTY`（须先 drain/等排空）**、同值 no-op（不落库不审计）、真正变更入审计 | P2 | 已交付@v0.10.0 |
| FR-72 | 全量审计覆盖（增强 FR-7）：写操作审计中间件兜底（拦 /admin/v1 所有 POST/PUT/DELETE 记 操作者+动作+目标，敏感内容不入 detail）+ 既有专项审计保留 | P2 | 已交付@v0.10.0 |
| FR-73 | 服务分析 / 平台用量看板（feat，依赖 FR-72，见 [docs/specs/service-analysis.md](specs/service-analysis.md)）：新「服务分析」页，后端加审计聚合端点（`GET /audits/analytics`，按时间窗 + 环境聚合，可移植 GORM 投影 + Go 侧日分桶、禁方言日期函数），从 audit_log 聚合 Beacon 平台运维活动（发布/修改/回滚/校验失败、反向抓取、区改派、文件树变更、登录、密钥、告警…）计数 + 成功率 + 每日趋势，KPI 卡片 + 图表；与 FR-32(MC负载)/FR-30(Prometheus) 区分 | P2 | 已交付@v0.10.0 |
| FR-74 | 配置/文件批量操作（增强 FR-38/FR-1）：列表多选 + 批量删除/禁用/导出（一事务），治「逐个删」低效；空选禁用、批量删走统一确认 | P2 | 已交付@v0.11.0 |
| FR-75 | 配置编辑器格式校验（增强 FR-1/FR-3）：Monaco 客户端 YAML/JSON lint，发布前行内标错、解析失败禁用发布，防发坏格式到 agent 才暴露 | P2 | 已交付@v0.11.0 |
| FR-76 | 破坏性写操作统一二次确认（增强 FR-67）：抽通用确认框 + 影响摘要（脱链哪层/影响哪些服）覆盖删 config/file、清小区默认入口等删除/清除入口 | P2 | 已交付@v0.11.0 |
| FR-77 | 运维设置页恢复默认 + 批量保存（增强 FR-62）：每项「恢复默认」、页脚「保存全部变更(N)」+ 改动摘要 | P2 | 已交付@v0.11.0 |
| FR-78 | 控制面连接状态指示 + 自动重连（feat，纯前端复用现有轮询派生连通态，见 [docs/specs/connection-status-indicator.md](specs/connection-status-indicator.md)）：轮询断显横幅「连接中断·重连中」+ 小灯转红、恢复自动重连并失效查询刷新，治控制面重部时 UI 静默掉线（管理台无浏览器 SSE，连通态由 system-status 轮询的成功/失败派生） | P2 | 已交付@v0.11.0 |
| FR-79 | 配置/文件发布影响面预览（增强 FR-22）：发布确认显「将影响 N 台在线服:[serverId…]」，后端按 zone_assignment + 注册表算受影响集 | P2 | 计划 |
| FR-80 | per-server 有效配置变更时间线（feat）：服务器详情「变更历史」列有效配置变更（版本/时间/触发发布），看「何时因哪次发布变过」 | P2 | 计划 |
| FR-81 | 健康流转原因展示（增强健康）：lost/degraded 状态带 lastHeartbeatAge + 触发阈值，悬浮显「Ns 未心跳 > ttl Ns」 | P2 | 计划 |
| FR-82 | 控制面自观测页（feat）：自指标端点 + 新「控制面健康」页显 DB 延迟/longpoll 挂起/注册表规模/命令队列深度，与 MC 负载看板区分、只读 | P2 | 计划 |
| FR-83 | 全局搜索 + 命令面板（feat）：Cmd-K 聚合搜 config/file/server/audit 并跳转/执行常用操作，纯键盘可达 | P2 | 计划 |
| FR-84 | 审计全文检索 + 导出（增强 FR-7）：审计按 detail 关键字检索 + `GET /admin/v1/audits/export` 出 CSV/JSON 流（复用过滤，可移植 GORM） | P2 | 计划 |
| FR-85 | 新服接入引导向导（feat）：「添加服务器」向导填 ns/serverId/角色/大区→生成 agent config.yml + env 片段供复制、校验 serverId 不重复、可预建 zone 指派 | P2 | 计划 |
| FR-86 | agent 版本/构建可见性（增强 FR-34/FR-52，依赖新 ADR，双端 jar）：agent 注册自报版本/build，InstanceView + 服务器页显示、集群内版本不一致黄标，治 agent 跑哪个构建运维不可见的盲区 | P2 | 计划 |
| FR-87 | 反向抓取受管任务进度/错误回传（增强 FR-58/FR-60，扩展 ADR-0037 spec，改 agent）：任务显 elapsed + 卡死阈值警示、agent 端错误回传进 task.lastError 展示 | P2 | 计划 |
| FR-88 | 在线日志/诊断查看器（feat，依赖新 ADR·安全边界，双端 jar）：agent 只读日志端点（限本机/脱敏/限速）+ 服务器详情「查看 agent 日志」拉最近 N 行，排障免上机 | P2 | 计划 |
| FR-89 | 告警历史 / 事件信息流（feat，依赖新 ADR·新实体）：健康流转/发布失败/后端不可达落 alert_event 表 + 新「事件」页时间线展示 | P2 | 计划 |
| FR-90 | admin API token 管理（feat，依赖新 ADR·鉴权面）：签发/吊销/到期 admin API token（区别 agent apikey）供脚本调 admin API + 关键操作「复制为 curl」 | P2 | 计划 |
| FR-91 | 服务器行快捷操作（增强 FR-65，依赖 FR-86+FR-88）：行菜单加 agent 详情/查看日志/强制重同步（触发该 agent 重拉有效配置） | P2 | 计划 |
| FR-92 | 暗色模式 / NOC 大屏只读 / 紧凑密度（feat）：暗色主题切换、只读「大屏」看板路由、表格密度开关，持久化偏好 | P3 | 计划 |

> **P1 范围说明（提示位归档 P2）**：心跳响应的 `configDirty` 优化提示位**不在 P1 实现、恒返 `false`**——变更感知由 FR-2 长轮询负责，agent 不依赖该位；作为 P2 优化（API 细节见 `docs/API.md` §2）。

## 5. 非功能需求（NFR）

- **可用性**：控制面单节点可秒级重启；**控制面挂 ≠ 数据面挂**，agent 本地快照 fail-static，不阻断玩家。
- **性能**：50 服规模；注册/健康在内存；agent 不在 MC 主线程做阻塞 IO。
- **可移植**：DB 经 GORM，禁 MySQL 专有特性，可切 Postgres。
- **简单优先**：不引入 Redis/MQ/DI 框架等重型件。
- **可维护**：中文注释/日志/提交；分级日志（ERROR/WARN/INFO/DEBUG）。
- **安全**：敏感项（DB 密码、token）走 env 不入库；管理面鉴权前移本批（见 [ADR-0009](adr/0009-control-plane-auth-pulled-forward.md)），数据面内网可信不变，配置加密仍属后期（FR-20）。运行时 API 密钥（FR-42）落库**只存哈希**、明文一次性返回不可二次读取（见 [ADR-0026](adr/0026-runtime-api-keys-and-readonly-role.md)）；只读角色对外部服务"只读拒写"。

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
