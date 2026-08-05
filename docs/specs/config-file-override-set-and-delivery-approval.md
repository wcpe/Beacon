# 功能规格：配置、文件、覆盖集与交付高危操作审批

> 状态：草拟　·　关联 PRD：FR-211　·　决策：[ADR-0079](../adr/0079-principal-capability-and-dangerous-operation-approval.md)　·　依赖：[dangerous-operation-approval-core.md](dangerous-operation-approval-core.md)

## 1. 背景与目标

Beacon 现有配置、文件树、覆盖集、灰度和 V2 ChangeOrder 各自存在直接写、二次确认或领域审批。尤其 ChangeOrder 当前是“提交 → human 批准 → 再点启动”，若再叠加统一审批，会形成 generic 与 domain 两套批准事实。

本功能只增加领域 adapter：统一审批核心仍是唯一审批真源，配置/文件/覆盖集/交付领域继续拥有自己的版本、目标、批次和回滚状态机。ChangeOrder 的 human 批准会自动进入持久启动路径，不再开放第二个人工 start 闸。

## 2. 需求（要什么）

### 2.1 操作分类

| 类别 | operation 示例 | 分类 |
|---|---|---|
| 只读与草稿 | list/get/diff/dry-run/impact/validate；V2 配置文件建档、保存未生效版本；ChangeOrder draft 编辑 | `direct`，按 capability 与既有审计执行 |
| Legacy 配置直接生效 | 配置 publish/rollback、gray publish/promote；批量动作中会发布、启用或扩大生效范围的分支 | `approval_required` |
| Legacy 文件与覆盖集直接生效 | 文件 publish/import/rollback、覆盖集 publish/rollback；批量动作中会覆盖线上内容的分支 | `approval_required` |
| V2 统一交付 | ChangeOrder submit 后的首次启动、暂停后的 resume、下一批推进、整单 rollback、带残留失败的 rollback finish | `approval_required` |
| 立即止损 | ChangeOrder withdraw、pause、cancel；gray abort；运行中文件任务 pause/cancel | `direct + 强审计` |
| 归档、删除与清除 | 配置、文件、覆盖集及 ChangeOrder 的 archive/delete/purge（含 draft 删除） | `approval_required`；冻结对象版本、引用/影响计数与保留策略，不复用 namespace/server 生命周期状态机 |

- 按 body 分支决定风险的 batch 端点必须在 service 层展开为明确 operation；不能仅因 HTTP 方法相同而统一放行。
- V1 与 V2 同义入口、REST/MCP/内部任务必须登记到同一 operation descriptor；新增未分类入口 fail-closed。
- V2 配置版本保存本身不下发，仍可 direct；任何让版本进入真实目标服有效态的动作必须经 ChangeOrder。
- 配置/文件/覆盖集/ChangeOrder 的 archive/delete/purge 只复用统一审批状态机和执行许可，不套用 FR-215～218 的资源生命周期；执行前必须重新校验引用、保留期、归档完整性与当前版本，不能因“对象已停用”而直接清除。

### 2.2 ChangeOrder 单一审批语义

- `POST /admin/v2/change-orders/{id}/submit` 冻结当前 revision、items、selector、目标影响、批次计划、生效方式、配置 pin/文件哈希摘要，并创建统一申请；返回申请编号和 24 小时过期时间。
- 新申请下 ChangeOrder 只走 `draft → pending_approval → rolling`：`pending_approval` 是关联统一申请的领域投影，不拥有独立 approve/reject 判据；稳定 `approved` 仅保留给历史数据兼容，不作为新流程停留态。
- human 在 `/approvals` 执行“批准并执行”后，持久 worker 经 `ChangeOrderApprovalAdapter` 获取 `ExecutionPermit`，执行现有 Start 不变量和目标固化，然后直接进入 `rolling`。
- `approved_by/approved_at` 等历史字段可保留为查询投影，但写入来源只能是统一审批事实，领域逻辑不得据它们绕过 permit。
- 原 `POST .../{id}/approve` 与 `POST .../{id}/start` 都固定失败关闭；审批人只能在统一审批中心决定，批准 worker 直接启动，不得出现“submit 后再 requestApprove”的双步骤。

### 2.3 快照、当前 diff 与漂移

冻结快照只保存可审计的元数据，不保存配置敏感值、文件内容或 blob：

- order revision、namespace、创建人、理由；
- 文件路径、大小、sha256 与增删改统计；
- 配置文件/作用域/版本 ID、内容哈希与脱敏 diff 统计；
- selector、排除项、解析出的目标 ID 集合哈希与目标数量；
- 批次大小、观察窗、熔断阈值、生效方式、预计传输量。

批准详情由领域 adapter 只读计算“申请快照 vs 当前事实”：

- revision、items、目标、版本、哈希或领域守卫任一漂移，首次启动置申请 `failed`，不得静默重算并扩大范围。
- resume、下一批推进和 rollback 分别冻结当时的剩余目标/下一批目标/已生效与备份可用性；批准只执行该快照内的动作。
- approved 后执行冲突、目标离线、blob/备份缺失等失败保留批准事实并置 `failed`，必须重提，不退回 pending。

## 3. 设计（怎么做）

### 3.1 领域 adapter 与 permit

- 配置、文件、覆盖集和交付各自提供小型 adapter，实现统一核心要求的 `describe/freeze/currentDiff/execute` 边界；不让统一审批 service 理解批次、pin、文件备份等领域细节。
- 产生线上副作用的 service 方法接收内部 `ExecutionPermit` 并核对 operation、目标引用、冻结哈希与申请 ID；普通 handler、MCP 或后台 goroutine 无法构造。
- worker 直接调 adapter，不回调 REST；领域事务与现有审计保持原子规则，执行结果再回写统一申请。

### 3.2 交付动作映射

| 动作 | 领域变化 | 批准后的自动行为 |
|---|---|---|
| 首次提交/启动 | `draft → pending_approval → rolling` | 复用现有冲突守卫、固化 `change_target`、准备 payload 并启动首批 |
| resume | `paused → rolling` | 仅按冻结的恢复 mode 继续，不扩大已批目标 |
| batch confirm | 当前批完成并放行下一批 | 重新校验观察窗/熔断与下一批目标哈希后自动推进 |
| rollback | `completed/paused/cancelled → rolling_back` | 按冻结备份与配置版本执行整单回退 |
| rollback finish | `rolling_back → rolled_back` | 明示残留失败目标后收单并保留告警 |

pause/cancel/withdraw 不创建申请；它们不得自动触发 resume、重新组批或回滚。

### 3.3 迁移与兼容

- 为活动 `pending_approval` ChangeOrder 建立一对一统一申请并保存关联；历史已终态记录保持只读。
- 迁移时发现同一 order 有多份有效审批证据、快照无法重建或字段不一致，标记为需人工撤回/重提，不猜测。
- 兼容端点与审批中心消费同一申请 ID、同一 timeline；不得为旧端点维护第二事件表。

## 4. UX / 交互

- 用户任务：创建者从配置/文件/覆盖集或变更单页面提交危险动作；审批人到审批中心比较影响后批准执行；事故处理中可立即暂停/取消。
- 进入路径：原领域按钮仍是提交入口；提交成功显示审批编号并跳 `/approvals/{id}`。ChangeOrder 详情的“审批”“启动”合并为一个审批状态卡。
- 操作闭环：影响预览 → 填原因提交 → 审批中心看快照/当前 diff → 批准并自动执行 → 回到领域详情看批次或失败；止损动作原地直接完成并给审计链接。
- IA 挂载：领域页面负责创建与执行结果，统一待办和历史在顶层 `/approvals`；审批中心页面规格见 [approval-center.md](approval-center.md)。

页面四态：

- **空态**：无差异、无目标或无可审批动作时说明缺失前置条件，不展示可启动的空 ChangeOrder。
- **加载态**：影响预览、目标解析、当前 diff 与审批状态分别显示骨架；任一冻结输入未完成时禁止提交或推进。
- **错误态**：扫描、预览、申请或执行失败保留原领域上下文，显示脱敏原因与重新扫描/重提入口；漂移失败不得提供“仍按当前值执行”。
- **超大量态**：目标、文件差异和审批影响均服务端分页/分批展示，只冻结服务端完整集合哈希，浏览器不持有全量目标或文件内容。
- **常规态**：pending/executing/succeeded/failed 与 ChangeOrder 领域状态并列且来源清楚；止损动作原地可用，批准后无需第二个启动按钮。

把 ChangeOrder 的“审批 + 启动”合并为统一“批准并自动执行”是主流程结构性变化。接真前必须提供配置、文件、覆盖集和 ChangeOrder 的可点击 mock，覆盖上述四态、单一审批状态卡、自动启动与止损直执，并经用户浏览器评审拍板。

## 5. 任务拆分

- [ ] 测试先红：完整 operation 覆盖表，锁定 V1/V2、body 风险分支、REST/MCP/内部调用；未分类入口失败。
- [x] FileService：首次创建、目录导入和批量删除/禁用/启用均冻结为加密 `FilePendingChange`，公开副作用入口失败关闭；仅无内容且不生效的未来草稿可 direct。
- [ ] 测试先红：ChangeOrder submit 只建一份统一申请，human 批准自动 Start，机器批准/直接 Start/无 permit service 调用均拒绝。
- [ ] 测试先红：order revision、目标哈希、配置版本、文件 sha256、下一批或备份漂移均 failed 且无副作用。
- [ ] 最小实现领域 adapters 与 permit 守卫，复用既有 ChangeOrder 状态机、编排器、配置/file/override service。
- [ ] 迁移活动 ChangeOrder 审批关联，收敛旧 approve/start 兼容入口，不建立第二审批事件流。
- [ ] 先完成四态可点击 mockup 与浏览器评审，再合并 ChangeOrder 审批/启动交互；止损动作继续直执并展示强审计。
- [ ] 独立复核：按 operation 清单从 handler 追到最终 service，确认没有双审批、内部 HTTP 回调和明文快照。
- [ ] 文档同步：PRD 状态、ARCHITECTURE 的领域 adapter、UX、API 兼容语义、v2-delivery-orchestration 状态机说明、CHANGELOG。

## 6. 验收标准

1. 所列配置/文件/覆盖集/灰度/交付强危险动作未批准时不产生线上副作用；所有机器主体只能申请、查询和撤回自己的 pending 申请。
2. ChangeOrder 每次危险动作只有一个统一申请；human 批准首次申请后自动启动首批，不再需要公开 start 步骤。
3. pause/cancel/withdraw/gray abort 可立即止损且强审计；resume、下一批推进与 rollback 仍需新审批。
4. 快照不含配置敏感值、文件内容或 blob；审批详情能展示哈希/版本/目标/批次差异。
5. 漂移、冲突、备份缺失或执行失败均保留批准事实并终态 failed，不扩大范围、不静默重算。
6. 旧 pending ChangeOrder 可安全迁移；无法证明一致的记录只能撤回重提。
7. 配置/文件/覆盖集/ChangeOrder 的 archive/delete/purge 均有显式 operation、影响快照和执行前保留/引用校验，任何旧直删入口不能绕过审批。
8. 四态与单一审批/自动启动 mockup 经用户浏览器评审拍板后才接真；相关后端与前端测试先红后绿，独立复核确认统一审批为唯一真源。

## 7. 风险 / 待定

- Legacy 直接生效端点数量多，实施前必须生成可执行 operation 清单并以覆盖测试锁住；不能只迁页面当前调用到的入口。
- ChangeOrder 的 `approved` 历史状态仍需可读，若直接删除会破坏旧数据；本功能只停止新流程写入稳定 approved，不做无关 schema 重构。
- 大目标集不在审批快照重复保存完整敏感详情；采用可分页影响明细 + 规范化哈希，执行时仍按领域真源逐项校验。
