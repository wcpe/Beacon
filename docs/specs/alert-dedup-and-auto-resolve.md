# 功能规格：告警收敛与防堆积

> 状态：已实现（单测通过；真机验收待做）　·　关联 PRD：FR-232（增强 FR-89 / FR-157）　·　分支：feature/alert-dedup-and-auto-resolve

## 1. 背景与目标

> ⚠️ 已按评审重写。原稿以「沿用 **ADR-0019** 告警不落库、内存侧合并」为前提——**该前提已废**：**ADR-0041** 已取代 ADR-0019「告警不落库」，告警已落 `alert_event` 表（FR-89），并有处理工作流（FR-157 / ADR-0064）。

问题依旧存在（现象）：实例抖动 / 反复启停时，`alert_event` 表**每次触发都插一行**，同类告警堆积上千条，待办清不完。**收敛应在 `alert_event` 上做**，而非内存。

本 FR = 「同类合并计数 + 恢复自动 resolve（更新 `status`）」。

## 2. 需求（要什么）

- **同类合并计数**：同一 `(serverId, type)` 在**未恢复期间只保留 1 行**，重复触发只**计数 +1**、刷新 `last_at`（+ 可能刷新 `level` 为最高级，见 FR-231），**不再插新行**。
- **恢复自动 resolve**：实例回到 `online` 时，其未恢复告警自动把 `status` 置为 `resolved`（复用 FR-157 的状态机与 `handled*` 语义）。
- **已处理不重开**：已被人工处理（非 `open`）的条目再次触发时**不自动回退为 `open`**，只更新计数 / 时间。
- 待办数**不随触发次数线性增长**。

范围内：`alert_event` 侧收敛（合并 / 自动 resolve / 状态保持）。
不做（范围外）：通知渠道；历史统计报表（可另立 FR）。

## 3. 设计（怎么做）

- **收敛键**：`(server_id, type)`（无 `serverId` 的集群级用 `namespace_id + type`）。
  - ⚠️ **方向丢失风险**：现网 `type` 恒为 `health-transition`，则本键 ≈ **每服一行**——会把一次抖动里的 `lost → offline → 恢复` 并成同一行、**丢失异常方向**。若需保留方向，建议键加"目标态"维度（如 `(server_id, type, to_status)`）。**实现时定。**
- **表结构调整**：`alert_event` 增加 `occurrence_count`（默认 1）与 `last_at`；`created_at` 保留为首发时间。
- **写入路径**：告警产生时先按收敛键查**未恢复行**（`status != 'resolved'`）：
  - 命中 → `occurrence_count += 1`、`last_at = now`、（按 FR-231）`level = max(level, 新级)`；
  - 未命中 → 插新行。
- **自动 resolve 触发点**：实例由**非 online → online**（心跳续上 / 重新注册）时，把其未恢复行 `status='resolved'`，且 **`handled_by = system`**、`handle_note` 记"实例恢复自动消解"——使 UI 能区分「系统自动消解」vs「人工已处理」（与 FR-229 §7 对齐）。
  - ⚠️ **实现修正**：恢复 online 由 `registry.Heartbeat` / `registry.Register` **直接置位**，**不经**健康扫描的 `SweepExpired` 输出（`healthByAge` 默认返回 `current`、永不产出 online）。故触发挂在 **`InstanceService.Register` / `Heartbeat`**（真正的恢复写点，写前读旧状态判迁移），**不在**健康扫描循环内——否则该分支在生产永不可达（一条只测分支、不测接线的测试会假绿）。
- **状态保持**：`acknowledged` 行再次触发仅更新计数 / 时间，**不回退 `open`**。
- **审计**：自动 resolve 与合并是否逐条审计——建议**不逐条**（量大）；可选按周期汇总（**待实现时定**）。

## 4. UX / 交互

- 用户任务：看到"当前真正待办几条"，而非上千条重复。
- 进入路径：`/alert-events` 列表；运维总览告警卡计数。
- 操作闭环：某服反复 lost → 列表仍是 1 行但显示「×12」「最后 14:32」→ 实例恢复 → 该行自动 `resolved`（待办数下降）。
- 状态设计：合并行显示计数徽标与 `last_at`；`resolved` 行与 `acknowledged` 行样式区分；空态 = 真无待办。
- IA 挂载：可观测域 `/alert-events`；不新增页面。

## 5. 任务拆分

- [x] `alert_event` 增 `occurrence_count` / `last_at` 列 + 迁移（另加 `to_status` 作方向维度）
- [x] 写入路径改为按收敛键合并（计数 + 取最高级）
- [x] 恢复写点（InstanceService.Register/Heartbeat）接入自动 resolve（接线测试覆盖）
- [x] `acknowledged` 状态保持（不因再触发回退 `open`）
- [x] 前端：计数徽标 / `resolved` 样式 / 待办计数口径
- [x] 文档同步：PRD 状态、ADR-0041/0064 引用、CHANGELOG

## 6. 验收标准

- 同一 `(serverId, type)` 连续触发 5 次：`alert_event` **只有 1 行**且 `occurrence_count=5`。
- 实例回 `online` 后该行自动 `status='resolved'`（待办数下降）。
- `acknowledged` 行再次触发仍为 `acknowledged`（不回退 `open`）。
- 抖动场景下待办计数保持有界（不随触发次数线性增长）。
- 数据落库（ADR-0041），重启后**保留**（与 ADR-0019 时代相反）。

## 7. 风险 / 待定

- ~~ADR-0019 不落库~~ → **已修正**：收敛在 `alert_event`（ADR-0041）上做。
- **收敛键**：`(server_id, type)` 是否够——同 type 不同 `level` 是否要分开计——**需拍板**（建议合并、级别取最高）。
- **自动 resolve 的 `handled_by`**：记 `system` 还是留空——实现时定。
- **是否需历史统计**：可选另立"告警统计报表"FR（本 FR 不做）。
- **通知抑制**：合并期间是否抑制重复通知——建议抑制（仅首发通知），**待确认**。
