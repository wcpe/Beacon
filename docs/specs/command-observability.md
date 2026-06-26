# 功能规格：命令观测 / 审查面板

> 状态：开发中　·　关联 PRD：FR-104　·　分支：feature/command-observability

## 1. 背景与目标
控制面↔agent 之间的「控制命令」（`agent_command` 表：反向抓取 ingest-plugins / 取日志 tail-logs / 强制重同步 resync-config）目前只能从审计日志（人触发那一刻）与控制面健康页（FR-82，仅命令队列**计数**）侧面观测，缺少一个能看到**命令双向生命周期**（下发 pending → agent 拉取 fetched → 回执 done/failed/expired）的专门视图。本功能（FR-104，P2，增强 FR-17/FR-82）新增「命令观测」页 + 两个只读后端端点，把命令从下发到终态的全过程做成可过滤、可聚合、可实时跟踪的运维面板，并吸收原「控制面健康逐条队列明细」诉求（把队列从计数升级为逐条）。

## 2. 需求（要什么）
- 后端只读查询端点 `GET /admin/v1/commands`：按 namespace / serverId / type / status / from / to 过滤 + 分页，按 createdAt 倒序，返回命令元数据列表。
- 后端只读聚合端点 `GET /admin/v1/commands/analytics`：按 namespace + 时间窗聚合——总数、按状态计数、按类型计数、按服务器 top-N 计数、命令量按天趋势（下发数 / 完成数 / 失败数）。
- 前端新「命令观测」页 `/commands`（归可观测导航组）：KPI（总数 / 按状态 / 按类型）+ 实时队列逐条（pending+fetched，自动刷新）+ 历史可过滤查询（含结果摘要）+ 命令量趋势图。
- 范围内：只读观测；复用既有分层 / 范式 / 组件 / 依赖。
- 不做（范围外）：不改 agent；不动其它页 / FR；不引新依赖；**绝不返回瞬态/敏感内容字段**（`ImprintContent` / `LogContent`），只返元数据 + 结果摘要（`ResultDetail`，已是不含敏感的摘要）；不提供任何写 / 改命令的旁路。

## 3. 设计（怎么做）
分层严格 router → handler → service → repository，handler 不碰 GORM；GORM 可移植（占位符过滤无方言、Go 侧日分桶，仿 FR-73 `/audits/analytics`）。

- **repository**（`agent_command_repo.go` 加方法）：
  - `CommandFilter`（namespace/serverId/type/status/from/to/page/size，零值不过滤）。
  - `List(f)`：占位符过滤 + 分页（createdAt desc, id desc）+ 总数；`Select` 投影**排除** `imprint_content` / `log_content` / `payload`（敏感 / 瞬态 / 大文本）。
  - `ScanForAnalytics(f)`：窗口内取聚合投影行（created_at / status / type / server_id 四列，按时间升序），日分桶与计数交由 service Go 侧做。
  - 复用既有 `idx_agent_command_lookup`（namespace / server_id / status）。
- **service**（新 `agent_command_observe_service.go`）：
  - `List`：规整 page/size，委托 repo。
  - `Analytics`：时间窗缺省（to=now，from=to-30d）+ 92 天上限校验（超限 `ErrInvalidParam`）；type/status 过滤值若非法枚举返 `ErrInvalidParam`；单遍扫描分桶计总数 / byStatus / byType / byServer(top-N) / byDay(下发·完成·失败)。空结果各数组 / map 为空（非 nil）。
- **handler**（新 `command_observe_handler.go`）：仅解析 query → 调 service → 编解码对外视图（小驼峰）；列表项含派生 `ageSeconds`（now - createdAt）。
- **前端**：`api/client.ts` 加 `listCommands` / `getCommandAnalytics` + 类型；新 `CommandObservabilityPage`；`navModel.ts` 可观测组加叶子；`App.tsx` 加路由；i18n 加 `nav.commandObservability` + `commandObs.*`。命令 type / status 中文标签走 i18n（不硬编码）。趋势图复用 recharts。

## 4. 任务拆分
- [x] repo：`CommandFilter` + `List` + `ScanForAnalytics`（投影排除敏感字段）+ 单测
- [x] service：`List` + `Analytics`（缺省 / 上限 / 枚举校验 / Go 分桶 / 不含敏感）+ 单测
- [x] handler：两端点编解码 + `ageSeconds` 派生 + 单测
- [x] router 注册（只读 GET，无 readonlyWriteGuard）+ main 装配
- [x] 前端：api / 类型 / 页 / 导航 / 路由 / i18n + vitest
- [x] 文档同步：PRD 状态（主控已置开发中）、API.md、CHANGELOG、本 spec

## 5. 验收标准
- 两端点按契约工作：列表过滤分页倒序；聚合计数 + Go 日分桶；超 92 天窗 400；非法 type/status 400。
- 响应**绝不含** `ImprintContent` / `LogContent` / `Payload`（单测断言）。
- 前端页 KPI / 实时队列逐条（含已等时长）/ 历史过滤 / 趋势图渲染；loading 骨架；i18n 无裸键。
- `go build ./... && go test ./...` 全绿；`cd web && pnpm test --run && pnpm build` 全绿。

## 6. 风险 / 待定
- 实时队列自动刷新节奏取 5s（与既有页轮询节奏一致），无 SSE（管理台无浏览器 SSE，沿用轮询）。
- top-N 的 N 取 10（运维场景够用，常量定义不外部化）。
