# 功能规格：告警收敛与防堆积

> 状态：已实现（单测通过；真机验收待做）　·　关联 PRD：FR-232（增强 FR-89 / FR-157）　·　分支：feature/alert-dedup-and-auto-resolve

## 1. 背景与目标

> ⚠️ 已按评审重写。原稿以「沿用 **ADR-0019** 告警不落库、内存侧合并」为前提——**该前提已废**：**ADR-0041** 已取代 ADR-0019「告警不落库」，告警已落 `alert_event` 表（FR-89），并有处理工作流（FR-157 / ADR-0064）。

问题依旧存在（现象）：实例抖动 / 反复启停时，`alert_event` 表**每次触发都插一行**，同类告警堆积上千条，待办清不完。**收敛应在 `alert_event` 上做**，而非内存。

本 FR = 「同类合并计数 + 恢复自动 resolve（更新 `status`）」。

## 2. 需求（要什么）

- **同类合并计数**：同一 `(serverId, type)` 在**未恢复期间只保留 1 行**，重复触发只**计数 +1**、刷新 `last_at`（+ 可能刷新 `level` 为最高级，见 FR-231），**不再插新行**。
- **一次恶化只留一条**：同一实例的一次健康恶化链（`degraded → lost → offline`）**只留 1 行**——收敛键**不含方向**，中间阶段不再各开一行；行内方向取**最严重态**（只升不降），`message` / `detail` 取最新一跳。（prod 实测：10 台实例下线，每台 `degraded(info) → lost(warning) → offline(warning)` 三跳各开一行 → 30 条告警刷屏；合并后 10 条。）
- **恢复自动 resolve**：实例回到 `online` 时，其未恢复告警自动把 `status` 置为 `resolved`（复用 FR-157 的状态机与 `handled*` 语义）。
- **生命周期自动 resolve**：实例 / 环境被**归档或永久删除**时，其未处理告警自动把 `status` 置为 `resolved`——这类实例已从 `server` 表消失、不可能再回 `online`，否则告警永久滞留待办（见 §3 触发点）。
- **已处理不重开**：已被人工处理（非 `open`）的条目再次触发时**不自动回退为 `open`**，只更新计数 / 时间。
- 待办数**不随触发次数线性增长**。

范围内：`alert_event` 侧收敛（合并 / 自动 resolve / 状态保持）。
不做（范围外）：通知渠道；历史统计报表（可另立 FR）。

## 3. 设计（怎么做）

- **收敛键**：`(namespace, server_id, type)`（无 `serverId` 的集群级用 `namespace + type`）。查询走复合索引 `idx_alert_event_dedup_v2 = (server_id, namespace, type)`。
  - **方向不再分行（已定，取代原「键加 `to_status` 保方向」的备选）**：早先实现把 `to_status` 计入收敛键，结果**方向每变一次就新开一行**——一次 `degraded → lost → offline` 恶化链被拆成 3 行刷屏（prod 实测 30 条），比「丢方向」更疼。故取舍为：**同一实例同类型未处理期间只保留 1 行，行内 `to_status` 记该链见过的最高严重度**（严重度序 `degraded(1) < lost(2) < offline(3)`，只升不降），中间阶段不再单独成条；`message` / `detail` 取**最新**一跳（行是「进行中的同一个事件」，不是历史归档，`created_at` 仍保持首发时间）。
  - `to_status` 列**保留**（历史行仍可读、仍可筛选 / 展示方向），只是不再参与收敛键。
  - **类型边界**：收敛当前**只对 `health-transition` 生效**（本次未扩大）；`identity-conflict` 等类型每次触发仍各自成行（其 `detail` 带不同的 boot 冲突明细，合并会丢上下文）。
- **表结构调整**：`alert_event` 增加 `occurrence_count`（默认 1）与 `last_at`；`created_at` 保留为首发时间。
- **索引换代（既有库升级）**：GORM `AutoMigrate` 对索引**只增不删**，且**同名索引不做列比对**（实测：表中已有同名索引时直接跳过，不重建）——因此改索引只靠改 tag 不会生效。做法：新索引换名 `idx_alert_event_dedup_v2`，并在 `store.Open` 迁移后**显式 DROP 旧索引** `idx_alert_event_dedup`（`HasIndex` 判存 → 幂等；只删索引、不删列、不动任何行，全新库上为无操作）。回退旧版本二进制时旧模型会重建该索引，无数据风险。
- **写入路径**：告警产生时先按收敛键查**未恢复行**（`status != 'resolved'`）：
  - 命中 → `occurrence_count += 1`、`last_at = now`、（按 FR-231）`level = max(level, 新级)`、方向 `to_status` 只升不降、`message` / `detail` 取最新；
  - 未命中 → 插新行。
- **自动 resolve 触发点**：实例由**非 online → online**（心跳续上 / 重新注册）时，把其未恢复行 `status='resolved'`，且 **`handled_by = system`**、`handle_note` 记"实例恢复自动消解"——使 UI 能区分「系统自动消解」vs「人工已处理」（与 FR-229 §7 对齐）。
  - ⚠️ **实现修正**：恢复 online 由 `registry.Heartbeat` / `registry.Register` **直接置位**，**不经**健康扫描的 `SweepExpired` 输出（`healthByAge` 默认返回 `current`、永不产出 online）。故触发挂在 **`InstanceService.Register` / `Heartbeat`**（真正的恢复写点，写前读旧状态判迁移），**不在**健康扫描循环内——否则该分支在生产永不可达（一条只测分支、不测接线的测试会假绿）。
  - **生命周期触发点（2026-10-02 增补）**：除「恢复 online」外，实例 / 环境**被归档或永久删除**时同样自动消解——这类实例已从 `server` 表消失，不可能再回 `online`，此前其告警会**永久滞留 `open`**（prod 实测 155 条 open 告警涉及 53 个已不存在的实例）。四条路径与既有命令过期（`expireServerCommands`）并列：
    1. **实例归档** `updateServerLifecycle`（`server.archive` 审批链，事务内原子）→ note「实例已归档，自动消解」；
    2. **实例永久删除** `applyServerPermanentDelete`（`server.permanent-delete` 审批链）→ note「实例已永久删除，自动消解」；
    3. **实例永久删除的并列落地路径** `updateServerLifecycle` 的永久删除分支（该分支暂不可由审批适配器路由到达，属防御性对称，同样接线并单测覆盖）；
    4. **环境归档 / 环境永久删除** `applyNamespaceLifecycle` → 关闭该环境**全部**未处理告警（含无 `serverId` 的集群级行）→ note「环境已归档，自动消解」/「环境已永久删除，自动消解」。
  - **消解边界**：实例级严格按 `(namespace, server_id)`、环境级按 `namespace` **code** 收敛（`alert_event.namespace` 存的是 code，实例路径手上只有 `namespace_id`，故先解析）；已 `resolved` 行不参与 UPDATE，重复执行幂等。**v1 主动下线 `InstanceService.Offline` 不在触发点内**（另行决策）。
- **失联自动关闭（2026-10-02 增补）**：实例被**外部删除**（压测实例用完即删、实例被直接销毁）时，控制面收不到任何删除信号、`server` 表里也没有对应行可走上面的生命周期消解，其告警同样会**永久滞留 `open`**（prod 实测：压测结束滞留 30+ 条）。新增后台清理器 `AlertOrphanSweeper`（`service/alert_orphan_sweeper.go`：单 goroutine + 10 分钟 ticker + 窄依赖接口，随进程关停退出），**四项判据同时成立**才自动消解：
    1. 该 `(namespace, server_id)` 存在**未处理**（`status <> 'resolved'`）告警；
    2. 该实例**不在运行时注册表** `runtime.Registry`（无任何在线 / 失联登记）；
    3. 该实例**不在 `server` 表的活动目录中**（不存在 `lifecycle = active` 的对应行；`archived` / `tombstoned` 行不算在册，其告警已由上面的生命周期消解覆盖）；
    4. 其告警的**最近触发时间**（`last_at`，空则 `created_at`）距现在**超过超时阈值**（设置项 `alert.orphan-timeout-hours`，默认 24 小时、下界 1，热改项，进 `dangerousSettingKeys` 走审批 + 审计）。
  - **安全红线**：判据 3 是防误关的关键——**在册**（`server.lifecycle = active`）的真实实例即使离线很久，其告警**绝不能**被自动关闭（运维必须看到）。实现上先取「在册 namespace code + serverId」键集合，命中即整条跳过；测试对「在册 + 不在注册表 + 远超阈值」构造断言其**保持 `open` 且无任何处理痕迹**（经变异验证：去掉判据 3 该用例立刻失败）。
  - **关闭动作**：复用 `AlertEventRepository.AutoResolveByServer`（一条 UPDATE、只影响 `status <> 'resolved'` 的行，天然幂等），`handled_by=system`、`handle_note=「实例长期失联且不在受管目录，自动消解」`，管理台仍可区分系统自动消解与人工处理；不逐条写审计（与其余自动消解触发点同口径），不新增 HTTP 端点。
  - **查询形态**：候选聚合与在册目录各一条集合查询（`repository/alert_orphan_repo.go`，新文件），严禁逐实例查库（N+1）。候选聚合取「行级读时间 + 应用层按实例取 `MAX(COALESCE(last_at, created_at))`」而非 SQL `MAX(...) GROUP BY`——聚合表达式列无声明类型，SQLite 驱动会把时间聚合结果当字符串返回（实测 `unsupported Scan ... into type *time.Time`），行级查询的时间列各驱动都能原生扫描成 `time.Time`（守 DB 可移植），代价是每次扫描多传「未处理告警行数」这点量（已被本 FR 的收敛压住）。
  - **非实例维度行**（`server_id` 为空，如集群级告警）不参与本清理器：它们不代表任何实例失联，不属其职责范围。
- **状态保持**：`acknowledged` 行再次触发仅更新计数 / 时间，**不回退 `open`**。
- **审计**：自动 resolve 与合并是否逐条审计——建议**不逐条**（量大）；可选按周期汇总（**待实现时定**）。

## 4. UX / 交互

- 用户任务：看到"当前真正待办几条"，而非上千条重复。
- 进入路径：`/alert-events` 列表；运维总览告警卡计数。
- 操作闭环：某服反复 lost → 列表仍是 1 行但显示「×12」「最后 14:32」→ 实例恢复 → 该行自动 `resolved`（待办数下降）。
- 状态设计：合并行显示计数徽标与 `last_at`；`resolved` 行与 `acknowledged` 行样式区分；空态 = 真无待办。
- IA 挂载：可观测域 `/alert-events`；不新增页面。

## 5. 任务拆分

- [x] `alert_event` 增 `occurrence_count` / `last_at` 列 + 迁移（另加 `to_status` 记录方向）
- [x] 写入路径改为按收敛键合并（计数 + 取最高级）
- [x] **恶化链合并**：收敛键去掉方向维度（不再按 `to_status` 分行），行内方向取最严重态（只升不降）、`message` / `detail` 取最新；索引换代 `idx_alert_event_dedup_v2` + 旧索引显式清理（既有库升级专项测试覆盖）
- [x] 恢复写点（InstanceService.Register/Heartbeat）接入自动 resolve（接线测试覆盖）
- [x] 生命周期触发点接入自动 resolve：实例归档 / 实例永久删除（两条落地路径）/ 环境归档 / 环境永久删除（接线测试覆盖，并断言不误伤同环境其它实例与其它环境）
- [x] **失联自动关闭**：`AlertOrphanSweeper` 周期关闭「不在运行时注册表 + 不在 `server` 表活动目录 + 超阈值」实例的未处理告警（四项判据测试覆盖，含「在册实例不关」红线）
- [x] `acknowledged` 状态保持（不因再触发回退 `open`）
- [x] 前端：计数徽标 / `resolved` 样式 / 待办计数口径
- [x] 文档同步：PRD 状态、ADR-0041/0064 引用、CHANGELOG

## 6. 验收标准

- 同一 `(serverId, type)` 连续触发 5 次：`alert_event` **只有 1 行**且 `occurrence_count=5`。
- **一次恶化只留一条**：同实例依次 `degraded → lost → offline` → 仍**只有 1 行**，`to_status=offline`（取最严重态）、`occurrence_count=3`、`level` 取最高、`message` 为最新一跳、`created_at` 为首次时间。
- **方向不回落**：先 `offline` 再来 `lost` → `to_status` 仍为 `offline`（级别 / 计数照常更新）。
- **不误合并**：不同实例、不同类型（`health-transition` vs `identity-conflict`）各成一行，另一行的计数 / 方向 / 文案原样保留。
- 实例回 `online` 后该行自动 `status='resolved'`（待办数下降）；已 `resolved` 行不参与合并，同键再触发**另起一行**。
- 实例 / 环境被归档或永久删除后，其未处理告警自动 `status='resolved'`（`handled_by=system`），且同环境其它实例、其它环境的告警原样保留（防一条 UPDATE 打宽误标已处理）。
- **失联自动关闭**：实例被外部删除（不在运行时注册表、不在 `server` 表活动目录）且未处理告警最近触发超过阈值（默认 24h）→ 该告警自动 `resolved`（`handled_by=system`、note「实例长期失联且不在受管目录，自动消解」）；未超阈值 / 仍在注册表 / **在册（`active`）实例**三类一律保持 `open`；重复扫描 `affected=0`。
- `acknowledged` 行再次触发仍为 `acknowledged`（不回退 `open`）。
- 抖动场景下待办计数保持有界（不随触发次数线性增长）。
- **既有库升级**：旧索引 `idx_alert_event_dedup`（含 `to_status`）升级后被新索引 `idx_alert_event_dedup_v2` 取代，`to_status` 列保留、历史行一列不改一行不丢。
- 数据落库（ADR-0041），重启后**保留**（与 ADR-0019 时代相反）。

## 7. 风险 / 待定

- ~~ADR-0019 不落库~~ → **已修正**：收敛在 `alert_event`（ADR-0041）上做。
- ~~**收敛键**：`(server_id, type)` 是否够~~ → **已定**：键 = `(namespace, server_id, type)`，**不含方向**；运行期方向取最严重态。代价是「一次恶化链只留一行、中间阶段不单独成条」——需要逐跳历史的场景由 `occurrence_count` + 管理台健康时间线补足。
- **`message` 与 `to_status` 可能不同步**：方向只升不降、`message` 取最新一跳，故「先 `offline` 后 `lost`」的行会显示 latest 文案而方向仍是 `offline`（刻意取舍：方向供筛选 / 判级保高水位，文案供人读保新鲜）。
- **自动 resolve 的 `handled_by`**：记 `system`（已实现）。
- **是否让所有类型都收敛**：当前只有 `health-transition` 参与合并；`identity-conflict` 等仍逐条留痕（`detail` 各自不同）。若要扩大，需先确认其 `detail` 的合并语义——**待拍板**。
- **是否需历史统计**：可选另立"告警统计报表"FR（本 FR 不做）。
- **通知抑制**：合并期间是否抑制重复通知——建议抑制（仅首发通知），**待确认**。
