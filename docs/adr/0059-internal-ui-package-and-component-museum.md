# ADR-0059：管理台通用 UI 包与控件博物馆

**状态**：已接受（补充 [ADR-0012](0012-web-shadcn-ui-design-system.md)，不取代）

## 背景

[ADR-0012](0012-web-shadcn-ui-design-system.md) 已确定管理台使用 shadcn-ui + Tailwind v4，并把组件源码作为项目自有代码维护。随着管理台页面扩展，`web/src/components` 同时承载通用控件、展示组件与 Beacon 业务壳组件，复用边界不清，新增页面难以快速确认可用控件与状态样式。

本 ADR 只决策前端代码组织与开发期控件展示方式，不改变 Go + 内嵌 React 单二进制主栈，不改变后端 API、鉴权、路由语义与生产发布形态。

## 决策

1. **新增内部 UI 包**：在 `web/packages/ui` 放置可复用的通用 UI 与展示组件，主后台 Web 通过包名消费该包；不发布到外部 npm registry。
2. **抽取边界按业务依赖判定**：不依赖 Beacon 业务 API、路由、auth、环境状态、React Query 查询与 i18n 业务 key 的组件可进入 UI 包；`Layout`、`SystemHeader`、`CommandPalette`、`EnvSelector`、`useMessage` 等业务壳组件继续留在主应用。
3. **shadcn 源码归属前移**：ADR-0012 中 `web/src/components/ui/` 的 vendored 组件源码迁入内部 UI 包维护；主应用不再直接把 shadcn 基元当作本地业务组件目录使用。
4. **新增独立控件博物馆**：在 `web/apps/ui-wiki` 新增开发期 wiki 子项目，展示 UI 包全部导出组件的基础用法、主要 variants、sizes 与 states；该子项目不注册到 Beacon 管理台生产路由，也不随主后台生产包发布。
5. **复用既有前端技术栈**：UI 包与 wiki 复用现有 React、Vite、Tailwind v4、Radix、lucide-react 与测试工具；不引入 Storybook 或功能重叠的新依赖。
6. **构建边界显式化**：UI 包需要具备独立类型检查 / 构建能力；主 Web 与 ui-wiki 均以包消费者身份引用，避免示例代码回流依赖 Beacon 后端。

## 理由

- 通用组件包让复用边界由目录结构表达，减少业务页面之间的隐式耦合。
- 保留业务壳组件在主应用内，能避免 UI 包反向依赖 Beacon API、鉴权与运行态，符合依赖单向与简单优先。
- 控件博物馆解决“已有控件不知道怎么用”的协作问题，比 Storybook 更轻，不新增依赖与独立运行时心智。
- 独立 wiki 子项目不会污染生产路由与单二进制发布边界，仍可在开发期完整展示 UI 包。

## 后果

- `web/` 下形成主应用、内部 UI 包、wiki 子项目三类前端代码边界，构建脚本与文档需要说明各自命令。
- 主应用需要把原本来自 `src/components/ui` 或通用展示组件的导入改为 UI 包导入。
- 新增组件时必须判断是否业务无关；进入 UI 包的组件需要同步补充 wiki 示例。
- ARCHITECTURE 与 FR-134~FR-136 的规格 / 文档需要同步更新，避免继续把 `web/src/components/ui` 作为唯一组件源码位置。

## 备选方案

- **继续把所有组件放在 `web/src/components`**：改动最小，但无法解决复用边界与控件发现问题。否决。
- **引入 Storybook**：生态成熟，但增加依赖、配置与维护成本；当前只需要内部控件展示，不需要完整设计系统平台。否决。
- **把 UI 包发布到私有 npm registry**：适合多仓复用；当前只有 Beacon 单仓消费，发布链路会增加无必要复杂度。否决。
- **把 wiki 挂到主后台 `/ui-wiki` 路由**：访问方便，但会污染生产管理台导航与发布包。否决。
