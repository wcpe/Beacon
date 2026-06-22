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
| [0007](0007-versioning-and-release-channels.md) | 版本来源与发布渠道（VERSION + 稳定/快照） | 已接受 |
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
| [0019](0019-health-alert-channel-abstraction.md) | 健康告警通道做成可扩展抽象（站内信 + webhook） | 已接受 |
| [0020](0020-prometheus-metrics-observability.md) | 控制面用标准 Prometheus client 暴露运行指标（/metrics） | 已接受 |
| [0021](0021-config-gray-cohort-version-selection.md) | 配置灰度：按显式 serverId 名单（cohort）在版本选择层叠加，promote/abort 收口 | 已接受 |
| [0022](0022-agent-roster-read-api.md) | agent-api 暴露玩家位置名册只读查询（扩展 ADR-0016 决策 5） | 已接受 |
| [0023](0023-control-plane-observability-dashboard.md) | 控制面自带可观测看板（指标 + 历史趋势），时序落 MySQL metric_sample | 已接受 |
| [0024](0024-bc-backend-membership-as-fact.md) | bc 上报自身后端归属作为控制面只读事实（仅内存、随注册/心跳更新） | 已接受 |
| [0025](0025-bc-proxy-metrics-and-netty-traffic.md) | bc 代理专属负载指标采集集合与角色分流展示（扩展 ADR-0023；Netty 吞吐无干净注入点本期不采、不留占位） | 已接受 |
| [0026](0026-runtime-api-keys-and-readonly-role.md) | 运行时 API 密钥 + 管理面只读角色（落库只存哈希，扩展 ADR-0009） | 已接受 |
| [0027](0027-reverse-fetch-channel-and-security.md) | 在线实例反向抓取的命令通道（复用 SSE）与安全边界（限 plugins/ 内、排除 jar、上限、双校验、鉴权审计） | 已接受 |
| [0028](0028-allow-hosting-agent-self-dir.md) | 放开控制面对 agent 自身目录的托管拦截，自我保护下沉到 agent observe-only（FR-38/FR-39 归真） | 已接受 |
| [0029](0029-file-tree-structured-deep-merge.md) | 文件树结构化文件跨层深合并、可按文件豁免（取代 ADR-0010 决策1） | 已接受（合并语义不变；**「值归一化可接受」一条被 [0034](0034-file-tree-lossless-merge.md) 取代**） |
| [0030](0030-git-export-mirror.md) | git 单向导出镜像（派生备份 / 灾备 / 外部可见，不作第二真源；推荐 go-git） | 已接受 |
| [0031](0031-zone-default-entry-and-bc-injection.md) | 小区默认入口（DB 权威）+ BC 注入 BungeeCord 默认/fallback 服（home-zone 为数据面路由配置，不违反 ADR-0004） | 已接受 |
| [0032](0032-instance-active-offline-state.md) | 实例主动下线态：落 DB 拒绝接入，区别于 drain 与健康 TTL（显式扩展 ADR-0017 范围） | 已接受 |
| [0033](0033-web-i18n-framework.md) | 管理台引入 react-i18next 国际化框架：zh-CN 先行、全站文案 key 化、审计 action 经 i18n 映射、等值迁移 | 已接受 |
| [0034](0034-file-tree-lossless-merge.md) | 文件树通道改无损深合并（保标量原文 / 精度 / 注释），配置中心维持有损（取代 ADR-0029「值归一化可接受」一条） | 已接受 |

> 模板：状态 / 背景 / 决策 / 理由 / 后果 / 备选方案。

> **别慌通读**：ADR 有意稀少（只为重大决策写），理解现状看 [`../ARCHITECTURE.md`](../ARCHITECTURE.md)，ADR 只按需查"为什么"；被取代的归档不打扰，当前架构 = 未取代的活跃集。增长过快是滥写信号——日常变更归 PRD 状态列 + CHANGELOG。
