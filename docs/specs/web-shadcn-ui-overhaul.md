# 功能规格：管理台 shadcn-ui 设计系统改造

> 状态：已交付@v0.3.0　·　关联 PRD：FR-21（增强 FR-6 / FR-18）　·　分支：feature/web-shadcn-ui

## 1. 背景与目标

管理台前端（FR-6 / FR-18）当前是纯手写 CSS（`web/src/styles.css`），控件用法不统一，且"查看详情"是在列表下方**内联展开**（`/configs/:id` 等由同一组件同时渲染列表与详情）。本次把整套 UI **全量改用 shadcn-ui 默认样式**，并把详情交互从"下方展开"改为**模态 / 独立详情页**。属第二期治理增强的体验完善，不新增业务能力。

## 2. 需求（要什么）

- 全量使用 shadcn-ui 控件的**默认样式**（纯 neutral 默认主题，不做品牌化定制）。
- "查看详情"不再内联展开，按页面分量改为：
  - **配置中心 / 文件树托管**：独立路由详情页 + Tabs（重详情、可深链）。
  - **文件覆盖集**：Sheet 侧边抽屉 + 发布二次确认（AlertDialog）。
  - **实例与健康 / 审计日志**：Dialog 模态。
  - **zone 分配 / 环境管理**：表单收进 Dialog。
- 范围内：8 个页面 + 顶层骨架（Layout/侧栏）+ 公共组件（StatusBadge / MessageBar / CodeEditor）的样式与交互重写；引入 Tailwind v4 + shadcn-ui 工具链。
- 不做（范围外）：**不改任何业务行为、API 调用、路由语义、查询逻辑**；不加新功能（如覆盖集"新建"入口、批量操作）；不引入鉴权/权限以外的能力；不改后端。

## 3. 设计（怎么做）

- 工具链与设计系统决策见 **[ADR-0012](../adr/0012-web-shadcn-ui-design-system.md)**（补充 [ADR-0002](../adr/0002-go-react-embedded-stack.md)，不取代）；此处不重复决策正文。
- **基础设施**：Tailwind v4（`@tailwindcss/vite` 插件）+ shadcn-ui（radix 基元），组件源码 vendored 进 `web/src/components/ui/`（仓库自有，非运行期黑盒依赖）；`@/*` 路径别名；`styles.css` 由含默认主题 token 的 `index.css` 取代。
- **构建链路不变**：Tailwind 在构建期产出静态 CSS，仍进 `dist/`，仍由 `go:embed all:web/dist` 内嵌、单二进制同端口；`vite.config.ts` 的 `proxy/outDir/emptyOutDir/base` 必须保留。
- **路由**：详情由"同组件内联渲染"改为真正的子路由页（配置/文件）；列表页与详情页拆成独立组件，保持现有 `:id` 深链可用。
- **数据层不动**：react-query 的 query/mutation、`api/client.ts`、`api/types.ts` 保持原样；只替换展示层与容器交互。
- **公共组件**：`StatusBadge`→ shadcn `Badge` 变体；`MessageBar`/`useMessage`→ `sonner` toast；`CodeEditor`（带行号 textarea）shadcn 无对应控件，保留实现并套用 shadcn 外观 token。

## 4. 任务拆分

- [ ] 搭 Tailwind v4 + shadcn 基础设施（deps / config / 主题 token / components.json / 别名），修复并保住 vite/tsconfig 关键配置
- [ ] 拉取所需 shadcn 组件到 `components/ui/`
- [ ] 改造顶层骨架 Layout + 公共组件（StatusBadge / MessageBar / CodeEditor）
- [ ] 改造配置中心（列表 + 独立详情页 + Tabs）
- [ ] 改造文件树托管（列表树/平铺 + 独立详情页 + Tabs）
- [ ] 改造文件覆盖集（列表 + Sheet 详情 + AlertDialog 二次确认）
- [ ] 改造实例 / zone / 审计 / 环境 / 登录
- [ ] `pnpm build` 绿
- [ ] 文档同步：PRD 状态、ARCHITECTURE（前端栈/依赖）、CHANGELOG 未发布段

## 5. 验收标准

- `web/` 内所有页面的列表、表单、按钮、徽标、对话框均由 shadcn-ui 控件渲染，无残留 `styles.css` 旧 class（CodeEditor 等无对应控件者除外，且其外观对齐 shadcn token）。
- 在配置中心 / 文件树托管点"查看详情"进入**独立详情页**（URL 仍为 `/configs/:id`、`/files/:id` 可深链）；文件覆盖集详情以 **Sheet** 弹出；实例 / 审计详情以 **Dialog** 弹出——均不再在列表下方内联展开。
- 行为零回归：发布 / 回滚 / diff / dry-run / 下线 / 指派 / 改派 / 分页 / 过滤 / 登录登出 / 401 跳登录，全部与改造前一致（手测逐项核对）。
- `pnpm build`（`tsc -b && vite build`）通过，产物落 `web/dist/`，`go run ./cmd/beacon` 能正常提供管理台。

## 6. 风险 / 待定

- **构建配置受保护**（CLAUDE.md §16）：本次必改 `package.json` / `vite.config.ts` / `tsconfig.json`，属任务本身所需；`shadcn init` 自动改写后须复核并复原 proxy/outDir 等关键项。
- **观感变化**：纯默认主题会改变现有蓝色主色与整体观感，已与用户确认接受。
- **文档 WIP 冲突**：`docs/ARCHITECTURE.md` / `CHANGELOG.md` 当前含用户并行 WIP，本次仅做 section 级追加，交付时由用户决定分别暂存，避免裹挟其改动。
- **shadcn 无代码编辑器控件**：CodeEditor 维持自写实现。
