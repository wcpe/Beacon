# 功能规格：namespace 归档与恢复

> 状态：草拟　·　关联 PRD：FR-216　·　分支：待执行时创建　·　依赖：FR-205、FR-206、FR-207、[ADR-0078](../adr/0078-stable-resource-identity-and-lifecycle-tombstones.md)、[ADR-0079](../adr/0079-principal-capability-and-dangerous-operation-approval.md)

## 1 背景与目标

namespace 是隔离、注册、调度、消息、配置、交付和 Agent 操作的权威根。当前没有资产生命周期，操作者不能在保留全部子资源状态和历史的前提下整域停用，也无法随后按原结构恢复。

本 FR 增加 namespace `active ↔ archived` 生命周期。归档在所有读取与执行边界把整个权威子树计算为“有效停用”，但不覆写 server 等子资源自身状态；恢复后只恢复父级资格，原本独立 archived 的 server 仍保持归档。归档和恢复都通过 FR-207 审批执行。

## 2 需求（要什么）

### 2.1 范围

- active namespace 可申请归档；archived namespace 可申请恢复。
- 归档保留 namespace code/displayName、token 哈希、env 映射、双向 trust、LobbyCluster、BC/大区/小区、server、AgentIdentity 和全部历史事实，不物理删除。
- namespace archived 时，接入 token、注册、身份确认、在线目录、调度、消息、配置目标、交付目标、BC 受管目录和 Agent 操作统一 fail-closed。
- 子资源自身状态不被批量改写；归档/恢复只改变根状态和有效资格。
- 管理查询可显式查看 archived namespace 及子树，运行时查询默认只看有效 active。

### 2.2 非目标

- 不实现永久删除或子树墓碑；由 FR-218 承接。
- 不给 env、BC、大区、小区增加独立 archive 状态。
- 不自动删除/吊销 namespace token、trust、env 映射或 AgentIdentity；归档只使它们暂时无效。
- 不把 server 的独立 archived 状态改回 active，也不重建 LobbyCluster。

## 3 设计（怎么做）

### 3.1 数据与状态机

namespace additive 增加：`lifecycle_status`（`active|archived`，默认 active）、`archived_at`、`archived_by`、`archive_reason`。FR-218 后续扩展同一状态列为 `tombstoned`。

```text
active ──审批执行 archive──▶ archived
   ▲                              │
   └────审批执行 restore──────────┘
```

- 申请与审批状态只存在统一 ApprovalRequest 中，不能用 namespace 中间状态冒充。
- `namespace.effectiveActive = lifecycle_status == active`；任一子资源 `effectiveActive = 自身资格 AND namespace.effectiveActive`。
- namespace 状态更新使用资源版本/CAS；重复 worker 以审批任务幂等键收敛。

### 3.2 影响快照与执行边界

operation 固定为 `namespace.archive`、`namespace.restore`，schemaVersion 均从 1 开始，requiredCapability 为 `approval.request`，classification 为 `approval_required`，riskLevel 为 `critical`，分别登记独立冻结/执行 adapter。申请冻结：namespace id/code/displayName、当前生命周期、资源版本、token/注册有效性摘要、env/trust 摘要，以及 LobbyCluster、BC、大区、小区、server、active identity、在线实例、活动交付/任务的分类计数和稳定目标摘要。

- 影响明细必须分页/有界；审批冻结完整目标集合的规范化哈希与计数，不能只冻结页面当前页。
- 批准后由 FR-207 worker 持 ExecutionPermit 调用 namespace 生命周期 adapter；不得由 handler 直接改状态。
- 执行前重新解析子树与资源版本。新建/迁移子资源、trust 或活动任务导致快照变化时，以 `approval_target_changed` 失败并要求重提。
- 事务内写根生命周期、领域执行证据和强审计。运行中长任务只在执行边界被拒绝/停止继续推进，不在请求线程轮询等待。

### 3.3 有效停用规则

所有属主 service 在共同 namespace gate 后继续执行自己的不变量：

| 边界 | archived 行为 |
|---|---|
| token/注册/身份确认 | 明确拒绝 `NAMESPACE_ARCHIVED`，不得创建或复活 server/identity |
| 在线目录/健康/调度/默认入口 | 子树不进入活动视图或候选集 |
| 消息/广播 | 不能作为来源或目标；不投递新消息 |
| 配置/文件/交付 | 不能新建、启动、放行或选择该 namespace 目标；历史只读可查 |
| BC 受管目录与 Agent 操作 | 下一次快照收敛为空/停用；新指令拒绝 |
| env/trust | 行保留但有效性为 false；恢复后按原状态重新计算 |

恢复只解除 namespace gate：已 archived server、disabled/unbound identity、已 revoked trust、已取消任务仍保持原状态；不得“一键重置子树”。

### 3.4 API 与错误

| 方法 | 路径 | 语义 |
|---|---|---|
| GET | `/admin/v2/namespaces/{id}/lifecycle-impact?action=archive|restore` | 返回当前脱敏影响预览 |
| POST | `/admin/v2/approval-requests` | `{operationKey:"namespace.archive"|"namespace.restore", parameters:{namespaceId:id}, reason}` + `Idempotency-Key`；由对应 adapter 冻结并创建统一申请 |
| GET | `/admin/v2/namespaces` | 增加 `lifecycleStatus=active|archived|all` 与 effective 状态摘要 |

申请响应、状态与幂等规则完全复用 FR-207，不另造领域审批 API。常见领域错误：404 `namespace_not_found`；409 `namespace_not_active`、`namespace_not_archived`、`approval_target_changed`、`lifecycle_operation_conflict`；403 `capability_denied`；422 `reason_required`。旧 `/admin/v1/namespaces/{id}` 与历史 `/admin/v2/namespaces/{id}` DELETE 统一返回 410 `namespace_delete_migrated`，指引显式归档/永久删除申请；不得物理删除、隐式创建申请或成为跳过 ApprovalRequest 的别名。

## 4 UX / 交互

入口为 `/namespaces` 行操作与详情。主循环：查看子树影响 → 填原因 → 提交审批 → 查看审批进度 → 执行完成后刷新全局 namespace 上下文。

- active 行提供“申请归档”；archived 行提供“申请恢复”和 FR-218 永久删除入口。
- 影响预览按 LobbyCluster、BC、大区、小区、server、在线身份、env/trust、运行任务分类展示计数，并明确“子资源状态不会被覆写”。
- archived namespace 在全局筛选、列表和详情中有明确标记；选择后进入只读观测模式，危险写入口禁用并解释原因。
- 恢复确认明确展示仍将保持 archived/disabled/revoked 的子项数量，避免误以为全域恢复所有状态。
- **空态**：无 namespace 或筛选后为空时提供创建/清除筛选指引。
- **加载态**：列表与影响统计分别使用骨架；快照未完成时禁止提交。
- **错误态**：加载失败、快照漂移、审批失败显示原因和重新预览入口，不静默继续。
- **超大量态**：子树影响只展示分类计数和分页抽样；稳定哈希覆盖完整集合，页面不渲染全量 server。

这是既有 `/namespaces` 页面结构性危险操作补齐；实现前须产出可点击 mockup，覆盖影响预览、审批跳转、只读 archived 观察和四态，经用户确认后再接真实 API。

## 5 任务拆分

- [ ] 运行 namespace、identity、调度、消息、配置、交付、BC 目录及 web 测试基线。
- [ ] 先完成 `/namespaces` 生命周期 mockup 与四态评审，冻结影响预览信息架构。
- [ ] 先写失败测试：状态转换、ExecutionPermit、快照漂移、子状态不变、各边界 fail-closed、恢复后的混合状态。
- [ ] 最小实现 namespace lifecycle 字段、资格 gate、repository 过滤与领域 adapter；不复制各下游状态机。
- [ ] 将统一 gate 接入注册、身份、目录、调度、消息、配置、交付、BC 与 Agent 操作边界，并补集成测试。
- [ ] 接入 FR-207、contracts、影响 API 与 `/namespaces` 页面；旧 V1/V2 DELETE 改为 410 迁移错误且无隐式申请。
- [ ] 独立复核目标全集哈希、事务/审计、重启恢复、查询边界、循环依赖和 diff 范围。
- [ ] 重跑完整相关门禁；文档同步由主任务回填 PRD/API/ARCHITECTURE/CHANGELOG。

## 6 验收标准

### 6.1 自动化验收

1. namespace 归档/恢复均只能由持 ExecutionPermit 的 adapter 执行，机器主体无法批准。
2. 归档前后所有子资源数据库自身状态、业务标识、归属与关系保持不变；没有物理 DELETE。
3. archived namespace 的 token、注册、调度、消息、配置、交付、目录和 Agent 操作逐项 fail-closed，历史只读查询仍可用。
4. 恢复后 active 子项重新取得资格，独立 archived server、disabled identity、revoked trust 和取消任务仍保持原状态。
5. 影响集合新增/迁移资源会触发漂移失败；重复 worker、事务失败与重启恢复保持幂等且有证据。
6. 管理查询能显式查看 archived 子树，普通运行查询默认排除；查询分页且无 N+1。
7. 旧 V1/V2 DELETE 返回 410 迁移错误，不能物理删除、隐式创建申请或绕过审批。

### 6.2 真实验收

1. 在测试 namespace 下准备 BC、大区、小区、Lobby、多个 server、trust/env 映射和活动 Agent；申请并批准归档后，子树不再参与任何运行路径，但管理面历史与结构完整可查。
2. 保持其中一台 server 自身 archived、一个 identity disabled、一个 trust revoked，再批准恢复 namespace；只有原本合格的子项恢复资格，上述三项状态不变。
3. 在影响预览后新增或迁移一台 server，原审批执行失败并要求重提；审批中心与审计可追溯完整原因。

## 7 风险 / 待定

- 最大风险是某个下游绕过 namespace gate。实现必须维护消费者清单并以跨域测试证明，不接受只改管理列表。
- 大子树不能在请求内逐行写或渲染；归档本身只更新根状态，影响快照用集合查询/分页，禁止 N+1。
- 归档和恢复都属于 ADR-0078 明确列出的危险操作，不能按“一键止损”绕过通用审批。
- 已决定：归档不覆写子状态；恢复只解除父级 gate；LobbyCluster 不新增名称；永久删除另由 FR-218 原子墓碑化。
