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

delivery:
  config-reload-timeout-ms: 30000
  reload-pool-size: 64
```

## 字段说明与边界

| 配置段 | 默认值 | 何时调整 |
| --- | --- | --- |
| `timing` | 如示例 | 仅在受控网络的超时策略需要调整时修改；服务端返回的心跳间隔仍是运行时权威。 |
| `backoff` | 如示例 | 大规模重连或网络故障恢复需要调整退避节奏时修改。 |
| `snapshot` | 启用 | 已确认 Agent 的 fail-static 依赖该快照；除故障诊断外不要关闭或手工编辑快照文件。 |
| `file-tree` | 启用 | 不使用受管文件交付时可关闭；`target-sub-dir` 必须是 plugins 目录下的相对路径。 |
| `assets` | 启用、1800 秒 | 不需要文件资产盘点时可关闭；扫描间隔低于 300 秒会自动收口到 300 秒。 |
| `override` | 空白名单 | 只有需要受控重载第三方插件时添加首命令词；空白名单表示拒绝全部重载命令。该名单可由控制面下发，但属高危项、必须走审批。 |
| `messaging` | 关闭 | 需要业务插件使用 HTTP 跨服消息时显式设为 `true`；不要添加 Redis 地址、密码、消费组或其他旧中间件字段。 |
| `delivery.config-reload-timeout-ms` | 30000，范围 0–3600000 | 单个业务插件的配置重载超时（毫秒）。填 0 = 不设超时（不建议：卡死的插件会一直占住派发线程）；须小于变更单的生效超时，否则控制面先判整体超时，回执里看不出是哪个插件卡住。 |
| `delivery.reload-pool-size` | 64，范围 1–256 | 单服并发 reload 的插件数上限。改动会换线程池，但在途 reload 不中断，语义见[配置、文件与交付](configuration-and-delivery.md)。 |

以上两项与其他可调参数一样**以控制面下发为准**，本地值只是离线兜底；热重载的生效细节见[配置、文件与交付](configuration-and-delivery.md)。

## 覆盖规则

- `BEACON_AGENT_*` 环境变量可覆盖对应本地字段，适合通过受控部署系统注入 endpoint 与 token；不要把真实 token 写入版本库。
- 控制面/Web 管理身份、拓扑、listener 地址覆盖、业务配置、受管文件与交付；这些字段不应出现在本地 Agent 配置。
- 控制面暂时不可用时，已确认 Agent 使用最后有效快照 fail-static；控制面恢复后以控制面权威状态重新收敛。

## 零配置接入：先装好，再决定归属

`beacon.endpoints` 与 `beacon.bootstrap-token` **都为空**是受支持的状态，不是配置错误：Agent 只打一条提示（引导执行 `/beacon join <控制面地址> <令牌>`），**不进入重连循环、不产生任何出站请求**。先装插件观察、稍后再接入属于正常流程。

| 形态 | Agent 行为 | 日志 |
| --- | --- | --- |
| endpoints 与 token 皆为空 | 等待接入，不出站 | 一条 INFO 指引 |
| 只填了其中一项 | 不接入 | WARN「接入配置不完整」 |
| 填的仍是 `<CONTROL_PLANE_URL>` 这类模板占位符 | 按未配置处理，占位符不会写入凭据 | WARN |

接入之后不要把凭据抄回配置：

- 身份标识只有一个真源，按「凭据文件 → 旧 `identity.yml`（一次性迁移）→ 新生成」解析。已有合法凭据时**绝不**被旧文件或 `config.yml` 改写，否则控制面上会多出一台重复身份。
- 令牌**只**写入数据目录的 `agent-credentials.json`（权限尽力 0600），**不写入 `config.yml`，也不写入 `identity.yml`**。后者只承载 `format-version` / `identity-id` / `created-at`，是历史遗留的副本。
- 凭据文件里的 `binding` 记录控制面确认的归属（namespace、serverId、角色、地址、时间），断连时也可据此判断「上次绑到哪」。**已绑定的实例执行 `/beacon join` 会被直接拒绝**并回显当前归属——一台服在控制面只能对应一个身份；要换控制面必须先停插件并由管理员解绑。
- 解绑或否决后本地 `binding` 会被清空，`/beacon join` 随即恢复可用，不必手删凭据文件。

> 不要把 `plugins/BeaconAgent/` 整个目录复制给第二台服：那会得到两份相同身份，触发身份冲突（见[排障手册](troubleshooting.md)）。

## 控制面下发运行参数

除接入目标与令牌外，本文件的可调项都可由控制面按作用域下发，不必改动本地文件：

- 入口是管理面的 agent 运行配置，作用域为 `namespace` 默认 + `server` 覆盖（server 级优先）。Agent 每 **30 秒**拉取一次 `GET /beacon/v2/agent/settings`。
- 生效优先级：**下发值 > `BEACON_AGENT_*` 环境变量 > `config.yml` > 代码默认**；下发值格式非法时告警并回退到下一层。
- 在下发里删掉某项 = 撤销该层覆盖，按「`config.yml` → 代码默认」逐级回退，日志写明「已被撤销，回落到本地配置」。
- `delivery.config-reload-timeout-ms` 与 `delivery.reload-pool-size` 直接应用到运行中的重载派发器；其余参数在下一次完整重载时重新读取。

**断连仍生效**：拉取到的值落到本地 `pushed-config.json`（带格式版本）。控制面不可达、Agent 重启或网络抖动时沿用缓存，**不退回代码默认**——下发过的重载超时若回退，等于悄悄关掉保护。缓存不可读 / 内容损坏 / 格式版本不受支持会降级到本地配置并告警：三种原因分别归类、同一原因只报一次（原因变化才再报），Agent 照常启动。

**危险键走审批**：`override.command-whitelist` 属高危项，提交时**整单转审批**，接口返回 **202** 票据、库内在审批通过前**零变更**。同一次提交里的普通键也一并等待审批，避免「一半已生效、一半待批」。

**禁区键不可下发**，提交即在申请阶段被拒绝（**400** `AGENT_SETTING_KEY_FORBIDDEN`，**不产生票据**）：

| 键 | 为什么 |
| --- | --- |
| `beacon.endpoints` | 改了等于把 Agent 指向另一个控制面（目标劫持） |
| `beacon.bootstrap-token` | 改了等于换身份接入或让 Agent 再也接不回来（身份劫持） |
| `identity.server-id`、`identity.identity-id`、整个 `identity.` 前缀 | 本机身份的权威在注册/审批流程，走配置下发这条旁路必然与之打架 |

> 这些键的权威只能来自本机。拉取侧对库内残留的禁区键另有一道剔除与告警，即使控制面版本较旧也不会让 Agent 被改指向。

配置、文件与变更单的操作流程见[配置、文件与交付](configuration-and-delivery.md)，本地安全边界与备份见[安全与维护](security-and-maintenance.md)。
