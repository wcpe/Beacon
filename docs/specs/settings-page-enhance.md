# 功能规格：运维设置页恢复默认 + 批量保存

> 状态：开发中　·　关联 PRD：FR-77（增强 FR-62）　·　分支：feature/fr-77-settings-defaults

## 1. 背景与目标

FR-62 的运维设置页（`web/src/pages/SettingsPage.tsx`）当前只能**逐项编辑、逐项保存**：每项显示中文说明 + 当前值 + 默认值，改一项点一次「保存」、单独 PUT。

实际运维体验有两处摩擦：

- **无「恢复默认」**：运维改坏了某项想回到默认值，只能记住默认值再手输回去（页面虽显示了默认值，但不能一键恢复）。
- **无「批量保存」**：一次想改多项（如同时调健康 TTL 与采样间隔）时，要逐项点保存，点 N 次、来回滚动，且没有「这次一共改了什么」的总览。

本 FR 是 FR-62 的纯前端体验增强（P2），不动后端、不加端点。

## 2. 需求（要什么）

- **每项「恢复默认」按钮**：点击把该项编辑值（草稿）置回该项 `default`；当草稿已等于该项 `default` 时禁用（已在默认值、无可恢复内容）。在常见的「当前值即默认值」场景下，初始草稿 = 当前值 = 默认值，按钮初始即禁用，改动后才启用。仅改本地草稿，不直接落库（仍走保存 / 批量保存）。
- **页脚「保存全部变更（N）」**：把所有「已改且未保存」项（草稿 ≠ 当前值）一次性逐个 PUT（前端循环复用现有 `updateSetting`），无需新后端端点。
  - N 为待保存项数；N=0 或保存中时禁用。
  - 保存中按钮禁用、显示进行态文案。
  - 逐项成功 / 失败汇总提示（全部成功一条成功提示；有失败则提示成功 M 项、失败 K 项，失败不阻断其余项）。
  - 全部完成后刷新设置列表（回显热生效后的值）。
- **「改动摘要」**：列出哪些项将从 X 改成 Y（`key：X → Y`）。顶部与页脚各呈现一处；无改动时不显示摘要、批量按钮禁用。
- 范围内：纯前端组件与 i18n 文案；逐项保存（FR-62 既有）保留不动。
- 不做（范围外）：① 不改后端 / 不加 `PUT /settings/{key}` 以外端点；② 不引入「批量 PUT」后端原子接口（前端循环逐个 PUT 即可，单个失败不影响其余）；③ 不做撤销 / 重置全部草稿等额外能力（YAGNI）。

## 3. 设计（怎么做）

涉及模块：**仅前端**（`web/src/pages/SettingsPage.tsx` + `web/src/i18n/locales/zh-CN.ts`）。复用 FR-61 既有 `PUT /admin/v1/settings/{key}`（前端 `updateSetting`），**不改后端、不动 API 契约**。

- **草稿状态上提**：当前每行（`SettingRow`）自持草稿 `useState`。为让页脚批量保存 / 摘要能看见全部草稿，把草稿集中到 `SettingsPage` 顶层 `Map<key, string>`（以 `listSettings` 返回项的 `key` 为键，初值为各项 `value`）。`SettingRow` 改为受控（接 `draft` + `onChange`）。
  - 列表刷新（保存成功后 `invalidateQueries`）后，对**已非脏**的项把草稿同步到最新 `value`（保留用户仍在编辑的脏项草稿不被覆盖）。
- **每行「恢复默认」**：复用既有 `Button`（`variant="outline"` / `size="sm"`），`onClick` 调 `onChange(item.default)`；`disabled = draft === item.default`（已在默认值则无可恢复）。
- **脏项判定**：`dirty(item) = draft.get(item.key) !== item.value`。复用现有逐项保存的 `dirty` 口径，集中算一次得「脏项列表」。
- **页脚批量保存**：`Button`「保存全部变更（N）」，`disabled = N===0 || saving`。点击后 `for (const it of dirtyItems) { try { await updateSetting(it.key, draft) ; ok++ } catch { fail++ } }`（逐个 await，单个失败计数不抛出），完成后 `invalidateQueries(['settings'])` 刷新，并按 `ok/fail` 出汇总 toast（全成功 `msgBatchSaved`，有失败 `msgBatchPartial`）。saving 态用本地 `useState<boolean>`。
- **改动摘要**：从脏项列表映射为 `key：value → draft` 文本行，渲染在顶部卡片与页脚各一处；脏项为空则不渲染摘要块。
- **i18n**：新增 `settings.resetDefault`（恢复默认）、`settings.saveAll`（含 `{{count}}` 插值）、`settings.savingAll`、`settings.changeSummaryTitle`、`settings.changeSummaryLine`（`{{key}} / {{from}} / {{to}}`）、`settings.msgBatchSaved`、`settings.msgBatchPartial`（`{{ok}} / {{fail}}`）。

## 4. 任务拆分

- [x] 规格本文 + PRD FR-77 状态「计划」→「开发中」
- [ ] 测试先行（vitest + RTL）：恢复默认置回默认 / 值==当前禁用；批量保存逐个 PUT + 汇总；改动摘要列出 X→Y；无改动时批量禁用 / 摘要不显示
- [ ] 实现：草稿上提受控、每行恢复默认、页脚批量保存 + 摘要、i18n 文案
- [ ] 文档同步：PRD（已）、CHANGELOG 未发布段追加一行

## 5. 验收标准

- 点某项「恢复默认」→ 该项编辑控件回显其默认值；草稿已等于默认值时「恢复默认」禁用。
- 改两项后点「保存全部变更（2）」→ 对两项各调一次 `updateSetting(key, 草稿值)`、出汇总成功提示、列表刷新。
- 批量保存中有一项后端 400 → 其余项仍保存、汇总提示成功 M / 失败 K。
- 「改动摘要」按脏项列出 `key：旧值 → 新值`；无脏项时批量按钮禁用、摘要不显示。
- FR-62 既有逐项保存、分组、log.level 下拉、非法值回显等行为不回归（既有单测保持绿）。
- `cd web && pnpm test` + `pnpm build` 绿。

## 6. 风险 / 待定

- 批量保存为前端循环逐个 PUT、**非后端事务**：部分成功是预期语义（单项失败不回滚已成功项），由汇总提示如实反馈，符合「不引入批量原子后端」的范围约束。
- 列表刷新与脏草稿合并：仅对非脏项同步最新 `value`，避免覆盖用户正在编辑的脏项。
