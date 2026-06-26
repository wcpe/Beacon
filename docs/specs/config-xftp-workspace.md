# 功能规格：配置中心双面板 Xftp 工作台真链路（FR-111）

> 状态：开发中　·　关联 PRD：FR-111（依赖 FR-110/109，见 ADR-0050）　·　前序：FR-114（原型）/ FR-115（前端全量测试 + mock 收口）

## 1. 背景与目标

配置中心的双面板 Xftp 工作台在 FR-114 已落地**纯前端原型**，由一组假端点 `/admin/v1/workbench/*`（mock，见 `web/src/api/mock/workbench.ts`）喂数据，只验交互、不接真链路。FR-115 为工作台各组件补齐了 vitest 单测（hook 层用 `vi.mock` 注入），为接真后端铺路。

本 FR 把工作台**从 mock 改接已有真实端点**，让「左受管树 / 右实时浏览 / 生效预览 / 同步队列」反映真实事实。范围属 P2。

## 2. 需求（要什么）

- **范围内**：
  - 工作台数据 hook（`web/src/pages/configs-workbench/useWorkbenchData.ts`）从 `/admin/v1/workbench/*` mock 改接 **ADR-0050 决策 2 映射表**里的既有真实端点，前端在客户端做编排 / 适配（薄编排，不造聚合 BFF）。
  - 右面板「某在线服实时浏览真实 plugins」接 **FR-110 浏览端点**（`GET /admin/v1/instances/{serverId}/browse`，懒列 / 读文件）。
  - 退役 `/admin/v1/workbench/*` mock 路由 + 被真 API 类型取代的 mock 数据。
  - 保持 dev（mock 模式）可用：改接的端点 mock 已服务的（config/effective/反抓/拓印/命令）天然可用；FR-110 浏览端点 mock 未服务，补一个**最小 mock handler**让 dev 右面板可浏览。
  - 同步 FR-115 测试：hook 改接真 api 后，测试注入层（`vi.mock` hook）与类型 import 源相应更新，**测试仍全绿**（组件契约尽量不破）。
- **不做（范围外）**：
  - **不造 `/admin/v1/workbench/*` 聚合 BFF**（ADR-0050 决策 2 已否决）。
  - **不改后端契约**（除已落的 FR-110 浏览端点）。
  - FR-112 编辑器真路由（`/configs/:id` 真详情）——下一 FR。
  - FR-113 三页合一 / 退役 ConfigsPage——再下一 FR。
  - 撤回 / 回滚真后端（操作日志可逆快照 + 撤回端点）——属 **FR-116（ADR-0051）**，本 FR 不做。

## 3. 设计（怎么做）

纯前端改动。核心是把 `useWorkbenchData.ts` 的 9 个 hook 由「直接 fetch mock `/workbench/*`」改为「调 `api/client` 真函数 + 客户端适配为既有视图形状」。视图类型从 `mock/workbench.ts` 抽到 `configs-workbench/types.ts`（真源），组件 / 测试改 import 源；这样删 mock 数据不破坏类型依赖。

### 3.1 能力 → 真实端点映射（落实 ADR-0050 决策 2）

| 工作台能力（hook） | 接的既有真实端点（`api/client`） | 适配说明 |
|---|---|---|
| 左受管树 `useManagedTree` | `listFiles({namespace, group})`（FR-14） | 把扁平文件清单装配成 `ManagedNode` 树；同步状态由与右侧实时浏览交叉算（部分，见 §6） |
| 右实时浏览 `useServerTree` | **`browse(serverId, ns, {op:'tree'\|'list'})`**（FR-110） | 把 agent 浏览结果（`entries`/`children`）映射成 `ServerNode`；懒列 vs 一次性树取 `op` 形态 |
| 生效预览 `useEffectivePreview` | `effectiveFiles({namespace, serverId})`（FR-45） | `files[].sources`（逐键 path[]+scope）→ `EffectiveFile.keys[].chain` 覆盖链 |
| 受管文件 + 历史 `useWorkbenchFile` | `getFile(id)` + `listFileRevisions(id)`（FR-14/67） | 文件 key↔fileId 解析；revisions → `WorkbenchRevision` |
| scope/server 候选 `useWorkbenchOptions` | `listInstances({namespace})` + 组 / zone（FR-3/16） | 在线实例 → `ServerOption`；覆盖层 → `ScopeOption` |
| 同步队列 `useSyncQueue` | `listCommands({namespace, status})`（FR-104） | `agent_command` 生命周期 → `SyncQueueRow`（pending/fetched/done/failed） |
| ingest 清单 `useIngestScanList` | 反向抓取受管任务 `getReverseFetchTask(id).files`（FR-58~60） | 任务清单 → `IngestScanItem`；ignoredByRule → ignored |
| 发布影响面 `usePublishImpact` | `impactPreview({namespace, scopeLevel, ...})`（FR-79） | 受影响在线服集合 → `PublishImpact.groups/driftCount` |
| 操作日志 `useOperationLog` | 见 §6（无专属端点，FR-116 territory） | 接既有审计读视图或维持本地态，标 partial |

### 3.2 退役 mock

- `mock/handlers.ts`：删 `/admin/v1/workbench/*`（managed-tree / server-tree / sync-queue / operation-log / options / files / ingest-scan / effective / publish-impact）路由分发。
- `mock/workbench.ts`：删被真 API 取代的 mock **种子数据**（树 / 队列 / 日志 / 候选 / 文件内容 / ingest 清单 / 生效预览 / 影响面解析）；视图**类型**迁 `types.ts`。
- 新增 FR-110 浏览端点最小 mock handler（list/tree/file 三态示意），让 dev 右面板可浏览。

### 3.3 类型迁移

新增 `web/src/pages/configs-workbench/types.ts` 承载工作台视图类型（真源）；`mock/workbench.ts`（若保留）、组件、测试均从 `types.ts` import。

## 4. 任务拆分

- [x] PRD §4 FR-111 行 计划→开发中
- [x] 写本 spec
- [ ] 抽视图类型到 `configs-workbench/types.ts`，组件 / 测试改 import 源
- [ ] `api/client.ts` 加 `browse()` 真函数；`mock/handlers.ts` 加 FR-110 浏览最小 mock
- [ ] rewire `useWorkbenchData.ts` 9 hook 接真端点 + 客户端适配
- [ ] 退役 `/admin/v1/workbench/*` mock 路由 + 被取代的 mock 数据
- [ ] 同步 FR-115 测试，`cd web && pnpm test` + `pnpm build` 全绿
- [ ] 文档同步：PRD 状态、ARCHITECTURE（工作台真链路）、CHANGELOG 未发布段

## 5. 验收标准

- 工作台不再依赖 `/admin/v1/workbench/*` mock；各 hook 调既有真实端点（ADR-0050 映射表逐条落实），不新造聚合 BFF、不改后端契约（除 FR-110）。
- dev（mock 模式）下 `/configs` 仍可渲染：右面板经 FR-110 浏览最小 mock 可浏览；其余视图经既有 mock 端点出数据。
- `cd web && pnpm test` 全绿、`pnpm build` 绿。
- **真机维度**：真实控制面 + 真机 agent 端到端——浏览 / 抓取 / 下发 / 生效预览来源真实。本会话无真机能力 → 标「待真机验」。

## 6. 风险 / 待定

- **操作日志 / 撤回无真后端**：工作台「操作日志 + 逐条 / 批量撤回」（FR-114 原型）的真实可逆能力属 **FR-116（ADR-0051）**，本 FR 不做。`useOperationLog` 在本 FR 维持「读既有事实 + 本地态」，撤回为前端态，标 partial。
- **同步状态交叉算**：左受管树各文件的「synced/drift/managed-only/server-gone」需与右侧实时浏览逐文件比对，懒列浏览下无法一次性算全；本 FR 以可得信息标注，完整比对依赖右面板已浏览的子树。标 partial。
- **拖拽真写流程**：右→左抓取走反抓受管任务（多步状态机）、左→右下发走拓印自审门（多步 diff/confirm），原型期为本地入队 + 浮层示意。本 FR 重点是**读链路接真 + 浏览接真**；写流程的端到端真链路触发在真机维度验证，浮层数据接真任务 / diff。

---

## 7. FR-112：配置文件真详情多标签编辑器（增量）

> 状态：开发中　·　关联 PRD：FR-112（增强 FR-23，依赖 FR-111，见 ADR-0050 决策 3）

### 7.1 背景与目标

FR-111 让工作台数据接真后端；双击文件此前仅在工作台页内开**悬浮覆盖编辑器**（`EditorOverlay`），`/configs/:id` 只是「同页内最大化恢复该文件」的深链。FR-112 按 ADR-0050 决策 3 把 `/configs/:id` 升级为**真子路由**——一个聚焦编辑单个受管文件的真详情页 `ConfigEditorPage`，保留多标签横切 + Monaco diff / 历史 + 保存确认（FR-67）+ 局部面包屑 / 返回，且**保存接既有配置更新 API 真落库**。

### 7.2 设计（怎么做）

纯前端改动，不改后端契约。

- **路由**：`web/src/App.tsx` 把 `/configs/:id` 从渲染 `ConfigWorkbenchPage`（页内浮层恢复）改为渲染新页 `ConfigEditorPage`。`/configs` 仍为双面板工作台。
- **进入方式**：工作台双击文件 → `navigate('/configs/<encodeURIComponent(key)>')` 进真详情页（替代原页内浮层）。工作台右键菜单「编辑 / diff / 回滚」仍保留页内浮层（轻量快捷编辑，非整页跳）。
- **`ConfigEditorPage`**（`web/src/pages/ConfigEditorPage.tsx`）：
  - 活跃文件 key = URL `:id` 解码；多标签随访问的 `:id` 累积（去重），切换标签 = 换 URL `:id`，关标签跳邻 / 回 `/configs`。
  - 数据复用 `useWorkbenchFile`（FR-111 已接真 `getFile` + `listFileRevisions`）；`WorkbenchFile` 增 `fileId` 字段供保存按 id 调既有文件 API。
  - 历史修订面板：点历史版本 → Monaco DiffEditor（左=选定历史版本内容，右=当前编辑态）；再点取消 diff 回编辑。
  - **保存（FR-67）**：点保存 / Ctrl+S **不直接发布**，先弹既有 `ConfigSaveConfirmDialog`（看 diff + 填备注 + 影响面预览），确认才调既有 `publishFile(id, content, comment)`（`PUT /admin/v1/files/:id`）真发布新版本；成功清脏 + 失效查询重拉。
  - 局部面包屑（注入页眉标题槽）：`配置中心 / 环境·组 / 文件名`，「配置中心」为返回 `/configs` 链接。
- **dev mock**：`api/mock/handlers.ts` 补 `PUT /admin/v1/files/:id` 最小 handler（`saveMockFile` 写回内存 + 版本自增，返回 `PublishResult`）；mock 文件补 `content` 种子让 dev 编辑器非空。

### 7.3 验收标准

- `/configs/:id` 为真子路由（渲染 `ConfigEditorPage`，非工作台页内浮层）；双击工作台文件进该路由。
- 多标签横切、Monaco diff / 历史、保存确认（FR-67）均不回归；保存接既有 `publishFile` 真落库（dev 经 mock PUT 可用）。
- 局部面包屑 / 返回可用。
- `cd web && pnpm test` 全绿（既有 `EditorOverlay` 测试不回归 + 新增 `ConfigEditorPage` 路由 / 保存确认测试）+ `pnpm build` 绿。
- **真机维度**：真实控制面下编辑 → 保存确认 → 真落库 + 出新版本 + 历史可见。本会话无真机能力 → 标「待真机验」。
