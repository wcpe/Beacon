# BC/Bungee Agent 全量可选配置

默认 `BeaconAgentProxy` 模板只保留控制面 endpoint 与 namespace 接入 token。以下内容是**按需追加**到同一 `config.yml` 的完整参考：未写出的字段会使用标明的默认值。

这份文件只描述 Beacon Agent 配置；BC 原生 `config.yml` 仍负责定义 listener。Agent 会自动上报全部 listener，控制面负责拓扑、全局大厅和首次落脚。完整拓扑流程见[全局大厅与 BC 接入操作手册](global-lobby-operations.md)。

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

## 字段说明与 BC 专项边界

| 配置段 | 默认值 | 何时调整 |
| --- | --- | --- |
| `timing` | 如示例 | 仅在网络超时策略需要调整时修改；服务端心跳间隔仍是运行时权威。 |
| `backoff` | 如示例 | 网络恢复或大量代理重连需要调整退避节奏时修改。 |
| `snapshot` | 启用 | 受控目录与大厅候选的 fail-static 依赖快照；不要手工编辑或跨机器复制。 |
| `file-tree` | 启用 | 不使用受管文件交付时可关闭；`target-sub-dir` 必须是 plugins 目录下的相对路径。 |
| `assets` | 启用、1800 秒 | 不需要文件资产盘点时可关闭；扫描间隔低于 300 秒会自动收口到 300 秒。 |
| `override` | 空白名单 | 只有需要受控重载第三方插件时添加首命令词；空白名单表示拒绝全部重载命令，控制面不能放宽它。 |
| `messaging` | 关闭 | 需要业务插件使用 HTTP 跨服消息时显式设为 `true`；不要添加 Redis 地址、密码、消费组或其他旧中间件字段。 |

不要添加 `proxy.home-group`、`proxy.home-zone` 或 listener `priorities` 来指定首次落脚。它们已不再是正确路径：全局 LobbyCluster、默认入口和可调度候选由控制面维护，业务插件负责大厅后的选区与排队。

## 覆盖规则

- `BEACON_AGENT_*` 环境变量可覆盖对应本地字段，适合由受控部署系统注入 endpoint 与 token；不要把真实 token 写入版本库。
- BC 原生配置是 listener 的事实来源；Agent 上报全部 listener，控制面以 `detected` 地址为准，并只在 NAT 或反向代理场景对单个 listener 设置 `override`。
- 控制面/Web 管理 BCCluster、LobbyCluster、Region、Zone、全局大厅候选与目录重同步；这些业务字段不应出现在 Agent 配置。
- 控制面暂时不可用时，已确认 Agent 使用最后有效快照 fail-static；没有安全大厅候选时 BC 应明确拒绝首次进入，不应回退到任意普通后端。

多 listener、目录命令和玩家首连验收见[全局大厅与 BC 接入操作手册](global-lobby-operations.md)，本地安全边界与备份见[安全与维护](security-and-maintenance.md)。
