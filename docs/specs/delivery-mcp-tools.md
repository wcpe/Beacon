# 功能规格：交付 MCP 工具集与审批面

> 状态：草拟（本波施工 FR-245 / FR-249；FR-246/247/248 由第二波按本规格施工）
> 关联 PRD：FR-245、FR-246、FR-247、FR-248、FR-249
> 分支：feature/fr-245-mcp-read-tools、feature/fr-249-approval-surface（第二波另开）
> 决策依据：FR-220 / FR-236 / FR-237 / FR-242 既有 MCP 架构；本规格不引入新 ADR

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
- 顺带修复（FR-246 范围内）：`Create` 忽略 `configChanges` 导致组单必须"先建后改"两段式。

不做（范围外）：

- 机器审批决定（归 FR-223 的 `allow-approval-decide` 开关，本规格不碰）。
- 回滚类工具（已有 `order.rollback` / `rollback.finish`；目标级回滚见 FR-270 的独立规格）。
- 交付数据面（blob / manifest / 回执）的 MCP 暴露——那是 agent 面，不是操作者面。
- 前端消费（202 展示、审批进度视图）归 FR-252 / FR-255，本规格只冻结契约。

## 3. 设计

### 3.1 工具目录（11 个新增，全部 `AutomationOnly`）

| 工具名 | 语义 | 风险 | 写语义 | 波次 |
|---|---|---|---|---|
| `beacon.delivery.order.list` | 变更单列表（分页筛选） | low | 只读 | 本波 |
| `beacon.delivery.order.get` | 变更单详情（含计数摘要） | low | 只读 | 本波 |
| `beacon.delivery.order.targets.list` | 目标分页（含逐台状态/失败原因/备份标记） | low | 只读 | 本波 |
| `beacon.delivery.order.impact.get` | 影响预览（汇总+逐目标） | low | 只读 | 本波 |
| `beacon.delivery.order.observe.get` | 当前批观察窗序列 | low | 只读 | 本波 |
| `beacon.delivery.order.events.list` | 进度事件（有界条数） | low | 只读 | 本波 |
| `beacon.delivery.order.create` | 建 draft（含 configChanges 一次成型） | low | direct + 审计 | 第二波 |
| `beacon.delivery.order.update` | 编辑 draft | low | direct + 审计 | 第二波 |
| `beacon.delivery.order.diff-scan` | 同步重扫差异 | low | direct + 审计 | 第二波 |
| `beacon.delivery.order.pause` | 人工暂停（止损） | high | direct + 强审计 | 第二波 |
| `beacon.delivery.order.cancel` | 紧急终止（止损，原因必填） | high | direct + 强审计 | 第二波 |

定级依据（对齐既有先例）：

- **只读 6 项 = low**：无副作用；observer 与 automation 共用（对齐既有只读段与 `beacon.topology.snapshot.get` 等）。
- **组单 3 项 = low**：均为 draft 阶段操作，无生产副作用；领域守卫已限界（draft 状态机、selector 跨 namespace 拒绝、模板源合格性校验）——对齐"拓扑建树 low"先例；对应 HTTP 路由为 `management.direct` 档。
- **止损 2 项 = high**：直执改变生产状态（暂停/终止进行中的灰度），但可逆（可 resume / 可回滚）——对齐"告警处置 high 直执"先例；对应 HTTP 路由为 `management.direct` 档；无审批票据（OperationKind 留空，与 ALERT handle 同理）。cancel 的 `reason` 必填（与 HTTP 面一致）。
- 全部 `AutomationOnly: true`（observer 不可见写工具）；无一项为 critical（回滚才是 critical，已有）。

### 3.2 只读工具契约（FR-245）

**统则**：返回体一律为**有界投影**——列表类强制分页（`page`/`pageSize`，pageSize 上限沿用域内既有上限 100）；详情类只回摘要与计数，不回传 items 全量文件清单与批/目标全量明细（那些走 `targets`/`impact` 分页拉取）；事件/观察窗回有界条数（沿用 HTTP 端点的有界行为，不新增上限语义）。

| 工具 | 输入 | 输出（投影字段） |
|---|---|---|
| `order.list` | `namespaceId`（必填）、`status?`、`createdBy?`、`keyword?`、`page?`、`pageSize?` | `items[]`（`id`/`title`/`status`/`pauseKind?`/`createdBy`/`createdAt`/`payloadState`）、`total`、`page` |
| `order.get` | `orderId` | 单字段摘要（`id`/`title`/`description?`/`namespaceId`/`status`/`pauseKind?`/`pauseReason?`/`selector` 摘要/`batchMode`/`batchSizes`/`activationMethod`/`observeWindowSec`/`activateTimeoutSec`/阈值两项/`payloadState`/`createdBy`/`submittedAt?`/`approvedAt?`/`startedAt?`/`finishedAt?`/`cancelReason?`/`rollback*` 三项）+ `counts`（`itemFiles`/`itemConfigs`/`batchTotal`/`batchDone`/`targetTotal`/`targetActivated`/`targetFailed`） |
| `order.targets.list` | `orderId`、`batch?`、`status?`、`serverId?`、`page?`、`pageSize?` | `items[]`（`serverId`/`batchNo`/`status`/`rollbackStatus?`/`error?`/`rollbackError?`/`backupPresent`/`changedFileCount`/`skippedFileCount`/`pushedAt?`/`activatedAt?`）、`total` |
| `order.impact.get` | `orderId`、`page?`、`pageSize?` | `summary`（目标数/批次划分/差异文件数/总字节/预计传输字节/配置作用域命中数/`snapshotAt`）+ `targets[]`（逐台：`serverId`/在线/健康/新增覆盖删除跳过计数/命中配置作用域数） |
| `order.observe.get` | `orderId` | 当前批观察窗序列（每点：时间桶/健康分/健康等级/TPS/告警计数，沿既有有界行为） |
| `order.events.list` | `orderId` | 派生事件列表（阶段/时间/摘要，沿既有有界行为；不含 SSE 流式语义） |

约定：

- `orderId` 的字段名统一为 `orderId`（交付域内一致；与 HTTP 面路径参数同名，便于映射）。
- 工具名统一遵循既有只读工具规范 `beacon.<域>.<资源>.<动词>`（先例：`beacon.metrics.health.list`、`beacon.topology.snapshot.get`），一律以动词结尾。
- `namespaceId` 必填（对齐 `beacon.topology.snapshot.get` 的 namespace 定位方式，禁止用可选参数留观测越界口子）；其余工具由 `orderId` 定位、服务端做 namespace 归属校验（跨 namespace 拒绝）。

### 3.3 组单工具契约（FR-246，第二波）

| 工具 | 输入 | 输出 | 说明 |
|---|---|---|---|
| `order.create` | `namespaceId`、`title`、`description?`、`sourceServerId?`、`scanDir?`、`selector`（JSON 对象）、`batchMode`、`batchSizes`、`activationMethod`、`observeWindowSec?`、`activateTimeoutSec?`、`failureRateThresholdPercent?`、`unhealthyRateThresholdPercent?`、`configChanges?`（`[{configFileId, scopeKind, scopeRefId, toVersionId}]`） | `{orderId, status}` | **修复点**：`Create` 现忽略 `configChanges`（service 注释明示），本项使其一次成型；无 `configChanges` 即纯文件单 |
| `order.update` | `orderId` + 可改字段子集（同 create 除去 `namespaceId`） | `{orderId, status}` | 仅 draft 可改（领域守卫不变） |
| `order.diff-scan` | `orderId` | `{add, update, delete, total, snapshotAt}` | 同步扫描；要求 draft + 有模板源（错误走 §3.5 映射） |

### 3.4 止损工具契约（FR-247，第二波）

| 工具 | 输入 | 输出 | 说明 |
|---|---|---|---|
| `order.pause` | `orderId` | `{orderId, status}` | rolling → paused（人工）；无原因要求（与 HTTP 面一致） |
| `order.cancel` | `orderId`、`reason`（必填） | `{orderId, status}` | rolling/paused → cancelled；原因入审计 |

### 3.5 错误理由映射（FR-248，第二波）

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
| `forbidden` | 当前主体无权执行该操作 |
| 其他 | 沿用既有通用透出规则（脱敏摘要），不得回空文案 |

实现约束：映射表以**一处常量表**维护（与 `apperr` 的 code 对齐，测试断言每个交付错误码在表内有条目）；映射不出 `apperr` 之外的第二真源（只做 code → 中文短语的稳定映射，不复制错误语义）。

### 3.6 票据与审批视图扩字段（FR-249，本波）

**A. 交付申请票据 `DeliveryApprovalTicketView`（HTTP 与 MCP 共用视图）扩两字段**：

```json
{
  "approvalRequestId": "apr_xxx",
  "status": "pending",
  "operation": "delivery.approve",
  "orderId": 87,
  "impactSummary": { "targetCount": 20, "batchCount": 3, "payloadFiles": 3, "payloadConfigs": 0 }
}
```

- `orderId`：uint，恒有（六类交付申请——submit/delete/resume/confirm/rollback/rollback-finish——都归属某单）。
- `impactSummary`：在**创建申请时刻**计算的冻结摘要（不随后续漂移）；字段缺省语义：目标/批次/载荷计数为 0 时保留 0 而不省略（便于 AI 稳定解析）。
- 该扩展同时服务 HTTP 面（FR-252 前端 202 展示消费）。

**B. `beacon.approvals.own.get` / `own.list` 投影扩字段**（从 `model.ApprovalRequest` 现成字段投影，无新表）：

| 字段 | 来源 | 脱敏要求 |
|---|---|---|
| `failureSummary` | `FailureSummary` / `FailureReason` | 已有脱敏语义（ADR-0057），原样透出 |
| `rejectReason` | `RejectReason` | 原样 |
| `decisionReason` | `DecisionReason` | 原样 |
| `approvedBy` | `ApprovedBy` | 主体标识（非敏感） |
| `decidedAt` / `approvedAt` / `executedAt` / `finishedAt` | 对应时间列 | 有值即返（不组装数组） |
| `impactSummary` / `safeSummary` | 对应列 | 原样（受理时已脱敏） |

- `own.list` 保持列表轻量：只补 `failureSummary`（截断）与 `finishedAt`；`own.get` 回全量新增字段（各时间字段**有值即返**、不额外组装数组——AI 按字段名自行解读时序）。
- 机器主体仍只可见自己的申请（既有 `RequesterType/RequesterID` 强制注入不变）。

### 3.7 注册与门禁（对齐 FR-236/237/242 既有机制）

- 11 个新工具全部登记 `mcpToolCatalog`（含风险等级与 `AutomationOnly`）；无一项 `RequireApprovalDecide`、无一项 critical——生产模式门禁对 low/high 无影响。
- 注册一律走 `mcpAddTool`（fail-closed；未登记即不注册并有痕迹），执行面 guard 自动覆盖。
- 既有覆盖测试（catalog ↔ 真实注册集合双向一致、等级 ≥ descriptor、observer 集合不变）自动守护新增项；本波另补：只读 6 工具 observer 可见断言、写工具 observer 不可见断言（FR-245 验收）、拒绝文案断言（FR-248）。

## 4. UX / 交互

本规格为纯 MCP / 后端契约（无新页面）。前端消费方：FR-252（202 票据展示）、FR-255（审批进度视图）——契约以 §3.6 为准，前端不得自行扩展字段语义。

## 5. 任务拆分

**本波（FR-245 / FR-249）**：

- [ ] 任务 A1：六个只读工具实现（投影层 + 注册 + 目录登记）
- [ ] 任务 A2：只读工具测试（observer/automation 可见性、投影有界性、跨 namespace 拒绝）
- [ ] 任务 A3：文档同步（FR-245 专属：`docs/API.md` MCP 工具清单补六个只读工具；PRD FR-245 行状态翻「开发中」；CHANGELOG 末尾追加）
- [ ] 任务 B1：`DeliveryApprovalTicketView` 扩 `orderId` + `impactSummary`（六类申请点接入）
- [ ] 任务 B2：`own.get/list` 投影扩字段（字段投影 + list 轻量策略）
- [ ] 任务 B3：本波测试（票据字段、视图字段、机器主体隔离不变）
- [ ] 任务 B4：文档同步（FR-249 专属：`docs/API.md` 票据/审批视图字段段——与 FR-245 的工具清单段不重叠；PRD FR-249 行状态；CHANGELOG 末尾追加）

**第二波（FR-246 / FR-247 / FR-248）**：

- [ ] 任务 D：组单三工具（含 `Create` 支持 `configChanges`）
- [ ] 任务 E：止损两工具
- [ ] 任务 F：错误映射表 + 全交付系工具接入
- [ ] 任务 G：第二波测试（零副作用断言、幂等断言、拒绝文案断言）+ API.md 更新

## 6. 验收标准

- **FR-245**：MCP `tools/list`（automation）可见六个只读工具，observer 同样可见；实调 `order.list`→`get`→`targets`→`impact`→`observe`→`events` 全链路可读且返回有界；跨 namespace `orderId` 被拒；真机 MCP 客户端实测通过。
- **FR-249**：任意交付申请创建响应含 `orderId` 与 `impactSummary`；`own.get` 可见失败原因/审批主体/时间线；`own.list` 保持轻量；机器主体只见自己的申请（负向）；真机实测。
- **FR-246/247/248（第二波）**：AI 可建单（含配置变更一次成型）、编辑、扫描；可暂停/终止且落强审计；被拒调用返回 §3.5 表中的稳定短语；单测逐错误码断言。
- 共同硬闸：`go test ./...` 全绿 + `mcp_tool_catalog_test` 覆盖测试通过 + 真机维度真机过（无真机环境时如实标"待真机验"，不冒充完成）。

## 7. 风险 / 待定

- **只读返回体规模**：`order.get` 的 `counts` 与 `impact` 的逐目标字段需按现有 view 裁剪，防止大单（1000+ 目标）返回体过大——实现时以分页 + 计数为界，禁止全量展开。
- **风险定级可复核**：组单 3 项定 low 的依据是"draft 无生产副作用 + 领域守卫限界"；若评审认为应升 high，仅需改 catalog 一行（不影响实现）。
- **`impactSummary` 冻结语义**：以"创建申请时刻"快照为准，实现复用申请冻结 payload 机制，不新增计算路径。
- **本波与第二波的文件热点**：`mcp_tools.go` 与 `mcp_tool_catalog.go` 两波都改——第二波开工前先 rebase 第一波结果。
