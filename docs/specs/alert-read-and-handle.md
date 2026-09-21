# 功能规格：告警批量处理（已读 = acknowledged / 已处理 = resolved，两态）

> 状态：已实现（单测通过；真机验收待做）　·　关联 PRD：FR-229（增强 FR-157 / FR-89）　·　分支：feature/alert-batch-handle

## 1. 背景与目标

> ⚠️ 本 spec 已按评审重写。原稿误以为「告警是进程内环形缓存、ADR-0019 不落库、重启清零、无任何标记处理手段」，**前提已过期**。

现状（已交付）：

- **告警已落库**：`alert_event` 表（FR-89 / **ADR-0041**，`apps/server/internal/model/alert_event.go`），ADR-0041 已取代 ADR-0019「告警不落库」那一条。
- **已有处理工作流**：`status`(`open`/`acknowledged`/`resolved`) + `handled_by` / `handled_at` / `handle_note`（FR-157 / **ADR-0064**）。
- **已有端点**：`GET /admin/v1/alert-events`（筛选 `type` / `level` / `from` / `to` / `page` / `size`）、`POST /admin/v1/alert-events/{id}/handle`（`status=acknowledged|resolved` 或 `action=acknowledge|resolve` + `note`）。
- **已有前端**：`apps/web/src/pages/alert-events.tsx` 已实现级别 / 类型 / 状态筛选、勾选 `open` 行、批量确认 + 批量已处理、详情面板处置。

因此本 FR **不新造状态机、不新落库**，只补三处缺口。

## 2. 需求（要什么）

1. **术语对齐（**已定，见 §7**）**：**保留既有三态** `open` / `acknowledged` / `resolved`，明确映射为——**「已读」= `acknowledged`**（已认领 / 已看过）、**「已处理」= `resolved`**（闭环）。「批量已读」落 `acknowledged`、「批量已处理」落 `resolved`；**不新增第四态**，**不改** FR-157 已交付的三态 UI。
2. **「按当前筛选一键」**：现有批量只作用于**当前页勾选行**；需支持**按筛选条件作用于整个结果集**（如"所有 `level=critical` 且 `open`"）。
3. **补审计**：批量操作落**一条**审计（含筛选条件、命中条数、操作者），不逐条刷屏。

范围内：术语对齐、按筛选批量、审计。
不做（范围外）：新状态机；新落库（已有）；通知渠道。

## 3. 设计（怎么做）

- **复用既有模型 / 端点**，仅**新增批量端点**（现状无批量）：
  - `POST /admin/v1/alert-events/handle`，body：`{ filter: {type?, level?, status?, from?, to?}, status: acknowledged|resolved, note? }`（或对既有端点加 `?all=true` + 筛选参数，**待实现时定**）。
  - 返回 `{ affected: <命中条数> }`。
- **服务层**：`AlertEventService.HandleBatch(filter, status, note, operator)`——一条 `UPDATE ... WHERE <filter> AND status='open'` 语义，写 `handled_by/handled_at/handle_note`。
- **guard**：复用 FR-213 观测范围契约（筛选结果不越出当前观测范围）。
- **审计**：写 `alert_event.batch_handled`（条件 + 命中数 + 操作者）。
- **幂等**：重复批量对已非 `open` 的条目无副作用。

## 4. UX / 交互

- 用户任务：把"当前筛选出的这类告警"整批处理掉，而不是翻页逐条勾。
- 进入路径：`/alert-events` 顶部——筛选后出现「处理当前筛选（N 条）」。
- 操作闭环：筛选 `level=critical` → 提示命中 N 条 → 点「标记已处理」→ 全量变更 → 待办数下降 → 审计可查。
- 状态设计：命中 0 条 → 按钮禁用；命中量大（如 >100）→ 二次确认显式写出条数；术语在按钮文案与状态徽标上统一（不再「已读」与「已处理」混用）。
- IA 挂载：可观测域 `/alert-events`；不新增页面。

## 5. 任务拆分

- [x] 术语收敛方案定稿（`acknowledged` / `resolved` 与"已读 / 已处理"的对应）
- [x] 批量处理端点（按筛选，非仅当前页）+ 服务层
- [x] 观测范围 guard 接入
- [x] 批量审计
- [x] 前端：筛选级批量入口 + 二次确认 + 文案统一
- [x] 文档同步：PRD 状态、API、ADR-0064 引用、CHANGELOG

## 6. 验收标准

- 筛选 `level=critical` 后一键处理，**跨页**全部命中条目变为目标状态（非仅当前页）。
- 命中条数与实际变更数一致；重复执行幂等。
- 落一条批量审计（条件 + 命中数 + 操作者）。
- 术语统一：UI 上"已读/已处理"不再指代不同状态。
- 重启后状态保留（已落库，天然满足）。

## 7. 已定 / 待定

- **术语取舍（已定，方案 A）**：保留三态，**「已读」= `acknowledged`**（已认领 / 看过）、**「已处理」= `resolved`**（闭环）；「按筛选一键已读」落 `acknowledged`、「一键已处理」落 `resolved`。**不改现有 UI**，只补"按筛选跨页批量"。
  - （备选方案 B——收敛为单态、把 `acknowledged` 从界面撤下、已读即已处理——**未采用**，代价是要动 FR-157 已交付的三态 UI。）
- **批量端点形态**：新端 `POST /alert-events/handle` vs 既有端加 `?all=true`——实现时定。
- **大结果集**：命中过万时是否分批 + 进度反馈——待实现时评估。
- **与 FR-232 对齐**：自动消解落 `resolved` 且 `handled_by=system`，使 UI 能区分「系统自动消解」vs「人工已处理」。
