# 功能规格：文件树预览全量 + 追踪态（FR-68）

> 状态：开发中　·　关联 PRD：FR-68（增强 FR-45，与 FR-58 共用扫描）　·　纯前端（复用 FR-58 扫描客户端 + FR-45 有效树，无后端/契约改动，无新 ADR）。

## 1. 背景与目标
文件树预览页（FR-45 FilePreviewPage）当前只展示「Beacon 管理的有效文件树」（追踪文件的合并结果）。FR-68 让它能看「当前服务器 `plugins/` 下**所有文件**」并区分**追踪**（Beacon 管）/**未追踪**（磁盘上有、Beacon 没管）——复用 FR-58 的 agent 扫描能力拿全量磁盘清单，与 FR-45 有效树交叉比对。纯前端。

## 2. 需求
- FilePreviewPage 加「全量预览（含未追踪）」能力：对所选在线服触发扫描（复用 FR-58 受管任务 scan），拿全量磁盘文件清单（path+size），与有效树（FR-45 effectiveFiles）交叉比对：
  - 清单文件 ∈ 有效树 → **追踪**（可点开看合并结果/逐键来源，FR-45 既有）。
  - 清单文件 ∉ 有效树 → **未追踪**（仅列 path/size，磁盘有但 Beacon 未管，不可看合并）。
- 追踪/未追踪以 Badge 清晰可分；追踪文件仍显合并结果/来源（FR-45 不变）。

### 不做
- 不改后端/契约（复用 FR-58 createScanTask/getTask/cancelTask + FR-45 effectiveFiles）。不做未追踪文件的编辑/纳管（那是反向抓取 FR-58~60 的事，可在 UI 加「去反向抓取」链接）。

## 3. 设计（纯前端）
- FilePreviewPage（或新子组件）加「全量预览」按钮/开关：选在线服后——
  1. `createScanTask(serverId, ns, {scope:'server', group, target:serverId})`（复用 FR-58/FR-60 客户端）→ 轮询 `getReverseFetchTask(id)` 至 `pending-review`（复用 FR-60 2s 轮询）→ 读 `task.files`（全量清单 path/size，scan 永不失败）。
  2. `effectiveFiles({namespace, serverId, ...})`（FR-45）→ 追踪文件 path 集。
  3. 交叉比对：清单每文件标 `tracked`（path ∈ 有效树）；列全量、Badge 追踪/未追踪；追踪点开复用 FR-45 合并/来源预览（FileEffectivePreview）。
  4. 读完清单后 `cancelReverseFetchTask(id)`（预览只读、不 ingest；避免遗留 pending 任务）。
- 复用：FilePreviewPage/FileEffectivePreview（FR-45）、createScanTask/getReverseFetchTask/cancelReverseFetchTask（FR-58/60 client）、effectiveFiles（FR-45 client）、Badge/ScrollArea/Button/useMessage、FR-60 轮询范式。
- **无 api/types 新增**（上述函数已齐）。
- 未追踪文件项可附「→ 反向抓取纳管」链接跳 `/reverse-fetch`（可选）。

## 4. 任务拆分
- [ ] FilePreviewPage 加「全量预览」：触发 scan → 轮询 → 读清单 → 比对有效树 → 列全量(追踪/未追踪 Badge) → 取消任务。
- [ ] 追踪文件点开复用 FileEffectivePreview；未追踪只读列。
- [ ] 测试：mock createScanTask/getTask/effectiveFiles → 全量列、追踪/未追踪正确分、追踪可看合并、读完调 cancel。
- [ ] doc-sync：PRD FR-68、CHANGELOG、本规格。

## 5. 验收标准
- 全量预览列「plugins 下所有文件」（含未追踪），追踪/未追踪清晰可分；追踪文件仍可看合并结果/来源。
- 前端测试全绿（pnpm test + build）。
- **真机浏览器**（末批）：对 lobby-1 全量预览列全树、ServerProbe 运行时文件等显「未追踪」、AllinCore 等显「追踪」可看合并。

## 6. 风险/待定
- 复用 FR-58 受管任务做预览：受单实例单活跃任务互斥约束（预览扫描期间该服不能并发真反向抓取）；预览读完即 cancel，避免遗留任务（task台会留一条 cancelled 预览任务，可接受）。
- 大树清单渲染：复用既有 ScrollArea 简洁行。
