# 功能规格：server 永久删除与墓碑

> 状态：草拟　·　关联 PRD：FR-217　·　分支：待执行时创建　·　依赖：FR-205、FR-206、FR-207、FR-215、[ADR-0078](../adr/0078-stable-resource-identity-and-lifecycle-tombstones.md)、[ADR-0079](../adr/0079-principal-capability-and-dangerous-operation-approval.md)

## 1 背景与目标

server 归档解决可恢复停用，但无法表达“该资产永久退出，旧 serverId 再也不能代表另一台机器”。直接物理删除会让旧审计、指标、连接、消息和外部自动化把复用后的标识误认成原资产，也可能遗留 active AgentIdentity 继续接入。

本 FR 将“永久删除”实现为不可恢复的主库墓碑：仅 archived server 可申请，经 FR-207 审批后从 archived 原子转换为 tombstoned，同时关闭可信身份绑定。server 行、稳定标识、拓扑事实和全部历史永久保留，`(namespace, serverId)` 永不复用。

## 2 需求（要什么）

### 2.1 范围

- 只有自身 `lifecycleStatus=archived` 且 namespace 未 tombstoned 的 server 可申请永久删除。
- 归档成功后不设置额外冷却期；满足状态、能力与冲突守卫即可立即创建审批申请。
- 批准执行后 server 变为 `tombstoned`，不可恢复、不可重新归档、不可重新确认同 serverId 身份。
- server 行、serverId、displayName、kind、BC/Zone/LobbyCluster 归属及审计关联保留；不执行物理 DELETE。
- 与该 server 关联的 AgentIdentity 可信活动关系在同一事务中永久关闭；身份行和关联标识仍保留供历史查询。
- 指标、健康快照、连接、消息、配置版本、交付记录、命令、文件与审计历史不删除、不改写，只通过原稳定标识关联墓碑。
- 所有创建、注册、身份确认、强制换绑和恢复路径都必须拒绝复用墓碑 serverId。

### 2.2 非目标

- 不做数据擦除、隐私删除、历史热冷归档或存储瘦身。
- 不允许 tombstoned server 恢复，也不提供“清空墓碑”运维开关。
- 不增加按天等待、延迟删除或冷却期；24 小时是审批申请有效期，不是资产删除冷却期。
- 不级联删除 BC、大区、小区、LobbyCluster 或 namespace。
- 不在通用审批表之外复制一套 server 删除工单状态机。

## 3 设计（怎么做）

### 3.1 数据与终态

FR-215 的 `server.lifecycle_status` 扩展为 `active|archived|tombstoned`，增加 `tombstoned_at`、`tombstoned_by`、`tombstone_reason`、`tombstone_approval_request_id` 与冻结影响哈希。墓碑字段仅在 archived → tombstoned 事务中一次写入，之后不可清空。

```text
active ──FR-215──▶ archived ──审批执行 permanent_delete──▶ tombstoned
                      ▲                                      │
                      └── FR-215 restore（删除前）            └── 无出边
```

- pending/rejected/expired/failed 的审批申请不改变 server。
- tombstoned 行继续参加 `(namespace_id, server_id)` 唯一约束；repository 创建/身份确认不得用“默认过滤 tombstone”误判标识空闲。
- 所有管理读模型明确区分 archived 与 tombstoned；运行时资格恒为 false。

### 3.2 可信绑定关闭

身份属主增加不可恢复的资产绑定关闭事实，例如 `asset_binding_closed_at`、`asset_binding_close_reason` 与来源审批 id；Agent 鉴权要求该事实为空且 server 有效 active。

- 执行事务对所有仍引用该 namespace/serverId 的 AgentIdentity 写关闭事实，保留 identityId、serverId、最后状态与历史字段。
- 不复用现有会清空 server 拓扑归属的普通 `unbind` helper；墓碑要求保留原 BC/Zone/LobbyCluster 归属供追溯。
- 旧身份随后注册、allow-reapply、approve、force-rebind 或 enable 时，先命中 server 墓碑并返回 `SERVER_ID_TOMBSTONED`，不能创建新 server 行。
- 内存 boot/在线目录在事务提交后按既有事件/租约机制失效；不得把外部断连等待放进数据库事务。

### 3.3 影响快照与审批执行

operation 为 `server.permanent_delete`，schemaVersion 从 1 开始，requiredCapability 为 `approval.request`，classification 为 `approval_required`，riskLevel 为 `critical`，登记独立冻结/执行 adapter。申请冻结：server id、namespace id/code、serverId、displayName、archived 元数据、资源版本、全部归属、关联 identity 数与稳定摘要、历史域计数、仍活动引用/任务计数及规范化原因。

- 申请人必须输入原因并显式确认 serverId；人或具备 capability 的机器可申请，只有 human 可批准。
- FR-207 批准后持久 worker 携 ExecutionPermit 调用领域 adapter，不回调 HTTP，也不允许普通 service 调用构造许可。
- 执行前重新校验 server 仍为 archived、namespace 未 tombstoned、资源版本与影响哈希未漂移、无正在执行的冲突生命周期任务。漂移记录 `failed` 并要求重提。
- 单事务写 server 墓碑、身份绑定关闭事实、审批领域证据和强审计；任一步失败整单回滚。事务提交后再做有界的内存目录失效通知。

### 3.4 查询与 API

| 方法 | 路径 | 语义 |
|---|---|---|
| GET | `/admin/v2/servers/{id}/permanent-deletion-impact` | 读取当前脱敏影响快照，不产生副作用 |
| POST | `/admin/v2/approval-requests` | `{operationKey:"server.permanent_delete", parameters:{serverRowId:id,confirmationServerId}, reason}` + `Idempotency-Key`；由 adapter 冻结并创建统一申请 |
| GET | `/admin/v2/servers` | 生命周期筛选扩展 `tombstoned|all`；墓碑默认不进入普通 active 列表 |
| GET | `/admin/v2/servers/{id}` | tombstoned 仍可读，返回墓碑摘要及只读历史入口 |

不存在公开 `DELETE /admin/v2/servers/{id}` 直接执行端点。旧 `/admin/v1` 删除路径必须移除或只创建同一 `server.permanent_delete` 申请，绝不能物理删除。

申请响应、状态与幂等规则完全复用 FR-207。常见领域错误：404 `server_not_found`；409 `server_not_archived`、`server_already_tombstoned`、`approval_target_changed`、`lifecycle_operation_conflict`；422 `reason_required`、`confirmation_mismatch`；注册/创建冲突为 409 `server_id_tombstoned`。

### 3.5 历史与性能边界

- 历史查询继续按 namespace id/serverId 命中，不强制改写旧表或给大表做级联 UPDATE。
- 影响预览按现有索引做分类 COUNT 与有界样本；不得一次加载全部指标/连接/消息到内存。
- 墓碑信息属于主库权威事实，不进入 FR-151 热冷归档；历史数据仍遵循各自归档策略。

## 4 UX / 交互

入口为 `/servers` 的 archived 筛选及详情危险区。操作循环：查看不可逆说明和影响 → 输入原因与完整 serverId → 创建审批 → 在审批中心跟踪自动执行 → 成功后查看只读墓碑。

- 仅 archived 行显示“申请永久删除”；active 行先引导走 FR-215 归档，tombstoned 行不显示恢复/删除动作。
- 影响页明确列出保留项与关闭项：历史和归属保留、运行资格和身份绑定永久关闭、serverId 永不复用。
- 删除成功后以“已墓碑”而非“已从系统清除”表述，并提供历史查询/审计入口。
- **空态**：无 archived/tombstoned server 时说明筛选含义，不展示误导性删除入口。
- **加载态**：影响统计与审批状态各自使用骨架，未取回稳定快照前不能提交。
- **错误态**：确认值错误、状态变化、快照漂移或执行失败均保留表单原因并引导重新预览/重提。
- **超大量态**：历史影响只给分类计数和分页样本，审批冻结全集哈希，不渲染全量历史行。

这是不可逆的结构性流程；实现前必须提供可点击 mockup，覆盖 archived 前置条件、双重确认、审批跳转、墓碑只读详情和四态，经用户确认后再接真实 API。

## 5 任务拆分

- [ ] 运行 server 生命周期、identity、注册、目录、历史查询、审批与 web 测试基线。
- [ ] 先完成永久删除 mockup 评审，冻结不可逆提示、影响分类和确认文案。
- [ ] 先写失败测试：非 archived 拒绝、无 ExecutionPermit 拒绝、快照漂移、事务回滚、墓碑不可恢复/不可复用、历史仍可查。
- [ ] 最小实现 server 墓碑字段、身份绑定关闭事实、repository 标识占用查询和领域 adapter；禁止物理删除。
- [ ] 接入 FR-207 operation/worker、影响 API、contracts、`/servers` 只读墓碑视图与审批跳转。
- [ ] 清点创建、注册、approve、allow-reapply、force-rebind、恢复及 V1 删除旁路，逐条补 `SERVER_ID_TOMBSTONED` 测试。
- [ ] 独立复核事务边界、历史引用、敏感信息、幂等/重启恢复、查询有界性和 diff 范围。
- [ ] 重跑完整相关门禁；文档同步由主任务回填 PRD/API/ARCHITECTURE/CHANGELOG。

## 6 验收标准

### 6.1 自动化验收

1. active server 不能永久删除；只有 archived + 有效 ExecutionPermit 能进入 tombstoned。
2. 执行后 server 行、serverId、displayName、归属和历史关联仍在，任何目标表均无物理 DELETE。
3. 所有关联 AgentIdentity 的可信活动关系关闭且行保留；旧 identity/新 identity 均不能用同 serverId 注册、确认、换绑或启用。
4. tombstoned server 的恢复、再次归档、再次永久删除均明确拒绝，唯一标识持续占用。
5. 指标、连接、消息、交付和审计历史仍能按原 serverId 查询，运行目录/调度/广播/交付/指令永不包含该资源。
6. 影响漂移、事务注入失败、重复 worker 与进程重启分别得到 fail-closed、原子、幂等且可追溯的结果。
7. 旧 V1 删除入口无法物理删除或绕过通用审批。

### 6.2 真实验收

1. 将一台测试 server 归档后提交永久删除审批；批准执行后页面显示墓碑，原身份立即失去运行资格，原历史与拓扑仍可查看。
2. 用同 namespace/serverId 的旧 identity 和新 identity 分别重新注册，均得到 `SERVER_ID_TOMBSTONED`；管理台也不能创建或恢复该标识。
3. 在预览后改变关联事实，旧申请执行失败且不产生半墓碑；重新申请成功后审批、执行、失败/成功证据和审计完整留档。

## 7 风险 / 待定

- 普通 repository 常默认排除 tombstone；标识占用检查必须显式包含墓碑，否则会产生严重身份复用漏洞。
- 不能调用现有“解绑并清归属”逻辑；永久删除需要关闭可信绑定同时保留拓扑历史，必须由独立小型 adapter 完成。
- 物理数据擦除属于不同合规需求，不能借“永久删除”名义删除历史；如未来需要，必须另立规格和审批模型。
- 已决定：serverId 永不复用；墓碑无恢复路径；只有 archived 可申请；批准执行后原子关闭绑定并永久留档。
