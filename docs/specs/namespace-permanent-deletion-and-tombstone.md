# 功能规格：namespace 永久删除与权威子树墓碑

> 状态：草拟　·　关联 PRD：FR-218　·　分支：待执行时创建　·　依赖：FR-205、FR-206、FR-207、FR-216、FR-217、[ADR-0078](../adr/0078-stable-resource-identity-and-lifecycle-tombstones.md)、[ADR-0079](../adr/0079-principal-capability-and-dangerous-operation-approval.md)

## 1 背景与目标

namespace 是完整权威子树的根。把旧 DELETE 改成“有子资源就拒绝”仍无法完成用户需要的永久清理；逐个删除子节点又会留下半删除状态、悬挂 token/trust/identity，并让审批影响范围在执行中漂移。

本 FR 将 namespace 永久删除定义为“审批后的原子子树墓碑”：仅 archived namespace 可申请；冻结完整影响快照后，在一个数据库事务中墓碑化 namespace、LobbyCluster、BC、大区、小区、server，并关闭 env 映射、双向 trust 和 AgentIdentity 活动关系。所有业务标识、墓碑与历史永久保留，无物理删除、无复用、无恢复。

## 2 需求（要什么）

### 2.1 范围

- 只有自身 `lifecycleStatus=archived` 的 namespace 可申请永久删除。
- 归档成功后不设置额外冷却期；满足状态、能力与冲突守卫即可立即创建审批申请。
- 子树存在不是拒绝条件；影响预览必须完整列出并冻结全部子树目标，批准后作为一单原子处理。
- 原子范围包含 namespace、唯一 LobbyCluster、全部 BC 集群、大区、小区、server，以及相关 env 映射、双向 trust、AgentIdentity 活动关系。
- namespace 和其 server 转为不可恢复 tombstoned；BC/大区/小区/LobbyCluster 写入仅供父级级联的墓碑事实，不获得独立归档/删除 API。
- namespace code 和全部 `(namespace, serverId)` 永久占用；注册、创建、映射、授权、身份确认或恢复不能复活任何墓碑。
- 配置版本、文件版本、指标、健康、连接、消息、交付、命令、审批与审计历史不物理删除，继续引用稳定标识/墓碑。

### 2.2 非目标

- 不做逐子资源人工审批、逐个 DELETE 或“先删空子树再删 namespace”。
- 不墓碑全局 env 本体；只关闭它到目标 namespace 的映射。
- 不物理清除时序/配置/交付/审计历史，也不提供墓碑恢复、code 释放或管理员后门。
- 不增加按天等待、延迟删除或冷却期；24 小时是审批申请有效期，不是 namespace 删除冷却期。
- 不把 BC/大区/小区变成可单独归档、恢复或永久删除的资源。

## 3 设计（怎么做）

### 3.1 子树墓碑模型

- FR-216 的 `namespace.lifecycle_status` 扩展为 `tombstoned`，并增加 tombstone 时间、操作者、原因、审批 id、影响哈希。
- 子树 server 使用 FR-217 的 `tombstoned` 状态与墓碑字段；无论执行前 server 是 active 还是 archived，都在本次父级原子操作中进入 tombstoned。
- BC 集群、大区、小区与 LobbyCluster 增加 `tombstoned_at`、`tombstone_approval_request_id` 级联标记；这些字段没有独立状态机或公开写入口。
- EnvNamespace 增加不可恢复的 `closed_at/close_reason/approval_request_id`，查询默认排除已关闭映射；不物理删行。
- 双向 NamespaceTrust 在事务中用既有 revoked 事实关闭，并写 `namespace_tombstoned` 原因；目标 namespace 墓碑 gate 永久阻止再次授予。
- AgentIdentity 复用 FR-217 的资产绑定关闭事实；namespace 下所有身份均关闭，行和原稳定引用保留。

```text
namespace archived ──审批执行──▶ namespace tombstoned
                                    ├─ LobbyCluster 级联墓碑
                                    ├─ BC → Region → Zone 级联墓碑
                                    ├─ 所有 server 墓碑
                                    ├─ env 映射关闭、双向 trust 收回
                                    └─ AgentIdentity 活动关系关闭
```

整个图只有一个事务边界和一个 approvalRequestId；任一步失败全部回滚。

### 3.2 影响快照

operation 为 `namespace.permanent_delete`，schemaVersion 从 1 开始，requiredCapability 为 `approval.request`，classification 为 `approval_required`，riskLevel 为 `critical`，登记独立冻结/执行 adapter。申请冻结：

- namespace id/code/displayName、archived 元数据、资源版本；
- LobbyCluster、BC、大区、小区、server 的内部 id、稳定 code/serverId、父级 id、当前自身状态及分类计数；
- env 映射、双向 active trust、AgentIdentity、在线实例、活动交付/任务的稳定集合与计数；
- 不被删除的历史域计数、规范化原因、全集排序哈希和 24 小时过期时间。

页面可分页展示，服务端冻结的必须是完整集合。申请创建后不能修改目标、原因或快照；任何新增、迁移、状态变化或关系变化都导致执行时哈希不一致。

### 3.3 执行算法与领域边界

1. FR-207 worker 持 `ExecutionPermit` 开启单一 GORM 事务并锁定 namespace 根与当前子树集合。
2. 重算资源版本和全集哈希；namespace 非 archived、申请过期、已有冲突任务或任一事实漂移时直接失败，零业务写入。
3. 按父级 id 的集合更新写子节点级联墓碑；禁止循环内逐行查询/远程调用，也禁止调用会物理删除或拆事务的旧 helper。
4. 关闭 EnvNamespace、双向 NamespaceTrust、AgentIdentity 活动关系；再写 namespace 根墓碑、领域执行证据与强审计。
5. 事务提交后才失效 token/内存目录/boot registry，并通过既有异步收敛停止 Agent 与活动目标；外部 IO 失败不能制造数据库半墓碑，须可重试且始终由墓碑 gate fail-closed。

历史表不参与上述 UPDATE/DELETE。运行时所有入口先检查 namespace 墓碑，因此即使内存通知延迟，也不能注册、授权、调度、投递或执行。

### 3.4 API 与错误

| 方法 | 路径 | 语义 |
|---|---|---|
| GET | `/admin/v2/namespaces/{id}/permanent-deletion-impact` | 返回分类计数、分页样本与冻结所需版本，不产生副作用 |
| POST | `/admin/v2/approval-requests` | `{operationKey:"namespace.permanent_delete", parameters:{namespaceId:id,confirmationCode}, reason}` + `Idempotency-Key`；由 adapter 冻结并创建统一申请 |
| GET | `/admin/v2/namespaces` | 生命周期筛选扩展 `tombstoned|all`；普通上下文选择不返回墓碑 |
| GET | `/admin/v2/namespaces/{id}` | 墓碑详情只读，显示子树摘要、审批和历史入口 |

不存在直接执行的 namespace DELETE。旧 `/admin/v1/namespaces/{code}` 与历史 `/admin/v2/namespaces/{id}` DELETE 统一返回 410 `namespace_delete_migrated`，指引使用显式归档/永久删除申请；不得隐式创建申请。任何 service/repository hard-delete 必须移除或封死。

申请响应、状态与幂等规则完全复用 FR-207。常见领域错误：404 `namespace_not_found`；409 `namespace_not_archived`、`namespace_already_tombstoned`、`approval_target_changed`、`lifecycle_operation_conflict`；422 `reason_required`、`confirmation_mismatch`；复用/恢复为 409 `namespace_code_tombstoned`。

### 3.5 不变量与查询规则

- 子树墓碑完成后不存在 active server、active trust、有效 env 映射或有效 identity；但对应历史行仍可读。
- namespace code、子资源 code 路径与 serverId 保持原值；任何默认过滤墓碑的查询都不能用于标识占用校验。
- tombstoned namespace 不出现在页眉/运行选择器；审计、历史和墓碑管理页用显式 `includeTombstoned` 读取。
- 对超大子树使用集合 UPDATE、批量锁定与索引 COUNT；不得把全集装入内存或在事务内进行网络 IO。

## 4 UX / 交互

入口为 `/namespaces` archived 筛选与详情危险区。操作循环：查看完整影响分类 → 输入原因和完整 namespace code → 提交审批 → 在审批中心跟踪自动执行 → 进入只读墓碑详情。

- 仅 archived namespace 显示“申请永久删除”；active 先引导 FR-216 归档，tombstoned 无恢复/再次删除动作。
- 预览明确说明“子树存在不会阻止删除；批准后整棵树一次墓碑化”，分组展示 server、拓扑、Lobby、identity、env/trust 与保留历史。
- 确认按钮旁同时显示不可恢复、code/serverId 永不复用、历史不被清除；必须精确输入 namespace code。
- 完成后从全局 namespace 选择器移除，但墓碑详情和审批/审计记录仍可直接访问。
- **空态**：无 archived namespace 时说明先归档；无某类子资源时仍显示 0 计数，避免误以为未加载。
- **加载态**：分类统计、分页样本、审批状态独立骨架；未形成完整哈希前禁用提交。
- **错误态**：确认不匹配、快照漂移、事务失败或异步收敛失败分别显示可行动信息；绝不展示“部分删除成功”。
- **超大量态**：只渲染分类计数与分页样本；展示“完整集合已由服务端冻结”，禁止浏览器持有全量子树。

这是不可逆且跨域的结构性流程；实现前必须提供可点击 mockup，覆盖分类影响、双重确认、审批跳转、墓碑详情与四态，经用户确认后再接真实 API。

## 5 任务拆分

- [ ] 运行 namespace/server 生命周期、identity、trust/env、Lobby/区服、运行目标、审批、历史查询和 web 测试基线。
- [ ] 先完成 namespace 永久删除 mockup 评审，冻结分类影响与不可逆确认文案。
- [ ] 先写失败测试：非 archived 拒绝、含子树可申请、无 Permit 拒绝、快照漂移、任一点失败全回滚、全标识不可复用、历史保留。
- [ ] 最小实现根/子树墓碑字段、映射/绑定关闭事实、集合 repository 操作与单事务 adapter；禁止复用 hard-delete helper。
- [ ] 接入 FR-207 operation/worker、影响 API、contracts、墓碑详情与审批跳转；封死 V1/V2/内部删除旁路。
- [ ] 对注册、token、trust、env 映射、身份确认、目录、调度、消息、配置、交付和 Agent 操作逐项补墓碑 gate 测试。
- [ ] 独立复核完整集合哈希、锁顺序、事务大小、集合查询、重启恢复、审计脱敏、MySQL/SQLite 可移植性和 diff 范围。
- [ ] 重跑完整相关门禁；文档同步由主任务回填 PRD/API/ARCHITECTURE/CHANGELOG。

## 6 验收标准

### 6.1 自动化验收

1. active namespace 不能永久删除；archived 且有有效 ExecutionPermit 才能执行。
2. 含任意数量子资源的 archived namespace 可以创建影响快照和申请，不因“非空”被拒；批准后整个权威子树在一个事务中墓碑化。
3. 注入 Lobby、任一拓扑层、server、env/trust、identity、根墓碑或审计写失败时，全事务回滚，不存在半墓碑。
4. env 本体保留、映射关闭；双向 trust 关闭；所有 AgentIdentity 活动关系关闭；无任何物理 DELETE。
5. namespace code、所有子 code/serverId 永久占用，创建、注册、确认、授权、映射和恢复均无法复活。
6. 历史配置/文件/指标/连接/消息/交付/命令/审批/审计仍可按原稳定标识查询，任何运行目标解析均排除墓碑。
7. 快照漂移、重复 worker 与进程重启分别得到 fail-closed、幂等、可恢复结果；超大子树使用集合操作且无 N+1/无界内存加载。
8. 旧 namespace hard-delete 路由、service 与 repository 不可从外部或内部绕过审批调用。

### 6.2 真实验收

1. 准备含 Lobby、两层以上区服树、多个 server、env 映射、双向 trust、身份与历史记录的 archived 测试 namespace；批准后一次完成全树墓碑，无部分成功。
2. 尝试复用 namespace code、任一 serverId、恢复墓碑或重新授予 trust/映射，均明确失败；页眉选择器不再出现该 namespace。
3. 从墓碑详情进入指标、连接、消息、交付和审计历史，原稳定标识仍可追溯；审批中心保留申请、批准、执行与影响快照证据。
4. 预览后新增/迁移子资源，旧申请因漂移失败且零业务写入；重新申请后成功收口。

## 7 风险 / 待定

- 最大风险是把 namespace 删除实现成逐子资源调用或“有子资源返回 409”；两者都违反 ADR-0078，必须由单一集合事务 adapter 完成。
- 超大子树事务可能持锁较久；实现需固定锁顺序、集合更新和批量预览，但不得为缩短锁时把一个批准范围拆成可见的半事务。
- 旧 hard-delete repository 若仍可被其他 handler/service 调用就是审批旁路，必须通过 operation 清单和结构测试封死。
- 已决定：env/BC/大区/小区没有独立生命周期；BC/大区/小区/Lobby 只接受 namespace 级联墓碑；无物理删除、无恢复、无标识复用。
