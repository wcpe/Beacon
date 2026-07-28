# ADR-0078：资源稳定业务标识、可变显示名称与不可复用墓碑

**状态**：已接受（2026-07-29）

## 背景

Beacon 的权威资源均已有数据库自增主键，但 namespace、env、BC 集群、大区、小区与 server 的业务标识形态不一致：部分实体只有 `name`，部分实体以 `name` 同时承担机器寻址与界面展示，server 则已有稳定 `serverId`、但缺少独立显示名称。显示文案一旦参与调度、广播、配置作用域或自动化引用，就无法安全改名；反过来，如果把数据库主键暴露为唯一业务标识，跨环境迁移、运维排查与外部自动化都难以理解。

namespace 与 server 还缺少完整资产生命周期。现有身份 `disabled/unbound` 不是资产归档，历史热冷归档也只处理时序数据，均不能表达“资源暂时退出运行但可恢复”以及“资源永久删除但业务标识永不复用”。

## 决策

### 1. 三类标识各司其职

- 数据库自增 `id` 是内部 surrogate key，只用于关系引用与数据库访问，不作为可编辑字段。
- `code` 是资源稳定业务标识，创建时由操作者显式填写，创建后不可修改。server 沿用既有 `serverId` 作为其业务标识，不另造 `code`。
- `displayName` 只用于界面展示，可修改、可重名，不参与唯一约束、运行时寻址、权限、调度、广播、配置作用域或审计关联。
- 管理台主显 `displayName`，同时辅显并允许复制 `code/serverId`；存在重名时以业务标识消歧。

LobbyCluster 是每个 namespace 唯一的无名称单例，不纳入本决策的可命名资源集合，继续沿用 [ADR-0075](0075-lobby-cluster-and-bc-first-entry.md) 的模型。

### 2. 业务标识按权威层级唯一

- `namespace.code` 与 `env.code` 全局唯一。
- `bc_cluster.code` 在 namespace 内唯一。
- `region.code` 在 BC 集群内唯一。
- `zone.code` 在大区内唯一。
- `server.serverId` 在 namespace 内唯一。

`displayName` 在所有层级均允许重复。公共 URL、API 请求、Agent 协议、MCP 工具参数及审计目标不得依赖显示名称定位资源。

### 3. 兼容迁移采用扩展再切换

迁移按 `expand → backfill → dual-read/write → runtime cutover → enforce` 执行：先增加可空新字段并回填，再切换运行时消费者，最后建立非空与唯一约束。namespace 复用既有 Code/Name，server 保留 `serverId`；env、BC、大区、小区以现有技术名称回填稳定 code 和初始 displayName。旧 wire 字段只做加法兼容，不得把同名旧字段的语义从机器标识静默改成显示名称。

调度、广播、默认入口、配置作用域与 Legacy group/zone 兼容解析在迁移窗内可双读，内部解析结果必须统一落到稳定 code。只修改 displayName 的集成测试必须证明目标集合与运行结果不变。

### 4. namespace 与 server 使用独立资产生命周期

本期只有 namespace 与 server 获得资产生命周期：

```text
active ──归档──▶ archived ──恢复──▶ active
                     │
                     └──永久删除──▶ tombstoned
```

- `archived` 可恢复；归档与身份 `disabled/unbound` 分离，不复用身份状态表达资产状态。
- `tombstoned` 不可恢复，墓碑本身永久保留，业务标识继续占用。
- env、BC、大区、小区本期只获得稳定 code/displayName，不新增独立归档或删除状态。

### 5. 归档只改变有效运行资格

- server 归档保留 `serverId`、identity 绑定、BC/大区/小区归属与历史事实；归档期间不进入在线目录、调度候选、广播目标、交付目标或 Agent 指令目标。
- server 恢复沿用原绑定与归属，不重新创建资产或重新审批身份/归属；恢复动作本身仍按危险操作审批。
- namespace 归档不覆写任何子资源自身状态，而是在读取与执行边界计算整个子树“有效停用”。恢复 namespace 后，原本独立归档的 server 仍保持归档。
- namespace 归档期间，其 token、注册、调度、消息、配置目标、交付目标与 Agent 操作均 fail-closed。

### 6. 永久删除采用主库墓碑，不物理清除历史

- server 只有在 archived 时才能申请永久删除；执行后终止当前运行资格与可信绑定，但指标、连接、消息、交付、审计等历史仍可按原 `serverId` 查询。
- namespace 只有在 archived 时才能申请永久删除；审批冻结影响快照，执行时在单一事务中将 namespace 及其权威子树原子墓碑化，并关闭 env 映射、双向 trust、LobbyCluster 与 AgentIdentity 的活动关系。任一步失败整单回滚。
- namespace code 与 `(namespace, serverId)` 永久保留。创建、注册、身份确认或恢复均不得复活墓碑或复用其业务标识。
- 永久删除不得级联物理删除时序、配置版本、连接、消息、交付或审计历史；历史记录继续引用稳定业务标识与墓碑事实。

资源生命周期归档与 [ADR-0066](0066-hot-cold-archive-dual-connection.md) 的时序热冷归档完全不同，也不改变 [ADR-0008](0008-config-soft-delete-and-effective-md5.md) 的配置项软删语义。

### 7. 生命周期执行受统一审批约束

归档、恢复与永久删除都属于危险操作，必须经 [ADR-0079](0079-principal-capability-and-dangerous-operation-approval.md) 的审批执行模型。申请绑定不可变影响快照；执行前事实漂移时拒绝执行并要求重新申请。旧 `/admin/v1` 删除入口不得成为绕过路径。

## 理由

- 将机器身份与显示文案分离后，运维可以安全改名，运行时与自动化仍有稳定引用。
- 层级内唯一与现有数据约束一致，避免不必要的全局命名限制和高风险迁移。
- 归档只改变有效运行资格，可保留身份、拓扑和历史，恢复不需要重建事实。
- 墓碑永久保留能防止旧审计、历史指标和外部自动化把新资源误认成旧资源。
- namespace 删除必须与其权威子树原子收口，否则会留下仍可注册、调度或授权的悬挂关系。

## 后果

- 权威表、contracts、管理 API 与管理台需要 additive 增加 code/displayName/lifecycle 字段，并提供显示名编辑能力。
- 所有按 region/zone 名称寻址的运行时消费者必须迁移到 code；迁移期需要兼容读与回归测试。
- namespace/server 的活跃查询需默认排除墓碑，并明确区分自身状态与父级导致的有效停用。
- 墓碑会永久占据业务标识和少量主库存储，这是避免身份复用的有意成本。
- MySQL、SQLite 与未来 Postgres 均须使用 GORM 可移植字段、索引和事务，不使用方言专有删除机制。

## 备选方案

- **继续让 `name` 同时承担机器标识和显示名称**：改名会破坏寻址与自动化。否决。
- **使用 UUID/ULID 作为唯一业务标识**：机器稳定但不利于运维阅读，且 serverId 已有成熟人类可读契约。否决。
- **所有资源类型共享全局 code 空间**：限制过强且迁移收益不足。否决。
- **永久物理删除并允许复用 code**：历史与自动化引用会产生身份混淆。否决。
- **namespace 删除逐个调用子资源删除**：中途失败会产生半删除状态，且审批影响范围无法保持原子。否决。
