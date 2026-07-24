# UI 控件博物馆（ui-wiki）

> 开发期控件展示与验收入口。对应 FR-175、[ADR-0059](adr/0059-internal-ui-package-and-component-museum.md)、[specs/ui-component-museum.md](specs/ui-component-museum.md)。  
> **不进生产路由**，不随控制面单二进制发布。

## 1. 定位

| 项 | 说明 |
|---|---|
| 路径 | `apps/ui-wiki` |
| 消费包 | `@beacon/ui`（`packages/ui`） |
| 数据 | 仅本地静态示例，**不请求** Beacon 后端 / 不依赖 auth / 环境 / React Query 业务查询 |
| 门禁 | `pnpm check:ui-wiki`（`scripts/check-ui-wiki-coverage.mjs`），`apps/ui-wiki` 的 `build` 会先跑覆盖率校验 |

每个 `@beacon/ui` **公开导出**必须在博物馆有展示或在覆盖清单中有登记；新增导出而未补展示会让 CI / `pnpm check:ui-wiki` 失败。

## 2. 启动与构建

在仓库根目录（需已 `pnpm install`）：

```bash
# 开发：默认 Vite 端口（本机空闲时多为 http://localhost:5173）
pnpm --filter @beacon/ui-wiki dev

# 覆盖率 + 类型检查 + 构建
pnpm --filter @beacon/ui-wiki build

# 仅跑覆盖率门禁
pnpm check:ui-wiki
```

浏览器打开终端打印的 Local URL。左侧按分组浏览控件，右侧看 variants / sizes / states 与导出清单。

## 3. 目录结构

```
apps/ui-wiki/
├── src/
│   ├── App.tsx          # 控件目录 + 各区块预览
│   ├── coverage.ts      # UI_WIKI_COVERED_EXPORTS 覆盖清单（与 packages/ui 导出对齐）
│   ├── main.tsx
│   └── index.css
├── package.json
└── vite.config.ts       # 别名指向 packages/ui 源码，共享主题 token
```

覆盖率校验对比：

1. `packages/ui/src/index.ts` 的 `export { ... }` 公开名  
2. `apps/ui-wiki/src/coverage.ts` 的 `UI_WIKI_COVERED_EXPORTS`

二者必须集合一致（不少不多）。

## 4. 新增控件时怎么做

1. 在 `packages/ui` 实现并加入 `packages/ui/src/index.ts` 导出。  
2. 在 `apps/ui-wiki/src/App.tsx` 增加或扩展预览区块（基础用法 + 主要 variants / sizes / states；不适用的状态用文字说明，不要空缺）。  
3. 把导出名写入 `apps/ui-wiki/src/coverage.ts` 的 `UI_WIKI_COVERED_EXPORTS`。  
4. 本地执行：

```bash
pnpm check:ui-wiki
pnpm --filter @beacon/ui-wiki build
```

5. **不要**把业务壳组件（依赖 auth、路由、环境、React Query 业务查询、Beacon API）放进 UI 包或博物馆。

## 5. 与管理台演示模式的边界

| | UI 控件博物馆 `apps/ui-wiki` | 管理台演示模式 `apps/web` |
|---|---|---|
| 目的 | 看通用控件怎么用 | 看业务页面与 API 契约形态 |
| 数据 | 静态示例 | `@beacon/devmock`（MSW）四态场景 |
| 启动 | `pnpm --filter @beacon/ui-wiki dev` | `pnpm --filter @beacon/web dev` |
| 鉴权 | 无 | demo 免登录 |
| 生产 | 不发布 | 常规 `vite build` 不带 mock；仅 `--mode demo` 保留 mock |

管理台演示模式（FR-159 / FR-172）：

```bash
pnpm --filter @beacon/web dev
# 浏览器打开终端 URL，默认可直接进管理台（demo 免登录）
# 顶栏可切换 mock 场景：empty / normal / huge / error
# 也可 URL：?mockScenario=normal
```

演示数据加载完成的判定：页面骨架（`Skeleton` / `*Skeleton`）消失，KPI / 列表出现真实数字或行，而不是空加载态。截 README 展示图时务必等 **normal** 场景数据就绪后再截。

## 6. 相关文档

- 规格：[specs/ui-component-museum.md](specs/ui-component-museum.md)
- ADR：[adr/0059-internal-ui-package-and-component-museum.md](adr/0059-internal-ui-package-and-component-museum.md)
- 前端 IA 与演示约定：[UX.md](UX.md) §4
- 仓库布局：[ARCHITECTURE.md](ARCHITECTURE.md) §2.1 / §6
- 根入口：[README.md](../README.md)
