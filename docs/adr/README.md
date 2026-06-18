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
| [0010](0010-file-tree-hosting-blob-channel.md) | 文件树托管 blob 通道（通道B），区别于配置深合并 | 已接受 |
| [0011](0011-third-party-file-override-and-restricted-reload-command.md) | 三方插件文件覆盖与受限重载命令的安全边界 | 已接受 |
| [0012](0012-web-shadcn-ui-design-system.md) | 管理台引入 shadcn-ui + Tailwind 作为设计系统 | 已接受 |
| [0013](0013-admin-effective-config-preview-and-provenance.md) | admin 有效配置只读预览 + 逐键来源 provenance | 已接受 |
| [0014](0014-downstream-identity-source-direction.md) | 下游身份来源方向：优先取自 Beacon agent，本地降级 | 已接受 |
| [0015](0015-sse-server-push-transport.md) | agent↔控制面 server→agent 推送合并为单条 SSE 流（取代 ADR-0006） | 已接受 |
| [0016](0016-agent-cross-server-messaging-middleware.md) | agent 内置跨服消息中间件：基于 Redis 的通用通信层 | 已接受 |
| [0017](0017-traffic-scheduling-decision-vs-execution.md) | 流量调度：控制面给决策 / 数据面做执行，drain 落 DB、canary 划范围外 | 已接受 |
| [0019](0019-health-alert-channel-abstraction.md) | 健康告警通道做成可扩展抽象（站内信 + webhook） | 已接受 |

> 模板：状态 / 背景 / 决策 / 理由 / 后果 / 备选方案。

> **别慌通读**：ADR 有意稀少（只为重大决策写），理解现状看 [`../ARCHITECTURE.md`](../ARCHITECTURE.md)，ADR 只按需查"为什么"；被取代的归档不打扰，当前架构 = 未取代的活跃集。增长过快是滥写信号——日常变更归 PRD 状态列 + CHANGELOG。
