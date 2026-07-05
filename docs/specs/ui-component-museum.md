# 功能规格：UI 控件博物馆 wiki 子项目

> 状态：开发中 · 关联 PRD：FR-135 · 分支：当前工作区

## 1. 背景与目标

FR-134 会把管理台通用 UI 与展示组件抽到内部包 `web/packages/ui`。FR-135 在此基础上提供独立的开发期控件博物馆，帮助开发者快速查看 UI 包导出的控件、变体、尺寸与状态，减少在业务页面中试错。

目标是建立一个不依赖 Beacon 后端、不进入主后台生产路由的轻量 wiki 子项目；它只服务开发与组件验收，不改变控制面业务能力。

## 2. 需求（要什么）

- 新增独立子项目 `web/apps/ui-wiki`，通过内部 UI 包导入并展示所有公开导出的组件。
- 每个组件页面或区块至少覆盖基础用法、主要 variants、sizes、states；不适用某类状态的组件需要用文字在展示数据中说明，而不是空缺。
- wiki 示例不得调用 Beacon 后端 API，不依赖 auth、环境选择器、React Query 业务查询或管理台路由。
- wiki 不注册到主后台 `/ui-wiki` 或其他生产路由，不随 Beacon 管理台生产入口展示。
- wiki 复用现有 React、Vite、Tailwind v4、Radix、lucide-react 与测试工具，不引入 Storybook 等新依赖。
- UI 包导出清单变化时，wiki 必须同步补展示，避免出现已导出但不可见的控件。

范围内：

- UI 包组件展示目录、组件示例、基础响应式布局、开发启动与构建命令。
- 主 Web 作为 UI 包消费者所需的最小配置调整。
- FR-136 所需的使用文档与新增控件展示约束。

不做（范围外）：

- 不做外部 npm 发布、私有 registry、版本发布流水线。
- 不做 Storybook / Chromatic / 自动视觉回归平台。
- 不把业务壳组件迁入 UI 包，不展示依赖 Beacon API 的业务容器。
- 不新增后端端点或修改现有后端业务逻辑。

## 3. 设计（怎么做）

架构决策见 [ADR-0059](../adr/0059-internal-ui-package-and-component-museum.md)。

- `web/packages/ui` 作为内部 UI 包，统一导出 shadcn 基元与已抽出的通用展示组件。
- `web/src` 主应用删除对本地通用 UI 目录的直接依赖，改从 UI 包导入；业务壳组件继续保留在主应用。
- `web/apps/ui-wiki` 作为独立 Vite React 应用，入口展示组件目录与示例区块，示例数据全为本地静态数据。
- wiki 与主 Web 共享 Tailwind 主题 token，保持现有后台 UI 风格；wiki 页面本身保持开发文档风格，不做营销页。
- 组件覆盖校验以 UI 包导出清单为准：新增导出组件时需同时补 wiki 示例或显式登记为不可展示的基础类型。

## 4. 任务拆分

- [x] 抽取 `web/packages/ui` 并定义公开导出入口。
- [x] 主 Web 改为从 UI 包消费通用组件，业务壳组件保留在 `web/src`。
- [x] 新增 `web/apps/ui-wiki`，完成组件目录、示例区块与静态示例数据。
- [x] 补充 wiki 覆盖校验，确保 UI 包公开导出都有展示或解释。
- [x] 文档同步：PRD 状态、ARCHITECTURE、开发文档、CHANGELOG。

## 5. 验收标准

- `web/packages/ui` 可独立类型检查 / 构建，且不依赖 Beacon 业务 API、路由、auth、环境状态。
- 主 Web 现有页面可正常构建，通用组件导入来源变为 UI 包，业务壳组件未被错误迁移。
- `web/apps/ui-wiki` 可独立启动 / 构建，示例不请求 Beacon 后端。
- wiki 覆盖 UI 包全部公开导出组件；每个组件至少展示基础用法与主要 variants / sizes / states。
- `cd web && pnpm test`、`tsc -b`、`vite build` 通过；wiki 构建命令通过。
- 文档说明 UI 包使用方式、组件导出规范、wiki 启动方式与新增控件展示约束。

## 6. 风险 / 待定

- `web/src/components` 里存在通用组件与业务组件混放，需要逐个按依赖边界判断，避免迁移过度。
- Tailwind v4 与多 Vite 应用的样式入口需要保持一致，防止 wiki 与主 Web 主题漂移。
- 现有未提交的大量前端改动需要保留，迁移时只改与 FR-134~FR-136 相关的导入和文件位置。
