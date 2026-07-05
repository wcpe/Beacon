# Beacon Web 前端

## 内部 UI 包

通用 UI 与展示组件维护在 `packages/ui`，主后台应用通过 `@beacon/ui` 消费：

```ts
import { Button, DataTable, type DataTableColumn } from '@beacon/ui'
```

可放入 UI 包的组件必须满足：

- 不依赖 Beacon 后端 API、React Query 查询、路由、auth、环境状态或业务 store。
- 不依赖主应用 `@/` 别名。
- 默认文案可内置中文，也可以通过 props 覆盖；不得读取主应用 i18n 业务 key。

业务壳组件继续留在 `src/components`，例如 `Layout`、`SystemHeader`、`CommandPalette`、`EnvSelector`、`PageHeader`、`RequireAuth`、`useMessage`。

## 控件博物馆

`apps/ui-wiki` 是独立开发期 wiki，不注册到 Beacon 管理台生产路由，也不会输出到 `web/dist`。

常用命令：

```bash
pnpm dev:ui-wiki
pnpm build:ui
pnpm build:ui-wiki
node scripts/check-ui-wiki-coverage.mjs
```

新增 UI 包导出时，必须同步：

- 在 `packages/ui/src/index.ts` 导出组件或工具。
- 在 `apps/ui-wiki/src/App.tsx` 增加基础用法、主要 variants / sizes / states 展示。
- 在 `apps/ui-wiki/src/coverage.ts` 登记导出名，并运行覆盖校验。

主后台构建仍使用：

```bash
pnpm build
pnpm test
```
