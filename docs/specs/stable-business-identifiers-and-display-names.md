# 功能规格：稳定业务标识与显示名称

> 状态：草拟　·　关联 PRD：FR-205　·　分支：待执行时创建　·　依赖：[ADR-0078](../adr/0078-stable-resource-identity-and-lifecycle-tombstones.md)

本规格只定义资源标识、兼容迁移和管理面展示名编辑。当前权威关系、分配约束仍以 [v2-zone-authority.md](v2-zone-authority.md) 与 [v2-namespace-isolation.md](v2-namespace-isolation.md) 为准；生命周期由 FR-215～218 承接，不在本文重复定义。

## 1 背景与目标

env、BC 集群、大区和小区目前由 `name` 同时承担机器寻址与界面展示；namespace 的 V2 DTO 又把既有 `code` 折叠成 `name`；server 虽已有稳定 `serverId`，却没有独立显示名称。结果是“改个名字”可能改变调度、广播、配置作用域或自动化引用。

目标是让三类标识各司其职：数据库自增 `id` 只做内部关系键，`code/serverId` 做不可变业务标识，`displayName` 做可修改且可重名的展示文案。旧数据必须无损回填，迁移过程中不得静默改变旧 wire 字段的机器语义。

## 2 需求（要什么）

### 2.1 范围

| 资源 | 内部键 | 稳定业务标识 | 唯一范围 | 显示名称 |
|---|---|---|---|---|
| namespace | 自增 `id` | `code` | 全局 | `displayName`，可改、可重名 |
| env | 自增 `id` | `code` | 全局 | `displayName`，可改、可重名 |
| BC 集群 | 自增 `id` | `code` | namespace 内 | `displayName`，可改、可重名 |
| 大区 | 自增 `id` | `code` | 所属 BC 集群内 | `displayName`，可改、可重名 |
| 小区 | 自增 `id` | `code` | 所属大区内 | `displayName`，可改、可重名 |
| server | 自增 `id` | 既有 `serverId` | namespace 内 | `displayName`，可改、可重名 |

- `code/serverId` 只在创建或首次身份确认时确定，此后所有普通更新接口均不得修改。
- `displayName` 不参与唯一约束、URL、权限、调度、广播、配置作用域、Agent/MCP 参数或审计关联；审计目标必须同时保存资源类型、内部 id 与稳定业务标识。
- 管理台主显 `displayName`，辅显可复制的 `code/serverId`；重名时必须用业务标识消歧。
- LobbyCluster 继续是 namespace 下唯一的无名称单例，不增加 `code` 或 `displayName`。
- 旧数据自动回填；不得要求操作者逐条重建，也不得为绕过冲突自动追加随机后缀。

### 2.2 非目标

- 不改变 env 的展示/过滤维度性质，也不改变 namespace、BC、大区、小区、server 的权威层级。
- 不给 env、BC、大区、小区增加归档或删除生命周期。
- 不引入 UUID/ULID、跨资源类型的全局 code 空间或新第三方依赖。
- 不在本 FR 删除旧字段；删除兼容字段须另立规格并经过 N-1 兼容窗口。

## 3 设计（怎么做）

### 3.1 数据模型与不变量

- namespace 复用既有 `namespace.code` 与 `namespace.name`：领域/DTO 将后者明确映射为 `displayName`。
- env、BC 集群、大区、小区增加最长 64 字符的 `code`；既有 `name` 列保留并作为最长 128 字符的 `displayName` 存储，不做高风险列重命名。
- server 增加最长 128 字符的 `display_name`；既有 `server_id` 保持不变。
- 新唯一索引分别为 `env(code)`、`bc_cluster(namespace_id, code)`、`region(bc_cluster_id, code)`、`zone(region_id, code)`；namespace 与 server 沿用既有唯一索引。
- 稳定业务标识与显示名称都须 trim 后非空。为兼容既有技术名称，本期不新增比现有 64 字符域更严格的字符集规则；未来收紧另立规格。
- service 层不暴露修改 `code/serverId` 的方法；更新请求若携带不同值，返回 `400 IMMUTABLE_IDENTIFIER`，相同值可幂等忽略。

### 3.2 扩展与回填

按 [ADR-0078](../adr/0078-stable-resource-identity-and-lifecycle-tombstones.md) 的 `expand → backfill → dual-read/write → runtime cutover → enforce` 执行：

1. 先增加可空字段和非唯一索引，不立刻改变旧读写路径。
2. namespace 保留现值；env、BC、大区、小区以既有 `name` 原样回填 `code`，既有 `name` 同时成为初始 `displayName`；server 以 `serverId` 回填初始 `displayName`。
3. 逐层校验非空、长度及目标唯一范围。发现异常时输出不含敏感值的中文错误并停止 enforce，不静默改名。
4. 新旧版本双读/双写期间，旧技术名称字段始终投影稳定 code；显示名称只通过新增字段传递。
5. 所有运行时消费者切换并通过回归后，才建立非空和唯一约束；整个阶段使用 GORM 可移植字段、索引与事务。

迁移必须可重入：重复启动不会覆盖已生成的 code/displayName，也不会产生第二组资源或索引。

### 3.3 管理 API 与兼容契约

所有读模型 additive 返回 `id`、`code`（server 为 `serverId`）、`displayName`、`description`（适用时）以及既有统计/归属字段。

| 方法 | 路径 | 本 FR 的请求/响应变化 |
|---|---|---|
| GET/POST/PATCH | `/admin/v2/namespaces`、`/admin/v2/namespaces/{id}` | 创建接收 `code, displayName, description`；PATCH 只允许 `displayName?, description?` |
| GET/POST/PATCH | `/admin/v2/envs`、`/admin/v2/envs/{id}` | 创建接收 `code, displayName, description`；PATCH 不得改 code |
| POST/PATCH | `/admin/v2/bc-clusters`、`/admin/v2/bc-clusters/{id}` | 创建接收 `namespaceId, code, displayName, description`；PATCH 只改展示信息 |
| POST/PATCH | `/admin/v2/regions`、`/admin/v2/regions/{id}` | 创建接收 `bcClusterId, code, displayName, description`；PATCH 只改展示信息 |
| POST/PATCH | `/admin/v2/zones`、`/admin/v2/zones/{id}` | 创建接收 `regionId, code, displayName, description`；PATCH 只改展示信息 |
| PATCH | `/admin/v2/servers/{id}` | 只接收 `displayName`；server 仍由身份确认流程创建并确定 `serverId` |
| GET | `/admin/v2/zone-tree`、`/admin/v2/servers` | 每层同时返回 code/serverId 与 displayName；`keyword` 同时匹配二者 |

兼容规则：

- 旧响应中的 `name` 在兼容窗内继续表示原机器标识，值等于 `code`；不得在一次 additive 发布中改成 `displayName`。
- 旧创建请求只有 `name` 时，以同一值初始化 `code` 与 `displayName`；新请求同时给 `name` 与 `code` 且二者不一致时返回 `400 AMBIGUOUS_IDENTIFIER`。
- 旧 PATCH 的 `name` 仍按机器标识解释：与既有 code 相同可幂等忽略，不同则返回 `400 IMMUTABLE_IDENTIFIER` 并提示改用 `displayName`；不得把同名旧字段静默改成显示名称写语义。
- 唯一冲突返回 409，并带资源类型与冲突范围，不回显敏感上下文。

### 3.4 运行时寻址切换

- 数据库内部关系仍以自增 id 连接；跨进程、Agent 协议、MCP 参数与审计目标使用稳定 code/serverId。
- 嵌套资源不能只靠裸小区 code 跨父级定位；边界契约使用完整稳定路径 `namespaceCode/bcClusterCode/regionCode/zoneCode`，进入服务层后解析为内部 id。
- 默认入口、健康调度、广播目标、配置/文件作用域、交付 selector、BC 受管目录与 Legacy group/zone 兼容解析必须逐项迁移。迁移窗允许双读，但解析后的权威值只能是稳定 code 路径。
- 面向旧 Agent 的既有 `group/zone/name` 字段继续发送 code，而不是 displayName；初始回填保证升级瞬间值不变。
- 单独修改 displayName 不得使任何目标集合、哈希、权限、调度决策、消息收件人或配置渲染结果变化。

### 3.5 审计与并发

- 创建与显示名称修改记录操作人、资源 id、稳定业务标识、旧/新 displayName；禁止把 access token 等敏感值写入审计。
- 同一资源并发改名采用现有更新时间/CAS 约定，陈旧写入返回 409，不得覆盖更新。
- 任何读取到 code 缺失或解析歧义的运行时路径 fail-closed，并记录中文 WARN；不得退回用 displayName 猜测。

## 4 UX / 交互

用户任务是“看懂资源是什么，并在不改变机器引用的前提下改显示名称”。入口沿用 `/namespaces`、`/envs`、`/zones`、`/servers`，不新建孤立页面。

- 列表/树主标题显示 `displayName`，下一行以等宽弱文案显示 `code/serverId`，提供复制按钮；相同显示名允许并列。
- 创建表单分别解释“业务标识创建后不可修改”和“显示名称以后可改”；确认前同时预览两者。
- 编辑抽屉中的 code/serverId 只读，显示名称可改；保存成功仅刷新对应资源与引用它的列表标签。
- 搜索同时命中显示名称与业务标识；筛选值和 URL 参数存 id/code，不存显示名称。
- **空态**：无资源时展示创建入口和两类名称说明。
- **加载态**：表格/树保留结构骨架，禁止短暂显示 code 缺失的伪数据。
- **错误态**：回填冲突、不可变标识修改或加载失败均显示可行动原因与重试入口，不静默退回旧 name。
- **超大量态**：沿用分页和区服树懒展开；搜索在服务端执行，不一次加载全部资源。

这是既有页面的字段与交互补齐，可不单独制作全页 mockup；实现前仍需以组件测试锁定双名称层级、只读字段和四态。

## 5 任务拆分

- [ ] 运行并记录模型、repository、service、contracts 与相关管理台测试基线；确认无失败后再改功能代码。
- [ ] 先写失败测试：唯一范围、不可变 code/serverId、displayName 可重名/可修改、旧数据回填幂等、旧 wire 字段语义不变。
- [ ] 以最小字段/索引实现 expand 与 backfill；不得在同一步删除旧字段或引入新依赖。
- [ ] 更新 contracts、管理 API 与 `/namespaces`、`/envs`、`/zones`、`/servers`，补齐创建/编辑/搜索和四态测试。
- [ ] 逐项迁移调度、广播、默认入口、配置/文件作用域、交付与 Agent 目录消费者，并增加“只改 displayName，运行结果不变”的集成测试。
- [ ] 独立复核迁移重入、父级唯一范围、N-1 Agent 兼容、日志脱敏、MySQL/SQLite 可移植性及 diff 范围。
- [ ] 重跑 server、contracts、web 全量相关门禁；文档同步由主任务回填 PRD、API、ARCHITECTURE、CHANGELOG，本文不代写它们。

## 6 验收标准

### 6.1 自动化验收

1. 六类资源都返回内部 id、稳定业务标识和显示名称；LobbyCluster 契约无新增命名字段。
2. 各层 code/serverId 在规定范围内唯一，跨父级允许相同子级 code；displayName 在同层重名仍创建/更新成功。
3. 所有更新入口无法改变 code/serverId；displayName 改名前后资源 id、父子关系和审计目标不变。
4. 旧数据回填可重入且无丢失；重复迁移不改既有 code，异常数据阻止 enforce 而非自动改名。
5. 旧 wire 字段继续输出机器标识；新旧客户端在兼容窗内都能完成列表、创建与显示名更新。
6. 修改 namespace/env/BC/大区/小区/server 的 displayName 后，调度候选、广播收件人、默认入口、配置/文件作用域与交付目标集合逐项保持一致。
7. 所有新增查询保持分页/懒加载，无 N+1 查询；MySQL 与 SQLite 相关测试通过。

### 6.2 真实验收

1. 用一份含既有 namespace、BC、大区、小区和 server 的数据库升级；管理台同时显示原 code/serverId 与初始 displayName，Agent 保持在线且无需重绑。
2. 将两个同父级资源改成相同 displayName，页面可用 code 消歧；随后执行一次调度、区服广播、配置下发和默认入口查询，目标均未变化。
3. 使用上一兼容版本 Agent 连接升级后的控制面，受管目录、group/zone 解析与消息寻址正常；证据只证明该环境，不替代发布验收。

## 7 风险 / 待定

- 最大风险是把旧 `name` 静默改成显示语义。必须先锁定旧字段值等于 code 的契约测试，再迁移调用者。
- 父级内唯一意味着裸 zone code 可能跨大区重名，任何跨边界调用必须携带完整 code 路径或内部 id，禁止“取第一条”。
- GORM AutoMigrate 不能替代分阶段数据校验；enforce 前必须有显式回填统计与失败报告。
- 已决定：内部 id 自动增长；code/serverId 不可变；displayName 可变且可重复；唯一范围按 ADR-0078；旧数据自动回填；LobbyCluster 不增加显示名称。
