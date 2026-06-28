# 架构决策记录（ADR）

记录 Beacon 的重大架构决策：背景、决策、理由、后果与被否的备选。每条决策一页，便于后来者理解"为什么是这样"。

| 编号 | 决策 | 状态 |
|---|---|---|
| [0001](0001-self-build-vs-nacos.md) | 自研控制面，而非直接使用 Nacos | 已接受 |
| [0002](0002-go-react-embedded-stack.md) | Go 后端 + 内嵌 React 单二进制技术栈 | 已接受 |
| [0003](0003-no-redis-in-mvp.md) | 第一期不引入 Redis | 已接受 |
| [0004](0004-zone-authority-control-plane.md) | zone 归属由控制面 DB 权威指派 | 已接受 |
| [0005](0005-agent-transport-codec-abstraction.md) | agent 的 HTTP 客户端与 JSON 序列化走抽象层 | 已接受 |
| [0006](0006-rest-long-poll-push.md) | 配置推送用 REST 长轮询 | 已被 [0015](0015-sse-server-push-transport.md) 取代 |
| [0007](0007-versioning-and-release-channels.md) | 版本来源与发布渠道（VERSION + 稳定/快照） | 已接受（**「快照=滚动 latest」一条被 [0046](0046-rc-prerelease-channel.md) 取代**） |
| [0008](0008-config-soft-delete-and-effective-md5.md) | 配置软删唯一性与有效配置 md5 取舍 | 已接受 |
| [0009](0009-control-plane-auth-pulled-forward.md) | 管理面鉴权从 P2 前移 | 已接受 |
| [0010](0010-file-tree-hosting-blob-channel.md) | 文件树托管 blob 通道（通道B），区别于配置深合并 | 已接受（决策1 被 [0029](0029-file-tree-structured-deep-merge.md) 取代） |
| [0011](0011-third-party-file-override-and-restricted-reload-command.md) | 三方插件文件覆盖与受限重载命令的安全边界 | 已接受 |
| [0012](0012-web-shadcn-ui-design-system.md) | 管理台引入 shadcn-ui + Tailwind 作为设计系统 | 已接受 |
| [0013](0013-admin-effective-config-preview-and-provenance.md) | admin 有效配置只读预览 + 逐键来源 provenance | 已接受 |
| [0014](0014-downstream-identity-source-direction.md) | 下游身份来源方向：优先取自 Beacon agent，本地降级 | 已接受 |
| [0015](0015-sse-server-push-transport.md) | agent↔控制面 server→agent 推送合并为单条 SSE 流（取代 ADR-0006） | 已接受 |
| [0016](0016-agent-cross-server-messaging-middleware.md) | agent 内置跨服消息中间件：基于 Redis 的通用通信层 | 已接受 |
| [0017](0017-traffic-scheduling-decision-vs-execution.md) | 流量调度：控制面给决策 / 数据面做执行，drain 落 DB、canary 划范围外 | 已接受 |
| [0018](0018-config-encryption-at-rest.md) | 敏感配置 at-rest 加密：AES-256-GCM、密钥走 env、密文落 TEXT 可移植 | 已接受 |
| [0019](0019-health-alert-channel-abstraction.md) | 健康告警通道做成可扩展抽象（站内信 + webhook） | 已接受（**「告警不落库」一条被 [0041](0041-alert-event-persistence.md) 取代**） |
| [0020](0020-prometheus-metrics-observability.md) | 控制面用标准 Prometheus client 暴露运行指标（/metrics） | 已接受 |
| [0021](0021-config-gray-cohort-version-selection.md) | 配置灰度：按显式 serverId 名单（cohort）在版本选择层叠加，promote/abort 收口 | 已接受 |
| [0022](0022-agent-roster-read-api.md) | agent-api 暴露玩家位置名册只读查询（扩展 ADR-0016 决策 5） | 已接受 |
| [0023](0023-control-plane-observability-dashboard.md) | 控制面自带可观测看板（指标 + 历史趋势），时序落 MySQL metric_sample | 已接受 |
| [0024](0024-bc-backend-membership-as-fact.md) | bc 上报自身后端归属作为控制面只读事实（仅内存、随注册/心跳更新） | 已接受 |
| [0025](0025-bc-proxy-metrics-and-netty-traffic.md) | bc 代理专属负载指标采集集合与角色分流展示（扩展 ADR-0023；Netty 吞吐无干净注入点本期不采、不留占位） | 已接受（后端可达性探测机制被 [0035](0035-backend-reachability-tcp-connect.md) 修订） |
| [0026](0026-runtime-api-keys-and-readonly-role.md) | 运行时 API 密钥 + 管理面只读角色（落库只存哈希，扩展 ADR-0009） | 已接受 |
| [0027](0027-reverse-fetch-channel-and-security.md) | 在线实例反向抓取的命令通道（复用 SSE）与安全边界（限 plugins/ 内、排除 jar、上限、双校验、鉴权审计） | 已接受 |
| [0028](0028-allow-hosting-agent-self-dir.md) | 放开控制面对 agent 自身目录的托管拦截，自我保护下沉到 agent observe-only（FR-38/FR-39 归真） | 已接受 |
| [0029](0029-file-tree-structured-deep-merge.md) | 文件树结构化文件跨层深合并、可按文件豁免（取代 ADR-0010 决策1） | 已接受（合并语义不变；**「值归一化可接受」一条被 [0034](0034-file-tree-lossless-merge.md) 取代**） |
| [0030](0030-git-export-mirror.md) | git 单向导出镜像（派生备份 / 灾备 / 外部可见，不作第二真源；推荐 go-git） | 已接受 |
| [0031](0031-zone-default-entry-and-bc-injection.md) | 小区默认入口（DB 权威）+ BC 注入 BungeeCord 默认/fallback 服（home-zone 为数据面路由配置，不违反 ADR-0004） | 已接受 |
| [0032](0032-instance-active-offline-state.md) | 实例主动下线态：落 DB 拒绝接入，区别于 drain 与健康 TTL（显式扩展 ADR-0017 范围） | 已接受 |
| [0033](0033-web-i18n-framework.md) | 管理台引入 react-i18next 国际化框架：zh-CN 先行、全站文案 key 化、审计 action 经 i18n 映射、等值迁移 | 已接受 |
| [0034](0034-file-tree-lossless-merge.md) | 文件树通道改无损深合并（保标量原文 / 精度 / 注释），配置中心维持有损（取代 ADR-0029「值归一化可接受」一条） | 已接受 |
| [0035](0035-backend-reachability-tcp-connect.md) | bc 后端可达性探测由 MC status-ping 改 TCP 连接（修订 ADR-0025 决策1/3 的探测机制，更稳健、对不应答 status 的代理后端不误判） | 已接受 |
| [0036](0036-zone-reassign-safety-drain-gate.md) | 区分配改派安全：排空门后端硬闸 + 高摩擦显式改派 | 已接受 |
| [0037](0037-reverse-fetch-managed-task.md) | 反向抓取受管任务 + 状态机 + 两段式 scan/submit（取代 ADR-0027 决策5、扩展决策1） | 已接受 |
| [0038](0038-ops-settings-store-hot-reload.md) | 运维设置 store + 热生效：热改项真源由 config.yml 移到 DB | 已接受 |
| [0039](0039-agent-self-reported-version.md) | agent 注册时自报构建版本（壳层经 TabooLib pluginVersion 注入），控制面只读暴露 InstanceView、管理台展示 + 集群版本不一致黄标 | 已接受 |
| [0040](0040-agent-readonly-log-tail.md) | agent 只读日志回传：自身日志内存环形缓冲 + 命令-回传 + 落缓冲脱敏（不读任意文件、行数有界、限速、agentToken 信任面） | 已接受 |
| [0041](0041-alert-event-persistence.md) | 告警事件持久化实体 alert_event（留痕 + UI 信息流，作 PersistAlerter 通道接入扇出，取代 ADR-0019「告警不落库」结论） | 已接受 |
| [0042](0042-admin-api-token.md) | 脚本化 admin API token 复用 FR-42 运行时 API 密钥，仅补「复制为 curl」自动化辅助（扩展 ADR-0026，不新增并行凭据） | 已接受 |
| [0043](0043-admin-nav-grouping-and-settings-aggregation.md) | 管理台导航分组 + 设置区聚合 IA：侧栏 5 组可折叠手风琴 + 设置三块顶层 tab/子 tab + 嵌套子路由 search param 深链 + 旧路由前端重定向 | 部分被 [0048](0048-flatten-system-nav-pages.md) 取代（聚合/折叠/二级子 tab 部分；5 组手风琴 + NAV 单一真源仍有效） |
| [0044](0044-control-plane-online-self-update.md) | 控制面在线自更新核心：按渠道查 wcpe/Beacon Release → 下载本平台资产（超时/上限/失败清理）→ SHA256 校验 → 原子落位 pending → 以退出码 70 交还 launcher → 任何阶段失败保留旧版不退；进度内存态不建表、审计落库（渠道语义引 ADR-0046） | 已接受（决策 5/6/7 的 launcher 交接部分被 [0053](0053-single-binary-self-replace.md) 取代） |
| [0045](0045-builtin-launcher-supervisor.md) | 内置 launcher 监督进程：独立第二二进制 + 退出码协议（0/1/70）+ 跨平台换二进制（Win rename 让位 / Unix rename 覆盖）+ 端口先退后起 + 裸跑兜底，本期 launcher 不自更新 | 已被 [0053](0053-single-binary-self-replace.md) 取代 |
| [0046](0046-rc-prerelease-channel.md) | rc 预发布渠道：语义化 rc 号（vX.Y.Z-rc.N）+ prerelease=true + 渠道判定（最新非 prerelease=正式 / 最新 prerelease=rc）+ 不做 nightly/beta（取代 ADR-0007「快照=滚动 latest」一条；标注 sdd-publish-snapshot 技能与新模型冲突） | rc 语义号/不滚动/rc 判定 被 [0052](0052-rolling-prerelease-channel.md) 取代 |
| [0047](0047-update-outbound-proxy-and-secret-redaction.md) | 控制面更新出站代理 + httpx 客户端工厂收口 + 含凭据设置项脱敏（扩展 ADR-0038；仅更新出站不动 webhook，不照搬 ADR-0005 抽象） | 已接受 |
| [0048](0048-flatten-system-nav-pages.md) | 管理台「系统」区拍平为 5 个扁平独立页（运维设置 / 版本与更新 / 控制面健康 / 密钥 / 环境）：取代 ADR-0043 的设置聚合页 + 旧页折叠 + 二级子 tab；版本与更新独立成页（渠道/检查/更新/代理/更新设置）；页眉版本徽章改跳转 | 已接受 |
| [0049](0049-agent-fs-browse.md) | agent 只读交互式文件浏览（懒列目录 / 读文件树 / 读单文件）：限 plugins 根 + path traversal 强校验 + 纯只读 + async 不碰主线程 + 大目录分页惰加载 + fail-static；控制面侧（FR-110）复用 ADR-0027/0037 命令通道 + FR-104 生命周期代理浏览（区别于 FR-58 一次性 scan） | 已接受 |
| [0050](0050-config-xftp-workspace.md) | 配置中心双面板 Xftp 工作台（左受管树 ↔ 右在线服实时浏览 plugins）：**前端改接已有分散端点、不新造 /workbench/* 聚合 BFF**（原型 mock 退场）；FR-112 真详情多标签编辑器、FR-113 三页合一 IA + 退役 ConfigsPage，反抓/拓印后端复用不变 | 已接受 |
| [0051](0051-config-operation-undo.md) | 配置操作级撤回子系统：新增可移植 `reversible_operation` 表对 push/publish/fetch 记可逆账目，撤回复用既有版本回滚 + 长轮询重推 + 反抓软删，多表写事务原子（提交后才唤醒）+ status 幂等闸 + 行/乐观锁并发安全 + 时间窗/被覆盖双闸防脏撤回，agent 零改 | 已接受 |
| [0052](0052-rolling-prerelease-channel.md) | 滚动预发布渠道 + 版本号判新（取代 ADR-0046 的 rc 模型）：渠道收敛为「正式/预发布」两条、去 rc；推 master 自动覆盖发布滚动 prerelease（移动 tag、版本=VERSION）；in-app 更新按语义版本号 X.Y.Z 比较判新（同号不提示、跨号才提示）；渠道仍用 GitHub prerelease 布尔区分、复用 _build-release.yml | 决策 2 被 [0054](0054-rolling-prerelease-version-ci-computed.md) 取代、决策 4/5 被 [0055](0055-rolling-prerelease-dev-sha-version.md) 取代、余仍有效 |
| [0053](0053-single-binary-self-replace.md) | 控制面单进程二进制自替换 + 自动回滚（取代 [0045](0045-builtin-launcher-supervisor.md)、部分取代 [0044](0044-control-plane-online-self-update.md) 决策 5/6/7）：Windows 可 rename 运行中 exe（证伪「主进程自换必败」），主进程 rename 让位三步自替换 + sentinel 启动自检 + 崩 N 次自动回退 .old；删 beacon-launcher 与 exitcode，崩溃自启交外部 docker/systemd | 已接受 |
| [0054](0054-rolling-prerelease-version-ci-computed.md) | 滚动预发布版本号由 CI 自算（取代 [0052](0052-rolling-prerelease-channel.md) 决策 2 版本取 VERSION）：CI 取最新正式 release minor+1、与 VERSION 解耦自动领先，根除「发版后忘 bump VERSION 致预发布与正式版同号」 | CI 自算 / 与 VERSION 解耦仍有效；基线 minor+1 与「纯 X.Y.Z」均被 [0056](0056-rolling-prerelease-dev-distance-version.md) 取代 |
| [0055](0055-rolling-prerelease-dev-sha-version.md) | 滚动预发布版本号带 -dev.&lt;sha&gt; + 同基线标识变即提示更新（取代 [0052](0052-rolling-prerelease-channel.md) 决策 4/5 与 [0054](0054-rolling-prerelease-version-ci-computed.md) 纯 X.Y.Z）：版本号 = minor+1 基线 + -dev.&lt;7位 commit sha&gt;（如 0.18.0-dev.715989a），in-app 判新改基线比较 + 同基线 dev.sha 标识变即更新，使每次 push 真机可反复检测触发、验证在线更新；正式渠道仍纯 X.Y.Z | 已被 [0056](0056-rolling-prerelease-dev-distance-version.md) 取代 |
| [0056](0056-rolling-prerelease-dev-distance-version.md) | 滚动预发布版本号改 &lt;基线&gt;-dev.&lt;提交距离&gt;.g&lt;sha&gt; + 提交距离序号判新（取代 [0055](0055-rolling-prerelease-dev-sha-version.md) 全部、[0054](0054-rolling-prerelease-version-ci-computed.md) 基线 minor+1）：基线 = 最新正式 tag 不 +1、提交距离作有序序号、收敛 scripts/dev-version.sh、移动 tag prerelease→dev；in-app 判新改「基线比较 + 提交距离序号」，无新提交不误报、有新提交必触发；正式渠道仍纯 X.Y.Z | 已接受 |
| [0057](0057-surface-desensitized-errors.md) | 操作错误脱敏后展示前端（反转「一律藏内部错误」）：新增 `internal/redact.Desensitize` 打码凭据（URL 账密 / token / password / secret / api-key / Bearer·Basic），`render.WriteError` 对内部错误返回脱敏真因（非笼统「内部错误」、仍记完整日志 + traceId），前端 MutationCache 全局兜底 toast；内网地址 / 路径等运维上下文不打码。让运维看得见失败原因又不泄露凭据（FR-122） | 已接受 |

> 模板：状态 / 背景 / 决策 / 理由 / 后果 / 备选方案。

> **别慌通读**：ADR 有意稀少（只为重大决策写），理解现状看 [`../ARCHITECTURE.md`](../ARCHITECTURE.md)，ADR 只按需查"为什么"；被取代的归档不打扰，当前架构 = 未取代的活跃集。增长过快是滥写信号——日常变更归 PRD 状态列 + CHANGELOG。
