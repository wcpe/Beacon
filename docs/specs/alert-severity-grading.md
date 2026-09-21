# 功能规格：告警分级与人工升降

> 状态：已实现（单测通过；真机验收待做）　·　关联 PRD：FR-231（增强 FR-157 / FR-89）　·　分支：feature/alert-severity-grading

## 1. 背景与目标

> ⚠️ 已按评审重写。原稿问"严重级是否需持久化"——**该问题不存在**：`alert_event.level`（`info`/`warning`/`critical`）**已存在且已落库**。

现状：告警已落库（FR-89 / ADR-0041），`level` 列在、页面已可按级别筛选。缺的是：**按「级别 + 角色」自动定级**（如今所有 `offline` 一视同仁），以及**人工覆盖**。

本 FR = 「判定矩阵 + 角色解析 + 自动定级 + 人工覆盖列」。

## 2. 需求（要什么）

- **自动定级**：按 **健康级别 × 角色** 矩阵给 `alert_event.level` 赋值（**矩阵见 §3，用户已给默认**）。
- **角色解析**：`proxy`（`kind=proxy`）/ `lobby`（**`lobby_cluster_id != NULL`**，与 ADR-0075 一致）/ `backend`（其余）——**不设 entry 角色**（入口服即大厅成员，见 ADR-0083）。
- **人工覆盖**：可手动升降某条的级别；新增 `severity_override` / `overridden_by` / `overridden_at` 列，改动**落审计**。
- 列表可按级别**排序 / 筛选**（筛选已有）。

范围内：判定矩阵、角色解析、自动定级、人工覆盖列与审计。
不做（范围外）：连续 N 次自动升级；通知渠道分级路由。

## 3. 设计（怎么做）

- **判定矩阵（用户已确认默认；**已去 `entry` 列**）**：

| 健康级别 | proxy / lobby | backend |
|---|---|---|
| `offline` | `critical` | `warning` |
| `lost` | `critical` | `warning` |
| `degraded` | `warning` | `info` |

> 说明：因 **ADR-0083** 定「入口服 = 大厅成员」（且 `is_entry` 本期不做），`entry` 角色与 `lobby` 完全重合，故**不设 entry 列**。

- **纯函数** `GradeAlert(healthLevel, role, override *string) string`：无副作用、确定性、**穷举单测**；命中 `override` 时以人工值为准。
- **角色解析**：`kind=proxy` → proxy；**`lobby_cluster_id != NULL` → lobby**（与 ADR-0075 一致）；其余 → backend。
- **写入**：告警产生时算级别写入 `alert_event.level`。
- **人工覆盖**：`POST /admin/v1/alert-events/{id}/level`，body `{level, note?}`；写 `severity_override` + `overridden_by` + `overridden_at`；审计 `alert_event.level_overridden`。
- **与 FR-232 交互**：合并计数时级别取**最高级**（用户默认）。

## 4. UX / 交互

- 用户任务：先看高危，再看普通；必要时手动调级。
- 进入路径：`/alert-events` 级别筛选 / 排序（既有）；详情面板「调整级别」（新增）。
- 操作闭环：筛 `critical` → 只见 proxy/入口/大厅高危 → 对误判手动降级 → 审计可查。
- 状态设计：级别 **色 + 文字双编码**；人工覆盖条目带"已手动调整"标记（可悬停看覆盖人/时间）。
- IA 挂载：可观测域 `/alert-events`；运维总览告警卡按级别排序。

## 5. 任务拆分

- [x] `GradeAlert` 纯函数 + 矩阵 + 穷举单测
- [x] 角色解析（proxy / lobby / backend）
- [x] 告警产生路径写入 `level`
- [x] `severity_override` 等列 + 迁移 + 覆盖接口 + 审计
- [x] 前端：级别徽标 / 覆盖标记 / 调级入口
- [x] 文档同步：PRD 状态、API、CHANGELOG

## 6. 验收标准

- 矩阵穷举单测全通：`offline` 的 proxy/入口/大厅 → `critical`，普通 backend → `warning`；`degraded` 的 proxy/入口 → `warning`、其余 → `info`。
- 人工改级后列表排序 / 筛选随之变化，且 `severity_override/by/at` 与审计可查。
- 级别展示色 + 文字双编码（色盲可用）。
- 无角色信息时安全降级为 backend 规则。
- FR-232 合并计数时取最高级。

## 7. 风险 / 待定

- ~~持久化~~ → **已修正**：`level` 已落库；人工覆盖新增列即可。
- **矩阵微调**：上表为默认，评审后如需调整（如 `lost` 与 `offline` 区分）以拍板为准。
- ~~依赖 FR-225 的 `entry` 角色~~ → **已去 entry 维度**（ADR-0083：入口服 = 大厅成员）。
