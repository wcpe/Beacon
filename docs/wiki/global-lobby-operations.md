# 全局大厅与 BC 接入操作手册

本文说明如何部署并验收一套「BC → 全局大厅集群 → 业务小区」拓扑。它只描述运维步骤；业务插件负责大厅内选区、排队、小区默认服与后续转服，Beacon 不接管这些行为。

相关权威决策：

- [ADR-0075：LobbyCluster 与 BC 首次落脚](../adr/0075-lobby-cluster-and-bc-first-entry.md)
- [ADR-0076：极简身份接入](../adr/0076-control-plane-identity-bootstrap.md)
- [ADR-0077：多 listener 地址权威](../adr/0077-agent-endpoint-address-authority.md)

## 1. 目标拓扑

一个 BC 服务一个 namespace。大区与小区仅是业务虚拟分隔；全局大厅独立于它们。

```text
玩家
  └─ BC 的任一 listener
       └─ 全局 LobbyCluster（login-1 / login-2 / …）
            └─ 业务插件：选区、排队、转入目标小区
                 └─ Region / Zone 内的小区大厅与游戏服
```

- BC 的首次连接只会从 `LobbyCluster` 的可调度成员中选择落点。
- 同一 namespace 只能有一个 `LobbyCluster`。
- 全球大厅成员不能同时属于 Region/Zone；小区大厅和游戏服也不能误加入全球大厅。
- 当前选择规则为：最高健康分优先；同分时选择容量占用更低者；仍相同时随机。因此短时间偏向某一大厅是预期行为，不承诺严格轮询。

## 2. 准备条件

1. 准备已启动的 Beacon 控制面，并使用 HTTPS 或受控内网访问。
2. 为 BC 准备 BungeeCord 与 `BeaconAgentProxy`；为每个 Paper/Bukkit 服务准备 `BeaconAgent`。
3. 规划至少一台 BC、两台全球登录服、一台小区大厅和一台普通小区游戏服。
4. 为每个服务分配唯一的游戏监听端口；BC 可有多个 listener。
5. 备份控制面数据库、各 Agent 的 `plugins/Beacon*` 本地状态和原有服务配置后再迁移。

不要把 token、密码、真实公网地址或实际端口写进仓库、截图或日志。

## 3. 创建 namespace 与接入 token

1. 以完整管理员权限登录管理台。
2. 在「命名空间」创建本次集群的 namespace。
3. 为该 namespace 创建接入 token，并仅通过受控的部署渠道交给同 namespace 的 Agent。
4. 记录 token 的保管位置，不在聊天、配置模板或版本库中复制明文。

namespace 由 token 权威推导；Agent 不再在本地持续声明 namespace、serverId、大区、小区或大厅归属。

## 4. 配置 Agent

### 4.1 Bukkit / Paper

将 `BeaconAgent` 放入对应服务的 `plugins/` 目录。最小引导配置只需要控制面 endpoint 列表与 namespace token：

```yaml
beacon:
  endpoints:
    - "https://beacon.example.invalid"
  bootstrap-token: "<仅通过受控渠道注入的 namespace token>"
```

首次启动会生成本机 identityId。不要手工填写或复制以下业务字段：`namespace`、`serverId`、`address`、大区、小区、LobbyCluster、容量或调度权重。

### 4.2 BC / BungeeCord

将 `BeaconAgentProxy` 放入 BC 的 `plugins/` 目录，并使用与 Bukkit 相同的最小 `beacon` 配置。BC 必须在其原生配置中声明至少一个 listener；多 listener 会被 Agent 全部上报。

新 BC 不再使用 `proxy.home-group`、`proxy.home-zone` 或 listener `priorities` 决定玩家首次落点。保留这些旧字段仅为兼容历史配置，不能作为新拓扑的正确性依据。

## 5. 启动与身份审批

按以下顺序启动，可减少待确认状态的排障成本：

1. 启动控制面。
2. 启动两台全球登录服、小区大厅和普通小区游戏服。
3. 启动 BC。
4. 打开管理台「服务器 → 待确认」，核对 Agent 的平台角色与监听事实。
5. 为每台待确认身份分配唯一 serverId 并批准。

建议角色与归属如下：

| 服务 | Agent 角色 | 控制面归属 |
| --- | --- | --- |
| BC | proxy | BCCluster |
| `login-*` | backend | 全局 LobbyCluster |
| 小区大厅 | backend | 对应 Region / Zone |
| 普通游戏服 | backend | 对应 Region / Zone |

审批完成后，身份应从 `pending` 变为 `active`。新身份在 `pending` 时不会启动依赖权威 serverId 的完整运行循环；这不是故障。

## 6. 配置拓扑

1. 创建或选择 BCCluster，并把 BC 分配给它。
2. 创建业务 Region 与 Zone，把小区大厅和游戏服分配到对应 Zone。
3. 打开「LobbyCluster」页面，确认该 namespace 的唯一全局大厅集群存在。
4. 将两台及以上 `login-*` backend 加入 LobbyCluster。
5. 确认 `login-*` 已不再带 Region/Zone 归属；确认小区大厅和游戏服不在 LobbyCluster。
6. 等待健康状态收敛，直到 LobbyCluster 显示成员数、可调度数与预期一致。

在线服务的迁移受 drain 门保护；若管理台拒绝移动，先按既有运维流程排空或处理玩家，再重试。不要通过直接修改数据库绕过归属约束。

## 7. BC 目录与本地命令验证

BC 成功注册后会同步当前 namespace 的受管 Bukkit 目录与大厅候选。可在 BC 控制台执行：

```text
beacon status
beacon servers
beacon server <serverId>
```

应核对：

- `beacon status` 显示生命周期运行、受管目录已同步、最近成功同步时间，以及大厅候选可调度数。
- `beacon servers` 以固定页大小展示受管服务器；全局大厅成员标为「全局大厅」。
- `beacon server <serverId>` 显示单服的归属、在线、健康、可调度与不可调度原因。

发布变更、故障恢复或需要立即收敛时，可在管理台对一个 BC 或 namespace 内全部在线 BC 触发「目录重同步」。命令应依次经历 `pending → fetched → done`；若失败，先看结果摘要与审计，不要删除 BC 的最后有效快照。

## 8. 多 listener 与地址覆盖

1. 在身份详情确认 BC 的所有 listener 均为 active。
2. 每个 listener 分别检查上报绑定、探测地址、生效地址和来源。
3. 直连地址被 NAT 或反向代理改写时，只为受影响的 listener 设置完整 `host:port` 覆盖，并填写原因。
4. 覆盖生效后来源应为 `override`；清除覆盖后应恢复 `detected`。

不要把一个 listener 的公网映射复制到其它 listener。旧单地址 `address` 只是首个 active listener 的兼容投影。

## 9. 玩家验收

1. 分别连接每个 BC listener。
2. 每次首次连接都应进入 LobbyCluster 的某个可调度 `login-*`，绝不能落到小区大厅或普通游戏服。
3. 进入全局大厅后的选区、排队和后续转服必须由业务插件完成；Beacon 不应拦截这些转服。
4. 临时让一台全球登录服不可用，等待候选快照移除它后再次连接，玩家应落到剩余登录服。
5. 恢复该登录服，确认它重新注册 active，LobbyCluster 的成员数与可调度数恢复。

若两台大厅健康分不同，连续连接可能都选中健康分更高的一台；应通过健康状态、候选快照和故障切换证明另一台可用，而不是把短时非均匀分布误判为故障。

## 10. 常见排障

| 现象 | 检查顺序 |
| --- | --- |
| 首次进服被拒绝 | BC `beacon status` 是否已有大厅候选；LobbyCluster 是否 ready；成员是否 active、healthy、可调度。 |
| 首次进入普通服 | 检查 BC 是否仍为旧 Agent 或仍有旧 listener priority/fallback 配置；确认首次事件只使用新大厅快照。 |
| 某登录服未被选择 | 对比候选的 health score、onlineCount、maxOnline 与 schedulable；最高分优先是预期。 |
| BC 看不到后端 | 核对 Agent token 对应同一 namespace、backend 身份 active、端口/endpoint 生效，以及目录重同步命令状态。 |
| 地址不可直连 | 检查对应 listener 的 detected/override/effective 地址；在 NAT/反代环境只对该 listener 设置覆盖。 |
| 重启后不能接入 | 保留并检查 Agent 本地 identity 与绑定快照；新节点无已确认绑定时必须重新进入 pending。 |

## 11. 回滚原则

- 先恢复数据库、Agent 本地状态和原始服务配置，再恢复服务进程。
- 不手工修改或删除 Agent identity / snapshot 文件来“强制激活”节点。
- 控制面不可用时，已有有效快照的 active Agent 按 fail-static 继续；没有安全大厅候选时 BC 应明确拒绝首次进入，而不是回退到任意业务服。
- 需要回退发布物时使用与当前发布物对应的已验证备份，不混用不同版本的控制面与 Agent。
