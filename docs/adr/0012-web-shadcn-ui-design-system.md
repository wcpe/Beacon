# ADR-0012：管理台引入 shadcn-ui + Tailwind 作为设计系统

**状态**：已接受（补充 [ADR-0002](0002-go-react-embedded-stack.md)，不取代）

## 背景

[ADR-0002](0002-go-react-embedded-stack.md) 锁定管理台为 React(Vite+TS)、`go:embed` 内嵌单二进制。其下未约定 UI 实现，首期用纯手写 CSS（`web/src/styles.css`）。随页面增多（配置 / 文件树 / 覆盖集 / 实例 / zone / 审计 / 环境 / 登录），手写 CSS 控件用法不统一、详情靠"列表下方内联展开"，可维护性与一致性变差。需要一套统一、低定制成本的设计系统。

本 ADR 只决策**管理台 UI 实现层**，不动后端、不动线协议、不动 ADR-0002 的"Go + 内嵌 React 单二进制"主栈。

## 决策

1. **设计系统选 shadcn-ui（radix 基元）+ Tailwind v4**，全量使用其**默认主题（neutral）默认样式**，不做品牌化定制。
2. **组件源码 vendored 进仓库** `web/src/components/ui/`：shadcn 的模式是"把组件源码拷进项目、由项目自有"，不是不可见的运行期黑盒依赖——契合"简单、可控、可移植"。
3. **详情交互按页面分量分层**：重详情（配置 / 文件树）走**独立路由详情页 + Tabs**（保留 `:id` 深链）；中量（覆盖集）走 **Sheet 抽屉** + 发布 **AlertDialog** 二次确认；轻量（实例 / 审计）走 **Dialog**；表单类（zone / 环境）收进 **Dialog**。统一取代原"列表下方内联展开"。
4. **构建链路不变**：Tailwind 在构建期产出静态 CSS，仍进 `dist/`、仍由 `go:embed all:web/dist` 内嵌、单二进制同端口。`vite.config.ts` 的 `proxy/outDir/emptyOutDir/base` 与 go:embed 集成保持不变；仅新增 `@tailwindcss/vite` 插件与 `@/*` 别名。
5. **数据/契约层零改动**：react-query、`api/client.ts`、`api/types.ts`、路由语义、所有 REST 调用保持原样；本次仅重写展示层与容器交互（纯 UI 改造，行为不回归）。

## 理由

- shadcn-ui 组件源码入库、依赖 Radix 无样式基元 + Tailwind 原子类，定制与移植成本低，不像 MUI/Antd 那样绑定重运行时与主题体系。
- 默认主题"开箱即用"，符合"内部运维台不需要品牌化、简单优先"。
- Tailwind 编译期出静态 CSS，与 `go:embed` 单二进制零摩擦，不违 ADR-0002。
- 详情分层用既有 Radix 基元（Dialog/Sheet/Tabs）即可，无需自造弹层。

## 后果

- 新增构建期依赖：`tailwindcss` `@tailwindcss/vite` 及 shadcn 组件运行期依赖（`class-variance-authority` `clsx` `tailwind-merge` `lucide-react` `tw-animate-css` 与按需 Radix 包）。
- 新增 `components.json`、`src/lib/utils.ts`、`src/index.css`（含默认主题 token）；`tsconfig.json` 加 `@/*` 别名；`styles.css` 退役（保留必要的 CodeEditor/树等无对应控件的样式片段）。
- `MessageBar`→`sonner`、`StatusBadge`→`Badge`；`CodeEditor` 因 shadcn 无对应控件保留自写。
- 详情改独立页/弹层后，列表与详情拆为独立组件（配置/文件）。

## 备选方案

- **保留纯手写 CSS**：与用户"全量改 shadcn"诉求冲突，且一致性问题不解。否决。
- **MUI / Ant Design**：重运行时、强主题绑定，定制/移植成本高，与"简单优先、组件自有"相悖。否决。
- **Tailwind v3 + 传统 `tailwind.config.js`**：v4 为当前 shadcn 默认、配置更简（CSS-first、无 postcss），无理由选旧版。否决。
- **详情统一用一种容器（全 Dialog / 全独立页）**：重详情塞 Dialog 拥挤、轻操作跳页割裂；按分量分层体验最佳。否决统一方案。
