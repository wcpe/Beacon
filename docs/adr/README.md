# 架构决策记录（ADR）

记录 Beacon 的重大架构决策：背景、决策、理由、后果与被否的备选。每条决策一页，便于后来者理解"为什么是这样"。

| 编号 | 决策 | 状态 |
|---|---|---|
| [0001](0001-self-build-vs-nacos.md) | 自研控制面，而非直接使用 Nacos | 已接受 |
| [0002](0002-go-react-embedded-stack.md) | Go 后端 + 内嵌 React 单二进制技术栈 | 已接受 |
| [0003](0003-no-redis-in-mvp.md) | 第一期不引入 Redis | 已接受 |
| [0004](0004-zone-authority-control-plane.md) | zone 归属由控制面 DB 权威指派 | 已接受 |
| [0005](0005-agent-transport-codec-abstraction.md) | agent 的 HTTP 客户端与 JSON 序列化走抽象层 | 已接受 |
| [0006](0006-rest-long-poll-push.md) | 配置推送用 REST 长轮询 | 已接受 |
| [0007](0007-versioning-and-release-channels.md) | 版本来源与发布渠道（VERSION + 稳定/快照） | 已接受 |
| [0008](0008-config-soft-delete-and-effective-md5.md) | 配置软删唯一性与有效配置 md5 取舍 | 已接受 |

> 模板：状态 / 背景 / 决策 / 理由 / 后果 / 备选方案。
