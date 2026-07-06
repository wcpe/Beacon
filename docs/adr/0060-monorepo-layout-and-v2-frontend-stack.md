# ADR-0060：monorepo 工作区布局与第二版前端栈

**状态**：已接受（部分取代 [ADR-0002](0002-go-react-embedded-stack.md) 的仓库布局约定，其 Go + 内嵌 React 单二进制主栈决策不变；承接 [ADR-0059](0059-internal-ui-package-and-component-museum.md) 的 UI 包与控件博物馆并迁移其路径；随 P1（0.21.x，FR-173/174/175）落地）

## 背景

第一版代码平铺在仓库根（Go module 在根、前端在 `web/`、agent 在 `agent/`），前端内部又长出了 `web/packages/ui` 与 `web/apps/ui-wiki` 的小工作区（ADR-0059），构建脚本靠手工串联，没有任务编排与缓存。第二版决定 Legacy 前端整体冻结（FR-138）、管理台另起炉灶并以 mock-first 交付（FR-172），需要一个能承载"多应用 + 共享包 + 统一任务编排"的工程底座。参考仓库 Vantaloom 验证了 pnpm workspace + Turborepo + `apps/*` / `packages/*` 布局与 `devmock`（MSW 集中 mock 包）模式的可行性。

## 决策

1. **仓库改为 monorepo**：根级 `pnpm-workspace.yaml`（`apps/*`、`packages/*`）+ `turbo.json` 任务编排（lint / test / build 及依赖拓扑缓存）。
2. **apps/ 收纳全部应用**：`apps/server`（Go 控制面，`cmd/`、`internal/` 整体迁入，go:embed、Makefile、CI、脚本同步适配）、`apps/agent`（Kotlin/TabooLib，五模块结构不变）、`apps/web`（第二版管理台，全新工程）、`apps/ui-wiki`（控件博物馆，自 `web/apps/ui-wiki` 提升）。
3. **packages/ 收纳共享包**：`packages/ui`（`@beacon/ui`，自 `web/packages/ui` 提升，shadcn/Radix 体系沿用 ADR-0012）、`packages/devmock`（MSW handlers 按 API 域组织，浏览器与测试双端共享）、`packages/eslint-config`、`packages/typescript-config`。
4. **第二版前端栈**：Vite + React Router + TanStack Query（服务器状态）+ **Zustand**（纯客户端 UI 状态）+ react-i18next（沿用 ADR-0033）+ **MSW**（mock-first 载体）。服务器状态一律归 TanStack Query，Zustand 不得缓存服务器数据。
5. **Legacy 前端冻结**：`web/` 原地冻结、不迁移、不再演进；第二版二进制只内嵌 `apps/web` 构建产物；真机依赖旧功能时继续运行 v0.19 及更早版本。
6. **不变项**：Go + chi + GORM、React(Vite+TS) 经 go:embed 内嵌单二进制同端口（ADR-0002 主体）、agent 五模块抽象（ADR-0005）、shadcn 设计系统（ADR-0012）、i18n 框架（ADR-0033）均不因本 ADR 改变。

## 理由

- 多应用（管理台 / 控件博物馆）+ 多共享包（ui / devmock / lint 配置）靠手工脚本串联已到极限，Turborepo 的任务图与缓存把"全仓一键 lint / test / build"变成 CI 门禁的可执行基础。
- mock-first 路线（P2 全量 mock 管理台）要求 mock 基建是一等公民：`packages/devmock` 让页面、vitest、Playwright 共享同一套 handlers，避免三处各造一份假数据。
- Zustand 补上纯客户端状态的空位（Legacy 靠散落的 context / useState），与 TanStack Query 边界清晰。
- 新台另起 `apps/web` 而非在 Legacy 上改造：Legacy 页面模型已被第二版 PRD 否定，冻结比翻修便宜且安全（真机还在用）。

## 后果

- go.mod、`//go:embed` 路径、Makefile、CI workflow、发布脚本随 FR-173 一次性适配；迁移期间历史 PR 的路径引用失效属预期。
- 根目录出现 Node 工具链文件（pnpm-workspace / turbo.json），Go 开发者需安装 pnpm 才能跑全仓任务（仅动 Go 时仍可单独 `go test`）。
- `web/` 冻结后其内部小工作区（ADR-0059 的路径）成为历史形态，新路径以本 ADR 为准。
- ARCHITECTURE §2 仓库布局、`.claude/rules/static-analysis.md` 的命令路径随 P1 实施同步。

## 备选方案

- **维持根级平铺 + 手工脚本**：改动最小，但多应用多包的任务编排与缓存缺失，CI 门禁难以做成"全仓一键"。否决。
- **前端生态单独 monorepo、Go/agent 留原位**：涟漪小，但仓库永远两套布局风格，与"多个应用都在 apps 下"的目标不符。否决。
- **照搬 Vantaloom 的 Next.js**：与 go:embed 静态 SPA 单二进制形态冲突（Next 需要 Node 运行时才能发挥价值）。否决，只取其布局与 devmock 模式。
- **Nx 替代 Turborepo**：能力更强但配置与心智负担重，单人维护的仓库用不到其插件生态。否决。
