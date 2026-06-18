# 配置中心 — VS Code 风格编辑器（Monaco）

> 状态：已交付@v0.3.0　·　关联 PRD：FR-22/FR-23　·　分支：feature/config-center-redesign

## 1. 背景

配置中心已完成 shadcn-ui 改造和基础编辑能力，但编辑体验仍停留在基础 textarea 阶段。运维需要更高效的配置编辑体验：语法高亮、自动缩进、代码折叠、Diff 对比。

## 2. 需求（要什么）

- 配置中心页面改为 VS Code 风格：左侧资源管理器树 + 右侧 Monaco 编辑器
- Monaco 编辑器支持 yaml/json/properties 语法高亮
- 自动缩进、括号匹配、代码折叠、Ctrl+S 保存
- Diff 模式使用 Monaco DiffEditor
- 生效预览模式调用 `GET /admin/v1/configs/effective` 展示合并后配置 + provenance
- 历史修订面板（可折叠，点击联动 Diff）
- 左侧资源管理器两段式：上面配置文件树 + 下面实例/分组树形选择器
- 页面整体固定（`h-screen overflow-hidden`），仅编辑器内容可滚动

## 3. 设计（怎么做）

- 使用 `@monaco-editor/react` 集成 Monaco Editor（VS Code 同源编辑器内核）
- 配置亮色主题（`theme="vs"`）
- 依赖：`monaco-editor` + `@monaco-editor/react`
- 单页面布局，不跳转多层路由
- 生效预览展示合并后配置项 + 逐键来源 + 被删除键

## 4. 文件变更

| 文件 | 操作 |
|------|------|
| `web/package.json` | +2 依赖 (monaco-editor + @monaco-editor/react) |
| `web/src/pages/ConfigsPage.tsx` | 重写（VS Code 风格 + 生效预览 Tab） |
| `web/src/components/CodeEditor.tsx` | 重写（Monaco Editor + DiffEditor） |
| `web/src/components/Layout.tsx` | 修改（移除文件树托管/覆盖集导航项，overflow-hidden） |
| `web/src/api/client.ts` | 新增 `effectiveConfig()` 函数 |
| `web/src/main.tsx` | 修改（条件启用 mock） |
| `web/vite-env.d.ts` | 新建（Vite 类型） |
| 删除 | FileTree.tsx、DiffView.tsx、RevisionPanel.tsx、TargetSelector.tsx |
