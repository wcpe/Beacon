# 功能规格：UI 控件博物馆 wiki 子项目

> 状态：已交付 · 关联 PRD：FR-175 · 使用说明见 [docs/UI-WIKI.md](../UI-WIKI.md)

## 1. 背景与目标

第二版 P1 将管理台通用 UI 与展示组件提升到根级内部包 `packages/ui`。FR-175 在此基础上提供独立的开发期控件博物馆，帮助开发者快速查看 UI 包导出的控件、变体、尺寸与状态，减少在业务页面中试错。

目标是建立一个不依赖 Beacon 后端、不进入主后台生产路由的轻量 wiki 子项目；它只服务开发与组件验收，不改变控制面业务能力。

## 2. 需求（要什么）

- 新增独立子项目 `apps/ui-wiki`，通过内部 UI 包导入并展示所有公开导出的组件。
- 每个组件页面或区块至少覆盖基础用法、主要 variants、sizes、states；不适用某类状态的组件需要用文字在展示数据中说明，而不是空缺。
- wiki 示例不得调用 Beacon 后端 API，不依赖 auth、环境选择器、React Query 业务查询或管理台路由。
- wiki 不注册到主后台 `/ui-wiki` 或其他生产路由，不随 Beacon 管理台生产入口展示。
- wiki 复用现有 React、Vite、Tailwind v4、Radix、lucide-react 与测试工具，不引入 Storybook 等新依赖。
- UI 包导出清单变化时，wiki 必须同步补展示，避免出现已导出但不可见的控件。

范围内：

- UI 包组件展示目录、组件示例、基础响应式布局、开发启动与构建命令。
- 第二版 `apps/web` 作为 UI 包消费者所需的最小配置调整。
- FR-175 所需的使用文档与新增控件展示约束。

不做（范围外）：

- 不做外部 npm 发布、私有 registry、版本发布流水线。
- 不做 Storybook / Chromatic / 自动视觉回归平台。
- 不把业务壳组件迁入 UI 包，不展示依赖 Beacon API 的业务容器。
- 不新增后端端点或修改现有后端业务逻辑。

## 3. 设计（怎么做）

架构决策见 [ADR-0059](../adr/0059-internal-ui-package-and-component-museum.md)。

- `packages/ui` 作为内部 UI 包，统一导出 shadcn 基元与已抽出的通用展示组件。
- `apps/web` 主应用从 UI 包导入通用控件；业务壳组件继续保留在主应用。
- `apps/ui-wiki` 作为独立 Vite React 应用，入口展示组件目录与示例区块，示例数据全为本地静态数据。
- wiki 与第二版主 Web 共享 Tailwind 主题 token，保持后台 UI 风格；wiki 页面本身保持开发文档风格，不做营销页。
- 组件覆盖校验以 UI 包导出清单为准：新增导出组件时需同时补 wiki 示例或显式登记为不可展示的基础类型。

## 4. 任务拆分

- [x] 提升 `packages/ui` 并定义公开导出入口。
- [x] `apps/web` 改为从 UI 包消费通用组件，业务壳组件保留在 `apps/web/src`。
- [x] 提升 `apps/ui-wiki`，完成组件目录、示例区块与静态示例数据。
- [x] 补充根级 wiki 覆盖校验，确保 UI 包公开导出都有展示或解释。
- [x] 文档同步：PRD 状态、ARCHITECTURE、[UI-WIKI.md](../UI-WIKI.md)、CHANGELOG。

## 5. 验收标准

- `packages/ui` 可独立类型检查 / 构建，且不依赖 Beacon 业务 API、路由、auth、环境状态。
- `apps/web` 可正常构建，通用组件导入来源变为 UI 包，业务壳组件未被错误迁移。
- `apps/ui-wiki` 可独立启动 / 构建，示例不请求 Beacon 后端。
- wiki 覆盖 UI 包全部公开导出组件；每个组件至少展示基础用法与主要 variants / sizes / states。
- 根级 `pnpm` 工作区构建通过；wiki 构建命令通过。
- 文档说明 UI 包使用方式、组件导出规范、wiki 启动方式与新增控件展示约束。

## 6. 风险 / 待定

- 第二版 `apps/web/src/components` 里需要避免通用组件与业务组件混放，按依赖边界判断，避免迁移过度。
- Tailwind v4 与多 Vite 应用的样式入口需要保持一致，防止 wiki 与主 Web 主题漂移。
- Legacy `web/` 冻结后仍需保留给历史版本追溯；P1 迁移时只改与 FR-175 相关的新根级路径。
