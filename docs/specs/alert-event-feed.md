# 功能规格：告警历史 / 事件信息流（alert_event）

> 状态：开发中　·　关联 PRD：FR-89　·　分支：feature/fr-89-alert-events

## 1. 背景与目标

FR-28（[ADR-0019](../adr/0019-health-alert-channel-abstraction.md)）的健康告警只往外推 webhook / 进程内站内信，控制台**无任何告警历史 / 信息流**：运维无法回看「上周哪台服掉过线、何时恢复」。本功能（P2）补一处**可跨重启留存、可过滤回看**的告警事件信息流——新增 `alert_event` 持久化实体 + 列表端点 + 管理台「事件」页时间线。架构决策见 [ADR-0041](../adr/0041-alert-event-persistence.md)。

## 2. 需求（要什么）

- 把既有告警事件（当前真实触发点：健康流转 degraded/lost/offline）**额外持久化**一条 `alert_event`，与 FR-28 通道并存。
- 新增只读列表端点，支持按 **类型 / 级别 / namespace / 时间范围** 过滤 + 分页（时间倒序）。
- 管理台新增「事件」页（路由 `/alert-events`，侧栏导航）：信息流时间线 + 上述过滤。
- 范围内：持久化既有告警事件 + 列表 + UI 信息流；可移植 GORM；落库失败不阻断告警 / 健康扫描。
- 不做（范围外，YAGNI）：新增告警规则；改 FR-28 通道行为；告警确认（ack）工作流；去重 / 静默窗口；保留期清理后台（量级小，按需再加）；事件导出（FR-84 是审计专属，不顺手扩到这）。

## 3. 设计（怎么做）

控制面（无 agent 改动）：

- **实体** `internal/model/alert_event.go`：`id`(自增) / `type`(VARCHAR 枚举) / `level`(VARCHAR 枚举) / `server_id`(VARCHAR) / `namespace`(VARCHAR) / `message`(VARCHAR) / `detail`(TEXT) / `created_at`(UTC，全局 NowFunc)。类型与级别常量进 `internal/model/enums.go`，应用层校验。`store.Open` 的 `AutoMigrate` 注册新表。
- **repository** `internal/repository/alert_event_repo.go`：`Create` + `List(filter)`（type/level/namespace/from/to 过滤 + 分页 + 总数，时间倒序）。仅占位符、无方言函数。
- **service** `internal/service/alert_event_service.go`：`Record`（构造并落库，供通道调用）+ `List`（规整 page/size 后委托 repo）。
- **持久化通道** `internal/runtime/alert/persist.go`：`PersistAlerter` 实现 `Alerter`，`Notify` 把 `alert.Alert` 映射为 `alert_event`（health-transition 类型、按 status 定级）落库。经一个窄接口 `EventSink` 依赖 service，避免 alert→service 反向依赖（守循环依赖红线，与 webhook 的 `WebhookSettings` 同范式）。在 `cmd/beacon/main.go` 把该通道加入 `alertChannels`。
- **handler** `internal/handler/alert_event_handler.go`：`List` 解析 query → service → JSON `{ total, items }`；handler 不碰 GORM。
- **路由**：`router.go` admin 组加 `GET /admin/v1/alert-events`。

前端（`web/`）：

- `api/types.ts` 加 `AlertEventView` / `AlertEventPage`；`api/client.ts` 加 `AlertEventFilter` + `listAlertEvents`。
- 新页 `pages/AlertEventsPage.tsx`：信息流时间线 + 类型 / 级别 / namespace / 时间过滤 + 分页（仿 AuditsPage 范式）。
- `App.tsx` 加路由 `/alert-events`；`Layout.tsx` 侧栏加导航项；i18n `zh-CN.ts` 加 `nav.alertEvents` + `alertEvent.*` 文案。

## 4. 任务拆分

- [ ] 实体 + 枚举常量 + AutoMigrate 注册
- [ ] repo（Create + List 过滤分页）+ 单测
- [ ] service（Record + List）
- [ ] PersistAlerter 通道 + 单测（映射 + 兜错），main.go 装配
- [ ] handler + router 端点；API.md 同步
- [ ] 集成测试（真 MySQL）：建表可移植 + 落库 + 列表过滤 + 健康流转触发持久化
- [ ] 前端 types/client/页面/路由/导航/i18n + vitest
- [ ] 文档同步：PRD 状态、ARCHITECTURE 数据模型、API、CHANGELOG

## 5. 验收标准

- 实例进入异常态（degraded/lost/offline）后，`alert_event` 表落一条对应事件（type=health-transition、level 按状态），经 `GET /admin/v1/alert-events` 可查回。
- 列表端点按 type / level / namespace / from / to 过滤 + 分页正确，时间倒序。
- 落库失败仅 WARN、不阻断健康扫描与其它告警通道。
- 真 MySQL 集成：建表（无方言）、落库、过滤、健康流转触发持久化全绿。
- `go test ./...` 绿；前端 `pnpm test` + `pnpm build` 绿；无 agent 改动。

## 6. 风险 / 待定

- 保留期清理本期不做（事件稀疏、量级小）；将来量级需要时按 metric_sample 范式补清理后台（新 ADR / 增强 FR）。
- 真机浏览器验证留「待真机浏览器验」。
