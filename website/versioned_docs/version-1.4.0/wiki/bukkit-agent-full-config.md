# Bukkit/Paper Agent 全量可选配置

默认 `BeaconAgent` 模板只保留控制面 endpoint 与 namespace 接入 token。以下内容是**按需追加**到同一 `config.yml` 的完整参考：未写出的字段会使用标明的默认值。

不要在本文件写入或复制 `namespace`、`serverId`、地址、Region、Zone、LobbyCluster、容量或调度权重。这些是控制面权威数据。完整接入流程见[快速开始](quick-start.md)。

```yaml
beacon:
  endpoints:
    - "<CONTROL_PLANE_URL>"
  bootstrap-token: "<NAMESPACE_ACCESS_TOKEN>"

timing:
  poll-timeout-ms: 30000
  request-timeout-ms: 5000
  heartbeat-fallback-ms: 10000

backoff:
  initial-ms: 1000
  max-ms: 30000
  multiplier: 2.0
  jitter-ratio: 0.2

snapshot:
  enabled: true
  file-name: "effective-config.snapshot.json"

file-tree:
  enabled: true
  target-sub-dir: ""
  applied-manifest-file-name: "file-tree.applied.json"

assets:
  enabled: true
  scan-interval-sec: 1800
  manifest-file-name: "asset-manifest.json"

override:
  command-whitelist: []
  backup-dir-name: "override-backup"

messaging:
  enabled: false
  rpc-timeout-ms: 5000
  stream-max-len: 10000
  consumer-name: "default"
```

## 字段说明与边界

| 配置段 | 默认值 | 何时调整 |
| --- | --- | --- |
| `timing` | 如示例 | 仅在受控网络的超时策略需要调整时修改；服务端返回的心跳间隔仍是运行时权威。 |
| `backoff` | 如示例 | 大规模重连或网络故障恢复需要调整退避节奏时修改。 |
| `snapshot` | 启用 | 已确认 Agent 的 fail-static 依赖该快照；除故障诊断外不要关闭或手工编辑快照文件。 |
| `file-tree` | 启用 | 不使用受管文件交付时可关闭；`target-sub-dir` 必须是 plugins 目录下的相对路径。 |
| `assets` | 启用、1800 秒 | 不需要文件资产盘点时可关闭；扫描间隔低于 300 秒会自动收口到 300 秒。 |
| `override` | 空白名单 | 只有需要受控重载第三方插件时添加首命令词；空白名单表示拒绝全部重载命令，控制面不能放宽它。 |
| `messaging` | 关闭 | 需要业务插件使用 HTTP 跨服消息时显式设为 `true`；不要添加 Redis 地址、密码、消费组或其他旧中间件字段。 |

## 覆盖规则

- `BEACON_AGENT_*` 环境变量可覆盖对应本地字段，适合通过受控部署系统注入 endpoint 与 token；不要把真实 token 写入版本库。
- 控制面/Web 管理身份、拓扑、listener 地址覆盖、业务配置、受管文件与交付；这些字段不应出现在本地 Agent 配置。
- 控制面暂时不可用时，已确认 Agent 使用最后有效快照 fail-static；控制面恢复后以控制面权威状态重新收敛。

配置、文件与变更单的操作流程见[配置、文件与交付](configuration-and-delivery.md)，本地安全边界与备份见[安全与维护](security-and-maintenance.md)。
