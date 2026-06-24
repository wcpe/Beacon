# 功能规格：破坏性写操作统一二次确认

> 状态：开发中　·　关联 PRD：FR-76　·　分支：feature/fr-76-destructive-confirm

## 1. 背景与目标

FR-67（在页保存确认）为「单文件保存→发布」加了一道看 diff + 填备注的确认闸，FR-71（[ADR-0036](../adr/0036-zone-reassign-explicit-friction.md)）为改派加了「手输 serverId 复述」的高摩擦档。但散落各页的**删除 / 吊销 / 下线**等破坏性写操作各自手搓一份 `AlertDialog`：文案口径不一、影响摘要（脱链哪层 / 影响哪些服）缺失或随意，且无统一的「需输入复述」高摩擦能力。

本功能（FR-76，属 P2 增强，承 FR-67 思路）抽一个**通用破坏性二次确认组件** `DestructiveConfirmDialog`，复用既有 `ui/alert-dialog`，统一：标题 / 破坏性动作描述 / **影响摘要**（由调用方传入「脱链哪层 / 影响哪些服」）/ 确认 + 取消，并提供可选的「需输入复述」高摩擦档（参考 FR-71）。再把它接入 FR-74 不触碰的破坏性入口，消除复制粘贴、补齐影响摘要。**纯前端**，不动后端 / API / 数据模型。

## 2. 需求（要什么）

- **通用组件** `web/src/components/DestructiveConfirmDialog.tsx`：
  - 入参：`open` / 标题 `title` / 破坏性动作描述 `description` / **影响摘要** `impacts`（字符串数组，调用方传入「脱链哪层 / 影响哪些服」，可空）/ 确认按钮文案 `confirmLabel` / `pending`（进行中禁用）/ `onConfirm` / `onOpenChange`。
  - 可选高摩擦档：传 `confirmPhrase` 时渲染一个手输框，输入须 `=== confirmPhrase` 才启用确认按钮（像 FR-71 手打 serverId、GitHub 删库手打仓库名）。
  - 确认才触发 `onConfirm`；取消 / 关闭不执行。
- **接入 FR-74 不碰的破坏性入口**（用通用组件替换各自手搓的 `AlertDialog`）：
  - 环境删除（`NamespacesPage`，删 namespace）——影响摘要：删除该环境本身及其下配置 / 文件 / 覆盖集（后端守卫仍是安全边界，前端仅提示）。
  - API 密钥吊销（`ApiKeysPage`，revoke）——影响摘要：使用该密钥的外部服务将立即失去访问；高摩擦档手输密钥名复述。
  - API 密钥重置（`ApiKeysPage`，reset）——影响摘要：旧明文立即失效、使用旧明文的外部服务需更新。
- 范围内：上述通用组件 + 接入这些入口；影响摘要由调用方传入；可选手输复述高摩擦档。
- 不做（范围外）：
  - 删配置 / 删文件 / 清小区默认入口的接入——**当前 UI 无对应写入口**（`deleteConfig` / `deleteFile` 仅存在于 API client 未接按钮；默认入口只有只读 `listDefaultEntries`、无清除写操作），无处可接，待相应入口落地后再纳入。
  - `ConfigsPage` 的**列表多选 + 批量删除栏**——属 FR-74 范围，本功能不碰该文件区域以减少 rebase 冲突（FR-74 的「批量删走统一确认」可直接复用本组件）。
  - 实例强制下线（`ServersPage`）——本期不动，避免与监控页并行改动冲突；其确认范式已存在。
  - 后端 / API / 数据模型改动；新增任何破坏性写端点。

## 3. 设计（怎么做）

> 仅 web 展示层改动，纯前端摩擦（防误触），后端守卫（如环境删除 409、密钥软删）才是安全边界，不涉及新架构决策（承 FR-67 / FR-71 既定范式，无新 ADR）。

### 3.1 通用组件 `DestructiveConfirmDialog`
- 基于 `ui/alert-dialog` 的 `AlertDialog` / `AlertDialogContent` / `Header` / `Title` / `Description` / `Footer` / `Action` / `Cancel`。
- 受控开合（`open` + `onOpenChange`），破坏性确认按钮用 `variant="destructive"`。
- `impacts` 非空时以列表渲染「影响摘要」区（脱链哪层 / 影响哪些服）。
- `confirmPhrase` 非空时渲染手输框，输入 `!== confirmPhrase` 时确认按钮 `disabled`；为空时确认按钮始终可点（仅 `pending` 时禁用）。
- 不自管业务调用：`onConfirm` 交由调用页触发既有 mutation。

### 3.2 接入各入口
- `NamespacesPage.tsx`：把删除处手搓的 `AlertDialog` 换成 `DestructiveConfirmDialog`（受控开合 + 选中行状态），影响摘要传入，确认调既有 `deleteMut`。
- `ApiKeysPage.tsx`：吊销 / 重置两处换成 `DestructiveConfirmDialog`（受控开合 + 选中密钥状态）；吊销带 `confirmPhrase = 密钥名` 高摩擦档，重置不带（仅影响摘要）。
- i18n 文案补到 `web/src/i18n/locales/zh-CN.ts`：通用组件自身的「手输复述」提示等放 `common`；各入口的影响摘要复用 / 微调既有 `namespaces.*` / `apikeys.*` 键。

## 4. 任务拆分
- [ ] 测试先行：`DestructiveConfirmDialog.test.tsx`（确认触发 / 取消不触发 / 影响摘要渲染 / 手输复述档启用禁用）。
- [ ] 实现 `DestructiveConfirmDialog.tsx`。
- [ ] 接入 `NamespacesPage`（删环境）+ 更新其单测断言仍绿。
- [ ] 接入 `ApiKeysPage`（吊销 + 重置）。
- [ ] i18n 文案补全。
- [ ] 文档同步：PRD 状态（FR-76 行）、CHANGELOG 未发布段。

## 5. 验收标准
- `DestructiveConfirmDialog`：点确认调 `onConfirm`、点取消 / 关闭不调；`impacts` 非空时影响摘要可见；传 `confirmPhrase` 时手输不符确认禁用、相符启用（vitest 覆盖）。
- `NamespacesPage` 删环境、`ApiKeysPage` 吊销 / 重置经新组件二次确认后才调既有写操作；既有页单测仍绿。
- web `pnpm test` + `pnpm build` 通过。
- 真机浏览器交互（弹窗外观 / 手输体验）标「待真机浏览器验」。

## 6. 风险 / 待定
- 删配置 / 删文件 / 清默认入口当前无 UI 写入口，本期不接（范围外说明已在 §2）；待入口落地再纳入，避免凭空造按钮（YAGNI）。
- 与 FR-74 并行改 `ConfigsPage`：本功能不碰其列表 / 批量区域；FR-74 落地后可复用本组件做「批量删统一确认」。
