# 功能规格：控制面自观测页

> 状态：开发中　·　关联 PRD：FR-82　·　分支：feature/fr-82-self-observability

## 1. 背景与目标

控制面（Beacon 进程本身）当前只有 FR-33 页眉条暴露版本 / 运行时长 / DB 连通 / 在线实例数 / 采样器状态 + Go 运行时资源（goroutine / 堆 / 进程 CPU）。运维要排查「控制面自己卡不卡、连接池有没有耗尽、长轮询挂了多少、命令队列堆没堆」时无处可看——这些是控制面内部运行态，既不属于 FR-32 的 agent 网络负载，也不在 FR-33 页眉的展示面内。

FR-82 补一页「控制面健康」，把控制面内部指标聚合成一个只读端点 + 一张 StatCard 页，让运维一眼看清控制面自身健康。属 P2。

## 2. 需求（要什么）

补 FR-33 之外的控制面内部指标，新端点 + 新页面：

- **DB 连接池统计**（`sql.DBStats`，database/sql 通用、非方言）：OpenConnections / InUse / Idle / WaitCount / WaitDurationMs / MaxOpenConnections。
- **longpoll 当前挂起 waiter 数**：四条通道（配置 / 文件 / 拓扑 / 命令）各自挂起数 + 合计。
- **注册表规模**：实例总数 + 按健康状态分布（online / degraded / lost / offline）。
- **命令队列深度**：agent_command 表按状态计数（pending / fetched / ready）——反映待 agent 拉取 / 执行中 / 待审的积压。

范围内：
- 新只读端点（见 §3 选型），返回上述快照。
- 新「控制面健康」页（路由 `/system`、侧栏导航项），StatCard 网格只读展示。

不做（范围外）：
- 不动 FR-33 `system/status`（页眉条仍只展示其原有字段）。
- 不引入 Prometheus / 时序存储 / 历史趋势（FR-30 另说）；本页只展示**当前快照**，前端短周期轮询刷新。
- 不展示任何业务 / 玩家数据（控制面只读自观测）。
- 不加鉴权面 / 告警面（属其它 FR）。

## 3. 设计（怎么做）

### 端点选型：新增 `GET /admin/v1/system/observability`（不扩 `system/status`）

PRD FR-82 给「扩 status 或新端点」二选一。**选新端点**，理由：
1. `system/status`（FR-33）被**每个页面**的页眉条以 5s 周期常驻轮询（含 FR-78 连通态派生），把 DB 连接池统计 / 注册表遍历 / 命令队列 DB 计数压进这条高频轮询不划算；自观测页只在打开时轮询，频次与受众都不同。
2. 关注点分离：`status` = 页眉健康（FR-33），`observability` = 自观测页（FR-82），各自独立演进。
3. 不改 `NewSystemService` 既有签名，既有 status 测试零波及（精准修改）。

### 分层（控制面）

新建 `ObservabilityService`（与 `SystemService` 并列，**不塞进 SystemService** 以免上帝类），按窄依赖注入只读快照来源：
- DB 连接池：窄接口 `dbStatsProvider { Stats() sql.DBStats }`，由 `*sql.DB` 实现（与 FR-33 同一连接池）。
- longpoll 挂起数：注入四个 `*longpoll.Hub`，调既有 `Hub.WaiterCount()`（已存在，纯内存、自带锁）。
- 注册表规模：新增 `Registry.StatusCounts() map[string]int`（持读锁一次遍历、锁内取计数，与 List 同口径深拷贝无关，只计数）。
- 命令队列：新增 `AgentCommandRepository.CountByStatus() map[string]int`（一条 GROUP BY 查询，可移植 GORM、无方言）。

`ObservabilityHandler` 仅组装视图、不碰 GORM / 内存结构细节（经 service 取快照）。`router.go` 仅加一行 `GET /system/observability`。

### 前端

- `api/types.ts` 加 `ObservabilityView`（对齐后端视图）。
- `api/client.ts` 加 `systemObservability()`。
- 新页 `pages/SystemObservabilityPage.tsx`：StatCard 网格分组（DB 连接池 / 长轮询 / 注册表 / 命令队列），短周期轮询刷新；与 FR-32 MC 负载看板、FR-73 服务分析清晰区分（页内标题 + 说明点明「控制面自身健康·只读」）。
- `App.tsx` 加路由 `/system`，`Layout.tsx` 加导航项，`i18n/locales/zh-CN.ts` 加文案，`api/mock/handlers.ts` 加 mock（供前端测试）。

## 4. 任务拆分

- [ ] 后端：`Registry.StatusCounts()` + 单测（红→绿）
- [ ] 后端：`AgentCommandRepository.CountByStatus()`（集成层，随既有命令仓库测试）
- [ ] 后端：`ObservabilityService` + 单测（DBStats / waiter / registry / command 快照）
- [ ] 后端：`ObservabilityHandler` + 单测 + `router.go` 注册
- [ ] 前端：types + client + 页面 + 路由 + 导航 + i18n + mock + 页面单测
- [ ] 文档同步：PRD 状态、API、CHANGELOG

## 5. 验收标准

- `GET /admin/v1/system/observability` 返回 DB 连接池 / longpoll 挂起 / 注册表规模（按状态） / 命令队列（按状态）四组快照；纯读、不落 DB、不参与决策。
- 后端单测覆盖：注册表状态计数、observability 服务聚合（含 DB 连接池透传 / 多 hub waiter 合计 / 命令队列计数）、handler 200 + 字段就位。
- `go build ./... && go test ./...` 全绿。
- 前端新页渲染四组指标，页面单测覆盖（加载 / 数据就位）；`pnpm test` + `pnpm build` 全绿。
- 与 FR-32 / FR-73 区分明确（独立路由 / 导航 / 标题文案）。
- 真机浏览器验：标「待真机浏览器验」（worktree 内跑不了真实部署）。

## 6. 风险 / 待定

- 命令队列计数走一次 DB GROUP BY：低频自观测页可接受，非热路径、无 N+1。
- `sql.DBStats.WaitDuration` 为累计值（自进程起）：直接透传为毫秒，前端标注为「累计」，不做速率换算（YAGNI）。
