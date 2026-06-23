# 功能规格：配置变更审批化（FR-66 上传预览审批 + FR-67 在页编辑确认）

> 状态：开发中　·　关联 PRD：FR-66（增强 FR-38）、FR-67（增强 FR-1/3/18）　·　纯前端（现有 API 前加预览/确认步，无后端/契约改动，无新 ADR）。

## 1. 背景与目标
配置上传（FR-38）选完文件直接入库、在页编辑（FR-1/3）改完直接发布——都缺「提交前看一眼」的防误步。FR-66 给上传加**预览待传内容 → 审批确认才入库**；FR-67 给单文件在页编辑加**轻量确认步（看 diff + 备注 → 确认才发布）**；批量/上传走 FR-66 的预览审批（重），在页单改走 FR-67 确认（轻）。纯前端。

## 2. 需求
### FR-66 上传预览审批（改 ImportFilesDialog）
- 选文件夹/多文件后**不直接传**：先弹**预览模态**——全量列待传文件（path + size + 文本/二进制标记 + 超大标记）+ 文本文件内容预览（前端 FileReader 读、截断防卡）+ 总数/总大小；**显式审阅确认（checkbox 或确认按钮）才调 `importFiles`**；取消则不入库。
### FR-67 在页编辑确认（改 ConfigEditorPane）
- 单文件在页编辑后点「保存」**不直接发布**：弹**确认对话框**——展示配置三元组(ns/group/dataId) + **diff（当前编辑态 ⟷ 上一已保存版本，Monaco DiffEditor）** + 备注输入；**确认才调 `publishConfig`**。
- 批量/上传不在此（走 FR-66）。

### 不做
- 后端/契约不改（复用 importFiles/publishConfig）。不引多人审批/变更请求实体（单人确认即可，守 scope）。不做大清单虚拟滚动（先简洁行，必要时后续）。

## 3. 设计
- **FR-66**：新组件 `web/src/pages/configs/ImportPreviewModal.tsx`——入参 entries(ImportFileEntry[])，本地 FileReader 读文本内容（二进制/超大跳读只标记，文本截断前 N 字节）、生成预览清单；列表 UI 复用 `ReverseFetchReviewPanel` 范式（path+size+Badge）、内容预览用只读 `CodeEditor`；底部审阅 checkbox + 确认/取消。`ImportFilesDialog`：选文件后「导入」按钮改为「预览」打开本模态，模态内「确认导入」才 mutate `importFiles`。
- **FR-67**：新组件 `web/src/pages/configs/ConfigSaveConfirmDialog.tsx`——入参 ns/group/dataId + originalContent(上一保存版本) + currentContent(编辑态) + 现有 comment；Monaco DiffEditor 展示 original⟷current + 备注输入 + 确认/取消。`ConfigEditorPane`：保存按钮改为打开本确认对话框，确认才调既有保存（`publishConfig`）。
- 复用：`ui/dialog`、`ui/alert-dialog`、`CodeEditor`(diff)、`ui/scroll-area`、`Badge`、`useMessage`、`humanSize`(或加 lib util)。i18n 新增 configs 审批/预览文案 key。
- **无 api/types 改动**（importFiles/publishConfig/ImportFileEntry/ImportResult 已齐）。

## 4. 任务拆分
- [ ] FR-66：ImportPreviewModal（FileReader 预览+清单+审阅闸）+ ImportFilesDialog 接线（预览→确认才传）。
- [ ] FR-67：ConfigSaveConfirmDialog（diff+备注+确认）+ ConfigEditorPane 接线（保存→确认才发布）。
- [ ] 测试：上传选文件→预览→确认调 importFiles / 取消不调；编辑→保存打确认→确认调 publishConfig / 取消不调；二进制/超大标记；diff 展示。
- [ ] doc-sync：PRD FR-66/67、CHANGELOG、本规格。

## 5. 验收标准
- 上传：选文件后先预览全部待传内容（含二进制/超大标记）、审阅确认才入库、取消不入库。
- 在页改：单文件改后点保存先看 diff + 备注、确认才发布、取消不发布。
- 前端测试全绿（pnpm test + build）。
- **真机浏览器**（末批）：上传预览审批、在页改确认可用。

## 6. 风险/待定
- 大文件内容预览截断防卡；二进制不读内容只标记；FileReader 异步。
- diff 取「上一已保存版本」内容来源（编辑器已持有原内容/或 config.content）。
