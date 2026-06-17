# 功能规格：管理台前端增强 L1

> 状态：开发中　·　关联 PRD：FR-18　·　分支：feature/fr-18-admin-ui

## 1. 背景与目标

控制面已落地 FR-11 鉴权（登录令牌 + 写操作授权）、FR-14 文件树托管（通道B）、FR-15 三方文件覆盖（override-set + dry-run）三套后端能力，但管理台（`web/`）只有配置中心 / 实例 / zone / 审计 / 环境五页，**没有登录入口、没有文件树托管页、没有 override-set 页**，鉴权后端无前端消费方。本批是 React 管理台的轻量增强（L1），让运维能在 Web 上登录、浏览/编辑托管文件、看文件级版本与回滚、对 override-set 做发布前只读预览。属第二期（P2）。

前端是后端契约的**消费方**，严格对齐 `docs/API.md` 与 `internal/handler/*.go`，不臆造端点、不在前端做配置合并。

## 2. 需求（要什么）

范围内（L1）：
- **登录 / 身份**（所有写操作前置）：`/login` 页（账号口令 → 令牌存 sessionStorage）；`client.ts` 单点注入 `Authorization: Bearer <token>` + 401 全局拦截跳 `/login`；operator 从登录态派生（`useOperator` 出参形态不变，既有页面零改动）。
- **文件树托管页 `FilesPage`**：按 namespace/group/path/scopeLevel 过滤列出托管文件；上百文件以自写递归树（Set 记展开）+ 平铺 path 表格两视图呈现；点进文件详情。
- **文本编辑**：复用既有 `content-editor` textarea + 等宽字体 + CSS 行号槽（不引 CodeMirror/Monaco）；`.allin`/`.js`/任意纯文本编辑。
- **文件级版本 / diff / 回滚**：复刻 `ConfigDetail` 的版本表 + 并排 `pre` diff + 回滚到此，套到文件对象上。文件无 `/diff` 端点，diff 由前端取两个历史版本内容并排展示（前端不做语义合并、不算差异，仅并列原文）。
- **override-set 页**：列表 + 详情（成员文件、目标根、重载命令）+ 发布/回滚/软删；**dry-run 只读预览**（调 `GET /override-sets/{id}/dry-run`，弹窗列出将覆盖哪些成员文件 + 将执行什么命令 + 勾选确认）。成员文件来源 = dry-run 的 `memberPaths`（控制面未单独暴露成员关系端点）。

不做（范围外，防镀金）：
- CodeMirror/Monaco 等编辑器、UI 组件库、树库、diff 库（全自写 + 复用 `styles.css`）。
- 单服灰度发布向导（FR-9/P2 红线）、覆盖来源徽标 / 版本树 / 灰度态可视（L3）、URL query 同步 scope、角色权限矩阵。
- **某服最终生效预览**：控制面**无 admin 侧"按 serverId 预览有效配置/文件"端点**（agent 侧 `effective`/`files/content` 需 agent token + 已注册实例，非 admin 面），本批不臆造端点、不在前端合并，留待后端补 admin 预览端点后再做（见 §6 待定）。

## 3. 设计（怎么做）

涉及模块：仅 `web/`（前端），无后端改动。

- **鉴权态**：新增 `state/auth.ts`（token + operator 存 sessionStorage，`useSyncExternalStore` 广播，仿 `state/operator.ts`）。`useOperator` 改为从 auth 态派生 operator，保留 `[operator, setOperator]` 出参形态使既有页面零改动；登录后 operator = 登录返回的 `operator`，`setOperator` 成无副作用空操作（身份已由令牌决定，手填不再生效，对齐 ADR-0009）。
- **client.ts**：`request()` 统一注入 `Authorization` 头；非 2xx 且 401 → 清 token + 跳 `/login`（用一个可注册的 `onUnauthorized` 回调，避免 client 直接依赖 router）。新增登录、文件 CRUD/发布/历史/回滚、override-set CRUD/发布/历史/回滚/dry-run 的 typed 函数。
- **路由**：`/login`（无 Layout）；`/files`、`/files/:id`、`/override-sets`、`/override-sets/:id` 挂 Layout；未登录访问受保护页 → 跳登录（`RequireAuth` 包裹 Layout）。
- **文件详情 diff**：取两版本 revision 内容并排 `pre`（复用 `.diff-panes`），与配置 diff 视觉一致。
- **行号槽**：纯 CSS + 一个轻量行号列（编辑区左侧等宽数字），不改 textarea 行为。

涉及架构决策已在 ADR-0009（鉴权）/ADR-0010（文件树）/ADR-0011（override-set）记录，此处不重复决策正文。本批为纯消费端，无新 ADR。

## 4. 任务拆分

- [ ] `state/auth.ts` 鉴权态 + `useOperator` 派生改造（既有页面零改动）
- [ ] `client.ts` 注入令牌 + 401 拦截 + 文件/override-set/login typed 函数 + types.ts 类型
- [ ] `/login` 页 + `RequireAuth` 路由守卫 + Layout 增导航与登出
- [ ] `FilesPage`（树 + 平铺）+ `FileDetail`（编辑/发布/版本/diff/回滚/软删 + 行号槽）
- [ ] `OverrideSetsPage` + `OverrideSetDetail`（成员/目标根/命令 + dry-run 预览弹窗 + 发布/回滚/软删）
- [ ] 文档同步：PRD 状态、ARCHITECTURE、API（前端消费侧说明）、CHANGELOG

## 5. 验收标准

- `pnpm -C web build`（`tsc -b` 严格模式 + `vite build`）通过。
- 未登录访问任意受保护页 → 跳 `/login`；登录成功 → 进入管理台；任意请求遇 401 → 自动跳登录。
- 文件树托管页能列出/树形浏览托管文件、点进详情看到内容与元数据、编辑并发布新版本、看历史/并排 diff、回滚到历史版本、软删该层。
- override-set 页能列出/查看成员与目标根与命令、跑 dry-run 预览（弹窗列成员+命令、勾选确认后才发布）、发布/回滚/软删。
- 深链 `/files/:id`、`/override-sets/:id` 可直接访问。
- 写操作 operator 以登录身份为准（后端已忽略手填，前端不再要求左侧手填操作人）。

## 6. 风险 / 待定

- **某服最终生效预览缺后端端点**：admin 面无"按 serverId 预览有效配置/文件树"端点，本批不做该项；如需，后端先补 `/admin/v1/.../effective?serverId=` 类只读端点（前端 `pre` 展示后端算好的结果，前端不合并）。
- 项目未配置 eslint（`web/` 无 eslint 配置 / 依赖），实际静态门为 `tsc -b` 严格模式（`noUnusedLocals`/`noUnusedParameters`/`strict`）；不擅自新增 eslint 依赖（依赖管理需确认）。
- 令牌存 sessionStorage：关页即失效，符合内部运维最小实现；XSS 防护不在本批范围。
