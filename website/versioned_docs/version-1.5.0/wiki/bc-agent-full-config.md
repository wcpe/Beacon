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

delivery:
  config-reload-timeout-ms: 30000
  reload-pool-size: 64
```

## 字段说明与 BC 专项边界

| 配置段 | 默认值 | 何时调整 |
| --- | --- | --- |
| `timing` | 如示例 | 仅在网络超时策略需要调整时修改；服务端心跳间隔仍是运行时权威。 |
| `backoff` | 如示例 | 网络恢复或大量代理重连需要调整退避节奏时修改。 |
| `snapshot` | 启用 | 受控目录与大厅候选的 fail-static 依赖快照；不要手工编辑或跨机器复制。 |
| `file-tree` | 启用 | 不使用受管文件交付时可关闭；`target-sub-dir` 必须是 plugins 目录下的相对路径。 |
| `assets` | 启用、1800 秒 | 不需要文件资产盘点时可关闭；扫描间隔低于 300 秒会自动收口到 300 秒。 |
| `override` | 空白名单 | 只有需要受控重载第三方插件时添加首命令词；空白名单表示拒绝全部重载命令。该名单可由控制面下发，但属高危项、必须走审批。 |
| `messaging` | 关闭 | 需要业务插件使用 HTTP 跨服消息时显式设为 `true`；不要添加 Redis 地址、密码、消费组或其他旧中间件字段。 |
| `delivery.config-reload-timeout-ms` | 30000，范围 0–3600000 | 单个业务插件的配置重载超时（毫秒）。填 0 = 不设超时（不建议）；须小于变更单的生效超时，否则控制面先判整体超时。 |
| `delivery.reload-pool-size` | 64，范围 1–256 | 单服并发 reload 的插件数上限。改动会换线程池，但在途 reload 不中断，语义见[配置、文件与交付](configuration-and-delivery.md)。 |

不要添加 `proxy.home-group`、`proxy.home-zone` 或 listener `priorities` 来指定首次落脚。它们已不再是正确路径：全局 LobbyCluster、默认入口和可调度候选由控制面维护，业务插件负责大厅后的选区与排队。

## 覆盖规则

- `BEACON_AGENT_*` 环境变量可覆盖对应本地字段，适合由受控部署系统注入 endpoint 与 token；不要把真实 token 写入版本库。
- BC 原生配置是 listener 的事实来源；Agent 上报全部 listener，控制面以 `detected` 地址为准，并只在 NAT 或反向代理场景对单个 listener 设置 `override`。
- 控制面/Web 管理 BCCluster、LobbyCluster、Region、Zone、全局大厅候选与目录重同步；这些业务字段不应出现在 Agent 配置。
- 控制面暂时不可用时，已确认 Agent 使用最后有效快照 fail-static；没有安全大厅候选时 BC 应明确拒绝首次进入，不应回退到任意普通后端。

## 零配置接入：先装好，再决定归属

`beacon.endpoints` 与 `beacon.bootstrap-token` **都为空**是受支持的状态，不是配置错误：代理侧同样只打一条提示（引导执行 `/beacon join <控制面地址> <令牌>`），**不进入重连循环、不产生任何出站请求**，listener 上报与大厅候选等能力在接入前不会启动。

| 形态 | 行为 | 日志 |
| --- | --- | --- |
| endpoints 与 token 皆为空 | 等待接入，不出站 | 一条 INFO 指引 |
| 只填了其中一项 | 不接入 | WARN「接入配置不完整」 |
| 填的仍是 `<CONTROL_PLANE_URL>` 这类模板占位符 | 按未配置处理，占位符不会写入凭据 | WARN |

接入之后不要把凭据抄回配置：

- 身份标识只有单一真源，按「凭据文件 → 旧 `identity.yml`（一次性迁移）→ 新生成」解析；已有合法凭据时不再被旧文件或 `config.yml` 改写，否则控制面上会多出一台重复代理身份。
- 令牌**只**写入数据目录的 `agent-credentials.json`（权限尽力 0600），**不写入 `config.yml`，也不写入 `identity.yml`**；后者只承载 `format-version` / `identity-id` / `created-at`。
- 凭据文件的 `binding` 记录控制面确认的归属，**已绑定的代理再执行 `/beacon join` 会被直接拒绝**并回显当前归属——一台代理只能对应一个身份，换控制面须先停插件并由管理员解绑。

> 不要把 `plugins/BeaconAgentProxy/` 整目录复制到第二台代理：两份相同身份会触发身份冲突，两台都不可调度（见[排障手册](troubleshooting.md)）。

## 控制面下发运行参数

接入目标与令牌之外的可调项都可由控制面按作用域下发（`namespace` 默认 + `server` 覆盖），代理侧与插件端规则一致：

- Agent 每 **30 秒**拉取一次 `GET /beacon/v2/agent/settings`；生效优先级为**下发值 > `BEACON_AGENT_*` 环境变量 > `config.yml` > 代码默认**。`delivery.config-reload-timeout-ms` 与 `delivery.reload-pool-size` 直接应用到运行中的重载派发器，其余参数在下一次完整重载时重新读取。
- **断连仍生效**：下发值落到本地 `pushed-config.json`（带格式版本），控制面不可达或 Agent 重启时沿用缓存，不退回代码默认；在下发里删掉某项则按「`config.yml` → 代码默认」逐级回退并在日志写明「已被撤销」。
- 缓存不可读 / 内容损坏 / 格式版本不受支持时降级到本地配置并告警，三种原因分别归类、同一原因只报一次，Agent 照常启动。
- `override.command-whitelist` 属高危项：提交**整单转审批**，接口返回 **202** 票据且库内零变更，审批通过后才写入。禁区键（`beacon.endpoints`、`beacon.bootstrap-token` 及整个 `identity.` 前缀）提交即在申请阶段被 **400** `AGENT_SETTING_KEY_FORBIDDEN` 拒绝，且不产生票据——接入目标与身份只能由本机决定，避免控制面被攻破后反向接管代理。

多 listener、目录命令和玩家首连验收见[全局大厅与 BC 接入操作手册](global-lobby-operations.md)，本地安全边界与备份见[安全与维护](security-and-maintenance.md)。
