# 功能规格：反向抓取审核台 + 任务台 + 冲突 diff（前端，FR-60）

> 状态：开发中　·　关联 PRD：FR-60（增强 FR-46，依赖 FR-58/FR-59）　·　沿用 [ADR-0037](../adr/0037-reverse-fetch-managed-task.md) + 复用 FR-46 拓印审核台范式，无新 ADR。纯前端（消费 FR-58/59 后端端点）。

## 1. 背景与目标

FR-58/59 把反向抓取做成受管任务（两段式 scan/submit + conflict-review + 忽略规则），但只有后端端点、无 UI。FR-60 建前端把整套工作流变可用：**任务台**（建任务 + 历史 + 状态/进度）+ **审核台**（扫描清单全量列 + 超阈值红标须确认 + 逐项/目录忽略[临时 + 保存持久规则] + 提交选定）+ **冲突 diff 确认**（目标已有版本 diff + 逐文件保留哪份）。最大化复用 FR-46 拓印审核台范式。

## 2. 需求（要什么）
- **任务台**：建扫描任务（选在线源 + scope[group/server] + group + target）；任务历史列表（按 ns/serverId/status 筛）；任务状态/进度可见（状态机 scanning→pending-review→fetching→conflict-review→ingesting→done/failed/cancelled/expired）；可取消。
- **审核台**（任务 pending-review 时）：扫描清单**全量列**每文件（path/size/isText/overThreshold/ignoredByRule）；**超阈值红标**、纳入须勾确认（confirmOverThreshold）；逐项 + 目录（前缀）忽略（临时勾除 + 可「保存为持久规则」）；命中持久规则的 `ignoredByRule` 默认排除可见；选定后提交。
- **冲突审核**（任务 conflict-review 时）：列冲突 path；逐文件 diff（抓取值 ⟷ 已有版本，Monaco DiffEditor）；逐文件决定「取新值(overwrite，须审阅自审)/保留已有(keep)」；resolve 落库。
- **忽略规则管理**：列/建/删持久规则（ns/scope/group/target，ruleType **exact/prefix**，pattern）。
- 轮询任务状态至终态/待人工态（复用 ImprintPage 2s 轮询范式）。

### 不做（范围外）
- 后端不改（FR-58/59 已落地）。不改 agent。
- 旧 FR-39 一次性 `ReverseFetchDialog`（configs 页）可保留或在 ConfigsPage 入口指向新任务台——本期**新增任务台页**为主，旧对话框可留（不强删，避免牵动 ConfigsPage 过多）。

## 3. 设计（怎么做）

### 3.1 页面与路由
- 新页 `web/src/pages/reverse-fetch/ReverseFetchTaskPage.tsx`（路由 `/reverse-fetch`，App.tsx 注册；Layout.tsx nav 加 `nav.reverseFetchTask`「反向抓取」）。
- 子面板（拆分，便于测试）：
  - `ReverseFetchTaskTrigger.tsx`：建任务（选在线 bukkit 源 + scope + group + target）→ `createScanTask`。复用 ImprintTrigger 的在线实例选择范式。
  - `ReverseFetchTaskList.tsx`：任务历史（`listReverseFetchTasks` + 状态 Badge + 进度 + 取消/查看）。
  - `ReverseFetchReviewPanel.tsx`：pending-review 任务的审核清单（全量列 + 复选 + 超阈值红标确认 + 逐项/目录忽略 + 保存规则 + 提交）。复用 FileEffectivePreview 卡片/ScrollArea 范式。
  - `ReverseFetchConflictPanel.tsx`：conflict-review 任务的冲突 diff（`listConflicts` + 逐 `conflictDiff` + Monaco DiffEditor + 逐文件 overwrite/keep + 审阅自审 + `resolveConflicts`）。复用 ImprintDiffPanel 的 DiffEditor + 自审闸范式。
  - `ReverseFetchIgnoreRulePanel.tsx`：忽略规则列/建/删。
- 选中任务后按其 status 决定显示哪个面板（pending-review→审核台、conflict-review→冲突台、其余→只读状态/进度）。

### 3.2 API 客户端 + 类型（`web/src/api/client.ts` / `types.ts`）
按后端 handler JSON 形状新增（**ruleType 取值为 `exact`/`prefix`，不是 glob/regex**）：
- `createScanTask(serverId, namespace, {scope,group,target})` → `ReverseFetchTaskView`
- `listReverseFetchTasks({namespace?,serverId?,status?})` → `ReverseFetchTaskView[]`（后端响应 `{items:[]}`，client 取 items）
- `getReverseFetchTask(id)` / `cancelReverseFetchTask(id)` → `ReverseFetchTaskView`
- `submitReverseFetchTask(id, {selectedPaths, confirmOverThreshold})` → `ReverseFetchTaskView`
- `listConflicts(id)` → `{conflicts:string[]}`
- `conflictDiff(id, path)` → `ConflictDiffView{path,fetchedContent,fetchedMd5,existingContent,existingMd5,version}`
- `resolveConflicts(id, {decisions:[{path,action:'overwrite'|'keep',reviewedMd5?}]})` → `{created,updated}`
- `listIgnoreRules({namespace,scope?,group?,target?})` / `createIgnoreRule({namespace,scope,group,target,ruleType,pattern,comment})` / `deleteIgnoreRule(id)`
- 类型：`ReverseFetchTaskView`（含 `files: ReverseFetchScanFileView[]{path,size,isText,overThreshold,ignoredByRule}`、status 全枚举含 `conflict-review`）、`ConflictDiffView`、`IgnoreRuleView`、`ResolveDecision`。

### 3.3 关键交互
- **超阈值确认**：清单中 overThreshold 文件红标（Badge destructive）；若选定集含超阈值文件，须勾「确认纳入超阈值」才可提交（submit 带 `confirmOverThreshold:true`）；否则前端拦/后端 400。
- **逐项/目录忽略**：清单行可「忽略」（临时不选）；目录忽略 = 前缀（选中某目录前缀下全部）；「保存为持久规则」→ `createIgnoreRule`（exact 单文件 / prefix 目录）。命中持久规则的行 `ignoredByRule=true` 默认不选、灰显可见。
- **冲突 diff 自审**：每冲突文件 DiffEditor（左抓取值/右已有），勾「我已审阅」后该文件可选 overwrite（resolve 带 `reviewedMd5=fetchedMd5`）或 keep；全部冲突有决定才能 resolve。复用 ImprintDiffPanel 自审 md5 门范式（盲确认后端 412）。
- 轮询：选中任务每 2s `getReverseFetchTask` 刷新至 pending-review/conflict-review/done/failed/cancelled/expired。

### 3.4 nav / i18n
- Layout.tsx nav 加项；i18n 新增 `reverseFetchTask` 命名空间（触发/列表/审核/冲突/规则各环节文案，中文）；`nav.reverseFetchTask='反向抓取'`。

## 4. 任务拆分
- [ ] api/client.ts + types.ts 新增上述函数与类型（对齐后端 JSON，ruleType=exact/prefix）。
- [ ] ReverseFetchTaskPage + 5 子面板（trigger/list/review/conflict/ignore-rule）。
- [ ] App.tsx 路由 + Layout.tsx nav + i18n 命名空间。
- [ ] vitest 测试（trigger 调 createScanTask 入参；review 超阈值未确认拦 + 提交入参；冲突 diff 审阅自审门 + resolve 入参；忽略规则建/删）。
- [ ] doc-sync：PRD FR-60、CHANGELOG、本规格。（API.md 无新端点——端点 FR-58/59 已记。）

## 5. 验收标准
- 任务台：建扫描任务、列任务历史、看状态/进度、取消。
- 审核台（pending-review）：列全树清单（path/size/超阈值红标/ignoredByRule）；超阈值须勾确认才纳入；逐项/目录忽略实时；保存持久规则；提交选定。
- 冲突审核（conflict-review）：列冲突 + 逐文件 diff；审阅后 overwrite（自审）/keep；resolve 落库。
- 忽略规则可列/建/删。
- 前端测试全绿（`cd web && pnpm test && pnpm build`）。
- **真机浏览器**：对 lobby-1 扫描→审核清单（含超阈值红标 ServerProbe 文件）→忽略运行时垃圾→提交小配置→（制造冲突则）冲突 diff→resolve→落库；任务台可查历史。

## 6. 风险 / 待定
- 大清单（579 文件）渲染：用 ScrollArea + 简洁行，必要时虚拟滚动（暂不引库，先简洁行）。
- 真机全链验在 FR-60 落地后做（重建 binary 含新 UI、重部 beacon-cp；agent 不变不重部）。
