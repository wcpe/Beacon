# ADR-0064：告警事件处理工作流（status/ack/resolve）与健康 activeAlerts 因子接真

**状态**：已接受（随 P5（FR-157）落地）

## 背景

第二版告警事件域此前"半接真"，存在三处漂移与断链：

1. **真后端 append-only、无处理状态**：`alert_event` 表（[ADR-0041](0041-alert-event-persistence.md) 落地）只有 `id/type/level/serverId/namespace/message/detail/createdAt`，是发生即留痕的历史表，无 `status` / ack / resolve 概念；`AlertEventService` 只有 `Record` / `List`，`GET /admin/v1/alert-events` 纯只读历史。
2. **前端 mock 契约是超集**：`packages/contracts` 的 `AlertEventItem` 含 `status`(open/acknowledged/resolved) / `handledBy` / `handledAt` / `handleNote`，devmock 提供 `/handle` 写闭环，`/dashboard` 告警卡按 `status==='open'` 过滤未处理告警——真后端全无对应，切真即字段恒 `undefined`、过滤全灭。
3. **健康公式 `activeAlerts` 因子无真值来源**：[v2-metrics-health-scheduling.md](../specs/v2-metrics-health-scheduling.md) §4.4 定义健康分含 `alert` 因子（`100 − activeAlerts × alertPenalty`），P4 期 `activeAlerts` 恒 0，规格指派"由 P5 告警事件域供给真值"。但"活跃告警"在 append-only 无状态模型下**无法定义**（只有发生时刻、无关闭时刻）。

需在 P5 拍板告警事件域怎么落。两条路：**收窄契约到只读**，或**补最小处理工作流**。经确认取后者——补齐真后端处理工作流，使"活跃 = 未处理"有明确语义、并保住 mock 已展示的处理能力闭环。

## 决策

1. **`alert_event` 表加处理状态列**：新增 `status`（`open` / `acknowledged` / `resolved`，落 `VARCHAR` + 应用层校验，遵 [architecture-invariants](../../.claude/rules/architecture-invariants.md) §4 禁 ENUM）、`handled_by`（处理人，`VARCHAR`）、`handled_at`（`DATETIME`，NULL 未处理）、`handle_note`（处理说明，`VARCHAR`）。新告警插入即 `status=open`。GORM Migrator 加列，向后兼容。
2. **存量历史行迁移为终态**：既有 append-only 历史行在迁移时回填 `status=resolved`（属过去已闭事件，不计入当前活跃），避免存量把 `activeAlerts` 撑爆。
3. **新增处理端点**：`POST /admin/v1/alert-events/{id}/handle`（挂既有 `/admin/v1` 告警面——列表 `GET /admin/v1/alert-events` 与前端消费均在 v1，处理端点同面不拆），入参 `{status: acknowledged|resolved, note?}`（对齐前端已锚定的 `HandleAlertBody`；等价措辞 `{action, handleNote}` 亦兼容归一），更新 status / handledBy（取登录身份）/ handledAt / handleNote，**写审计**（含操作者 / 事件 id / 动作 / 原因），错误经 `render.WriteError` 脱敏（[ADR-0057](0057-surface-desensitized-errors.md)）。
4. **`activeAlerts` 接真 = 当前 `status=open` 计数**：健康计算轮（`health_compute_service`）**在轮次开始一次性批量**取各实例的 open 告警计数（`namespace + serverId → open 数`），再按 key 注入 `HealthFactorInputs.ActiveAlerts`——**禁在逐实例循环里查库**（[testing-and-quality](../../.claude/rules/testing-and-quality.md) §3 / 规则 §17）。
5. **契约保留 status 族字段**：`AlertEventItem` 的 `status` / `handledBy` / `handledAt` / `handleNote` 由真后端补齐，消除 mock 超集漂移；`/alert-events` 页与 `/dashboard` 告警卡的处理 UI 从 mock 平移接真。

## 理由

- **"活跃 = 未处理"语义清晰**：`activeAlerts` 有明确、可解释的口径（当前 open 的告警数），比"近时间窗事件数"近似更契合健康分"当前有多少未处置告警"的直觉，也可回放解释。
- **完成已展示能力的闭环，非镀金**：处理工作流的 UI 已在第二版 mock 拍板展示、`/dashboard` 已按其过滤，补齐真后端是"让已展示的能力真正可用"，且属运维可观测闭环（审计 + 处置留痕），不涉游戏逻辑、不越 MVP 边界。
- **防契约漂移**：真后端补齐 status 族后，`AlertEventItem` 契约不再是 mock 超集，devmock 的 `satisfies` 与真响应一致。

## 后果

- `alert_event` schema 演进（加 4 列，向后兼容；存量回填 `resolved`）。承接 [ADR-0041](0041-alert-event-persistence.md) 的持久化表，不取代它——本 ADR 只加处理状态维度。
- v1 告警扫描器（`HealthScanner` → `PersistAlerter`）继续写入 `alert_event`，新行默认 `status=open`；健康分因此对未处理告警敏感（未处置越多分越低），促使运维处置。
- `/alert-events` 页从 mock 切真时保留 ack / resolve 处理 UI；`/dashboard` `AlertOverview` 的 `status==='open'` 过滤对真数据生效。
- 新增管理面写端点 `/handle`，纳入统一 `adminAuth → readonlyWriteGuard → auditWrite` 中间件链与审计。

## 备选方案

- **收窄契约到真后端 8 只读字段（去 status 族与 /handle 写闭环）**：改动最小、最守 YAGNI，但失去 mock 已展示的告警处理能力，`/dashboard` 告警卡退化为"近窗告警"，`activeAlerts` 只能用时间窗近似、语义模糊。**否决**（取"补工作流"以求 activeAlerts 语义清晰 + 保住已展示闭环）。
- **`activeAlerts` 用"近 N 分钟事件数"近似、不改表**：无需 schema 演进，但"活跃"随时间窗漂移、不可处置（运维无法通过处理告警降低活跃数），与"未处理"直觉不符。**否决**。
- **新建独立 `alert_state` 表关联 alert_event**：多一张表与联表查询，处理状态与事件是一对一强绑定，加列比拆表简单。**否决**（简单优先）。
