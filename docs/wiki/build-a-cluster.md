# 从零搭建完整集群

本教程搭建并验收以下最小生产拓扑：一个 Beacon 控制面、一个 BC、两个全局登录
服、一个小区大厅和一个小区游戏服。它采用 v2 的 namespace token、Agent 身份审批
和控制面权威分配模型。

业务插件负责全局大厅内的选区、排队与后续转服；Beacon 只负责身份、目录、健康
候选与 BC 的首次落脚。相关行为以[全局大厅与 BC 接入操作手册](global-lobby-operations.md)
为准。

## 1. 目标与边界

```text
玩家
  └─ BC 的任一 listener
       └─ 全局 LobbyCluster：login-a、login-b
            └─ 业务插件：选区、排队、转服
                 └─ Region / Zone
                      ├─ zone-lobby
                      └─ game-a
```

这个拓扑有以下不可破坏的约束：

- 一个 namespace 只有一个 LobbyCluster；
- `login-a` 和 `login-b` 只能属于 LobbyCluster，不能同时属于 Region/Zone；
- `zone-lobby` 与 `game-a` 必须属于同一个业务 Zone，不能加入 LobbyCluster；
- 每个服务使用自己的游戏监听端口；BC 可以有多个 listener；
- namespace、`serverId`、地址、区服归属与调度权重都由控制面权威维护，Agent 只做
  endpoint 与 token 引导。

下文中的服务名仅用于区分角色。请替换为你的命名方案，但不要在 Wiki、仓库、截图
或日志中写入真实公网地址、端口、令牌、密码、证书或 SSH 信息。

## 2. 准备控制面

### 2.1 选择数据库与运行方式

首次验证可按[快速开始](quick-start.md)使用单二进制 + SQLite；SQLite 数据目录必须
位于持久化磁盘。

生产环境使用外置 MySQL：先由数据库管理员创建应用数据库和受限账户，再通过控制
面的 `config.yml` 或受控环境变量配置 `database.driver` 与 `database.dsn`。数据库
凭据只应存在于秘密管理或部署环境中，不要写进文档、命令历史或仓库。

如果启用热冷归档，还要预建独立归档数据库，或提供独立归档 DSN。控制面只建表，
不负责创建数据库实例或数据库本身。

仓库当前的 `docker-compose.yml` 只提供 Beacon + SQLite 持久卷，**不提供 MySQL**。
不要假定存在名为 `beacon-mysql` 的容器，也不要把旧的容器备份命令直接用于这套
部署。

### 2.2 生产前置检查

1. 让所有游戏节点能访问控制面；若经反向代理传递 Agent SSE，关闭流响应缓冲并
   将读取超时设得高于保活间隔。
2. 对外暴露管理台时使用 TLS，并把管理面访问限制在受控网络或访问者范围内。
3. 用 systemd、容器 restart policy 或等价监督器托管控制面进程。
4. 在接入任何 Agent 前完成数据库备份策略和一次恢复演练。

## 3. 准备六个运行节点

| 节点 | 软件与 Agent | 最终控制面归属 |
| --- | --- | --- |
| 控制面 | Beacon | namespace 的权威服务 |
| BC | BungeeCord/BC + `BeaconAgentProxy` | BCCluster |
| `login-a` | Paper/Bukkit + `BeaconAgent` | LobbyCluster |
| `login-b` | Paper/Bukkit + `BeaconAgent` | LobbyCluster |
| `zone-lobby` | Paper/Bukkit + `BeaconAgent` | Region / Zone |
| `game-a` | Paper/Bukkit + `BeaconAgent` | Region / Zone |

在每台 Bukkit/Paper 服务的 `plugins/` 目录放入 `BeaconAgent`，在 BC 的 `plugins/`
目录放入 `BeaconAgentProxy`。所有 Agent 必须与控制面使用兼容的发布版本。

BC 原生配置必须至少声明一个 listener。多 listener 会逐个上报；不要用旧的
`proxy.home-group`、`proxy.home-zone` 或 listener `priorities` 作为首次落脚依据。

## 4. 创建 namespace 并写入最小 Agent 配置

1. 启动控制面并以管理员身份登录。
2. 创建本集群的 namespace，安全保存其一次性明文接入 token。
3. 为五个 Agent 分别写入同样结构的最小引导配置：

```yaml
beacon:
  endpoints:
    - "<CONTROL_PLANE_URL>"
  bootstrap-token: "<NAMESPACE_ACCESS_TOKEN>"
```

`<CONTROL_PLANE_URL>` 必须从每个节点可达；`<NAMESPACE_ACCESS_TOKEN>` 只通过秘密
管理或受控部署工具下发。不要在任一 Agent 配置中添加或复制 `namespace`、`serverId`、
地址、Region、Zone、LobbyCluster、容量或权重字段。

## 5. 按顺序启动并审批身份

按以下顺序启动可减少排障成本：

1. 启动控制面；
2. 启动四个 Bukkit/Paper 后端：两个全局登录服、小区大厅和游戏服；
3. 启动 BC；
4. 在“服务器 → 待确认”逐条核对平台角色、身份和监听事实；
5. 为每条身份分配唯一 `serverId` 并批准。

审批前，Agent 处于 `pending`，不会启动依赖权威 `serverId` 的完整运行循环；这是预期
状态，不是故障。审批后应为 `active`。遇到重复身份或服务迁移，使用管理台的
冲突处置、禁用、解绑或换区流程，不要删除 identity/snapshot 文件或直接修改数据库。

## 6. 在控制面建立拓扑

在所有身份已经审批后，按以下顺序进行控制面分配：

1. 创建一个 BCCluster，并把 BC 分配到该集群。
2. 创建一个 Region，再在其下创建一个 Zone。
3. 把 `zone-lobby` 和 `game-a` 分配到该 Zone。
4. 创建或打开 namespace 的唯一 LobbyCluster，把 `login-a` 与 `login-b` 加入其中。
5. 复核两个登录服没有 Region/Zone 归属，且两个业务后端没有 LobbyCluster 归属。
6. 等待健康状态收敛，直到 LobbyCluster 的成员数和可调度数均为两个。

已在线服务的迁移受 drain 保护。管理台拒绝迁移时，先排空受影响服务、确认玩家
已处理，再重试；不要通过数据库绕过这一约束。

## 7. 校验 BC 目录和 listener 地址

BC 注册成功后，应获取本 namespace 的受管 Bukkit 目录和大厅候选。可在 BC 控制台
执行：

```text
beacon status
beacon servers
beacon server <SERVER_ID>
```

应看到生命周期运行、最近成功同步时间和两个可调度的大厅候选；全局登录服在列表
中应明确标为全局大厅。

随后逐个检查 BC listener 的 detected、effective 地址及来源。仅当 NAT 或反向代理
使某个 listener 的实际可达地址错误时，才为该 listener 设置完整的 `host:port` 覆盖
并填写原因。一个 listener 的覆盖不能复制给另一个 listener；旧的单地址字段只是
兼容投影。

发布拓扑变更、恢复故障或需要立即收敛时，可在管理台对 BC 触发目录重同步。命令应
经过 `pending → fetched → done`；失败时查看命令结果与审计，保留最后有效快照。

## 8. 玩家验收与故障切换

按此清单验证玩家链路：

1. 分别通过每个 BC listener 连接。
2. 每次首次连接必须进入 `login-a` 或 `login-b`，绝不能直接进入 `zone-lobby` 或
   `game-a`。
3. 在全局大厅完成选区、排队和后续转服时，确认这些动作由业务插件执行，Beacon
   不拦截它们。
4. 让一台登录服按既有变更流程临时不可调度或不可用，等待大厅候选快照移除它。
5. 再次连接，确认玩家只进入仍健康、可调度的登录服。
6. 恢复该登录服，确认它重新 `active`，LobbyCluster 的成员数和可调度数恢复为两个。

当前候选选择规则是：最高健康分优先；同分时容量占用较低者优先；仍相同时随机。
因此短时间的连接分布不均不是故障证据，应以候选快照和故障切换结果判断。

## 9. 上线后的最小运维闭环

- 定期备份控制面数据库及每个 Agent 的本地身份、快照和原始服务配置；上线前演练
  恢复。
- 观测身份状态、健康分级、调度决策和 BC 目录同步。控制面短暂不可用时，已确认
  Agent 按本地快照 fail-static，不能借此跳过恢复后的核对。
- 升级时使用同一已验证发布线的控制面和 Agent，按批替换 Agent 并保留其本地状态；
  容器生产升级以重建/更新镜像为准，不依赖容器内二进制自更新。
- 回滚时优先恢复已验证的数据库备份、Agent 本地状态与原始服务配置；不要混用不同
  版本的控制面和 Agent。

有关配置发布、消息审计、归档和更多故障处理，请继续阅读 [运维手册](../OPERATIONS.md)
及 [REST 契约](../API.md)。
