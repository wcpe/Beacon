# 5 分钟快速开始：控制面与一台 Paper 服务

本教程用 SQLite 跑通最小闭环：启动 Beacon 控制面、让一台 Paper/Bukkit
服务上的 Agent 注册、在管理台确认身份。它适合本机验证和功能体验，不替代
生产部署。

完成后，你会得到一台状态为 `active` 的受管后端服务。BC、全局大厅和小区
拓扑请继续阅读[从零搭建完整集群](build-a-cluster.md)。

## 1. 开始前

准备与同一 Beacon 发布版本匹配的以下文件：

- Beacon 控制面二进制；
- 一台可启动的 Paper/Bukkit 服务及 `BeaconAgent`；
- 可从该服务访问控制面的网络路径。

在一个新的、可持久化的工作目录中运行控制面。首次启动会在该目录生成
`config.yml` 和 SQLite 数据文件；不要把这个目录、其中的口令或数据库文件提交
到版本库。

> 本教程不使用 Docker Compose 的 MySQL。仓库内 Compose 当前是单控制面 +
> SQLite 持久卷；生产 MySQL 应由你单独部署，详见[完整集群教程](build-a-cluster.md)。

## 2. 启动 SQLite 控制面

将控制面二进制放入工作目录并运行它。以下命令仅示意可执行文件名；请按操作
系统调整启动方式：

```text
<beacon-binary>
```

首次运行会释放 `config.yml`，并为管理面认证生成随机强口令与签名密钥。打开本
机的 `config.yml`，只在受控终端中读取 `auth.username` 和 `auth.password`，然后用
该文件中配置的监听地址打开管理台。

不要把生成的口令、签名密钥、数据库文件或管理台 URL 复制到聊天记录、截图、
仓库或日志。需要修改控制面监听地址、数据库或认证配置时，先停止进程、修改
`config.yml`，再启动；这些是启动配置而非管理台热改项。

## 3. 创建 namespace 与接入 token

1. 使用刚生成的管理员凭据登录管理台。
2. 在“命名空间”创建一个仅供本次测试使用的 namespace。
3. 创建或取得该 namespace 的接入 token，并立即保存到受控的秘密管理或部署
   渠道；一次性明文展示后不应期待可以再次读取。

namespace 由 token 权威推导。不要在 Agent 配置中手工声明 namespace、`serverId`、
Region、Zone、LobbyCluster、地址、容量或调度权重。

## 4. 安装并配置 BeaconAgent

1. 停止 Paper/Bukkit 服务。
2. 将与控制面版本匹配的 `BeaconAgent` 放入该服务的 `plugins/` 目录。
3. 在 Agent 配置中只提供控制面 endpoint 列表和上一步取得的 namespace token：

```yaml
beacon:
  endpoints:
    - "<CONTROL_PLANE_URL>"
  bootstrap-token: "<NAMESPACE_ACCESS_TOKEN>"
```

`<CONTROL_PLANE_URL>` 必须是这台 Paper 服务实际可访问的地址。`<NAMESPACE_ACCESS_TOKEN>`
只能由受控部署渠道注入；示例中的占位符不能原样使用。

首次启动时，Agent 会为本机生成 identityId 并向控制面申请注册。保留
`plugins/Beacon*` 下的本地身份与快照文件；它们不是可复制的模板，也不能靠删除
来“重置”或强制激活实例。

## 5. 确认身份

1. 启动 Paper/Bukkit 服务。
2. 在管理台的“服务器 → 待确认”中找到新身份。
3. 核对它是 backend 角色以及上报的监听事实，填写审批原因并申请分配唯一 `serverId`。
4. 在审批中心由另一位 human 批准，等待 worker 执行后身份从 `pending` 变为 `active`。

首次批准可以先保持该服务未分配到 Region/Zone；这足以验证身份闭环。要让它参与
业务小区调度，后续必须在控制面创建拓扑后再分配，不要把区服关系写回 Agent
配置。

## 6. 验收与下一步

在管理台确认该实例为 `active`，并能看到最近心跳或健康信息。若实例停留在
`pending`，检查 namespace token、控制面可达性和待确认记录；不要手改数据库或
Agent 的身份文件。

控制面短暂不可用时，已确认的 Agent 会按本地快照 fail-static 继续运行；这不表示
可以删除快照或跳过恢复后的身份核对。

接下来请选择对应目标：

- 需要玩家从代理首次进入全局大厅：继续[从零搭建完整集群](build-a-cluster.md)。
- 需要接入业务插件：阅读 [SDK 接入指南](../SDK.md)，业务插件只能调用本机
  Agent API，不能直连 Beacon HTTP。
- 需要生产运行、备份、升级或故障处理：阅读 [运维手册](../OPERATIONS.md)。
