# 功能规格：健康流转原因展示

> 状态：开发中　·　关联 PRD：FR-81　·　分支：feature/fr-81-health-reason

## 1. 背景与目标
实例健康状态机按心跳陈旧度推进 online→degraded→lost→offline（FR-28）。运维在管理台只看到一个状态徽标（如 lost），但**不知道为什么**——心跳多久没来了？是超过了哪一档阈值？要回答得手动算「当前时间 − lastHeartbeat」再对照设置里的阈值，体验差、易错。

本需求（P2，增强健康）在实例视图上补两条只读事实——距上次心跳秒数 + 触发该状态的原因文案——并在管理台状态徽标上以悬浮提示展示，让运维一眼看懂「为何是这个状态」。

## 2. 需求（要什么）
- 实例对外视图（`InstanceView`）新增两字段：
  - `lastHeartbeatAgeSec`：距上次心跳的秒数（按控制面当前 UTC 时刻 − `lastHeartbeat` 取整秒，负值归零）。
  - `healthReason`：触发当前状态的原因中文文案；`online` 时为空串。
- 文案规则（对齐 PRD「Ns 未心跳 > ttl Ns」范式）：
  - degraded：`Ns 未心跳 > degraded-after Ns`
  - lost：`Ns 未心跳 > ttl Ns`
  - offline：`Ns 未心跳 > offline-grace Ns`
  - online：空串
- 原因里的阈值取**控制面当前生效的健康阈值**（设置 store 热改项 FR-61），与健康扫描判定同源，保证文案与实际状态机口径一致。
- 前端服务器页表格、看板（zone 看板卡片）、单服详情的状态徽标，在 `healthReason` 非空时以悬浮提示展示该文案。
- 范围内：`instance_handler.go` 的 `InstanceView` 渲染、`runtime` 原因文案纯函数、前端状态展示（StatusBadge / ServerCard / ServerDetailSheet）、`types.ts`、`docs/API.md`、`CHANGELOG.md`。
- 不做（范围外）：
  - 不落 DB（纯内存派生，随读随算）。
  - 不改健康状态机阈值判定逻辑（`healthByAge` 不动）。
  - 不新增端点、不改注册/心跳/上报路径。
  - 不补 degraded 的徽标配色（StatusBadge 现状未含 degraded，属既有缺口，不在本 FR 镀金）。

## 3. 设计（怎么做）
### 3.1 控制面（Go）
- `internal/runtime/registry.go` 新增纯函数 `HealthReason(age, degradedAfter, ttl, offlineGrace time.Duration, status string) string`：按状态返回原因文案；与 `healthByAge` 并置、同一组阈值入参，口径一致。秒数由 `time.Duration.Seconds()` 取整呈现。
- `internal/handler/instance_handler.go`：
  - `InstanceView` 增 `lastHeartbeatAgeSec int` 与 `healthReason string`。
  - `InstanceHandler` 注入窄读接口 `healthThresholds`（`GetInt(key string) int`，由 `service.SettingsService` 实现），渲染时读当前三档阈值；同时记录渲染时刻 UTC 算 age。
  - 渲染辅助下沉到 `toInstanceView`：算 `age = now − LastHeartbeat`（负值归零），`lastHeartbeatAgeSec = int(age.Seconds())`，`healthReason = runtime.HealthReason(...)`。
  - 阈值 key 复用 runtime 已声明的字面常量（`health.degraded-after-sec` 等，FR-61 同字面真源）。
- 装配：`cmd/beacon/main.go` 给 `NewInstanceHandler` 传入 `settingsService`。

### 3.2 前端（React/TS）
- `web/src/api/types.ts`：`InstanceView` 增 `lastHeartbeatAgeSec: number` 与 `healthReason: string`。
- `web/src/components/StatusBadge.tsx`：新增可选 `reason?: string`，非空时设原生 `title`（悬浮提示），保持徽标渲染与配色不变。
- `web/src/pages/ServersPage.tsx`、`web/src/pages/servers/ServerDetailSheet.tsx`：状态徽标传 `reason={i.healthReason}`。
- `web/src/pages/zones/ServerCard.tsx`：看板状态点在 `healthReason` 非空时设 `title`。

> 悬浮提示采用原生 `title` 属性而非 radix Tooltip：项目尚无全局 `TooltipProvider`，引入需在根装配且 jsdom 测试成本高；`title` 即可满足「悬浮显原因」且无障碍、零额外依赖（简单优先）。

## 4. 任务拆分
- [x] runtime 原因文案纯函数 + 单测（age 计算口径 + 各状态文案）
- [x] handler `InstanceView` 新字段 + 注入阈值读接口 + 渲染
- [x] main.go 装配传入 settingsService
- [x] 前端 types/StatusBadge/ServersPage/ServerDetailSheet/ServerCard + vitest
- [x] 文档同步：PRD 状态、docs/API.md、CHANGELOG

## 5. 验收标准
- `go build ./... && go test ./...` 绿，含 runtime 新增 `HealthReason` 单测（覆盖 online 空串、degraded/lost/offline 三档文案与阈值秒数、age 负值归零）。
- 前端 `pnpm test` + `pnpm build` 绿，含 StatusBadge `reason` 悬浮提示用例。
- 服务器页/看板状态徽标在 lost/degraded/offline 时悬浮显「Ns 未心跳 > <阈值名> Ns」（待真机浏览器验）。

## 6. 风险 / 待定
- `lastHeartbeatAgeSec` 与 `healthReason` 为读时刻派生：列表渲染时刻与健康扫描时刻可能差一个扫描周期，age 文案与状态可能瞬时略有出入（如刚跨过 ttl 边界）。可接受——均为只读展示、不参与决策。
- 原因文案为后端中文成品串，前端直显不再 i18n（与现状单语 zh-CN 一致）。
