# 功能规格：按需拓印回写 + 前端审核台

> 状态：开发中　·　关联 PRD：FR-46　·　分支：feature/fr-46-imprint-review-console

## 1. 背景与目标

FR-39（已交付@v0.7.0）做了「在线实例反向抓取」：admin 命令一台在线 agent 读其真实 `plugins/` 文本配置回传，控制面经 `FileService.Import` **直接 ingest 落库**为组 / 实例级文件树覆盖（复用 FR-38 通道B，见 [ADR-0027](../adr/0027-reverse-fetch-channel-and-security.md)）。它是「一键把现网某台当模板灌进来」，但**没有人工 diff 审核环节**——抓上来即落库，运维看不到「本地实际值 vs 控制面期望合并值」的差异、也无法选择把差异并入哪一层。

FR-44（[ADR-0029](../adr/0029-file-tree-structured-deep-merge.md)）给通道B 接上了键级深合并、FR-45 给通道B 接上了 admin 只读有效预览 + 逐键来源（`GET /admin/v1/files/effective`）。两者合起来，正好补齐了「期望合并值」一侧的数据源。

FR-46 把 FR-39 升级为**可控审核台**：admin 选一台在线服 + 一个文件 → agent 回传**该文件当前真实磁盘内容**（事实）→ 控制面把**本地实际内容**与**期望合并值**（FR-45 某文件合并结果）做 **diff** → admin 选**并入哪一层**（单服 / 小区 / 大区 / 全局）+ 预览将入库内容 → **单人自审门**（强制看过 diff 才能确认）→ 确认后复用既有 `FileService.Create/Publish` 落为该层覆盖 → 走既有文件长轮询 / SSE 正常下发。前端把 FR-23 的配置中心从「重编辑器」**降级保留（仅小修）**、新增一个「diff + 单人自审 + 同步」的轻量审核台页。属 P2 治理增强，依赖 FR-44 + FR-45。

## 2. 需求（要什么）

- 范围内：
  - **拓印抓取（复用 FR-39 命令通道、零 agent 改动）**：admin 触发对某在线 serverId 的「拓印某文件」→ 复用 `agent_command` 的 `ingest-plugins` 类型 + SSE `command-pending` 唤醒 + agent 既有「读真实 `plugins/` 树并回传」能力（[ADR-0027](../adr/0027-reverse-fetch-channel-and-security.md)）。命令载荷加 `mode=imprint` + 目标 `path`；agent **完全不变**（仍拉 `ingest-plugins`、读整棵 `plugins/` 树、回传 `/files/ingest`）。
  - **拓印态（不落库、转存待审）**：控制面收到 `mode=imprint` 的回传时**不 ingest 落库**，而是同口径再校验（上限 / 排除 jar / path）后，从回传文件集中**取出 admin 指定的那个 `path`** 的内容**转存到命令记录**（瞬态 `imprint_content` 列），命令转新状态 `ready`（已抓取、待审核确认）。指定 path 不在回传树中 → 命令 `failed`（该文件磁盘上不存在）。
  - **diff（本地实际值 ⟷ 期望合并值）**：admin 拉 `GET /admin/v1/imprints/{commandId}/diff?scope=&group=&zone=&target=`——命令须 `ready`，返回 `path` / 本地实际内容（命令转存的磁盘内容 + md5）/ 期望合并值（复用 FR-45 `FileEffectiveService.ResolveWithProvenance` 对该 (scope 解出的 group/zone/server) 解出该 path 的合并 `content` + md5 + 逐键来源）/ 是否有差异。
  - **定层并入 + 预览**：admin 选并入层（`server`/`zone`/`group`/`global`）+ 目标键（zone 编码 / serverId），diff 端点据此解出该层视角的「期望合并值」做对照；**将入库内容 = 本地实际内容**（整文件覆盖该层）。
  - **单人自审门**：`POST /admin/v1/imprints/{commandId}/confirm` body 带 `reviewedMd5`——**必须等于命令转存内容的 md5**（看过 diff 才能拿到该 md5；盲确认拿不到正确 md5 即被拒）。**非多人审批、不引入「变更请求」实体 / 状态机**——仅一道自审门。
  - **落库 + 下发**：确认通过后复用既有 `FileService.Create`（该层 path 首次）或 `Publish`（该层 path 已存在则发新版本）落为**该层文件覆盖**，写 `file.imprint` 审计，清空命令瞬态内容、命令转 `done` → 既有文件长轮询 / SSE 正常下发（控制面零新增下发路径）。
  - **审计**：触发拓印记 `file.imprint-fetch`、确认落库记 `file.imprint`（operator / 目标层 / path / md5，**detail 不含文件内容**，沿 ADR-0027 决策 7）。
  - **运行时数据文件排除**：沿用 FR-39 安全面（agent 限 `plugins/`、排除 `.jar` / 二进制、上限、双校验）；落库前 `validateFileContent`（结构化文件解析校验）拒坏内容，运行时数据（`.db` 等二进制）本就被 agent 过滤 + 控制面再校验排除。
  - **前端审核台**：web 配置中心转「diff + 单人自审 + 同步」轻量台——选在线服 + 文件 → 触发拓印 → 轮询命令至 `ready` → 展示 diff（本地实际值 ⟷ 期望合并值，复用 FR-45 逐键来源徽标 / Monaco DiffEditor）→ 选并入层 + 预览 → 确认同步。**FR-23 Monaco 重编辑器降级保留（仅小修，不做重量级在线编辑）**。
- 不做（范围外，守边界）：
  - **不做全自动 / 后台双向同步**：改动必经控制面人确认（守架构不变量 #1，不退化为「控制面在服务器上执行」）。
  - **不引入多人审批 / 变更请求实体 / 状态机**：单人自审门即可（`reviewedMd5` 匹配）。
  - **不抓运行时数据文件**（`.db` / 玩家数据等）：沿 FR-39 排除。
  - **不碰 agent 读盘安全边界**：沿 FR-39（限 `plugins/`、排除 jar / 二进制、上限、双校验）；**agent 零改动**。
  - 不做分块传输、不做拓印历史台账（命令记录即可追溯，瞬态内容用后即清）。
  - 不新增 ADR（沿用 ADR-0027 通道 + ADR-0013/0029 合并 / provenance）。

## 3. 设计（怎么做）

**无需新 ADR**：拓印抓取沿 [ADR-0027](../adr/0027-reverse-fetch-channel-and-security.md)（命令通道与安全面）、diff「期望合并值」沿 [ADR-0013](../adr/0013-admin-effective-config-preview-and-provenance.md) / [ADR-0029](../adr/0029-file-tree-structured-deep-merge.md)（合并与逐键来源）、落库沿 [ADR-0010](../adr/0010-file-tree-hosting-blob-channel.md)（通道B 整文件覆盖）。本 FR 是这三者的编排 + 一道自审门，未引入新架构决策。

### 3.1 命令模型扩展（向后兼容）

`agent_command` 表（`model/agent_command.go`）新增一列、`enums.go` 新增一个状态值，AutoMigrate 自动补列（新列可空、旧行不受影响）：
- `ImprintContent string`（`column:imprint_content;type:text`，**瞬态**）：`mode=imprint` 回传后转存的**单个目标文件磁盘内容**，仅供审核 diff 与确认落库；**确认 / 失败 / 过期后即清空**。与 `ResultDetail`（结果摘要、绝不含文件内容）分立——`ImprintContent` 是受控的瞬态审核暂存，持有一个文件、生命周期到确认即止，比 FR-39 永久落整棵树**数据暴露更少**。
- `CommandStatusReady = "ready"`（新状态）：`mode=imprint` 已抓取、待审核确认。生命周期：`pending → fetched → ready →（确认）done /（自审失败仍 ready 可重确认）/（超时）expired`。
- 命令载荷 `ingestPayload` 加两字段：`Mode string`（空 / `land` = FR-39 直接落库；`imprint` = FR-46 转存待审）、`Path string`（`imprint` 模式下 admin 指定的目标文件相对 path）。

### 3.2 控制面 service（`AgentCommandService`）

- `RequestImprint(ns, serverID, path, operator, clientIP)`：事务内建 `pending` 命令（载荷 `mode=imprint` + `path`）+ `file.imprint-fetch` 审计（target=命令，detail 含 path）；提交后 `NotifyCommand` 唤醒该 agent SSE。与 `RequestReverseFetch` 平行、复用同一仓库 / 唤醒器。`path` 须非空且经 `normalizePath` 合法。
- `ReceiveIngest`（既有，扩展分流）：解析载荷后按 `payload.Mode` 分流——
  - 非 `imprint`（FR-39）：维持原样，`Import` 落库 + `done`。
  - `imprint`（FR-46）：**不套** FR-39 的整批数量 / 总量闸（那为整批落库设、会误伤大插件目录下的单文件拓印）；从回传集找 `payload.Path` 的内容——找到则由 `transferImprint` 对目标单文件兜底（排除 `.jar`、限单文件大小）后转存命令 `imprint_content` + CAS `fetched → ready`（不落 `file_object`、不记 `file.import` 审计）；找不到则 `failed`（磁盘无此文件）。
- `ImprintDiff(commandID, scope, group, zone)`：命令须 `ready` 且 `mode=imprint`；调 `FileEffectiveService.ResolveWithProvenance` 解出**期望合并值**（恒为拓印源服有效视角，取 `payload.Path` 那个文件的合并 `content`/md5/来源），与命令转存的**本地实际内容**组装为 diff 结果。不取 `target`：期望视角由源服身份决定、与确认落库的目标键无关。`FileEffectiveService` 经构造注入（service 间依赖，不经 handler）。
- `ConfirmImprint(commandID, scope, group, zone, target, reviewedMd5, operator, clientIP)`：命令须 `ready` 且 `mode=imprint`；**自审门**——`reviewedMd5` 须等于命令转存内容 md5，否则 `ErrImprintReviewMismatch`（412）。`scope=server` 时 `target` 须等于命令源服 serverId（只能落回源服自身，挡跨服 / 跨 ns 悬空覆盖）。过门后**先 CAS `ready → done` 认领并清空 `imprint_content`**（赢者独占、挡并发双确认），再复用 `FileService.Create`（该层 path 不存在）或 `Publish`（已存在）落该层覆盖（事务由 FileService 内部保证），最后写 `file.imprint` 审计。落库即触发 FileService 既有 notify / git 导出。

> 自审门的「强制看过 diff」由 `reviewedMd5` 实现：diff 端点返回本地实际内容 md5，confirm 必须回带同一 md5。不调 diff 端点、盲构造 confirm 拿不到正确 md5 → 412 拒。**单人即可**（无第二人、无审批状态机）。

### 3.3 控制面 handler / router

- `CommandHandler` 注入 `FileEffectiveService`，新增：
  - `Imprint`（`POST /admin/v1/instances/{serverId}/imprint?namespace=`，body `{path}`）：校验目标在线 → `RequestImprint` → 202 + 命令视图。写操作，readonly 经 `readonlyWriteGuard` 403。
  - `ImprintDiff`（`GET /admin/v1/imprints/{commandId}/diff?scope=&group=&zone=`）：返回 diff 视图（本地实际值 + 期望合并值 + 逐键来源 + 是否有差异）。
  - `ConfirmImprint`（`POST /admin/v1/imprints/{commandId}/confirm`，body `{scope, group, zone, target, reviewedMd5}`）：自审 + 落库 → 200 + 落库结果（层 / version / md5）。写操作，readonly 403。
- 路由（`server/router.go` admin 组）：`r.Post("/instances/{serverId}/imprint", ...)`（紧邻 reverse-fetch）；`r.Get("/imprints/{commandId}/diff", ...)` + `r.Post("/imprints/{commandId}/confirm", ...)`。
- `apperr`：新增 `ErrImprintReviewMismatch`（412 `IMPRINT_REVIEW_MISMATCH`，自审 md5 不匹配）、`ErrImprintNotReady`（409 `IMPRINT_NOT_READY`，命令非 ready 态不可 diff/confirm）。

### 3.4 agent

**零改动**。agent 仍只认 `ingest-plugins`、读整棵 `plugins/` 树、回传 `/files/ingest`；`mode` 分流纯在控制面。回传整棵树取单文件是 PRD FR-46 明示许可的「复用整目录抓取后取单文件」路径，省去 agent 单文件读取的新平台能力与不可在本机验证的 agent 改动。

### 3.5 前端（审核台 + 客户端）

- `api/client.ts` + `types.ts`：新增 `triggerImprint(serverId, ns, {path})`、`imprintDiff(commandId, {scope,group,zone,target})`、`confirmImprint(commandId, {scope,group,zone,target,reviewedMd5})` 及对应类型；dev mock handler 平行补齐。
- 审核台页（在配置中心页内或独立页）：选在线服 + 文件 path → 触发拓印 → `useQuery` 轮询命令状态至 `ready` → 拉 diff → Monaco `DiffEditor`（左期望合并值、右本地实际值）+ FR-45 逐键来源徽标 → 选并入层（server/zone/group/global）+ 目标键 → 预览将入库内容（= 本地实际内容）→ 确认（带 diff 返回的 `reviewedMd5`）→ 成功提示 + 失效相关缓存。
- **FR-23 Monaco 降级**：配置中心重编辑器只做必要小修以容纳审核台入口，不扩重量级编辑。

## 4. 任务拆分

- [ ] 规格（本文）+ PRD FR-46 状态「计划」→「开发中」。
- [ ] `model`/`enums`：`agent_command.imprint_content` 列 + `CommandStatusReady` + 载荷 `mode`/`path`（含 AutoMigrate；单测可选）。
- [ ] `apperr`：`ErrImprintReviewMismatch` / `ErrImprintNotReady`。
- [ ] `service`：`RequestImprint` + `ReceiveIngest` 分流 + `ImprintDiff` + `ConfirmImprint`（穷举单测先行：拓印转存不落库 / 指定 path 缺失 failed / diff 取期望合并值 / 自审 md5 匹配与不匹配 / 确认落库该层 + 清空瞬态 + done / 非 ready 拒）。
- [ ] `handler`/`router`：三端点 + 视图 + 参数校验 + 在线校验单测。
- [ ] 前端：客户端 + 类型 + 审核台页 + Monaco DiffEditor + mock + vitest。
- [ ] 文档同步：ARCHITECTURE（§8 命令通道拓印分流 + 自审门）、API（三新端点）、CHANGELOG 未发布段**末尾追加**一行。

## 5. 验收标准

- admin 对某在线 serverId + 文件 `AllinCore/config.yml` 触发拓印 → 命令 `pending`；agent 回传整棵树（mock）→ 控制面取该 path 内容转存、命令转 `ready`、**不落 `file_object`**、**不记 `file.import`**。
- 指定 path 磁盘上不存在（回传树无）→ 命令 `failed`，不转存。
- `GET .../imprints/{id}/diff?scope=server&group=area1&target=lobby-1`：返回本地实际内容 + 该 server 视角期望合并值（FR-45 解析）+ 逐键来源 + `differs` 标志；命令非 `ready` → 409 `IMPRINT_NOT_READY`。
- `POST .../imprints/{id}/confirm` 带正确 `reviewedMd5` → 落为所选层 path 覆盖（首次 `create`、已存在 `publish`），写 `file.imprint` 审计，命令转 `done`、`imprint_content` 清空；下游文件长轮询 / SSE 据覆盖链生效（控制面零新增下发路径）。
- confirm 带**错误 / 缺失 `reviewedMd5`** → 412 `IMPRINT_REVIEW_MISMATCH`，不落库（自审门生效）。
- readonly 密钥 / 角色触发拓印或确认 → 403。
- 触发拓印记 `file.imprint-fetch`、确认落库记 `file.imprint`；detail 均**不含文件内容**。
- 前端审核台：选服 + 文件 → 触发 → 轮询至 ready → DiffEditor 展示期望 ⟷ 实际 → 选层 + 预览 → 确认；vitest 绿。
- 受影响组件测试全绿（`go test ./...` + `cd web && pnpm build && pnpm test`）。真机维度（拓印需在线 agent 真机回传）如实标「待真机验」。

## 6. 风险 / 待定

- **瞬态内容暴露**：`imprint_content` 持有一个文件的磁盘原文待审——比 FR-39 永久落整棵树**暴露更少**，且确认 / 失败 / 过期即清；不入审计 detail、不导出 git（命令表不在 FR-47 导出源层内）。仍属把现网真实配置搬入控制面库，运维须知情（同 FR-39）。
- **整棵树回传取单文件的浪费**：为零 agent 改动接受（PRD 明示许可），受 FR-39 上限约束；单文件 agent 读取属后续优化、本期不做。
- **自审门强度**：`reviewedMd5` 钉死「确认的内容 = 看过的内容」，杜绝盲确认与抓取后磁盘漂移；但它是单人门、非合规级多人审批（PRD 明确不做）。
- **期望合并值随覆盖链动态**：diff 端点按 admin 选的层实时解析（不快照），确认到落库间若他人改了其它层，落库后的最终合并值可能与 diff 时不同——本地实际内容（覆盖该层那一份）仍是 admin 审过的；合并结果天然随覆盖链演进，符合通道B 语义。
