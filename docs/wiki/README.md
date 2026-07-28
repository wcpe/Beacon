# Beacon 使用 Wiki

本目录存放面向服主、运维和业务插件开发者的 **v2 使用教程**。它说明“怎样使用当前交付的能力”；需求、接口和架构决策仍分别以 `docs/PRD.md`、`docs/API.md`、`docs/adr/` 为权威来源。

## 从这里开始

1. [快速开始](quick-start.md)：用单机 SQLite 控制面接入第一台 Paper/Bukkit 服务。
2. [从零搭建完整服务器](build-a-cluster.md)：搭建控制面、BC、两台全局登录服、小区大厅与游戏服，并完成玩家首连验收。
3. [全局大厅与 BC 接入操作手册](global-lobby-operations.md)：运营现有的 BC → LobbyCluster → 小区拓扑及故障切换。

## 功能教程

| 主题 | 阅读文档 | 覆盖能力 |
| --- | --- | --- |
| 配置、文件与交付 | [配置、文件与交付](configuration-and-delivery.md) | 有效配置、受管文件、配置版本、变更单、Agent 拉取与回退。 |
| Agent 本地可选项 | [Bukkit/Paper 全量配置](bukkit-agent-full-config.md) / [BC/Bungee 全量配置](bc-agent-full-config.md) | 最小引导外的本地运行参数、默认值与安全边界。 |
| 拓扑与玩家首连 | [全局大厅与 BC 接入操作手册](global-lobby-operations.md) | namespace、BCCluster、Region、Zone、LobbyCluster、多 listener 与首连落点。 |
| 健康、命令与审计 | [可观测与日常运维](observability-and-operations.md) | 健康状态、告警、BC 查询/目录命令、重同步、审计与调度判断。 |
| 业务插件接入 | [业务插件 SDK](business-plugin-sdk.md) | Bukkit/Bungee 本地 SDK、配置读取、命令、HTTP 中转消息与 fail-static。 |
| 权限、备份与升级 | [安全与维护](security-and-maintenance.md) | 身份审批、token、最小权限、SQLite/外置 MySQL 备份、升级和回滚。 |
| 问题定位 | [故障排查](troubleshooting.md) | 注册、审批、配置、SSE、地址、全局大厅和消息链路。 |

## 适用边界

- 新部署以 v2 的 namespace token、身份审批与控制面拓扑为准；`docs/API.md` 中较早的 v1 路由和旧配置仅作兼容性参考，不作为新接入步骤。
- 当前教程覆盖 Bukkit/Paper 后端与 Bungee/BC 代理的 Agent 接入。未在本目录单独给出安装步骤的平台，不应据此推定受支持。
- Redis 跨服消息配置已由 HTTP 单跳中转取代；新集群不要为 Beacon 配置 Redis。
- 业务插件负责选区、排队、小区默认服和后续转服；Beacon 只负责控制面、受管目录与 BC 首次落到全局 LobbyCluster。

## 相关权威资料

- [部署与运维手册](../OPERATIONS.md)
- [架构说明](../ARCHITECTURE.md)
- [API 参考](../API.md)
- [业务插件 SDK API](../SDK.md)
- [架构决策记录](../adr/)
