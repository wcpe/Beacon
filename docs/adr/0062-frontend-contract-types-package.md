# ADR-0062：前端响应契约类型独立成包（devmock 反向依赖 contracts）

**状态**：已接受（承接 [ADR-0060](0060-monorepo-layout-and-v2-frontend-stack.md) 的 monorepo 布局，新增 `packages/contracts` 共享包；随 P3（FR-155 前置）落地）

## 背景

第二版管理台的对外响应契约类型（`ServerItem` / `ZoneTreeResponse` / `HealthDetail` / `NamespaceTrustItem` 等各域 HTTP 响应形状）此前定义在 `packages/devmock` 里，与「mock 数据 builder + MSW handlers」三合一，且 `@beacon/devmock` 运行时依赖 `msw`。`apps/web` 生产代码（api 层 + 页面）通过 `import type … from '@beacon/devmock'` 引用这些契约类型，共计 65 处——**生产代码在类型层依赖了 mock 包**。

依赖方向因此是错的：真实业务代码依赖演示 mock。P3 起后端按域接真、页面从 mock 切真时，这个方向会让「契约」与「mock 实现」纠缠，也让 `apps/web` 的类型正确性名义上系于一个含 `msw` 运行时依赖的包。

## 决策

1. **新增 `packages/contracts`（`@beacon/contracts`）**：纯 type-only 包，`private: true`、`type: module`、**无任何运行时依赖**，`exports` `.` → `src/index.ts` barrel，按域拆文件（cluster / identity / namespace / metrics-health / config-center / file-assets / system / observability / archive / connections-messages / delivery + 通用壳 common）。
2. **对外响应契约类型的真源迁入 contracts**：各域 HTTP 响应形状、其枚举构件（如 `ServerKind` / `IdentityStatus` / `TrustCapability`）与通用壳（`Paged<T>` / `MockErrorBody`）从 devmock 迁入 contracts。
3. **依赖倒置：devmock → contracts**：`@beacon/devmock` 依赖 `@beacon/contracts`，其 handler 的 `satisfies XxxResponse` 约束仍锚定契约类型（防 mock 与真契约漂移）；mock 私有状态类型（`ClusterState` / `ServerRow` / `IdentityRow` / `TrustRow` 等）留在 devmock 不迁。
4. **生产代码只依赖 contracts**：`apps/web` 的 `import type` 一律改指向 `@beacon/contracts`，收敛到零 `import type from '@beacon/devmock'`；运行时 mock（`main.tsx` 动态 import、`@beacon/devmock/scenario` · `/support` 子路径、测试 harness 的 `allHandlers` / `resetMockData` / `setMockScenario`）仍指向 `@beacon/devmock`。
5. **纯类型搬迁、零行为变更**：本次不改任何运行时逻辑，演示 mock 的构建隔离门控（`isDemoMode()` + `vite.config.ts`）保持不变，生产构建产物依旧不含 devmock / msw。

## 理由

- 依赖方向摆正：契约是真实业务的一等公民，应由生产代码与 mock 共同依赖，而非生产代码依赖 mock。
- 防漂移不丢：契约独立后，devmock 的 handler 仍 `satisfies` 契约类型，mock 与「真契约」的一致性靠类型强制，切真时页面类型不必大改。
- 隔离更干净：`@beacon/contracts` 无运行时依赖，`apps/web` 的类型正确性不再名义上牵连含 `msw` 的包。
- 成本低：纯类型移动，无行为变更，测试与构建门禁全绿即可验证等价。

## 后果

- `packages/devmock` 与 `apps/web` 均新增 `@beacon/contracts` workspace 依赖；`turbo run build` 拓扑多一个 type-only 包（`tsc --noEmit`，无 dist 产物，turbo 对其 `build` 报「no output files」属预期，与 devmock 同）。
- `@beacon/devmock` 的 barrel 整包 re-export `@beacon/contracts`，旧的 `@beacon/devmock` 类型引用不破（向后兼容）。
- ARCHITECTURE §2.1 布局与 §6 前端架构同步登记 `packages/contracts` 与依赖方向。

## 备选方案

- **维持契约类型留在 devmock**：改动最小，但生产代码永久在类型层依赖 mock 包，依赖方向错误不解决。否决。
- **契约类型下沉进 `packages/ui` 或 apps/web 内部**：ui 是组件库、职责不符；放 apps/web 内部则 devmock 无法复用同一真源做 `satisfies` 防漂移。否决。
- **由后端 OpenAPI/代码生成契约类型**：P3 后端尚未接真，当前契约真源是 `docs/API.md` 草案与各域 spec，生成链路时机未到；本 ADR 只做「搬到独立包」这一步，不预设未来生成方案。
