# 功能规格：区分配改派安全化

> 状态：开发中　·　关联 PRD：FR-71（增强 FR-8/FR-35）　·　ADR：[ADR-0036](../adr/0036-zone-reassign-safety-drain-gate.md)（扩展 [ADR-0004](../adr/0004-zone-authority-control-plane.md)）

## 1. 背景与目标

区（zone）归派看板（FR-35）当前**拖拽卡片即指派/改派/取消指派、无任何确认**。但改派会热更该服有效配置覆盖链（ARCHITECTURE §5/§6），把**在场有玩家**的服改区会扰动玩家；拖拽又极易手滑误触。目标：在不改 ADR-0004 的 DB 权威指派、不改 agent、不动 `zone_assignment` 表与唤醒机制的前提下，给改派加**后端排空门硬闸** + **前端高摩擦显式操作**，并把面向用户的「zone」改称「区」。详见 ADR-0036。

## 2. 需求（要什么）

### 2.1 后端纵深（安全权威）
- **排空门**：目标 serverId 在注册表 `Status == online` 且 `PlayerCount > 0`，且本次操作**会改变其解析 `(group, zone)`** —— 含改派（→不同区）、取消指派、首次指派 —— 一律返回 `ZONE_SERVER_ONLINE_NONEMPTY`(409)，不落库/不审计/不唤醒。
- **同值 no-op**：指派到与现有 `zone_assignment` 完全相同的 `(group, zone)` → 不落库、不审计、不唤醒，幂等返回现有记录（先于排空门判定，同值即便在线非空也放行）。
- **改派入审计**：真正发生的变更仍记 `zone.assign` / `zone.move` / `zone.unassign`（既有，保留）。
- 既有 BC 角色守卫（在线 bungee 拒指派区）保留不变。

### 2.2 前端高摩擦
- 移除 @dnd-kit 拖拽归派（draggable/droppable/onDragEnd/dragAction）。
- 看板**默认只读**；顶部「解锁改派」开关，未解锁不显示改派/取消入口。
- 「改派」走确认对话框：选目标大区/小区 + **手输目标 serverId 原样复述**才可提交（空服/离线服也要）。
- 「取消指派」同样需显式确认。
- 被后端 409 拒时明确提示"该服在线且有玩家，请先排空（drain / 等玩家离开）再改派"。

### 2.3 文案
- 面向用户「zone 分配」→「区分配」、「zone」→「区」（i18n key 值改写；导航/标题/按钮/审计 action 展示）。**不改**代码标识符、表名/列名、审计 action 枚举值、REST 字段名。

### 不做（范围外）
- 不自动 drain（drain 仍由运维显式触发，FR-10）。
- 不改 `zone_assignment` 表结构、不改 agent、不改 REST 契约（路径/字段）。
- 不引入区分配的灰度/编排（属 P2/P3 其它 FR）。

## 3. 设计（怎么做）

### 3.1 后端（internal/）
- **错误码**：`internal/apperr` 新增 `ErrZoneServerOnlineNonempty`（409，`ZONE_SERVER_ONLINE_NONEMPTY`，中文消息含"先排空"提示）。
- **`ZoneService.Assign`**（`internal/service/zone_service.go`）判定顺序：
  1. 参数校验（既有）。
  2. BC 角色守卫（既有：在线 bungee 拒）。
  3. 查现有指派 `assignRepo.FindByServer(ns, serverID)`。
  4. **同值 no-op**：现有存在且 `(group,zone)` 与目标完全相同 → 直接返回现有记录，不入事务。
  5. **排空门**：本次为变更（首次指派 or 改到不同区）→ 查 `registry.Get(ns, serverID)`；若 `inst != nil && inst.Status == online && inst.PlayerCount > 0` → 返回 `ErrZoneServerOnlineNonempty`。
  6. 既有事务：upsert + 审计（`zone.assign`/`zone.move`）+ 事务后唤醒/导出。
- **`ZoneService.Unassign`**：取消前若有现有指派且该服 `online && PlayerCount>0` → 返回 `ErrZoneServerOnlineNonempty`；否则既有软删 + `zone.unassign` 审计。
- 「在线非空」判据封装为一个无副作用小helper（如 `isOnlineNonempty(ns, serverID) bool` 读 registry），便于单测。
- handler 层无需改（错误由 service 返回、统一 render）。

### 3.2 前端（web/src/）
- **删拖拽**：`ZonesPage.tsx` 去 DndContext/DragOverlay/sensors/dragging/onDragStart/onDragEnd；`zones/ServerCard.tsx` 去 useDraggable；`zones/DropBucket.tsx` 去 useDroppable；删 `zones/dragAction.ts`。
- **解锁开关**：`ZonesPage` 加 `unlocked` 状态（默认 false）+ 顶部开关；未解锁卡片不显示改派/取消按钮。
- **改派对话框**：新组件 `zones/ReassignDialog.tsx`——选目标大区/小区（复用 Combobox，候选来自 zone 汇总/实例并集）+ 手输 serverId 复述（输入需 === 该卡 serverId 才启用「确认改派」）+ 备注；提交调既有 `assignZone`。
- **取消指派确认**：复用 AlertDialog 二次确认（手输或显式确认），调 `unassignZone`。
- **409 处理**：mutation onError 若是 `ZONE_SERVER_ONLINE_NONEMPTY` → 中文提示先排空。
- **i18n**：`web/src/i18n/locales/zh-CN.ts` 改 `nav.zones`/`zones.title`/`zones.addAssign`/`zones.assignDialogDesc`/`zones.kanbanHint`/`zones.summaryTitle`/`zones.noZones` 及 `audit.action['zone.*']` 展示文案 zone→区；新增解锁/改派/手输确认相关 key。

### 3.3 架构/契约
- 无新表、无 REST 契约变更（路径/字段不变）。`docs/API.md` 在 `PUT /zones/assignments` 处补一句"在线非空服改区返 409 `ZONE_SERVER_ONLINE_NONEMPTY`（排空门）"。ARCHITECTURE zone 章节补一句改派排空门。

## 4. 任务拆分
- [ ] 后端：`apperr` 加错误码 + `ZoneService.Assign/Unassign` 排空门与同值 no-op + helper
- [ ] 后端测试（先行红）：同值 no-op 不写不审计 / 在线非空改派·取消·首次指派均 409 / 空·离线服放行 / drain 后（players=0）放行 / BC 守卫不回归
- [ ] 前端：删拖拽 + 解锁开关 + ReassignDialog（手输确认）+ 取消确认 + 409 提示
- [ ] 前端测试（先行红）：未解锁无改派入口 / 手输 serverId 不符不可提交 / 提交调 assignZone / 取消需确认 / i18n「区」文案
- [ ] i18n zone→区
- [ ] doc-sync：PRD 验收、API.md、ARCHITECTURE、CHANGELOG、ADR-0036（本批）

## 5. 验收标准
- 在线非空（players>0）服**改派 / 取消指派 / 首次指派**经后端均 409 `ZONE_SERVER_ONLINE_NONEMPTY`，不落库不审计。
- 同值指派 no-op：不新增审计、不改库。
- 空服 / 离线服 / 已排空（players=0）服改派：后端放行、入审计。
- 前端默认只读，须解锁才出改派；改派须手输 serverId 复述；取消须确认。
- 各面向用户文案显示「区」。
- 受影响组件测试全绿（`go build/test/vet ./...` + `cd web && pnpm test && pnpm build`）。
- **真机（浏览器）**：在线非空服改派被拒并提示先排空；排空后可改派；文案显「区」。

## 6. 风险 / 待定
- 「在线非空」判据取 `Status==online && PlayerCount>0`；degraded/lost/offline 不拦（失联/失败服改区不扰动健康游戏；若未来要把 degraded 也纳入再议）。
- 排空门是有意摩擦：运维改在场服区须先 drain，前端须把 409 提示清楚，避免被误读为 bug。
- 前端手输确认仅防误触、非安全边界；安全由后端硬闸兜底（前端可绕、后端不可绕）。
