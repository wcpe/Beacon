# 功能规格：服务器行快捷操作

> 状态：开发中　·　关联 PRD：FR-91（增强 FR-65，依赖 FR-86 + FR-88）　·　分支：feature/fr-91-resync　·　命令通路：[ADR-0027](../adr/0027-reverse-fetch-channel-and-security.md)（复用，不新增 ADR）

## 1. 背景与目标

服务器页（FR-65）的行操作此前散落为多个内联按钮（下线 / drain / 改派），随能力增多会越来越挤。FR-91 把行操作收进单个「⋯」下拉菜单，并补三项快捷入口：① agent 详情 ② 查看日志 ③ 强制重同步。前两项复用既有 FR-86 / FR-88 能力（打开服务器详情 Sheet / 直达其日志区），**真正的新能力只有「强制重同步」**——令该 agent 立即重拉控制面权威的有效配置/文件树/覆盖集并 apply，免等长轮询、免上机。属 P2。

## 2. 需求（要什么）

- 服务器列表行操作改造为单个下拉菜单（「⋯」触发），含全部行操作：
  - 新增「agent 详情」：打开服务器详情 Sheet（即设选中实例）。
  - 新增「查看日志」：打开服务器详情 Sheet 并自动触发其日志区取日志（复用 FR-88）。
  - 新增「强制重同步」：下发 resync-config 命令，成功 toast「已下发重同步」/失败 toast。
  - 保留「下线」（FR-49，保留 FR-76 二次确认弹窗，绝不丢确认）、drain/undrain（FR-10）、改派（FR-71）。
  - 保持点击行仍打开详情 Sheet 的现有行为不破坏。
- 强制重同步语义 = agent 重拉**有效配置**：忠实复用既有「以本地 md5 拉一次 → 幂等 apply」三条路径（配置 / 文件树 / 覆盖集），已是最新则合法 no-op（不做绕过 applier md5 幂等守卫的 force-apply）。
- 范围内：resync-config 命令类型 + 触发/结果回传端点 + agent 重同步执行 + 行操作下拉菜单。
- 不做（范围外）：远程改配置/重启进程；新命令通路或新 ADR（复用 ADR-0027/0037 既有命令队列与单飞排空）。

## 3. 设计（怎么做）

复用命令队列既有模式（ADR-0027，与 ingest-plugins / tail-logs 同机制），不新增 ADR——resync-config 只是命令队列既有模式的第三个命令类型，无新架构决策。

### 3.1 控制面（Go）
- `model.enums`：加 `CommandTypeResyncConfig = "resync-config"`（`IsValidCommandType` 纳入）；加审计动作 `ActionInstanceResync = "instance.resync"`。
- `AgentCommandService`：
  - `RequestResync(ns, serverId, operator, clientIP)`：事务内建 pending resync-config 命令（空 JSON 载荷）+ instance.resync 审计（detail 仅 commandId/serverId，无内容）→ 唤醒 agent（与 RequestTailLogs 同形）。
  - `ReceiveResyncResult(commandID, ok, reason)`：命令须 fetched 且 type=resync-config → ok 则 CAS done，否则 CAS failed 记原因摘要（无敏感内容）。
- handler：`POST /admin/v1/instances/{serverId}/resync?namespace=`（触发，202 返回命令视图，写、readonly 403，专项审计已纳入 coveredWriteRoutes）；`POST /beacon/v1/agent/commands/result`（agentToken 信任面，agent 回传命令结果）。
- 过期清理沿用既有 ExpireStale（pending/fetched 超时转 expired）。

### 3.2 agent-core（Kotlin，双端通用）
- `AgentCommand.TYPE_RESYNC_CONFIG = "resync-config"`。
- `AgentLifecycle.forceResyncNow()`（public）：依次调既有 `fetchAndApplyConfigOnce()` / `fetchAndApplyFileTreeOnce()` / `fetchAndApplyOverrideOnce()`（各 applier md5 幂等守卫兜底）；须在 async 线程调用（由执行器在 async 线程触发），不额外起线程、不上 MC 主线程。
- `ReverseFetchExecutor`：`runOnce()` 加 resync-config 分支——调注入的 `onResyncConfig` 回调 → 经 `BeaconApiClient.uploadCommandResult(id, ok, reason)` 回传 done/failed（回调抛异常回传 failed）；**不读 plugins 树**。复用同一命令通路与单飞排空。
- `AgentAssembly`：以延迟持有者（`AtomicReference<AgentLifecycle?>`）打破 executor 先于 lifecycle 的构造顺序——`onResyncConfig = { lifecycleRef.get()?.forceResyncNow() }`，lifecycle 建好后回填。tail-logs / resync-config 不依赖 plugins 基目录有效性（不读盘），故执行器始终装配以响应。

### 3.3 前端（React/TS）
- `api/client.ts`：`triggerResync(serverId, namespace): Promise<{ commandId }>`（POST resync 端点，取命令视图 id）。
- `ServersPage`：行操作区整合为单个 shadcn `DropdownMenu`（「⋯」触发）。下线二次确认弹窗（AlertDialog）提到菜单外层受控触发（offlineTarget state），避免菜单关闭吞掉弹窗。
- `ServerDetailSheet`：加 `focusLogs` 入参——为 true 时日志区打开即自动触发一次取日志（「查看日志」入口直达）；LogsSection 以实例 key 绑定，切换服务器重置取日志状态。
- i18n：行操作三项标签 + resync 成功/失败 toast + `instance.resync` 审计动作中文映射（同步 `auditActionCoverage.test.ts` 漂移守护清单）。

## 4. 任务拆分
- [x] 控制面：enums（命令类型 + 审计动作）+ RequestResync/ReceiveResyncResult + 触发/结果端点 + 路由 + coveredWriteRoutes（测试先行）
- [x] agent-core：TYPE_RESYNC_CONFIG + forceResyncNow + executor resync 分支 + uploadCommandResult + AgentAssembly 回调接线（测试先行）
- [x] 前端：triggerResync + ServersPage 下拉菜单（保留下线确认）+ ServerDetailSheet focusLogs + i18n（vitest + build）
- [x] 文档同步：spec、PRD（§4 状态发版时翻）、API.md、ARCHITECTURE.md、CHANGELOG

## 5. 验收标准
- 控制面：`go test ./...` 绿——RequestResync 建 pending resync-config 命令 + 记一条 instance.resync 审计；ReceiveResyncResult ok→done / fail→failed，存在性/状态/类型守卫；coveredWriteRoutes 不变量测试绿。
- agent：`gradlew build`（全量，含 bukkit/bungee 壳）BUILD SUCCESSFUL；executor 单测——resync-config 命中调回调 → 回传 done；回调抛异常 → 回传 failed；未注入回调 → 忽略不回传不读盘。
- 前端：`pnpm test` + `pnpm build` 绿——行操作菜单渲染三新项；点「强制重同步」调 triggerResync 并提示成功；下线经菜单仍保留二次确认；audit coverage 漂移守护含 instance.resync。
- 真机（主控）：lobby-1 经行菜单「强制重同步」下发命令，agent 重拉有效配置并回传 done；改一处配置后重同步即时生效（旁路长轮询）。

## 6. 风险 / 待定
- 「查看日志」入口自动触发取日志：仅在 Sheet 打开本次会话首次自动发一次（避免重复刷命令）；与手动「取日志」按钮共存。
- 强制重同步对已是最新的目标是合法 no-op（幂等守卫），命令仍标 done——这是预期语义，非空操作错误。
