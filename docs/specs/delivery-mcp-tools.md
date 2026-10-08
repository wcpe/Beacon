# 功能规格：交付 MCP 工具集与审批面

> 状态：**代码与单测已交付，待真机验收**（FR-245 / FR-246 / FR-247 / FR-248 / FR-249；FR-250 的入参与审计口径收口项一并落地）。真机维度（真实控制面 + automation MCP 客户端）尚未实测——按 §6 共同硬闸，验收结论以真机通过为准，PRD 各 FR 行保持「开发中」直到真机验收通过。
> 关联 PRD：FR-245、FR-246、FR-247、FR-248、FR-249
> 分支：feature/fr-245-mcp-read-tools、feature/fr-249-approval-surface、feature/fr-246-mcp-write-tools
> 决策依据：FR-220 / FR-236 / FR-237 / FR-242 既有 MCP 架构；本规格不引入新 ADR
> 批次口径：PRD 的「交付优化批 1 / 批 2」是验收批次；本规格按 FR 编号组织施工（FR-245 / FR-249 与 FR-246 / FR-247 / FR-248 同属批 1）。

## 1. 背景与目标

交付域是全部领域中 MCP 覆盖度最低的一个：HTTP 面 21 项操作只暴露 6 项（29%），且**全部是"创建审批申请"的写操作，只读侧为 0**——机器主体拿不到变更单号、看不见状态、无止损手段、被拒时收不到原因，等于"盲写"。本规格补齐交付域的 MCP 能力面，兑现"AI 全流程托管（除审批）"：

- **看得见**：列表 / 详情 / 目标 / 影响 / 观察窗 / 事件六个只读工具（FR-245）
- **建得单**：创建 / 编辑 / 差异扫描三个组单工具（FR-246）
- **能止血**：暂停 / 终止两个止损工具（FR-247）
- **说得清**：被拒时返回可区分的中文原因（FR-248）
- **跟得上**：申请票据与审批视图补关键字段，提交后能感知"申请了什么、结果如何"（FR-249）

不变量（延续 FR-206/220）：机器主体**永不审批**；全部工具走 `mcpAddTool` 唯一注册入口与既有发现面/执行面门禁；工具进 `mcpToolCatalog` 风险目录（FR-236 覆盖测试守护）。

## 2. 需求

范围内：

- 11 个新 MCP 工具（6 只读 + 3 组单 + 2 止损），语义与既有 `/admin/v2` HTTP 端点逐一对齐。
- 全部只读返回**有界投影**（见 §3.2），不透传大字段（吸取 `metrics.health.list` 直接透传的反例）。
- 交付系工具（含既有 6 个）的拒绝文案改为可区分原因（§3.5）。
- 交付申请票据补 `orderId` 与影响摘要；`beacon.approvals.own.get/list` 投影补执行与失败信息（§3.6）。
- 顺带修复（FR-246 范围内）：`Create` 此前忽略 `configChanges`，导致组单必须「先建后改」两段式（已随本 FR 修复）。

不做（范围外）：

- 机器审批决定（归 FR-223 的 `allow-approval-decide` 开关，本规格不碰）。
- 回滚类工具（已有 `order.rollback` / `rollback.finish`；目标级回滚见 FR-270 的独立规格）。
- 交付数据面（blob / manifest / 回执）的 MCP 暴露——那是 agent 面，不是操作者面。
- 前端消费（202 展示、审批进度视图）归 FR-252 / FR-255，本规格只冻结契约。

## 3. 设计

### 3.1 工具目录（11 个新增）

命名统一遵循既有规范 `beacon.<域>.<资源>.<动词>`（先例：`beacon.metrics.health.list`、`beacon.topology.snapshot.get`），**一律 4 段、动词结尾**（仓库既有 81 项最长为 4 段）。

| 工具名 | 语义 | 风险 | AutomationOnly | OperationKind | 写语义 | 归属 FR（开发状态） |
|---|---|---|---|---|---|---|
| `beacon.delivery.order.list` | 变更单列表（分页筛选） | low | 否（observer 可见） | 空 | 只读 | FR-245（开发完成） |
| `beacon.delivery.order.get` | 变更单详情（含计数摘要） | low | 否（observer 可见） | 空 | 只读 | FR-245（开发完成） |
| `beacon.delivery.targets.list` | 目标分页（含逐台状态/失败原因/备份标记） | low | 否（observer 可见） | 空 | 只读 | FR-245（开发完成） |
| `beacon.delivery.impact.get` | 影响预览（汇总+逐目标） | low | 否（observer 可见） | 空 | 只读 | FR-245（开发完成） |
| `beacon.delivery.observe.get` | 当前批观察窗序列 | low | 否（observer 可见） | 空 | 只读 | FR-245（开发完成） |
| `beacon.delivery.events.list` | 进度事件（有界条数） | low | 否（observer 可见） | 空 | 只读 | FR-245（开发完成） |
| `beacon.delivery.order.create` | 建 draft（含 configChanges 一次成型） | low | 是 | 空 | direct + 审计 | FR-246（开发完成） |
| `beacon.delivery.order.update` | 编辑 draft | low | 是 | 空 | direct + 审计 | FR-246（开发完成） |
| `beacon.delivery.order.diff-scan` | 同步重扫差异 | low | 是 | 空 | direct + 审计 | FR-246（开发完成） |
| `beacon.delivery.order.pause` | 人工暂停（止损） | high | 是 | 空 | direct + 强审计 | FR-247（开发完成） |
| `beacon.delivery.order.cancel` | 紧急终止（止损，原因必填） | high | 是 | 空 | direct + 强审计 | FR-247（开发完成） |

定级与可见性依据（对齐既有先例）：

- **只读 6 项 = low、不带 `AutomationOnly`**：无副作用；observer 与 automation 共用（对齐既有只读段与 `beacon.topology.snapshot.get` 等）。`AutomationOnly=true` 的真实语义是"仅 automation 可发现"（observer 不可见），只读工具**不得**带该标记，否则 FR-245 的 observer 可见验收必红。
- **组单 3 项 = low + `AutomationOnly`**：均为 draft 阶段操作，无生产副作用；领域守卫已限界（draft 状态机、selector 跨 namespace 拒绝、模板源合格性校验）——对齐"拓扑建树 low"先例；对应 HTTP 路由为 `management.direct` 档。
- **止损 2 项 = high + `AutomationOnly`**：直执改变生产状态（暂停/终止进行中的灰度），但可逆（可 resume / 可回滚）——对齐"告警处置 high 直执"先例；对应 HTTP 路由为 `management.direct` 档；cancel 的 `reason` 必填（与 HTTP 面一致）。
- **全部 `OperationKind` 留空**：只读无审批语义；组单/止损为 direct 档、无审批票据（对齐 ALERT handle 先例）。故不触发「catalog 等级 ≥ descriptor」约束（仅当误挂 `delivery.rollback` 等 kind 才会），且覆盖测试的 `wantChecked`（现 49）不变。
- **无一项 critical**：回滚才是 critical（已有）；生产模式门禁（FR-237/242）对 low/high 无影响。

### 3.2 只读工具契约（FR-245）

**统则**：返回体一律为**有界投影**——列表类强制分页（`page`/`pageSize`，pageSize 上限沿用域内既有上限 100）；详情类只回摘要与计数，不回传 items 全量文件清单与批/目标全量明细（那些走 `targets.list`/`impact.get` 分页拉取）；事件/观察窗回有界条数（沿用 HTTP 端点的有界行为，不新增上限语义）。**输出字段名一律沿用既有 HTTP 视图字段名**（以 `apps/server/internal/service/delivery_views.go` 为准），不得自造键名。

| 工具 | 输入 | 输出（投影字段） |
|---|---|---|
| `order.list` | `namespaceId`（必填）、`status?`、`createdBy?`、`keyword?`、`page?`、`pageSize?` | `items[]`（`id`/`title`/`status`/`pauseKind?`/`createdBy`/`createdAt`/`payloadState`）、`total`（对齐既有 `ChangeOrderListView`，不含额外键） |
| `order.get` | `orderId` | 单字段摘要（沿既有详情视图：`id`/`title`/`description?`/`namespaceId`/`status`/`pauseKind?`/`pauseReason?`/selector 摘要/`batchMode`/`batchSizes`/`activationMethod`/`observeWindowSec`/`activateTimeoutSec`/阈值两项/`payloadState`/`createdBy`/`submittedAt?`/`approvedAt?`/`startedAt?`/`finishedAt?`/`cancelReason?`/回滚三项）+ 计数（沿既有视图的 `targetCounts`/`rollbackCounts` 映射；其余计数以 `delivery_views.go` 既有字段为准，不得自造键名） |
| `targets.list` | `orderId`、`batch?`、`status?`、`serverId?`、`page?`、`pageSize?` | `items[]`（沿既有目标视图：`serverId`/`batchNo`/`status`/`rollbackStatus?`/`error?`/`rollbackError?`/`backupPresent`/`changedFileCount`/`skippedFileCount`/`pushedAt?`/`activatedAt?`）、`total` |
| `impact.get` | `orderId`、`page?`、`pageSize?` | `summary`（沿既有影响视图：目标数/批次划分/差异文件数/总字节/预计传输字节/配置作用域命中数/`snapshotAt`）+ `targets[]`（逐台：`serverId`/在线/健康/新增覆盖删除跳过计数/命中配置作用域数） |
| `observe.get` | `orderId` | 当前批观察窗序列（沿既有视图：时间桶/健康分/健康等级/TPS/告警计数） |
| `events.list` | `orderId` | 派生事件列表（沿既有视图：阶段/时间/摘要；不含 SSE 流式语义） |

约定：

- `orderId` 的字段名统一为 `orderId`（交付域内一致；与 HTTP 面路径参数同名，便于映射）。
- `namespaceId` 必填（对齐 `beacon.topology.snapshot.get` 的 namespace 定位方式，禁止用可选参数留观测越界口子）；其余工具由 `orderId` 定位、服务端做 namespace 归属校验（跨 namespace 拒绝）。

### 3.3 组单工具契约（FR-246）

| 工具 | 输入 | 输出 | 说明 |
|---|---|---|---|
| `order.create` | `namespaceId`、`title`、`description?`、`sourceServerId?`、`scanDir?`、`selector`（JSON 对象）、`batchMode`、`batchSizes`、`activationMethod`、`observeWindowSec?`、`activateTimeoutSec?`、`failureRateThresholdPercent?`、`unhealthyRateThresholdPercent?`、`configChanges?`（`[{configScopeKind, configScopeId, configToVersionId}]`） | `{orderId, status}` | **修复点**：`Create` 此前忽略 `configChanges`（service 注释明示），本项**已使其一次成型**；无 `configChanges` 即纯文件单 |
| `order.update` | `orderId` + 可改字段子集（同 create 除去 `namespaceId`） | `{orderId, status}` | 仅 draft 可改（领域守卫不变） |
| `order.diff-scan` | `orderId` | `{add, update, delete, total, snapshotAt}` | 同步扫描；要求 draft + 有模板源（错误走 §3.5 映射） |

> **勘误（FR-246 施工期）**：上表 `configChanges` 的元素键名以 **HTTP 面真源**为准——`configScopeKind` / `configScopeId` / `configToVersionId`（`apps/server/internal/handler/delivery_admin_handler.go` 的 `changeConfigChangeInput`、contracts 的 `ConfigChangeInput`）；`configFromVersionId` 由服务端按 ADR-0071 计算、客户端携带不采信。本规格初稿写的 `configFileId` / `scopeKind` / `scopeRefId` / `toVersionId` 是拟定简写，从未落地，不得据此实现（§3.2 的「输出键名一律沿用既有 HTTP 视图字段名，不得自造键名」同样适用于入参）。

### 3.4 止损工具契约（FR-247）

| 工具 | 输入 | 输出 | 说明 |
|---|---|---|---|
| `order.pause` | `orderId` | `{orderId, status}` | rolling → paused（人工）；无原因要求（与 HTTP 面一致） |
| `order.cancel` | `orderId`、`reason`（必填） | `{orderId, status}` | rolling/paused → cancelled；原因入审计 |

### 3.5 错误理由映射（FR-248）

交付系全部 MCP 工具的拒绝路径由 `mcpRejectedResult`（硬编码文案、丢弃 err）改为 `mcpRejectedResultWithReason`（对齐 `mcp_alert_tools.go` 先例），把领域错误映射为稳定中文短语：

| 错误码 | 中文短语（返回给 AI） |
|---|---|
| `approval_reason_required` | 必须填写原因（reason） |
| `illegal_state` | 当前状态不允许该操作（附当前状态与动作） |
| `no_items` | 变更单没有任何变更项，无法提交审批 |
| `no_target` | 未解析出任何合格目标 |
| `no_rollback_target` | 单内无曾推送的目标可回滚 |
| `missing_source` | 未指定黄金模板源，无法扫描文件差异 |
| `source_invalid` | 模板源必须已确认绑定且在线的 backend 子服 |
| `source_snapshot_missing` | 模板源尚无文件资产快照，请先重扫 |
| `selector_cross_namespace` | selector 引用了不属于本环境的实体 |
| `config_version_invalid` | 配置版本不存在或与作用域不匹配 |
| `batch_not_found` | 批次不存在 |
| `resume_mode_required` | 熔断/准备失败暂停必须指定 mode 与原因 |
| `change_order_not_found` | 变更单不存在 |
| `approver_separation` | 审批人不得是创建人 |
| `not_creator` | 仅创建人可撤回变更单 |
| `FORBIDDEN` | 当前主体无权执行该操作 |
| 其他 | 沿用既有通用透出规则（脱敏摘要），不得回空文案 |

实现约束：映射表以**一处常量表**维护（与 `apperr` 的 code 对齐，测试断言每个交付错误码在表内有条目）；映射不出 `apperr` 之外的第二真源（只做 code → 中文短语的稳定映射，不复制错误语义）。

### 3.6 票据与审批视图扩字段（FR-249）

**A. 交付申请票据 `DeliveryApprovalTicketView`（HTTP 与 MCP 共用结构体）扩两字段**：

```json
{
  "approvalRequestId": "apr_xxx",
  "status": "pending",
  "operationKey": "delivery.approve",
  "orderId": 87,
  "impactSummary": { "targetCount": 20, "batchCount": 3, "payloadFiles": 3, "payloadConfigs": 0 }
}
```

- **键名注意**：HTTP 面直出结构体，既有键为 `operationKey`；MCP 侧投影（`mcpDeliveryTicketView` 手写 map）既有键为 `operation`——本项**保持两侧现状键名不变**，仅两侧同步新增 `orderId` 与 `impactSummary`（两键两侧同名）。FR-252 前端按 HTTP 键名（`operationKey`）消费。
- `orderId`：uint，恒有（六类交付申请——submit/delete/resume/confirm/rollback/rollback-finish——都归属某单）。
- `impactSummary`：**对象**，在创建申请时刻**即时查询**（targets/items/batches 计数），不写入冻结 payload、不随后续漂移；计数为 0 时保留 0 而不省略（便于 AI 稳定解析）。
- `mcpDeliveryTicketView`（`mcp_tools.go` 手写 map）必须同步补两键——不改则 MCP 侧 AI 永远看不到 `orderId`。

**B. `beacon.approvals.own.get` / `own.list` 投影扩字段**（从 `model.ApprovalRequest` 现成字段投影，无新表）：

| 字段 | 来源 | 脱敏要求 |
|---|---|---|
| `failureSummary` | `FailureSummary` / `FailureReason` | 已有脱敏语义（ADR-0057），原样透出 |
| `rejectReason` | `RejectReason` | 原样 |
| `decisionReason` | `DecisionReason` | 原样 |
| `approvedBy` | `ApprovedBy` | 主体标识（非敏感） |
| `decidedAt` / `approvedAt` / `executedAt` / `finishedAt` | 对应时间列 | 有值即返（不组装数组） |

- **不投影审批 text 列 `ImpactSummary` / `SafeSummary`**：避免与票据对象键 `impactSummary` 同名两型（对象 vs 字符串）；后续如需展示审批摘要再单列键名讨论。
- `mcpApprovalView` 现为 `own.list`/`own.get`/`own.withdraw`/`approve`/`reject` 五个工具共用投影：本项拆为**轻量档**（list）与**全量档**（get）两个投影函数；`own.withdraw`/`approve`/`reject` 沿用轻量档。
- `own.list` 保持轻量：只补 `failureSummary`（截断）与 `finishedAt`；`own.get` 回全量新增字段（各时间字段**有值即返**、不额外组装数组——AI 按字段名自行解读时序）。
- 机器主体仍只可见自己的申请（既有 `RequesterType/RequesterID` 强制注入不变）。

### 3.7 注册与门禁（对齐 FR-236/237/242 既有机制）

- 11 个新工具全部登记 `mcpToolCatalog`（含风险等级、`AutomationOnly`、`OperationKind` 留空）；无一项 `RequireApprovalDecide`、无一项 critical——生产模式门禁对 low/high 无影响。
- 注册一律走 `mcpAddTool`（fail-closed；未登记即不注册并有痕迹），执行面 guard 自动覆盖。
- 既有覆盖测试（catalog ↔ 真实注册集合双向一致、等级 ≥ descriptor、observer 集合不变、未登记留痕）自动守护新增项。**注意 `wantChecked`（现 49）为人工精确值**：本批新工具 `OperationKind` 全部留空故该值不变；若实现者给任何新工具挂了 kind，必须同步核对并更新该值（测试失败文案已提示）。
- 另补测试：只读 6 工具 observer 可见断言、写工具 observer 不可见断言（FR-245 验收）、拒绝文案断言（FR-248）、只建申请的业务表零副作用与同幂等键幂等断言（FR-246 / FR-247）。

## 4. UX / 交互

本规格为纯 MCP / 后端契约（无新页面）。前端消费方：FR-252（202 票据展示）、FR-255（审批进度视图）——契约以 §3.6 为准，前端不得自行扩展字段语义。

## 5. 任务拆分

**FR-245 / FR-249（开发完成，待真机验收）：**

- [x] 任务 A1：六个只读工具实现（投影层 + 注册 + 目录登记）
- [x] 任务 A2：只读工具测试（observer/automation 可见性、投影有界性、跨 namespace 拒绝）
- [x] 任务 A3：文档同步（FR-245 专属：`docs/API.md` MCP 工具清单补六个只读工具；`docs/specs/mcp-tool-risk-grading.md` §3.3 清单与合计（81 → 92 项中 +6 low）同步；PRD FR-245 行状态翻「开发中」；CHANGELOG 末尾追加）
- [x] 任务 B1：`DeliveryApprovalTicketView` 扩 `orderId` + `impactSummary`（六类申请点接入 + `mcpDeliveryTicketView` 同步补键）
- [x] 任务 B2：`own.get/list` 投影扩字段（`mcpApprovalView` 拆分轻量/全量档 + 字段投影）
- [x] 任务 B3：票据与审批视图测试（票据字段、视图字段、机器主体隔离不变）
- [x] 任务 B4：文档同步（FR-249 专属：`docs/API.md` 票据/审批视图字段段——与 FR-245 的工具清单段不重叠；PRD FR-249 行状态；CHANGELOG 末尾追加）

**文件边界（并行防冲突）**：

- **A（FR-245）改**：`apps/server/internal/server/mcp_read_tools.go`（新增只读注册段）+ `mcp_tool_catalog.go`（登记 6 条）+ 对应测试；文档：`API.md` 工具清单段 + `mcp-tool-risk-grading.md`。
- **B（FR-249）改**：`apps/server/internal/server/mcp_tools.go`（`mcpApprovalView` 拆分与扩字段、`mcpDeliveryTicketView` 补键）+ `delivery_order_service.go` / `delivery_dangerous_approval.go`（ticket 结构体与六类申请点）+ 对应测试；文档：`API.md` 票据/审批视图字段段。
- 共享文件**无重叠**（catalog 仅 A 改、mcp_tools.go 仅 B 改）；PRD 各改自己那行、CHANGELOG 各自末尾追加。

**FR-246 / FR-247 / FR-248（开发完成，待真机验收）：**

- [x] 任务 D：组单三工具（含 `Create` 支持 `configChanges`）
- [x] 任务 E：止损两工具
- [x] 任务 F：错误映射表 + 全交付系工具接入
- [x] 任务 G：交付写工具测试（只建申请的业务表零副作用断言、同幂等键幂等断言、逐错误码拒绝文案断言）+ `API.md` 更新 + `mcp-tool-risk-grading.md` §3.3 合计二次同步（92 项 = critical 10 + high 48 + low 34）

## 6. 验收标准

- **FR-245**：MCP `tools/list`（automation）可见六个只读工具，**observer 同样可见**；实调 `order.list`→`order.get`→`targets.list`→`impact.get`→`observe.get`→`events.list` 全链路可读且返回有界；跨 namespace `orderId` 被拒；真机 MCP 客户端实测通过。
- **FR-249**：任意交付申请创建响应含 `orderId` 与 `impactSummary`（HTTP 与 MCP 两侧同名键均可见）；`own.get` 可见失败原因/审批主体/时间线；`own.list` 保持轻量；机器主体只见自己的申请（负向）；真机实测。
- **FR-246 / FR-247 / FR-248**：AI 可建单（含配置变更一次成型）、编辑、扫描；可暂停/终止且落强审计；被拒调用返回 §3.5 表中的稳定短语；单测逐错误码断言。
- 共同硬闸：`go test ./...` 全绿 + `mcp_tool_catalog_test` 覆盖测试通过 + 真机维度真机过（无真机环境时如实标"待真机验"，不冒充完成）。

## 7. 风险 / 待定

- **只读返回体规模**：`order.get` 的计数与 `impact.get` 的逐目标字段需按既有 view 裁剪，防止大单（1000+ 目标）返回体过大——实现时以分页 + 计数为界，禁止全量展开。
- **风险定级可复核**：组单 3 项定 low 的依据是"draft 无生产副作用 + 领域守卫限界"；若评审认为应升 high，仅需改 catalog 一行（不影响实现）。
- **`impactSummary` 实现路径**：六类申请点的冻结 payload 现仅含 orderId/expectedStatus/snapshotHash 等定位字段（**无计数**）——需在各申请点**新增计数查询**（targets/items/batches 计数）并随申请响应返回；**不写入冻结 payload**（票据是创建时刻的即时读数，无需冻结语义）；该键仅用于票据对象，与审批 request 的 text 列不重名（见 §3.6 B）。
- **跨批文件热点（已化解）**：FR-245 / FR-249 的 A/B 文件边界无重叠（见 §5）；FR-246 / FR-247 与它们在 `mcp_tool_catalog.go` 有交集（新增工具需登记），已按「先 rebase 前序结果再开工」的做法施工，无冲突。
