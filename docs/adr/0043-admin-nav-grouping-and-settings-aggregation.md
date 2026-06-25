# ADR-0043：管理台导航分组与设置区聚合 IA（5 组手风琴 + 设置三块子 tab + 嵌套子路由深链）

**状态**：已接受

## 背景

管理台侧栏导航随 FR 迭代膨胀到 15 项平铺，缺层级、扫读成本高；同时设置类页面散落在多个顶层路由（运维设置 `/settings`、控制面健康 `/system`、密钥管理 `/api-keys`、环境管理 `/namespaces`），彼此无聚合、运维要在多个侧栏项间跳。

FR-93（侧栏导航层级化）、FR-94（设置区聚合页骨架）、FR-95（旧页折叠进设置页）是同一信息架构（IA）重构的三步：先给导航分组、再给设置搭聚合骨架、最后把旧页折叠进来。三者共用本 ADR 记录的 IA 决策。

此外，Layout 的 `NAV_ITEMS` 与命令面板（CommandPalette）的导航目标当前是两份各自维护的重复清单——加一个页面要改两处，易漂移。

## 决策

1. **侧栏导航收为 5 组可折叠手风琴**（FR-93）：15 项平铺导航按业务域归为 5 组——
   - **概览**：`/dashboard`
   - **配置管理**：`/configs`、`/file-preview`、`/imprint`、`/reverse-fetch`
   - **集群**：`/servers`、`/topology`、`/zones`
   - **可观测**：`/service-analysis`、`/audits`、`/alert-events`
   - **系统**：`/settings`

   命中当前路由的组按 `location.pathname` 前缀判定**自动展开**；用户手动展开 / 折叠态**持久化到前端偏好** store（`web/src/state/preferences.ts` 新增 `navExpandedGroups` 字段，非法值逐字段回落默认）。折叠交互**用原生 `<details>/<summary>` 或 `useState`+偏好，不引入 radix accordion 等新依赖**（遵守依赖管理规则）。

2. **NAV 单一真源收敛**（FR-93）：把 Layout 的 `NAV_ITEMS` 与 CommandPalette 的导航目标抽到一处共享常量（`web/src/lib/navModel.ts`）——Layout 按分组树形渲染、CommandPalette 取扁平叶子，消除双源漂移。

3. **设置区升级为聚合页骨架**（FR-94）：`/settings` 升为聚合页，三块顶层 tab——**运维设置 / 系统信息 / 系统设置**，每块再含子 tab。
   - 顶层三块用 **嵌套子路由**：`/settings` 重定向到 `/settings/ops`；三块分别为 `/settings/ops`、`/settings/system-info`、`/settings/system-config`。
   - 块内子 tab 用 **search param**（如 `/settings/ops?tab=health`）。
   - 由此满足深链 / 刷新保持 / 命令面板可检索 / 浏览器后退。复用 `web/src/components/ui/tabs.tsx`（受控 `value` + `useSearchParams`）。

4. **运维设置 6 域改子 tab 呈现但保留跨子 tab 统一草稿**（FR-94）：把现有按 key 前缀的 6 分组（health/metric/longpoll/alert/log/reverse-fetch）改为 6 个子 tab；但**必须保留 SettingsPage 现有的顶层集中草稿 + dirty 计算 + 页脚批量保存 saveAll + 逐项恢复默认**，草稿 / 脏项 / 批量保存仍**跨子 tab 全局统观**（不把草稿态下沉进单个子 tab，否则回归已交付的 FR-62/FR-77）。

5. **预留空壳子 tab 容器**（FR-94）：系统信息块预留「版本与更新」「控制面健康」空壳子 tab；系统设置块预留「网络代理」「更新设置」「API 密钥」「环境管理」空壳子 tab。**本批只搭占位容器 + 占位文案**，真实逻辑由后续 FR 填（版本更新 FR-100/99、代理 / 更新设置 FR-101、旧页并入 FR-95）。

6. **一屏不滚动版式**（FR-94）：tab 栏常驻（非滚动容器），运维设置的批量保存条改子 tab 内 sticky 底栏，内容卡片化 / 局部滚动；判据 = 常见桌面视口 1440×900 主操作无需滚到底可达。不引虚拟滚动。

7. **旧路由前端重定向策略**（由 FR-95 落地，本 ADR 仅记策略）：`/system`、`/api-keys`、`/namespaces` 三页将折叠进设置聚合页对应子 tab，旧路由用前端 `Navigate` 重定向到新深链，命令面板目标改指新深链。本批（FR-93/94）只搭骨架与空壳，**不动旧路由、不实现旧页内容并入**。

## 理由

- **嵌套子路由 + search param 而非纯前端 state**：深链 / 刷新保持 / 浏览器后退 / 命令面板可检索都要求 tab 状态进 URL；顶层三块用路径段语义清晰、可作命令面板跳转目标，块内子 tab 用 search param 轻量、不为每个子 tab 增路由声明。
- **保留集中草稿不下沉**：FR-62/FR-77 的「跨域统一草稿 + 批量保存 + 改动摘要」是已交付且有测试锁定的行为；按子 tab 切分**呈现**不等于切分**状态**，状态必须仍在顶层集中持有，否则跨子 tab 改两项无法在一个批量保存里统观。
- **不引 accordion 新依赖**：原生 `<details>/<summary>` 或受控 `useState`+偏好即可满足折叠，依赖管理规则要求不为可自实现的小交互引第三方件。
- **NAV 收敛单一真源**：两份导航清单是典型重复，抽共享常量消除漂移，符合「复制粘贴」反模式禁令。

## 后果

- FR-93/FR-94 落地后，PRD §4 两行标记已交付；侧栏成 5 组手风琴、`/settings` 成三块子 tab 聚合骨架。
- 偏好 store 新增 `navExpandedGroups` 字段，`beacon.preferences` 序列化结构向后兼容（旧值缺该键时回落默认全展开命中组）。
- 设置聚合页预留多个空壳子 tab，后续 FR-95/100/99/101 据此填内容、零结构改动。
- 本 ADR 由 FR-93、FR-94、FR-95 共用；FR-95 的旧路由重定向落地不再另起 ADR。

## 被否的备选

- **引入 radix accordion / 第三方导航库**：可自实现的小交互引依赖，违反依赖管理规则。否。
- **tab 状态只存前端 state、不进 URL**：无法深链 / 刷新保持 / 后退 / 命令面板检索，违背 FR-94 验收。否。
- **运维设置草稿按子 tab 各自持有**：会回归 FR-62/FR-77 的跨域统一草稿 / 批量保存，跨子 tab 改多项无法统观。否。
- **一次性把旧页（FR-95）也并入**：超出 FR-93/94 范围（范围纪律），FR-95 单独做。否。
